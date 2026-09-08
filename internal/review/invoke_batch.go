package review

import (
	"context"
	"encoding/json"
	"os"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/cred"
	"github.com/carlosboeing/crossrev/internal/harness"
	"github.com/carlosboeing/crossrev/internal/intel"
	"github.com/carlosboeing/crossrev/internal/prompt"
	"github.com/carlosboeing/crossrev/internal/prstate"
	"github.com/carlosboeing/crossrev/internal/ui"
	"github.com/carlosboeing/crossrev/internal/validate"
	"github.com/carlosboeing/crossrev/internal/vcs"
)

// vcsRepo returns the git checkout behind the leg's file reads, or nil in
// tests that stub the reads. Production wires *vcs.Repository.
func vcsRepo(l *Leg) *vcs.Repository {
	if l == nil {
		return nil
	}
	repo, _ := l.VCS.(*vcs.Repository)
	return repo
}

// ledgerStoreFor returns the coverage ledger over the leg's forge client, or
// nil when the client does not implement the store contract.
func ledgerStoreFor(l *Leg) prstate.LedgerStore {
	if l == nil || l.Forge == nil {
		return nil
	}
	store, _ := l.Forge.(prstate.LedgerStore)
	return store
}

// renderBatchPrompt renders one batch's complete prompt — headers, prior
// context and file content together — for the byte budget the packing
// measures. It renders through the same Review value the invoke path sends,
// so the measured bytes are the sent bytes.
func (l *Leg) renderBatchPrompt(ctx context.Context, req Request, loaded Context, settings legSettings, pass int, files []intel.FileUnit, scope intel.Scope) []byte {
	expected, units := batchExpectations(files, scope.Base, scope.Head)
	_ = expected
	advisory := intel.AdvisoryFiles(ctx, scope, scopeSearcher{vcs: l.VCS})
	advisoryRefs, excludedRefs := advisoryPromptRefs(scope, advisory)
	diffBytes, _ := l.reviewDiff(ctx, loaded)
	return prompt.Review{
		Skill:    prompt.ReviewSkill(),
		Diff:     diffBytes,
		Meta:     reviewMeta(loaded, req, pass),
		Prior:    priorFindings(loaded),
		Threads:  promptThreads(l.Forge.ReviewThreads(ctx, loaded.Repo, req.PR)),
		ReviewMD: loaded.ReviewMD,
		Batch:    units,
		Advisory: advisoryRefs,
		Excluded: excludedRefs,
	}.Render()
}

// invokeBatch invokes the reviewer for one numbered batch and validates the
// answer against the batch's own expectations. A semantic failure gets one
// retry whose prompt names the exact missing, duplicate and unknown unit
// numbers; a second failure is fatal and publishes nothing.
func (l *Leg) invokeBatch(ctx context.Context, req Request, loaded Context, settings legSettings, pass int, units []prompt.BatchUnit, advisory []prompt.AdvisoryRef, excluded []prompt.ExclusionRef, expected validate.ReviewExpectations) (json.RawMessage, harness.Envelope, []ui.Line, error) {
	diffBytes, err := l.reviewDiff(ctx, loaded)
	if err != nil {
		return nil, harness.Envelope{}, nil, err
	}
	promptBytes := prompt.Review{
		Skill:    prompt.ReviewSkill(),
		Diff:     diffBytes,
		Meta:     reviewMeta(loaded, req, pass),
		Prior:    priorFindings(loaded),
		Threads:  promptThreads(l.Forge.ReviewThreads(ctx, loaded.Repo, req.PR)),
		ReviewMD: loaded.ReviewMD,
		Batch:    units,
		Advisory: advisory,
		Excluded: excluded,
	}.Render()
	return l.invokePrompt(ctx, req, loaded, settings, expected, promptBytes)
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

// checkBatchPayload validates one batch answer against its own expectations
// through the leg's seam: the injected substitute in tests, the semantic
// check in production. Empty output is a shape error; every rejected answer
// adds no disposition.
func (l *Leg) checkBatchPayload(payload []byte, expected validate.ReviewExpectations) error {
	if l != nil && l.Validate != nil {
		return l.Validate(payload, expected)
	}
	return validate.Review(payload, expected)
}

// currentGeneration selects the current complete coverage generation for
// this base, head and engine from the trusted author's comments. An
// unreadable comment list is an error, never an empty ledger. No complete
// generation is not an error: the first pass starts from zero accepted
// dispositions and publishes the initial outstanding generation.
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
		return prstate.Generation{}, nil
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

var _ = intel.MaxFilesPerBatch
var _ core.UnitID
