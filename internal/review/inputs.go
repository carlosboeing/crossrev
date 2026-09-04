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
	"github.com/carlosboeing/crossrev/internal/prstate"
	"github.com/carlosboeing/crossrev/internal/runlog"
	"github.com/carlosboeing/crossrev/internal/ui"
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
}

// VCS is the base-revision file reader. Production wires *vcs.Repository.
type VCS interface {
	Show(ctx context.Context, revision core.Revision, path string) ([]byte, vcs.FileStatus, error)
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
	// Validate checks the review payload. Nil means validate.Findings.
	Validate func([]byte) error
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
