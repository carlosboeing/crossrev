package resolve

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/exec"
	"github.com/carlosboeing/crossrev/internal/forge"
	"github.com/carlosboeing/crossrev/internal/harness"
	"github.com/carlosboeing/crossrev/internal/prstate"
	"github.com/carlosboeing/crossrev/internal/runlog"
	"github.com/carlosboeing/crossrev/internal/ui"
	"github.com/carlosboeing/crossrev/internal/vcs"
)

// Outcome is how the resolve leg stopped.
type Outcome string

// The outcomes this unit can return. Publication, persistence and the push
// belong to a later unit.
const (
	// OutcomeInvoked means the resolver ran (or a resumed claim already held
	// its resolutions) and the payload validated.
	OutcomeInvoked Outcome = "invoked"
	// OutcomeAlreadyResolved means the current pass's resolve marker is
	// complete and not redrivable (lib/run.sh:1810-1813).
	OutcomeAlreadyResolved Outcome = "already_resolved"
	// OutcomeNoFindings means the review raised nothing to resolve
	// (lib/run.sh:1818-1836).
	OutcomeNoFindings Outcome = "no_findings"
	// OutcomeHalted means an empty review pass still has a standing
	// escalation (lib/run.sh:1828-1831).
	OutcomeHalted Outcome = "halted"
	// OutcomeStopped means crossrev/stop is on the pull request
	// (lib/run.sh:1782-1786).
	OutcomeStopped Outcome = "stopped"
	// OutcomeSkipped means an automatic invocation met a draft
	// (lib/run.sh:259-262, ctx_load return 2).
	OutcomeSkipped Outcome = "skipped"
	// OutcomeComplete means replies, persistence, the commit and the push
	// finished (lib/run.sh:2412-2458).
	OutcomeComplete Outcome = "complete"
	// OutcomeRefused means the leg died the way ui_die does.
	OutcomeRefused Outcome = "refused"
)

// Trigger is who asked for the leg (lib/run.sh:1731-1756).
type Trigger string

const (
	TriggerHuman     Trigger = "human"
	TriggerAutomatic Trigger = "automatic"
)

// Request is one resolve-leg invocation.
type Request struct {
	PR              int
	Repo            core.Slug
	Trigger         Trigger
	Harness         string
	Author          string
	KeepTranscripts bool
}

// Result is what Run returns after selection, claim, invocation, replies,
// persistence, commit and push.
type Result struct {
	Outcome     Outcome
	Pass        int
	Marker      prstate.Marker
	Resolutions json.RawMessage
	Envelope    harness.Envelope
	Prompt      []byte
	Invocation  harness.Invocation
	Message     string
	Messages    []ui.Line
	Err         error
}

// Refusal is the two strings ui_die prints (lib/ui.sh:113).
type Refusal struct {
	Message string
	Hint    string
}

func (r *Refusal) Error() string { return r.Message }

// Leg is the resolve orchestrator. The harness runner is a model-facing
// exec.NewOSRunner; git and gh already hold their own runners outside this
// package.
type Leg struct {
	Forge   forge.Forge
	Git     Git
	Runner  exec.Runner
	Log     *runlog.Log
	Clock   func() time.Time
	Env     []string
	Harness harness.Document
	// Adapter, when set, is used instead of harness.For. Tests inject one;
	// production leaves it nil.
	Adapter harness.Adapter
	// LookPath reports whether a harness binary is on PATH. Nil searches PATH
	// the way command -v does (lib/run.sh:524).
	LookPath func(string) (string, error)

	// reported is CROSSREV_LEG_REPORTED (lib/run.sh:725): the fatal record has
	// been written for this leg, and a second attempt must not write it again.
	reported bool
}

func (l *Leg) runner() exec.Runner {
	if l != nil && l.Runner != nil {
		return l.Runner
	}
	return exec.NewOSRunner()
}

// Git is the checkout the resolve leg uses. Production wraps *vcs.Repository
// through GitFrom. Git and gh already hold the orchestrator runner.
type Git interface {
	Dir() string
	WithDir(dir string) Git
	Show(ctx context.Context, revision core.Revision, path string) ([]byte, vcs.FileStatus, error)
	HasCommit(ctx context.Context, revision core.Revision) (bool, error)
	Head(ctx context.Context) (core.Revision, error)
	ConfigGet(ctx context.Context, key string) (string, error)
	AddWorktree(ctx context.Context, dir string, revision core.Revision) error
	WorktreeReusable(ctx context.Context, dir string, revision core.Revision) (bool, error)
	PruneWorktrees(ctx context.Context)
	Fetch(ctx context.Context, remote, refspec string) error
	ResolvePushRepo(ctx context.Context, remote string) (vcs.PushTarget, error)
	CaptureTree(ctx context.Context, indexPath string) (string, error)
	RestoreTree(ctx context.Context, indexPath, tree string) error
	LogSubjects(ctx context.Context, revision core.Revision) ([]byte, error)
	StageAll(ctx context.Context)
	HasStagedChanges(ctx context.Context) (bool, error)
	Commit(ctx context.Context, options vcs.CommitOptions) error
	Push(ctx context.Context, remote, branch string, runHooks bool) error
	PushURL(ctx context.Context, remote string) (string, error)
	RemoteHead(ctx context.Context, url, branch string) (string, error)
	RemoveWorktree(ctx context.Context, dir string) error
}

