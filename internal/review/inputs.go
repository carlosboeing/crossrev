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
	PR              int
	Repo            core.Slug
	Trigger         Trigger
	Continuation    bool
	HarnessOverride string
	Author          string
	Workdir         string
	RunID           string
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
type VCS interface {
	Show(ctx context.Context, revision core.Revision, path string) ([]byte, vcs.FileStatus, error)
	ChangedFiles(ctx context.Context, base, head core.Revision) ([]core.FileChange, error)
	ExactSearch(ctx context.Context, revision core.Revision, term string, limit int) ([]vcs.SearchHit, bool, error)
	RangeDiff(ctx context.Context, base, head core.Revision) ([]byte, error)
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
	// finding references, valid evidence revisions and spans, evidence
	// for not_affected, and failed-fallback reasons for could_not_review.
	// Tests that drive the retry budgets without a batch set a substitute
	// directly.
	Validate func(payload []byte, expected validate.ReviewExpectations) error
	// Expect holds the numbered batch units this leg's payload must cover:
	// the positions 1 to len(Units) by prompt order, with the base and head
	// the batch was built between. The batch loop fills it (C1 wires that
	// loop); empty means the frozen prompt with no batch input.
	Expect validate.ReviewExpectations
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
