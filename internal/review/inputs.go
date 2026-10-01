package review

import (
	"context"
	"encoding/json"
	"time"

	"github.com/carlosboeing/crossrev/internal/config"
	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/exec"
	"github.com/carlosboeing/crossrev/internal/forge"
	"github.com/carlosboeing/crossrev/internal/harness"
	"github.com/carlosboeing/crossrev/internal/intel"
	"github.com/carlosboeing/crossrev/internal/prstate"
	"github.com/carlosboeing/crossrev/internal/runlog"
	"github.com/carlosboeing/crossrev/internal/ui"
	"github.com/carlosboeing/crossrev/internal/validate"
	"github.com/carlosboeing/crossrev/internal/vcs"
)

// Trigger is who asked for the leg (lib/run.sh:943-945).
type Trigger string

const (
	TriggerHuman     Trigger = "human"
	TriggerAutomatic Trigger = "automatic"
)

// Request is one review-leg invocation.
type Request struct {
	PR                  int
	Repo                core.Slug
	Trigger             Trigger
	Continuation        bool
	HarnessOverride     string
	ModelOverride       string
	EffortOverride      string
	InputPolicyOverride string
	ConcernsOverride    string
	CheckOverride       string
	RequiredChecks      []string
	NoRequiredChecks    bool
	CheckWait           string
	Author              string
	// Workdir overrides the pinned worktree: empty pins a clean detached
	// worktree at the pull request head, set runs the harness there after
	// proving its HEAD is the head.
	Workdir string
	RunID   string
}

// Outcome is how the leg stopped.
type Outcome string

const (
	OutcomeInvoked  Outcome = "invoked"
	OutcomeSkipped  Outcome = "skipped"
	OutcomeDeclined Outcome = "declined"
	OutcomeHalted   Outcome = "halted"
	OutcomeError    Outcome = "error"
)

// Result is what Leg.Run reports.
type Result struct {
	Outcome  Outcome
	Reason   string
	Pass     int
	ClaimID  int64
	Marker   prstate.Marker
	Context  Context
	Envelope *harness.Envelope
	Payload  json.RawMessage
	Messages []ui.Line
	// Nudge asks the caller to print the upgrade tip. run_upgrade_nudge is a
	// terminal write and a leg holds no terminal, so the decision travels and
	// the composition root does the printing (lib/run.sh:1325-1330).
	Nudge bool
	Err   error
	// Covered carries a fully covered batch pass's findings and scope
	// claims to the enrich-and-publish path. Nil on the frozen path and on
	// a bounded halt.
	Covered any
	// KeptWorktree is the pinned worktree a failed leg kept for debugging:
	// the directory selectWorktree actually selected, not the canonical
	// path it was selected from. Empty when the leg created none (an
	// explicit workdir override stays the operator's), when the finish
	// was clean (the leg-end hook removed it), or when the leg never
	// reached the worktree. The composition root reports exactly this
	// path, so it never names a preserved occupant this run worked beside.
	KeptWorktree string
}

// Context is the one base/head load a review starts from (lib/run.sh:233-319).
type Context struct {
	Repo              core.Slug
	PR                forge.PullRequest
	DefaultBranch     string
	Config            *config.Config
	Author            string
	Markers           []prstate.Marker
	ReviewMD          []byte
	GitMessage        []byte
	ProjectMapTracker string
	Backlog           config.Backlog
	// Scope is the deterministic required file set for the current base and
	// head under the file engine, built before the first model call. Nil in
	// runs that carry the frozen prompt with no batch input.
	Scope *intel.Scope
}

