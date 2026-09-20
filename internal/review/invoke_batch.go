package review

import (
	"context"
	"encoding/json"
	"errors"
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

// ledgerStoreFor returns the coverage ledger over the leg's forge client, or
// nil when the client does not implement the store contract.
func ledgerStoreFor(l *Leg) prstate.CommentStore {
	if l == nil || l.Forge == nil {
		return nil
	}
	store, _ := l.Forge.(prstate.CommentStore)
	return store
}

// batchContext is one pass's shared prompt context, discovered once before
// packing measures the first candidate: the diff parsed for per-batch
// slicing, the open threads, the advisory and exclusion refs, and the repair
// delta. Packing measures one candidate per admitted file, and each
// discovery leg is a git process or a forge request, so candidates render
// from this snapshot rather than repeating the discovery — forty files
// carrying two search terms otherwise meant 82 searches before the first
// model call.
type batchContext struct {
	diff         *diff.Diff
	diffErr      error
	meta         prompt.Meta
	prior        []prompt.Prior
	threads      []prompt.Thread
	reviewMD     []byte
	advisory     []prompt.AdvisoryRef
	excluded     []prompt.ExclusionRef
	confirmation []byte
}

// discoverBatchContext reads the pass's shared context exactly once. The
// advisory summary comes from the caller — the coverage ledger persists its
// counts and cap reasons, so the prompt refs and the ledger records are the
// one discovery rather than two that could disagree.
func (l *Leg) discoverBatchContext(ctx context.Context, req Request, loaded Context, pass int, scope intel.Scope, advisory intel.AdvisorySummary, confirmation []byte) batchContext {
	diffBytes, err := l.reviewDiff(ctx, loaded)
	advisoryRefs, excludedRefs := advisoryPromptRefs(scope, advisory)
	return batchContext{
		diff:         diff.Parse(diffBytes, core.RevisionPair{}),
		diffErr:      err,
		meta:         reviewMeta(loaded, req, pass),
		prior:        priorFindings(loaded),
		threads:      promptThreads(l.Forge.ReviewThreads(ctx, loaded.Repo, req.PR)),
		reviewMD:     loaded.ReviewMD,
		advisory:     advisoryRefs,
		excluded:     excludedRefs,
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
	return prompt.Review{
		Skill:        prompt.ReviewSkill(),
		Diff:         c.diff.Only(batchPaths(files)),
		Meta:         c.meta,
		Prior:        c.prior,
		Threads:      c.threads,
		ReviewMD:     c.reviewMD,
		Batch:        units,
		Advisory:     c.advisory,
		Excluded:     c.excluded,
		Confirmation: c.confirmation,
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
// numbers, then a fatal refusal that publishes nothing.
func (l *Leg) invokePrompt(ctx context.Context, req Request, loaded Context, settings legSettings, expected validate.ReviewExpectations, promptBytes []byte) (json.RawMessage, harness.Envelope, []ui.Line, error) {
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
	return l.invokeWithStaged(ctx, req, loaded, settings, adapter, entry, staged, expected, promptBytes)
}

// invokeWithStaged runs one batch prompt through the already-staged
// credential: one sandbox quarantine per prompt (not per attempt), the
// validation seam scoped to this batch, and the staged credential discarded
// by the caller.
func (l *Leg) invokeWithStaged(ctx context.Context, req Request, loaded Context, settings legSettings, adapter harness.Adapter, entry harness.Descriptor, staged *cred.Staged, expected validate.ReviewExpectations, promptBytes []byte) (json.RawMessage, harness.Envelope, []ui.Line, error) {
	tmp, err := os.MkdirTemp("", "crossrev-review-")
	if err != nil {
		return nil, harness.Envelope{}, nil, err
	}
	defer os.RemoveAll(tmp)
	envelope, payload, msgs, err := l.runPrompt(ctx, req, loaded, settings, adapter, entry, staged, tmp, promptBytes, nil)
	return payload, envelope, msgs, err
}

// currentGeneration selects the current complete coverage generation for
// this base, head and engine from the trusted author's comments. An
// unreadable comment list is an error, never an empty ledger. No complete
// generation is not an error: the first pass starts from zero accepted
// verdicts and publishes the initial outstanding generation. Any other
// selection failure — corrupt, missing, reordered or future-schema bytes —
// fails the pass: re-reviewing from zero over coverage that cannot be read
// would hide the integrity failure behind wasted work.
func (l *Leg) currentGeneration(ctx context.Context, loaded Context, base, head core.Revision, engine string) (prstate.Generation, error) {
	store := ledgerStoreFor(l)
	if store == nil {
		return prstate.Generation{}, errNoLedgerStore{}
	}
	comments, err := store.CoverageComments(ctx, loaded.Repo, loaded.PR.Number)
	if err != nil {
		return prstate.Generation{}, err
	}
	gen, err := prstate.SelectGeneration(comments, loaded.Author, core.RevisionPair{Base: base, Head: head}, engine)
	if err != nil {
		if errors.Is(err, prstate.ErrNoCompleteGeneration) {
			return prstate.Generation{}, nil
		}
		return prstate.Generation{}, err
	}
	return gen, nil
}

// errNoLedgerStore reports a leg whose forge client does not implement the
// coverage ledger. Production always wires the orchestrator-facing client,
// which implements it; callers without one take the frozen single-prompt
// path instead of failing the pass.
type errNoLedgerStore struct{}

func (e errNoLedgerStore) Error() string {
	return "no ledger store on this leg"
}
