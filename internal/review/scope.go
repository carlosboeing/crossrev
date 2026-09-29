package review

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/diff"
	"github.com/carlosboeing/crossrev/internal/intel"
	"github.com/carlosboeing/crossrev/internal/prompt"
	"github.com/carlosboeing/crossrev/internal/prstate"
	"github.com/carlosboeing/crossrev/internal/validate"
	"github.com/carlosboeing/crossrev/internal/vcs"
)

// scopeReader adapts the leg's VCS file reads to the intel evidence reader.
// Discovery stays pure in tier 1; this tier-3 type carries the effect.
type scopeReader struct {
	leg *Leg
}

func (r scopeReader) Read(ctx context.Context, revision core.Revision, path string) (intel.FileBody, error) {
	if r.leg == nil || r.leg.VCS == nil {
		return intel.FileBody{Unavailable: true, Reason: "no file reader"}, nil
	}
	body, status, err := r.leg.VCS.Show(ctx, revision, path)
	if err != nil {
		return intel.FileBody{Unavailable: true, Reason: "unreadable"}, nil
	}
	if status != vcs.IsFile {
		return intel.FileBody{Unavailable: true, Reason: "unreadable"}, nil
	}
	return intel.FileBody{Data: body}, nil
}

// scopeSearcher adapts git search to the advisory discovery surface.
type scopeSearcher struct {
	vcs VCS
}

func (s scopeSearcher) SearchAll(ctx context.Context, revision core.Revision, terms []string, limit int) ([]intel.TermResult, error) {
	if s.vcs == nil {
		return nil, nil
	}
	results, err := s.vcs.SearchAll(ctx, revision, terms, limit)
	if err != nil {
		return nil, nil
	}
	out := make([]intel.TermResult, 0, len(results))
	for _, res := range results {
		hits := make([]intel.SearchHit, 0, len(res.Hits))
		for _, hit := range res.Hits {
			hits = append(hits, intel.SearchHit{Path: hit.Path, Lines: append([]int(nil), hit.Lines...), OmittedLines: hit.OmittedLines})
		}
		out = append(out, intel.TermResult{Term: res.Term, Hits: hits, TooCommon: res.TooCommon})
	}
	return out, nil
}

func (s scopeSearcher) Exists(ctx context.Context, revision core.Revision, path string) (bool, error) {
	if s.vcs == nil {
		return false, nil
	}
	body, status, err := s.vcs.Show(ctx, revision, path)
	if err != nil {
		return false, nil
	}
	return status == vcs.IsFile && body != nil, nil
}

// buildScope enumerates every changed path between base and head and reads
// the required evidence, before the first model call. A changed path the
// enumeration cannot list — a git failure — stops the leg, because an
// unlisted path is a silent loss. A path whose bytes cannot be read stays
// required with a visible access limit.
//
// The base tree's linguist-generated answers join the enumeration here. A
// git too old for check-attr --source degrades to one warning and the
// built-in rules; any other attribute failure stops the leg the way a failed
// ChangedFiles does, because reading it as unspecified would review paths
// the repository marked generated.
func (l *Leg) buildScope(ctx context.Context, base, head core.Revision, excluded []intel.Exclusion) (intel.Scope, *vcs.Warning, error) {
	if l.VCS == nil {
		return intel.Scope{}, nil, errNoScopeReader{}
	}
	start := l.now()
	changes, err := l.VCS.ChangedFiles(ctx, base, head)
	l.Log.Phase("enumerate", l.now().Sub(start).Milliseconds())
	if err != nil {
		return intel.Scope{}, nil, err
	}
	paths := make([]string, 0, len(changes))
	for _, change := range changes {
		paths = append(paths, change.Path)
	}
	start = l.now()
	attrs, warning, err := l.VCS.GeneratedAttributes(ctx, base, paths)
	if err != nil {
		return intel.Scope{}, nil, err
	}
	scope, err := intel.RequiredFiles(ctx, changes, scopeReader{leg: l}, base, head, excluded, intelAttributeDecisions(attrs))
	l.Log.Phase("reads", l.now().Sub(start).Milliseconds())
	if err != nil {
		return intel.Scope{}, nil, err
	}
	// Hunk shaping runs once per pass, before packing measures the first
	// candidate: every unit carries its own gutter-numbered hunks into
	// every render, so the packer measures what the reviewer is actually
	// given. A git failure shaping one file fails the pass, the way a
	// failed enumeration does; a shaping the leg cannot run — no git
	// reader behind the interface — keeps the legacy body rendering.
	if shaping, err := shapeScope(ctx, l.VCS, base, head, &scope); err != nil {
		return intel.Scope{}, nil, err
	} else if shaping != nil {
		warning = joinWarnings(warning, shaping)
	}
	return scope, warning, nil
}