// VCS is the base-revision file reader. Production wires *vcs.Repository.
//
// The second half is the pinned-worktree surface the review leg shares with
// the resolve leg: the head fetch fallbacks, the detached worktree at the
// pull request head, and the HEAD proof that an explicit workdir override
// sits at that head.
type VCS interface {
	Show(ctx context.Context, revision core.Revision, path string) ([]byte, vcs.FileStatus, error)
	ChangedFiles(ctx context.Context, base, head core.Revision) ([]core.FileChange, error)
	ChangedLines(ctx context.Context, base, head core.Revision) ([]byte, error)
	SearchAll(ctx context.Context, revision core.Revision, terms []string, limit int) ([]vcs.TermResult, error)
	RangeDiff(ctx context.Context, base, head core.Revision) ([]byte, error)
	GeneratedAttributes(ctx context.Context, base core.Revision, paths []string) (map[string]vcs.AttributeDecision, *vcs.Warning, error)
	RemovePersistedCredentials(ctx context.Context) ([]vcs.RemovedCredential, error)
	HasCommit(ctx context.Context, revision core.Revision) (bool, error)
	HeadAt(ctx context.Context, dir string) (core.Revision, error)
	ConfigGet(ctx context.Context, key string) (string, error)
	Fetch(ctx context.Context, remote, refspec string) error
	WorktreeReusable(ctx context.Context, dir string, revision core.Revision) (bool, error)
	WorktreeClean(ctx context.Context, dir string) (bool, error)
	AddWorktree(ctx context.Context, dir string, revision core.Revision) error
	RemoveWorktree(ctx context.Context, dir string) error
	PruneWorktrees(ctx context.Context)
}

// intelAttributeDecisions maps the VCS attribute answer onto discovery's
// vocabulary. intel imports nothing effectful and vcs imports nothing about
// review scope, so the conversion lives with the leg.
func intelAttributeDecisions(attrs map[string]vcs.AttributeDecision) map[string]intel.AttributeDecision {
	if len(attrs) == 0 {
		return nil
	}
	out := make(map[string]intel.AttributeDecision, len(attrs))
	for path, decision := range attrs {
		switch decision {
		case vcs.AttributeSet:
			out[path] = intel.AttributeSet
		case vcs.AttributeNegated:
			out[path] = intel.AttributeNegated
		default:
			out[path] = intel.AttributeUnspecified
		}
	}
	return out
}

// Leg is the review orchestrator. Dependencies are injected.
//
// The harness child is started through Runner. Production sets it to
// exec.NewOSRunner, which refuses a forge credential.
type Leg struct {
	Forge   forge.Forge
	VCS     VCS
	Config  *config.Config
	Harness harness.Document
	Log     *runlog.Log
	Now     func() time.Time
	Runner  exec.Runner
	Env     []string
	// LookPath reports whether a harness binary is on PATH. Nil searches PATH
	// the way command -v does (lib/run.sh:530).
	LookPath func(string) (string, error)
	// Validate checks the review payload. Nil means Review against the
	// leg's own batch expectations: exact unit-number coverage, valid
	// finding references, valid evidence paths and spans, evidence
	// for not_affected, and failed-fallback reasons for could_not_review.
	// The model's evidence revision is not checked: the reviewed revision
	// is recorded on the verdict when it is accepted.
	// Tests that drive the retry budgets without a batch set a substitute
	// directly.
	Validate func(payload []byte, expected validate.ReviewExpectations) error
	// Expect holds the numbered batch units this leg's payload must cover:
	// the positions 1 to len(Units) by prompt order, with the base and head
	// the batch was built between. The batch loop fills it (C1 wires that
	// loop); empty means the frozen prompt with no batch input.
	Expect validate.ReviewExpectations
	// Progress receives each accepted batch's progress line while the pass
	// runs. A leg holds no terminal, so production wires this to print
	// immediately and the line reports while later batches still run. Nil
	// queues the line in Result.Messages instead, the way every other leg
	// line travels.
	Progress func(ui.Line)

	// readsNotes records one entry per review call for the marker and
	// generation reads envelopes finalized later in the pass. Calls run
	// sequentially, so the slice needs no mutex.
	readsNotes []readsNote
}

func (l *Leg) now() time.Time {
	if l != nil && l.Now != nil {
		return l.Now()
	}
	return time.Now()
}

func (l *Leg) runner() exec.Runner {
	if l != nil && l.Runner != nil {
		return l.Runner
	}
	return exec.NewOSRunner()
}

func (l *Leg) show() config.ShowFile {
	return func(ctx context.Context, revision core.Revision, path string) ([]byte, config.FileStatus, error) {
		if l == nil || l.VCS == nil {
			return nil, config.NotFound, nil
		}
		body, status, err := l.VCS.Show(ctx, revision, path)
		return body, config.FileStatus(status), err
	}
}