// GitFrom wraps a *vcs.Repository as Git.
func GitFrom(r *vcs.Repository) Git { return repoGit{r} }

type repoGit struct{ repo *vcs.Repository }

func (g repoGit) Dir() string { return g.repo.Dir() }
func (g repoGit) WithDir(dir string) Git {
	return repoGit{g.repo.Git().At(dir)}
}
func (g repoGit) Show(ctx context.Context, revision core.Revision, path string) ([]byte, vcs.FileStatus, error) {
	return g.repo.Show(ctx, revision, path)
}
func (g repoGit) HasCommit(ctx context.Context, revision core.Revision) (bool, error) {
	return g.repo.HasCommit(ctx, revision)
}
func (g repoGit) Head(ctx context.Context) (core.Revision, error) { return g.repo.Head(ctx) }
func (g repoGit) ConfigGet(ctx context.Context, key string) (string, error) {
	return g.repo.ConfigGet(ctx, key)
}
func (g repoGit) AddWorktree(ctx context.Context, dir string, revision core.Revision) error {
	return g.repo.AddWorktree(ctx, dir, revision)
}
func (g repoGit) WorktreeReusable(ctx context.Context, dir string, revision core.Revision) (bool, error) {
	return g.repo.WorktreeReusable(ctx, dir, revision)
}
func (g repoGit) PruneWorktrees(ctx context.Context) { g.repo.PruneWorktrees(ctx) }
func (g repoGit) Fetch(ctx context.Context, remote, refspec string) error {
	args := []string{"fetch", remote}
	if refspec != "" {
		args = append(args, refspec)
	}
	out, err := g.repo.Run(ctx, args...)
	if err != nil {
		return err
	}
	if !out.OK() {
		return fmt.Errorf("git fetch: %s", out.Stderr)
	}
	return nil
}
func (g repoGit) ResolvePushRepo(ctx context.Context, remote string) (vcs.PushTarget, error) {
	return g.repo.ResolvePushRepo(ctx, remote)
}
func (g repoGit) CaptureTree(ctx context.Context, indexPath string) (string, error) {
	return g.repo.CaptureTree(ctx, indexPath)
}
func (g repoGit) RestoreTree(ctx context.Context, indexPath, tree string) error {
	return g.repo.RestoreTree(ctx, indexPath, tree)
}
func (g repoGit) LogSubjects(ctx context.Context, revision core.Revision) ([]byte, error) {
	out, err := g.repo.Run(ctx, "log", "--format=%ae%x09%s", revision.SHA())
	if err != nil {
		return nil, err
	}
	if !out.OK() {
		return nil, nil
	}
	return []byte(out.Stdout), nil
}
func (g repoGit) StageAll(ctx context.Context) { g.repo.StageAll(ctx) }
func (g repoGit) HasStagedChanges(ctx context.Context) (bool, error) {
	return g.repo.HasStagedChanges(ctx)
}
func (g repoGit) Commit(ctx context.Context, options vcs.CommitOptions) error {
	return g.repo.Commit(ctx, options)
}
func (g repoGit) Push(ctx context.Context, remote, branch string, runHooks bool) error {
	return g.repo.Push(ctx, remote, branch, runHooks)
}
func (g repoGit) PushURL(ctx context.Context, remote string) (string, error) {
	return g.repo.PushURL(ctx, remote)
}
func (g repoGit) RemoteHead(ctx context.Context, url, branch string) (string, error) {
	return g.repo.RemoteHead(ctx, url, branch)
}
func (g repoGit) RemoveWorktree(ctx context.Context, dir string) error {
	out, err := g.repo.RunCombined(ctx, "worktree", "remove", "--force", dir)
	if err != nil || !out.OK() {
		_ = os.RemoveAll(dir)
	}
	_ = os.Remove(filepath.Dir(dir))
	return nil
}

func refuse(msg, hint string) Result {
	return Result{Outcome: OutcomeRefused, Err: &Refusal{Message: msg, Hint: hint}}
}

func (l *Leg) now() time.Time {
	if l.Clock != nil {
		return l.Clock()
	}
	return time.Now()
}
