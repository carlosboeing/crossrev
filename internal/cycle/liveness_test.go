package cycle_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/cycle"
	"github.com/carlosboeing/crossrev/internal/forge"
	"github.com/carlosboeing/crossrev/internal/prstate"
)

// livenessMarker is one open claim carrying a run id, which is the only field
// the probe reads (lib/run.sh:3286).
func livenessMarker(t *testing.T, runID string) prstate.Marker {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"v": 1, "leg": "review", "pass": 1, "state": "started",
		"ts": 1787000000, "run_id": runID, "head_sha": statusBase,
	})
	if err != nil {
		t.Fatalf("building the marker: %v", err)
	}
	marker, err := prstate.ParseMarker(body)
	if err != nil {
		t.Fatalf("parsing the marker: %v", err)
	}
	return marker
}

// livenessForge counts the Actions reads, because the memo is a claim about how
// many of them one report makes and nothing else can see it.
type livenessForge struct {
	statusForge
	status forge.RunStatus
	calls  int
	asked  []string
}

func (f *livenessForge) WorkflowRunStatus(_ context.Context, _ core.Slug, runID string) forge.RunStatus {
	f.calls++
	f.asked = append(f.asked, runID)
	return f.status
}

// livenessLock writes the line run_lock_acquire leaves in the git dir,
// `<pid> on <host> since <timestamp>` (lib/run.sh:213-215), and answers the
// directory it wrote it under.
func livenessLock(t *testing.T, pr int, line string) string {
	t.Helper()
	gitdir := t.TempDir()
	if line == "" {
		return gitdir
	}
	if err := os.MkdirAll(filepath.Join(gitdir, "crossrev"), 0o755); err != nil {
		t.Fatalf("making the lock directory: %v", err)
	}
	path := filepath.Join(gitdir, "crossrev", "pr-"+strconv.Itoa(pr)+".lock")
	if err := os.WriteFile(path, []byte(line+"\n"), 0o644); err != nil {
		t.Fatalf("writing the lock: %v", err)
	}
	return gitdir
}

func livenessProbe(gitdir string, f forge.Forge) *cycle.LivenessProbe {
	slug, _ := core.ParseSlug(statusRepo)
	return &cycle.LivenessProbe{
		Repo:  slug,
		PR:    statusPR,
		Forge: f,
		GitDir: func(context.Context) (string, error) {
			if gitdir == "" {
				return "", errors.New("not a repository")
			}
			return gitdir, nil
		},
	}
}

// TestLivenessLocal covers every arm of _status_liveness_local
// (lib/run.sh:3311-3338): the lock has to exist, name the marker's pid, and
// name this host before `kill -0` is asked anything at all.
func TestLivenessLocal(t *testing.T) {
	const host = "buildbox"
	cases := []struct {
		name       string
		runID      string
		lock       string
		alive      bool
		hostname   string
		wantLife   cycle.Life
		wantDetail string
	}{
		{
			name: "a pid the lock names and the host answers for is running",
			// The lock is crossrev's own record that this pid, on this
			// host, is running against this pull request.
			runID: "local-4242", lock: "4242 on runner since 2026-08-12T00:00:00Z",
			alive: true, hostname: "runner", wantLife: cycle.LifeRunning,
		},
		{
			name:  "a pid nothing is behind is provably over, however recent",
			runID: "local-4242", lock: "4242 on runner since 2026-08-12T00:00:00Z",
			alive: false, hostname: "runner",
			wantLife: cycle.LifeGone, wantDetail: "the process that started it is gone",
		},
		{
			name: "a lock on another host names the host and claims nothing else",
			// A checkout on a shared filesystem is the one case where the
			// lock is readable from a machine that cannot test the pid.
			runID: "local-4242", lock: "4242 on " + host + " since 2026-08-12T00:00:00Z",
			alive: true, hostname: "runner",
			wantLife: cycle.LifeElsewhere, wantDetail: host,
		},
		{
			name:  "no lock at all is no evidence either way",
			runID: "local-4242", lock: "", alive: true, hostname: "runner",
			wantLife: cycle.LifeUnknown,
		},
		{
			name: "a lock held by another pid says nothing about this claim",
			// Pids are recycled and every machine has its own, so a bare
			// probe could find a stranger's process and print "running now"
			// over a leg that died.
			runID: "local-4242", lock: "9999 on runner since 2026-08-12T00:00:00Z",
			alive: true, hostname: "runner", wantLife: cycle.LifeUnknown,
		},
		{
			name:  "a run id that is not a pid is not probed",
			runID: "local-abc", lock: "abc on runner since 2026-08-12T00:00:00Z",
			alive: true, hostname: "runner", wantLife: cycle.LifeUnknown,
		},
		{
			// Measured, not assumed. `${holder#* on }` leaves a line with
			// no ` on ` in it whole, so the shell reads what is left as the
			// host and reports a run elsewhere on a host called 4242. It is
			// a shape run_lock_acquire never writes, and it is what the
			// shell does with it.
			name:  "a lock line with no host field is read as a host anyway",
			runID: "local-4242", lock: "4242",
			alive: true, hostname: "runner",
			wantLife: cycle.LifeElsewhere, wantDetail: "4242",
		},
		{
			// An empty host field is not another machine, so the pid is
			// probed rather than the run being reported as elsewhere.
			name:  "an empty host field falls through to the pid",
			runID: "local-4242", lock: "4242 on  since 2026-08-12T00:00:00Z",
			alive: false, hostname: "runner",
			wantLife: cycle.LifeGone, wantDetail: "the process that started it is gone",
		},
		{
			name:  "a lock line with no timestamp still names its host",
			runID: "local-4242", lock: "4242 on runner",
			alive: false, hostname: "runner",
			wantLife: cycle.LifeGone, wantDetail: "the process that started it is gone",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gitdir := livenessLock(t, statusPR, c.lock)
			f := &livenessForge{}
			probe := livenessProbe(gitdir, f)
			probe.PIDAlive = func(int) bool { return c.alive }
			probe.Hostname = func() string { return c.hostname }

			life, detail := probe.Alive(context.Background(), livenessMarker(t, c.runID))
			if life != c.wantLife || detail != c.wantDetail {
				t.Errorf("life=%q detail=%q, want %q %q",
					life, detail, c.wantLife, c.wantDetail)
			}
			if f.calls != 0 {
				t.Errorf("a local run id asked the Actions API %d time(s)", f.calls)
			}
		})
	}
}