// hunkShaper is the per-file hunk shaping surface. Production wires
// *vcs.Repository; a VCS that does not implement it keeps the legacy
// prompt, where each unit renders from its body alone.
type hunkShaper interface {
	HunkDiffSupport(ctx context.Context) (bool, *vcs.Warning, error)
	ShapeFileDiff(ctx context.Context, base, head core.Revision, change core.FileChange, body []byte, binary bool, unavailableReason string, support bool) (vcs.ShapedFile, error)
}

// shapeScope shapes every required unit's hunk input and answers the
// old-git warning, if any. Units a shaping failure would leave behind
// never exist: the error fails the pass before packing measures anything.
func shapeScope(ctx context.Context, vcsIface VCS, base, head core.Revision, scope *intel.Scope) (*vcs.Warning, error) {
	shaper, ok := vcsIface.(hunkShaper)
	if !ok {
		return nil, nil
	}
	support, warning, err := shaper.HunkDiffSupport(ctx)
	if err != nil {
		return nil, err
	}
	for i := range scope.Required {
		unit := &scope.Required[i]
		var reason string
		if !unit.Available {
			reason = unit.Reason
		}
		shaped, err := shaper.ShapeFileDiff(ctx, base, head,
			core.FileChange{OldPath: unit.OldPath, Path: unit.Path, Kind: unit.Change},
			unit.Body, unit.Binary, reason, support)
		if err != nil {
			return nil, err
		}
		unit.Form = shaped.Form
		unit.Diff = shaped.Diff
	}
	return warning, nil
}

// joinWarnings carries two non-fatal conditions in the one warning slot
// the scope read returns: an old git trips both the attribute read and
// the hunk shaping gate, and the operator should see both halves.
func joinWarnings(first, second *vcs.Warning) *vcs.Warning {
	if first == nil {
		return second
	}
	if second == nil {
		return first
	}
	return &vcs.Warning{
		Message: first.Message + "; " + second.Message,
		Hint:    first.Hint + " " + second.Hint,
	}
}

type errNoScopeReader struct{}

func (e errNoScopeReader) Error() string {
	return "the review leg has no git reader, so the required file set cannot be enumerated"
}

// scopeExclusions carries the backlog paths out of the required denominator.
// The backlog file or folder at the base revision is operator state, not
// review work; C2 owns the confirmation delta, which reuses this list.
func scopeExclusions(backlogPath string) []intel.Exclusion {
	if backlogPath == "" {
		return nil
	}
	return []intel.Exclusion{{Path: backlogPath, Reason: "backlog destination"}}
}

// acceptedFromGeneration reads the verdicts the current generation
// accepted at this exact base, head and engine, with the finding ids,
// evidence and reasons they carry: resuming a pass republishes the full
// judgement, never an empty verdict (which the strict decoder refuses).
// Any other revision or engine contributes nothing: a repair changes the
// head and retires every earlier verdict.
func acceptedFromGeneration(gen prstate.Generation, base, head core.Revision, engine string) map[core.UnitID]recordVerdict {
	accepted := map[core.UnitID]recordVerdict{}
	if gen.Revision.Base.SHA() != base.SHA() || gen.Revision.Head.SHA() != head.SHA() || gen.Engine != engine {
		return accepted
	}
	for _, record := range gen.Records {
		if record.Type != prstate.CoverageRecordUnit {
			continue
		}
		disp, ok := record.Verdict.Get()
		if !ok || disp == "" {
			continue
		}
		var ids []string
		ids = append(ids, record.FindingIDs...)
		var evidence []prstate.Evidence
		evidence = append(evidence, record.Evidence...)
		reason, _ := record.Reason.Get()
		accepted[core.UnitID(record.UnitID)] = recordVerdict{Verdict: disp, FindingIDs: ids, Evidence: evidence, Reason: reason}
	}
	return accepted
}

// outstandingPaths lists the required paths the current generation leaves
// uncovered, in scope order. A fresh process reconstructs the same list from
// the manifest and shards alone.
func outstandingPaths(scope intel.Scope, accepted map[core.UnitID]bool) []string {
	var out []string
	for _, unit := range scope.Required {
		if !accepted[unit.ID] {
			out = append(out, unit.Path)
		}
	}
	return out
}

// scopeReportOf carries the reviewer's own scope claims into the manifest,
// labelled as reviewer claims rather than deterministic discovery.
func scopeReportOf(examined string, limits []string) prstate.ScopeReport {
	known := append([]string(nil), limits...)
	sort.Strings(known)
	return prstate.ScopeReport{ExaminedScope: examined, KnownLimits: known}
}

