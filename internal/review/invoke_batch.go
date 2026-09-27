package review

import (
	"context"
	"encoding/json"
	"os"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/cred"
	"github.com/carlosboeing/crossrev/internal/diff"
	"github.com/carlosboeing/crossrev/internal/harness"
	"github.com/carlosboeing/crossrev/internal/intel"
	"github.com/carlosboeing/crossrev/internal/prompt"
	"github.com/carlosboeing/crossrev/internal/prstate"
	"github.com/carlosboeing/crossrev/internal/ui"
	"github.com/carlosboeing/crossrev/internal/validate"
)

// batchContext is one pass's shared prompt context, discovered once before
// packing measures the first candidate: the diff parsed for per-batch
// slicing, the open threads, the whole-pass advisory summary with the
// per-file changed terms each call selects its own pointers from, the
// exclusion refs, and the repair delta. Packing measures one candidate per
// admitted file, and each discovery leg is a git process or a forge request,
// so candidates render from this snapshot rather than repeating the
// discovery — forty files otherwise meant forty blob passes before the first
// model call.
type batchContext struct {
	diff         *diff.Diff
	diffErr      error
	meta         prompt.Meta
	prior        []prompt.Prior
	threads      []prompt.Thread
	reviewMD     []byte
	advisory     intel.AdvisorySummary
	fileTerms    map[string][]string
	excluded     []prompt.ExclusionRef
	confirmation []byte
}

// discoverBatchContext reads the pass's shared context exactly once. The
// advisory summary comes from the caller — the coverage ledger persists its
// counts and cap reasons, so the prompt pointers and the ledger records are
// the one discovery rather than two that could disagree.
func (l *Leg) discoverBatchContext(ctx context.Context, req Request, loaded Context, pass int, scope intel.Scope, advisory intel.AdvisorySummary, fileTerms map[string][]string, confirmation []byte) batchContext {
	start := l.now()
	diffBytes, err := l.reviewDiff(ctx, loaded)
	l.Log.Phase("diff", l.now().Sub(start).Milliseconds())
	return batchContext{
		diff:         diff.Parse(diffBytes, core.RevisionPair{}),
		diffErr:      err,
		meta:         reviewMeta(loaded, req, pass),
		prior:        priorFindings(loaded),
		threads:      promptThreads(l.Forge.ReviewThreads(ctx, loaded.Repo, req.PR)),
		reviewMD:     loaded.ReviewMD,
		advisory:     advisory,
		fileTerms:    fileTerms,
		excluded:     excludedPromptRefs(scope),
		confirmation: confirmation,
	}
}

// render builds one candidate batch's complete prompt from the snapshot —
// headers, prior context and file content together, with the diff sliced to
// the batch's own files — and measures what the reviewer is actually given
// for each unit, from the exact batch units the prompt renders. It is pure
// over the snapshot: no git, no forge, and deterministic, so the packer can
// measure it for every candidate and the invoke path sends exactly the
// measured bytes. The measurement happens once, here, and travels with the
// batch to publication; it is never recomputed there from a second read,
// which could disagree with what was sent.
func (c batchContext) render(files []intel.FileUnit, base, head core.Revision) ([]byte, map[core.UnitID]prstate.SuppliedInput) {
	_, units := batchExpectations(files, base, head)
	supplied := make(map[core.UnitID]prstate.SuppliedInput, len(files))
	for i, unit := range units {
		supplied[files[i].ID] = suppliedFor(unit)
	}
	advisory, omitted := batchPointerRefs(c.advisory, c.fileTerms, files)
	return prompt.Review{
		Skill:           prompt.ReviewSkill(),
		Diff:            c.diff.Only(batchPaths(files)),
		Meta:            c.meta,
		Prior:           c.prior,
		Threads:         c.threads,
		ReviewMD:        c.reviewMD,
		Batch:           units,
		Advisory:        advisory,
		AdvisoryOmitted: omitted,
		Excluded:        c.excluded,
		Confirmation:    c.confirmation,
	}.Render(), supplied
}

// suppliedFor measures what the reviewer is actually given for one unit: a
// digest over the unit's body bytes as handed to prompt rendering. A unit
// with readable bytes supplied in full reads full_text; an unavailable or
// binary unit reaches the model through the diff slice alone and reads
// diff_only, with the digest over the empty input because no body bytes were
// handed over. Truncated stays false: a file that cannot fit a prompt alone
// halts with input_exceeds_budget rather than being cut.
func suppliedFor(unit prompt.BatchUnit) prstate.SuppliedInput {
	if unit.Available && !unit.Binary {
		return prstate.SuppliedInput{Digest: core.BodyDigestHex(unit.Body), Form: prstate.SuppliedFormFullText}
	}
	return prstate.SuppliedInput{Digest: core.BodyDigestHex(nil), Form: prstate.SuppliedFormDiffOnly}
}

// batchPaths names the sections one batch keeps from the full diff: each
// unit's current path, plus its previous path for a rename or a deletion.
// The repair delta is not part of this input — Confirmation carries it
// whole, ahead of the sliced scope.
func batchPaths(files []intel.FileUnit) []string {
	paths := make([]string, 0, len(files))
	for _, unit := range files {
		paths = append(paths, unit.Path)
		if unit.OldPath != "" && unit.OldPath != unit.Path {
			paths = append(paths, unit.OldPath)
		}
	}
	return paths
}

