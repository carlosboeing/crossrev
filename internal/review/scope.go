package review

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/carlosboeing/crossrev/internal/core"
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

func (s scopeSearcher) ExactSearch(ctx context.Context, revision core.Revision, term string, limit int) ([]string, bool, error) {
	if s.vcs == nil {
		return nil, false, nil
	}
	hits, tooCommon, err := s.vcs.ExactSearch(ctx, revision, term, limit)
	if err != nil {
		return nil, false, nil
	}
	paths := make([]string, 0, len(hits))
	for _, hit := range hits {
		paths = append(paths, hit.Path)
	}
	return paths, tooCommon, nil
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
func (l *Leg) buildScope(ctx context.Context, base, head core.Revision, excluded []intel.Exclusion) (intel.Scope, error) {
	if l.VCS == nil {
		return intel.Scope{}, errNoScopeReader{}
	}
	changes, err := l.VCS.ChangedFiles(ctx, base, head)
	if err != nil {
		return intel.Scope{}, err
	}
	return intel.RequiredFiles(ctx, changes, scopeReader{leg: l}, base, head, excluded)
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

// acceptedFromGeneration reads the dispositions the current generation
// accepted at this exact base, head and engine, with the finding ids,
// evidence and reasons they carry: resuming a pass republishes the full
// judgement, never an empty disposition (which the strict decoder refuses).
// Any other revision or engine contributes nothing: a repair changes the
// head and retires every earlier disposition.
func acceptedFromGeneration(gen prstate.Generation, base, head core.Revision, engine string) map[core.UnitID]recordDisposition {
	accepted := map[core.UnitID]recordDisposition{}
	if gen.Revision.Base.SHA() != base.SHA() || gen.Revision.Head.SHA() != head.SHA() || gen.Engine != engine {
		return accepted
	}
	for _, record := range gen.Records {
		if record.Type != prstate.CoverageRecordUnit {
			continue
		}
		disp, ok := record.Disposition.Get()
		if !ok || disp == "" {
			continue
		}
		var ids []string
		ids = append(ids, record.FindingIDs...)
		var evidence []prstate.Evidence
		evidence = append(evidence, record.Evidence...)
		reason, _ := record.Reason.Get()
		accepted[core.UnitID(record.UnitID)] = recordDisposition{Disposition: disp, FindingIDs: ids, Evidence: evidence, Reason: reason}
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
// and the dispositions accepted so far: covered units carry their
// disposition, the rest stay outstanding with their access reason.
func generationRecords(scope intel.Scope, dispositions map[core.UnitID]recordDisposition, pathIndex map[string]int) []prstate.Record {
	records := make([]prstate.Record, 0, len(scope.Required))
	for _, unit := range scope.Required {
		if disp, ok := dispositions[unit.ID]; ok {
			records = append(records, unitRecord(unit, pathIndex[unit.Path], disp))
		} else {
			records = append(records, prstate.OutstandingRecord(string(unit.ID), pathIndex[unit.Path], string(unit.Change), unit.BodyDigest, outstandingReason(unit)))
		}
	}
	return records
}

// recordDisposition is one accepted unit: its disposition, finding ids and
// evidence, as the reviewer reported them.
type recordDisposition struct {
	Disposition string
	FindingIDs  []string
	Evidence    []prstate.Evidence
	Reason      string
}

func unitRecord(unit intel.FileUnit, pathIdx int, disp recordDisposition) prstate.Record {
	record := prstate.Record{
		Type:       prstate.CoverageRecordUnit,
		UnitID:     string(unit.ID),
		PathIndex:  pathIdx,
		Kind:       "file",
		Change:     string(unit.Change),
		BodyDigest: unit.BodyDigest,
	}
	record.Disposition = prstate.Some(disp.Disposition)
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

// batchExpectations maps one rendered batch to the numbered expectations the
// semantic check holds the answer against: positions 1 to len(units) in
// prompt order, with the base and head the batch was built between.
func batchExpectations(units []intel.FileUnit, base, head core.Revision) (expected validate.ReviewExpectations, promptUnits []prompt.BatchUnit) {
	expected.Base = base
	expected.Head = head
	for _, unit := range units {
		lines := 0
		readable := unit.Available && !unit.Binary && len(unit.Body) > 0
		if readable {
			lines = evidenceLines(unit.Body)
		}
		expected.Units = append(expected.Units, validate.UnitExpectation{Path: unit.Path, Revision: unit.ContentRevision, Lines: lines, Readable: readable})
		promptUnits = append(promptUnits, prompt.BatchUnit{
			Path:            unit.Path,
			OldPath:         unit.OldPath,
			Change:          unit.Change,
			ContentRevision: unit.ContentRevision,
			Body:            unit.Body,
			Available:       unit.Available,
			Binary:          unit.Binary,
			Reason:          unit.Reason,
		})
	}
	return expected, promptUnits
}

// haltBody renders the bounded halt the claim carries when the pass cannot
// cover every required file: the halt word, the stop counts and the
// outstanding paths, so a fresh process can reconstruct the same list.
func haltBody(paths []string, stop prstate.CoverageStop, halt string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "**crossrev halted before covering every changed file** — %s.\n\n", halt)
	fmt.Fprintf(&b, "Required %d, covered %d, outstanding %d. The last complete generation stands; nothing partial was published as complete.\n\n", stop.RequiredCount, stop.CoveredCount, stop.OutstandingCount)
	if len(paths) > 0 {
		b.WriteString("Outstanding paths:\n\n")
		for _, path := range paths {
			fmt.Fprintf(&b, "- `%s`\n", path)
		}
		b.WriteString("\nA re-drive at the same base, head and engine resumes these paths. Any changed value starts from zero accepted dispositions.\n")
	}
	return b.String()
}