// generationRecords renders one complete generation's records from the scope
// and the verdicts accepted so far: covered units carry their verdict and
// the measurement of what the reviewer was given for them, the rest stay
// outstanding with their access reason and a null supplied input — nothing
// was handed to a reviewer for a file nobody reviewed.
func generationRecords(scope intel.Scope, verdicts map[core.UnitID]recordVerdict, pathIndex map[string]int, supplied map[core.UnitID]prstate.SuppliedInput) []prstate.Record {
	records := make([]prstate.Record, 0, len(scope.Required))
	for _, unit := range scope.Required {
		if disp, ok := verdicts[unit.ID]; ok {
			record := unitRecord(unit, pathIndex[unit.Path], disp)
			if s, ok := supplied[unit.ID]; ok {
				record.Supplied = prstate.Some(s)
			}
			records = append(records, record)
		} else {
			records = append(records, prstate.OutstandingRecord(string(unit.ID), pathIndex[unit.Path], string(unit.Change), unit.BodyDigest, outstandingReason(unit)))
		}
	}
	return records
}

// recordVerdict is one accepted unit: its verdict, finding ids and
// evidence, as the reviewer reported them.
type recordVerdict struct {
	Verdict    string
	FindingIDs []string
	Evidence   []prstate.Evidence
	Reason     string
}

func unitRecord(unit intel.FileUnit, pathIdx int, disp recordVerdict) prstate.Record {
	record := prstate.Record{
		Type:       prstate.CoverageRecordUnit,
		UnitID:     string(unit.ID),
		PathIndex:  pathIdx,
		Kind:       "file",
		Change:     string(unit.Change),
		BodyDigest: unit.BodyDigest,
	}
	record.Verdict = prstate.Some(disp.Verdict)
	if len(disp.FindingIDs) > 0 {
		record.FindingIDs = append([]string(nil), disp.FindingIDs...)
	}
	if len(disp.Evidence) > 0 {
		record.Evidence = append([]prstate.Evidence(nil), disp.Evidence...)
	}
	if disp.Reason != "" {
		record.Reason = prstate.Some(disp.Reason)
	}
	return record
}

func outstandingReason(unit intel.FileUnit) string {
	if unit.Reason != "" {
		return unit.Reason
	}
	return "awaiting review"
}

// evidenceLines counts the readable lines in the evidence bytes, the span
// bound the semantic check holds a finding's lines inside of.
func evidenceLines(body []byte) int {
	if len(body) == 0 {
		return 0
	}
	n := bytes.Count(body, []byte{'\n'})
	if !bytes.HasSuffix(body, []byte{'\n'}) {
		n++
	}
	return n
}

// WholePolicy promotes hunk-shaped files to whole-file rendering under
// the whole_when_fits input policy: a file whose whole body fits
// MaxBytes renders in full, and anything past it keeps the Task 1 hunk
// form. Nil means hunks_first: every file renders in its shaped form.
// Splitting still applies to anything over the budget under either
// policy.
type WholePolicy struct {
	MaxBytes int
}

// unitRanges answers the spans the reviewer is actually shown for one
// required file, numbered as the gutter shows them. form is the rendered
// form after whole-file promotion: a hunk form reads its ranges off its
// own diff, a promoted or legacy unit renders its body in full on its
// content side — the base for a deletion, the head for every other kind —
// and a unit with no readable bytes shows nothing on either side. The
// validation expectations and the supplied measurement both read this, so
// the two never disagree about what was shown.
func unitRanges(unit intel.FileUnit, form intel.InputForm, lines int, readable bool) core.SuppliedRanges {
	if form != "" && len(unit.Diff) > 0 {
		base, head := diff.Parse(unit.Diff, core.RevisionPair{}).LineRanges()
		return core.SuppliedRanges{Base: base, Head: head}
	}
	if !readable || lines == 0 {
		return core.SuppliedRanges{}
	}
	span := []core.LineSpan{{Start: 1, End: lines}}
	if unit.Change == core.ChangeDeleted {
		return core.SuppliedRanges{Base: span}
	}
	return core.SuppliedRanges{Head: span}
}