// invokePrompt runs one rendered prompt through the harness with the
// batch-scoped validation seam: one semantic retry naming the rejected
// numbers, then a fatal refusal that publishes nothing. call is the call's
// number in the pass, naming its transcripts and its run-log line.
func (l *Leg) invokePrompt(ctx context.Context, req Request, loaded Context, settings legSettings, expected validate.ReviewExpectations, promptBytes []byte, call int) (json.RawMessage, harness.Envelope, []ui.Line, error) {
	if err := harness.AssertEnvClean(l.Env); err != nil {
		return nil, harness.Envelope{}, nil, err
	}
	entry, _ := l.Harness.For(settings.harness)
	staged, err := cred.Prepare(l.Harness.Credentials().For(settings.harness), settings.endpoint, cred.Options{Now: l.Now})
	if err != nil {
		return nil, harness.Envelope{}, nil, err
	}
	defer func() { _ = cred.Discard(staged) }()
	adapter, known := harness.For(l.Harness, settings.harness)
	if !known {
		return nil, harness.Envelope{}, nil, noAdapterRefusal(l.Harness, settings.harness)
	}
	saved := l.Expect
	l.Expect = expected
	defer func() { l.Expect = saved }()
	return l.invokeWithStaged(ctx, req, loaded, settings, adapter, entry, staged, expected, promptBytes, call)
}

// logAcceptedCall writes one accepted call's run-log line: the rendered
// prompt's byte length, the evidence bytes handed over with it, the usage
// buckets the accepted envelope folded in (refused attempts included), the
// answering model, and the call's wall time. A refused answer judged
// nothing and gets no line.
func (l *Leg) logAcceptedCall(call int, promptBytes []byte, suppliedBytes int, envelope harness.Envelope, ms int64) {
	if l.Log == nil {
		return
	}
	var fresh, cached, output int64
	if envelope.Usage != nil {
		fresh = envelope.Usage.InputFresh
		cached = envelope.Usage.Cached()
		output = envelope.Usage.Output
	}
	model := ""
	if envelope.ModelReported != nil {
		model = *envelope.ModelReported
	}
	l.Log.Call(call, len(promptBytes), suppliedBytes, fresh, cached, output, model, ms)
}

// suppliedBytes measures what the reviewer was actually given for one batch:
// the evidence body bytes handed to prompt rendering. Units reaching the
// model through the diff slice alone contribute nothing, the way their
// supplied record reads diff_only.
func suppliedBytes(files []intel.FileUnit) int {
	total := 0
	for _, unit := range files {
		if unit.Available && !unit.Binary {
			total += len(unit.Body)
		}
	}
	return total
}

// changedTermCount is the whole-pass search set's size: every file's terms
// deduplicated. The advisory blob pass searches this set, so the terms phase
// carries the count the search ran with.
func changedTermCount(fileTerms map[string][]string) int {
	seen := make(map[string]bool)
	for _, terms := range fileTerms {
		for _, term := range terms {
			seen[term] = true
		}
	}
	return len(seen)
}

// invokeWithStaged runs one batch prompt through the already-staged
// credential: one sandbox quarantine per prompt (not per attempt), the
// validation seam scoped to this batch, and the staged credential discarded
// by the caller.
func (l *Leg) invokeWithStaged(ctx context.Context, req Request, loaded Context, settings legSettings, adapter harness.Adapter, entry harness.Descriptor, staged *cred.Staged, expected validate.ReviewExpectations, promptBytes []byte, call int) (json.RawMessage, harness.Envelope, []ui.Line, error) {
	tmp, err := os.MkdirTemp("", "crossrev-review-")
	if err != nil {
		return nil, harness.Envelope{}, nil, err
	}
	defer os.RemoveAll(tmp)
	envelope, payload, msgs, err := l.runPrompt(ctx, req, loaded, settings, adapter, entry, staged, tmp, promptBytes, nil, call)
	return payload, envelope, msgs, err
}

// currentGeneration reads the pass's resumption point off the marker as it
// stands: the handle the marker names, the generation behind it, retired
// unless its revision pair, engine and producer are all still in force.
//
// No claim means no coverage pass ran — or a legacy manifest id whose
// comment is never read again, which is the lost-ledger row. Both resume
// from zero; the legacy case costs at most one re-review of the current
// head. A lost ledger resumes from zero too. Anything else that goes wrong
// — a corrupt claim, a corrupt generation, an unreadable store — fails the
// pass: re-reviewing from zero over coverage that cannot be read would hide
// the integrity failure behind wasted work.
func (l *Leg) currentGeneration(ctx context.Context, loaded Context, store prstate.LedgerStore, marker prstate.Marker, scope intel.Scope, producer prstate.Producer) (prstate.Generation, error) {
	h, claimed, err := marker.CoverageHandle()
	if err != nil {
		return prstate.Generation{}, err
	}
	if !claimed {
		return prstate.Generation{}, nil
	}
	gen, err := store.ReadGeneration(ctx, slotRefFor(loaded), h)
	if err != nil {
		if lost, _ := coverageOutcome(err); lost {
			return prstate.Generation{}, nil
		}
		return prstate.Generation{}, err
	}
	if !prstate.GenerationCurrent(gen, core.RevisionPair{Base: scope.Base, Head: scope.Head}, scope.Engine, producer) {
		return prstate.Generation{}, nil
	}
	return gen, nil
}