// TestLivenessWithNowhereToKeepALock pins the `|| return 0` at
// lib/run.sh:3316-3318: a directory that is not a repository has nowhere to
// keep a lock, and that is not a claim that the leg died.
//
// Both arms put a lock the probe would answer `running` from where it would
// find it, so a probe that ignored the failed read would report that instead of
// admitting it has no evidence.
func TestLivenessWithNowhereToKeepALock(t *testing.T) {
	pid := strconv.Itoa(os.Getpid())
	line := pid + " on " + hostOrSkip(t) + " since 2026-08-12T00:00:00Z"

	t.Run("the git dir could not be read", func(t *testing.T) {
		gitdir := livenessLock(t, statusPR, line)
		probe := livenessProbe(gitdir, &livenessForge{})
		probe.GitDir = func(context.Context) (string, error) {
			return gitdir, errors.New("not a repository")
		}
		life, detail := probe.Alive(context.Background(), livenessMarker(t, "local-"+pid))
		if life != cycle.LifeUnknown || detail != "" {
			t.Errorf("life=%q detail=%q, want unknown and no detail", life, detail)
		}
	})

	t.Run("the git dir came back empty", func(t *testing.T) {
		// An empty answer would make the lock path relative, so the probe
		// would read whatever `crossrev/pr-42.lock` the process happens to
		// be standing next to. This test stands next to one.
		t.Chdir(livenessLock(t, statusPR, line))
		probe := livenessProbe("", &livenessForge{})
		probe.GitDir = func(context.Context) (string, error) { return "", nil }
		life, detail := probe.Alive(context.Background(), livenessMarker(t, "local-"+pid))
		if life != cycle.LifeUnknown || detail != "" {
			t.Errorf("life=%q detail=%q, want unknown and no detail", life, detail)
		}
	})
}

// hostOrSkip is this machine's own name, which the probe compares the lock's
// host against when nothing is injected.
func hostOrSkip(t *testing.T) string {
	t.Helper()
	host, err := os.Hostname()
	if err != nil || host == "" {
		t.Skip("this machine will not name itself, which is the `|| printf local` arm")
	}
	return host
}

// TestLivenessProbesTheRealProcessAndHost goes through the probe with nothing
// injected: the real `kill -0` and the real hostname, against a lock this test
// wrote for its own process.
//
// The table above answers both from a function it hands in, so a defect in
// either default would pass every case in it.
func TestLivenessProbesTheRealProcessAndHost(t *testing.T) {
	host := hostOrSkip(t)
	pid := os.Getpid()
	gitdir := livenessLock(t, statusPR,
		strconv.Itoa(pid)+" on "+host+" since 2026-08-12T00:00:00Z")

	probe := livenessProbe(gitdir, &livenessForge{})
	life, detail := probe.Alive(context.Background(),
		livenessMarker(t, "local-"+strconv.Itoa(pid)))
	if life != cycle.LifeRunning || detail != "" {
		t.Fatalf("this test's own process: life=%q detail=%q, want running", life, detail)
	}
}

