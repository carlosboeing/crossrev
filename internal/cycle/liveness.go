package cycle

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/forge"
	"github.com/carlosboeing/crossrev/internal/prstate"
	"github.com/carlosboeing/crossrev/internal/vcs"
)

// statusLocalRunPrefix marks a run id written by a local leg, whose remainder
// is the pid (lib/run.sh:3291-3292). The shape of the run id picks the probe,
// not the configured mode: a marker written on a runner reads the same from a
// laptop.
const statusLocalRunPrefix = "local-"

// statusGoneLocally and statusGoneInWorkflow are the two reasons a `gone` is
// known (lib/run.sh:3334 and lib/run.sh:3348). They are the whole of what the
// row prints after the dash, so they are strings rather than a rendering
// decision made later.
const (
	statusGoneLocally    = "the process that started it is gone"
	statusGoneInWorkflow = "the workflow run finished without it"
)

// LivenessProbe answers whether the run behind one open claim is still working
// (lib/run.sh:3284-3352).
//
// Empty is a real answer and the common one. Anything that cannot be shown is
// not claimed: an unreadable lock, a run in another checkout, a pull request
// being watched from a machine that never held the lock.
//
// One probe answers for one pull request, and it keeps the last answer. Do not
// share one across two reports: the shell's globals are per process and a
// second `crossrev status` is a second process.
type LivenessProbe struct {
	// Repo and PR name what the claim belongs to: the repository for the
	// Actions read, the number for the lock file's name.
	Repo core.Slug
	PR   int

	// Forge answers the Actions API for a run id a workflow wrote.
	Forge forge.Forge

	// GitDir is `git rev-parse --git-common-dir` put through `pwd -P`
	// (lib/run.sh:3316-3318), which production wires to
	// (*vcs.Repository).CommonDir. The lock keys on the shared git directory
	// so that every working tree of a clone finds the same one.
	//
	// An error is the shell's `|| return 0`: nothing to key a lock on is not
	// a claim that the leg died.
	GitDir func(ctx context.Context) (string, error)

	// PIDAlive is `kill -0` (lib/run.sh:3331). Nil means the real signal.
	PIDAlive func(pid int) bool

	// Hostname is `hostname 2>/dev/null || printf 'local'`
	// (lib/run.sh:3327). Nil means this machine's own.
	Hostname func() string

	// askedFor, life and detail are _STATUS_LIVENESS_FOR, STATUS_LIVENESS and
	// STATUS_LIVENESS_DETAIL at lib/run.sh:3281-3283. The memo holds one run
	// id, exactly as the globals do, so a third question about an earlier
	// claim is asked again rather than answered from a cache the shell does
	// not have.
	askedFor string
	life     Life
	detail   string
}

// Alive answers for one claim, and remembers the answer (lib/run.sh:3284-3297).
//
// The memo is not an optimisation added here: one report asks about the same
// claim twice, once for its row and once for the NEXT line under it, and for an
// automated leg an unmemoised answer is a second Actions API call per row.
func (p *LivenessProbe) Alive(ctx context.Context, claim prstate.Marker) (Life, string) {
	runID := claim.RunID.Value()
	if runID == p.askedFor {
		return p.life, p.detail
	}
	p.askedFor = runID
	p.life, p.detail = LifeUnknown, ""
	if runID == "" {
		return p.life, p.detail
	}
	if pid, found := strings.CutPrefix(runID, statusLocalRunPrefix); found {
		p.life, p.detail = p.local(ctx, pid)
	} else {
		p.life, p.detail = p.workflow(ctx, runID)
	}
	return p.life, p.detail
}