// batchExpectations maps one rendered batch to the numbered expectations the
// semantic check holds the answer against: positions 1 to len(units) in
// prompt order, with the base and head the batch was built between. A
// shaped unit carries its gutter-numbered hunks into the prompt; an
// unshaped one renders from its body the way it always did.
func batchExpectations(units []intel.FileUnit, base, head core.Revision, whole *WholePolicy) (expected validate.ReviewExpectations, promptUnits []prompt.BatchUnit) {
	expected.Base = base
	expected.Head = head
	for _, unit := range units {
		lines := 0
		readable := unit.Available && !unit.Binary && len(unit.Body) > 0
		if readable {
			lines = evidenceLines(unit.Body)
		}
		reason := unit.Reason
		if unit.Form == intel.FormDiffOnly && reason == "" && !unit.Binary {
			reason = intel.DiffOnlyReason(unit.Change, len(unit.Body), false, "")
		}
		form := unit.Form
		if whole != nil && form == intel.FormHunksContext && readable && len(unit.Body) <= whole.MaxBytes {
			form = ""
		}
		expected.Units = append(expected.Units, validate.UnitExpectation{Path: unit.Path, Revision: unit.ContentRevision, Lines: lines, Readable: readable, Ranges: unitRanges(unit, form, lines, readable)})
		pu := prompt.BatchUnit{
			Path:            unit.Path,
			OldPath:         unit.OldPath,
			Change:          unit.Change,
			ContentRevision: unit.ContentRevision,
			Body:            unit.Body,
			Available:       unit.Available,
			Binary:          unit.Binary,
			Reason:          reason,
			Form:            form,
		}
		if form != "" && len(unit.Diff) > 0 {
			pu.NumberedDiff = diff.Parse(unit.Diff, core.RevisionPair{}).Numbered()
		}
		promptUnits = append(promptUnits, pu)
	}
	return expected, promptUnits
}

// partExpectations maps one split-file part to the numbered expectations
// the semantic check holds the answer against: the part's single position
// with the base and head the pass was built between. The ranges are the
// part's own slice, not the whole file: the part reviewer judges the
// slice's lines, and a verdict resting on unseen lines is refused the way
// any span outside the supplied ranges is.
func partExpectations(part *intel.FilePart, base, head core.Revision) (expected validate.ReviewExpectations, promptUnits []prompt.BatchUnit) {
	unit := part.Unit
	expected.Base = base
	expected.Head = head
	lines := 0
	readable := unit.Available && !unit.Binary && len(unit.Body) > 0
	if readable {
		lines = evidenceLines(unit.Body)
	}
	expected.Units = append(expected.Units, validate.UnitExpectation{Path: unit.Path, Revision: unit.ContentRevision, Lines: lines, Readable: readable, Ranges: partRanges(part, unit, lines, readable)})
	promptUnits = append(promptUnits, prompt.BatchUnit{
		Path:            unit.Path,
		OldPath:         unit.OldPath,
		Change:          unit.Change,
		ContentRevision: unit.ContentRevision,
		Available:       unit.Available,
		Binary:          unit.Binary,
		Reason:          unit.Reason,
		Form:            unit.Form,
		Part:            fmt.Sprintf("%d of %d", part.Index+1, part.Count),
		NumberedDiff:    diff.Parse(part.Diff, core.RevisionPair{}).Numbered(),
	})
	return expected, promptUnits
}

// partRanges answers the spans one split-file part shows: its own slice's
// gutter runs. A part with no diff of its own — the blockless fallback no
// packing path produces — falls back to the unit's rule, so validation and
// the merged measurement read the same spans.
func partRanges(part *intel.FilePart, unit intel.FileUnit, lines int, readable bool) core.SuppliedRanges {
	if len(part.Diff) > 0 {
		base, head := diff.Parse(part.Diff, core.RevisionPair{}).LineRanges()
		return core.SuppliedRanges{Base: base, Head: head}
	}
	return unitRanges(unit, unit.Form, lines, readable)
}

// haltPathListBudget caps the outstanding-path section of a halt report in
// bytes. The halt must fit the comment cap even when the outstanding set
// does not, so the list truncates and the counts stay exact: a re-drive
// rebuilds the full list from the generation's outstanding records, not
// from this prose.
const haltPathListBudget = 8 * 1024

// haltBody renders the bounded halt the claim carries when the pass cannot
// cover every required file: the halt word, the exact stop counts and the
// outstanding paths up to a fixed budget, with an overflow count past it.
func haltBody(paths []string, stop prstate.CoverageStop, halt string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "**crossrev halted before covering every changed file** — %s.\n\n", halt)
	fmt.Fprintf(&b, "Required %d, covered %d, outstanding %d. The last complete generation stands; nothing partial was published as complete.\n\n", stop.RequiredCount, stop.CoveredCount, stop.OutstandingCount)
	if len(paths) > 0 {
		b.WriteString("Outstanding paths:\n\n")
		budget := haltPathListBudget
		listed := 0
		for _, path := range paths {
			line := "- `" + path + "`\n"
			if len(line) > budget {
				break
			}
			b.WriteString(line)
			budget -= len(line)
			listed++
		}
		if rest := len(paths) - listed; rest > 0 {
			fmt.Fprintf(&b, "- …and %d more outstanding paths\n", rest)
		}
		b.WriteString("\nA re-drive at the same base, head and engine resumes these paths. Any changed value starts from zero accepted verdicts.\n")
	}
	return b.String()
}