// TestLivenessWorkflow is _status_liveness_workflow (lib/run.sh:3341-3352).
// `completed` is the useful half: the run is over and the marker never reached
// `complete`, so the leg died inside it however recently that was.
func TestLivenessWorkflow(t *testing.T) {
	cases := []struct {
		status     forge.RunStatus
		wantLife   cycle.Life
		wantDetail string
	}{
		{status: "queued", wantLife: cycle.LifeRunning},
		{status: "in_progress", wantLife: cycle.LifeRunning},
		{status: "requested", wantLife: cycle.LifeRunning},
		{status: "waiting", wantLife: cycle.LifeRunning},
		{status: "pending", wantLife: cycle.LifeRunning},
		{status: "completed", wantLife: cycle.LifeGone,
			wantDetail: "the workflow run finished without it"},
		// The API not answering is not evidence of death: a run in another
		// repository, a token without actions:read and no network all look
		// the same from here.
		{status: "", wantLife: cycle.LifeUnknown},
		{status: "cancelled", wantLife: cycle.LifeUnknown},
	}
	for _, c := range cases {
		t.Run(string(c.status), func(t *testing.T) {
			f := &livenessForge{status: c.status}
			probe := livenessProbe(t.TempDir(), f)
			life, detail := probe.Alive(context.Background(), livenessMarker(t, "55501"))
			if life != c.wantLife || detail != c.wantDetail {
				t.Errorf("life=%q detail=%q, want %q %q",
					life, detail, c.wantLife, c.wantDetail)
			}
			if f.calls != 1 || f.asked[0] != "55501" {
				t.Errorf("asked the API %v, want one read of 55501", f.asked)
			}
		})
	}
}

// TestLivenessWithoutARunIDAsksNothing pins lib/run.sh:3290: a marker with no
// run id has nothing to probe, and neither the API nor the lock is read for it.
//
// The probe is asked about a real run id first, because an empty run id is the
// value the memo starts on and a probe that had never been asked anything would
// short-circuit before reaching the guard.
func TestLivenessWithoutARunIDAsksNothing(t *testing.T) {
	f := &livenessForge{status: "in_progress"}
	probe := livenessProbe(t.TempDir(), f)
	reads := 0
	probe.GitDir = func(context.Context) (string, error) { reads++; return t.TempDir(), nil }

	if life, _ := probe.Alive(context.Background(), livenessMarker(t, "55501")); life != cycle.LifeRunning {
		t.Fatalf("the first claim answered %q, want running", life)
	}
	life, detail := probe.Alive(context.Background(), livenessMarker(t, ""))
	if life != cycle.LifeUnknown || detail != "" {
		t.Errorf("life=%q detail=%q, want unknown and no detail", life, detail)
	}
	if f.calls != 1 || reads != 0 {
		t.Errorf("%d API read(s) and %d git dir read(s), want one and none", f.calls, reads)
	}
}

// TestLivenessMemoisesOnTheRunID pins the memo at lib/run.sh:3287-3288.
//
// One report asks about the same claim twice — once for its row and once for
// the NEXT line under it — and for an automated leg an unmemoised answer is a
// second Actions API call per row.
func TestLivenessMemoisesOnTheRunID(t *testing.T) {
	f := &livenessForge{status: "in_progress"}
	probe := livenessProbe(t.TempDir(), f)
	marker := livenessMarker(t, "55501")

	for i := 0; i < 3; i++ {
		life, _ := probe.Alive(context.Background(), marker)
		if life != cycle.LifeRunning {
			t.Fatalf("read %d answered %q, want running", i, life)
		}
	}
	if f.calls != 1 {
		t.Errorf("%d API reads for one run id, want 1", f.calls)
	}

	// A different claim is a different question, and the memo holds one.
	f.status = "completed"
	life, detail := probe.Alive(context.Background(), livenessMarker(t, "55502"))
	if life != cycle.LifeGone || detail != "the workflow run finished without it" {
		t.Errorf("second run id: life=%q detail=%q, want gone", life, detail)
	}
	if f.calls != 2 {
		t.Errorf("%d API reads for two run ids, want 2", f.calls)
	}
}

// TestLivenessRemembersOnlyTheLastRunID pins the shape of the memo rather than
// only its effect: lib/run.sh:3283-3288 keeps one run id in a global, so a
// third question about the first claim is asked again.
//
// A map would answer it from the cache and make one fewer API call than the
// shell, which is a divergence in what the command does to the network.
func TestLivenessRemembersOnlyTheLastRunID(t *testing.T) {
	f := &livenessForge{status: "in_progress"}
	probe := livenessProbe(t.TempDir(), f)
	first := livenessMarker(t, "55501")

	probe.Alive(context.Background(), first)
	probe.Alive(context.Background(), livenessMarker(t, "55502"))
	probe.Alive(context.Background(), first)

	want := []string{"55501", "55502", "55501"}
	if len(f.asked) != len(want) {
		t.Fatalf("asked %v, want %v", f.asked, want)
	}
	for i := range want {
		if f.asked[i] != want[i] {
			t.Fatalf("asked %v, want %v", f.asked, want)
		}
	}
}

// TestLivenessSatisfiesTheSeam is the compile-time check that the probe is what
// Status.Liveness takes, so the two halves cannot drift apart silently.
func TestLivenessSatisfiesTheSeam(t *testing.T) {
	var _ cycle.Liveness = &cycle.LivenessProbe{}
}