// local answers from the lock file the run took over the same pull request
// (lib/run.sh:3311-3338).
//
// `kill -0` on the marker's pid alone would be cheaper, but it is only sound
// where the pid means what the marker meant by it. Pids are recycled and every
// machine has its own, so a bare probe from a second machine, or from the same
// one an hour later, can find a stranger's process and report a leg that died
// as running. The lock is crossrev's own record that this pid, on this host, is
// running against this pull request.
func (p *LivenessProbe) local(ctx context.Context, pid string) (Life, string) {
	if !statusIsPID(pid) {
		return LifeUnknown, ""
	}
	if p.GitDir == nil {
		return LifeUnknown, ""
	}
	gitdir, err := p.GitDir(ctx)
	if err != nil || gitdir == "" {
		return LifeUnknown, ""
	}
	// The same path run_lock_acquire writes (lib/run.sh:194).
	raw, err := os.ReadFile(filepath.Join(gitdir, "crossrev", "pr-"+strconv.Itoa(p.PR)+".lock"))
	if err != nil {
		return LifeUnknown, ""
	}
	// `holder="$(cat "$lock")"`, and a command substitution drops the
	// trailing newline run_lock_acquire wrote.
	holder := strings.TrimRight(string(raw), "\n")

	// `lock_pid="${holder%% *}"` compares the first field as text. Reading it
	// as a number instead would make a lock whose pid field is not one parse
	// to zero and match a marker that said `local-0`.
	lockPID, _, _ := strings.Cut(holder, " ")
	if lockPID != pid {
		return LifeUnknown, ""
	}

	// A checkout on a shared filesystem is the one case where the lock is
	// readable from a machine that cannot test the pid in it. Naming the host
	// is the whole of what is knowable, and it is enough to go and look.
	if host := statusLockHost(holder); host != "" && host != p.hostname() {
		return LifeElsewhere, host
	}
	if p.alive(pid) {
		return LifeRunning, ""
	}
	return LifeGone, statusGoneLocally
}

// workflow answers from the Actions API, which reaches a run from anywhere
// (lib/run.sh:3341-3352).
//
// `completed` is the useful half: the run is over and the marker never reached
// `complete`, so the leg died inside it however recently that was. Every other
// answer, the unreadable one included, leaves the row saying only what it knows.
func (p *LivenessProbe) workflow(ctx context.Context, runID string) (Life, string) {
	if p.Forge == nil {
		return LifeUnknown, ""
	}
	switch p.Forge.WorkflowRunStatus(ctx, p.Repo, runID) {
	case "queued", "in_progress", "requested", "waiting", "pending":
		return LifeRunning, ""
	case "completed":
		return LifeGone, statusGoneInWorkflow
	default:
		return LifeUnknown, ""
	}
}

// hostname is `hostname 2>/dev/null || printf 'local'` (lib/run.sh:3327).
func (p *LivenessProbe) hostname() string {
	if p.Hostname != nil {
		return p.Hostname()
	}
	name, err := os.Hostname()
	if err != nil || name == "" {
		return "local"
	}
	return name
}

// alive is `kill -0 "$pid" 2>/dev/null` (lib/run.sh:3331).
//
// Signal zero delivers nothing and reports whether the process could be
// signalled. A process owned by another user answers "permission denied", which
// the shell reads as a non-zero status and therefore as "not running", and so
// does this. It is the same probe vcs.AcquireRunLock makes, and it is repeated
// here rather than shared because internal/vcs does not export it.
func (p *LivenessProbe) alive(pid string) bool {
	n, err := strconv.Atoi(pid)
	if err != nil {
		return false
	}
	if p.PIDAlive != nil {
		return p.PIDAlive(n)
	}
	process, err := os.FindProcess(n)
	if err != nil {
		return false
	}
	return process.Signal(syscall.Signal(0)) == nil
}

// statusIsPID is `[[ "$pid" =~ ^[0-9]+$ ]]` (lib/run.sh:3313).
func statusIsPID(pid string) bool {
	if pid == "" {
		return false
	}
	for i := 0; i < len(pid); i++ {
		if pid[i] < '0' || pid[i] > '9' {
			return false
		}
	}
	return true
}

// statusLockHost is `rest="${holder#* on }"; lock_host="${rest%% since *}"`
// (lib/run.sh:3325).
//
// vcs.ParseHolder answers the same host for every line run_lock_acquire writes,
// and it is what reads the line everywhere else. It differs on one shape the
// probe can still be handed: a line with no ` on ` in it at all. Bash's
// `${holder#* on }` leaves such a line whole, so the shell reads the text
// before ` since ` as the host — a lock reading `4242` alone is reported as
// running elsewhere, on a host called `4242`. ParseHolder answers the empty
// string, which would send the probe on to `kill -0` instead. Both were
// measured against lib/run.sh, and parity is what decides it.
func statusLockHost(holder string) string {
	if _, _, found := strings.Cut(holder, " on "); found {
		return vcs.ParseHolder(holder).Host
	}
	before, _, _ := strings.Cut(holder, " since ")
	return before
}
