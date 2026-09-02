package main_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	crexec "github.com/carlosboeing/crossrev/internal/exec"
)

// The names a child started here may inherit, spelled out rather than taken
// from this process wholesale. See the note in binary().
var (
	toolchainEnvironment = []string{
		"PATH", "HOME", "TMPDIR", "GOTOOLCHAIN",
		"GOPATH", "GOCACHE", "GOMODCACHE", "GOFLAGS", "GOPROXY", "GONOSUMDB", "GONOSUMCHECK",
	}
	childEnvironment = []string{"PATH", "HOME", "TMPDIR"}
)

// The binary, built once for every case here.
//
// It is built rather than called in-process because what these cases are about
// is the composition root: which runner reaches which child, which stream a
// line lands on, and what status the process ends with. None of that is
// observable from a function call.
func binary(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("building the binary is not a short test")
	}
	built := filepath.Join(t.TempDir(), "crossrev")
	build := exec.Command("go", "build", "-o", built, ".")
	// Named, not inherited. internal/archtest confines every bulk read of the
	// environment to internal/exec/env.go, and it scans test files too — which
	// is the rule working rather than getting in the way: os.Environ() here
	// would be the fourth reader of the whole environment in this tree.
	build.Env = append(crexec.Inherit(toolchainEnvironment), "GOTOOLCHAIN=go1.27.0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building the binary: %v\n%s", err, out)
	}
	return built
}

type run struct {
	stdout string
	stderr string
	status int
}

func invoke(t *testing.T, bin string, env []string, args ...string) run {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Env = append(append(crexec.Inherit(childEnvironment), "NO_COLOR=1"), env...)
	var out, errOut strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	cmd.Stdin = nil
	err := cmd.Run()
	status := 0
	var exitErr *exec.ExitError
	if err != nil {
		if !errorAs(err, &exitErr) {
			t.Fatalf("running %v: %v", args, err)
		}
		status = exitErr.ExitCode()
	}
	return run{stdout: out.String(), stderr: errOut.String(), status: status}
}

func errorAs(err error, target **exec.ExitError) bool {
	e, ok := err.(*exec.ExitError)
	if ok {
		*target = e
	}
	return ok
}

// One command per package, through the built binary, asserting all three of
// stdout, stderr and the status — the way tests/harness.sh drives the shell.
//
// These are the commands that answer without a repository, a network or a
// harness. The ones that need all three are the CLI-driven shell suites' job:
// scripts/test-native.sh points them at this same binary.
func TestTheBinaryAnswersOneCommandPerPackage(t *testing.T) {
	bin := binary(t)

	for _, row := range []struct {
		name       string
		args       []string
		wantStatus int
		stdoutHas  string
		stderrHas  string
		stdoutOnly bool
	}{
		{
			name:       "version, from internal/cli's embedded asset",
			args:       []string{"version"},
			stdoutHas:  readVersion(t),
			stdoutOnly: true,
		},
		{
			name:       "help, with the descriptor's harness names",
			args:       []string{"help"},
			stdoutHas:  "--harness <one of: claude|codex|agy|grok|opencode>",
			stdoutOnly: true,
		},
		{
			name:       "an unknown command refuses on stderr and says what to run",
			args:       []string{"reviw", "--pr", "3"},
			wantStatus: 1,
			stderrHas:  "unknown command: reviw",
		},
		{
			name:       "a leg with no pull request number refuses with its usage line",
			args:       []string{"review"},
			wantStatus: 1,
			stderrHas:  "Usage: crossrev review --pr 42",
		},
		{
			name:       "the watchdog with no repository names the flag that fixes it",
			args:       []string{"watchdog", "--repo", "bogus"},
			wantStatus: 1,
			stderrHas:  "--repo must be owner/name",
		},
	} {
		t.Run(row.name, func(t *testing.T) {
			got := invoke(t, bin, nil, row.args...)

			if got.status != row.wantStatus {
				t.Errorf("status = %d, want %d\nstdout: %q\nstderr: %q",
					got.status, row.wantStatus, got.stdout, got.stderr)
			}
			if row.stdoutHas != "" && !strings.Contains(got.stdout, row.stdoutHas) {
				t.Errorf("stdout = %q, want it to contain %q", got.stdout, row.stdoutHas)
			}
			if row.stderrHas != "" && !strings.Contains(got.stderr, row.stderrHas) {
				t.Errorf("stderr = %q, want it to contain %q", got.stderr, row.stderrHas)
			}
			if row.stdoutOnly && got.stderr != "" {
				t.Errorf("stderr = %q, want nothing", got.stderr)
			}
			if row.stderrHas != "" && got.stdout != "" {
				t.Errorf("stdout = %q, want nothing: a refusal is stderr's", got.stdout)
			}
		})
	}
}

// A refusal is exit 1 and nothing else, and it is on stderr. There is no third
// status a command can end with except the interrupt, which nothing here can
// send without racing the child.
func TestTheBinaryEndsWithOneOfThreeStatuses(t *testing.T) {
	bin := binary(t)

	for _, args := range [][]string{
		{"version"}, {"help"}, {"--version"}, {},
		{"reviw"}, {"review"}, {"status"}, {"config", "bogus"},
		{"auth", "bogus"}, {"cycle", "--trigger", "sideways", "--pr", "1"},
	} {
		got := invoke(t, bin, nil, args...)
		if got.status != 0 && got.status != 1 {
			t.Errorf("crossrev %v = status %d, want 0 or 1\nstderr: %q", args, got.status, got.stderr)
		}
	}
}

func readVersion(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("../../VERSION")
	if err != nil {
		t.Fatalf("reading VERSION: %v", err)
	}
	return strings.TrimSpace(string(raw))
}

// A leg's report, through the built binary, split across the two streams by
// kind.
//
// This is the end of the wire the kinds exist for. internal/review answers
// ui.Lines, reportLeg hands them to ui.IO.PrintAll, and PrintAll sends a ui_ok
// to stdout with its glyph and a ui_warn to stderr with its consequence indent.
// Measured against the shell in this checkout:
//
//	$ NO_COLOR=1 bash -c 'source lib/ui.sh; ui_ok "O"; ui_warn "C" "Q"'
//	stdout: "│  ✓ O\n"     stderr: "\n⚠  C\n   Q\n\n"
//
// Before the kinds, every line here arrived on stdout with two leading spaces.
func TestALegsReportIsSplitAcrossTheStreamsByKind(t *testing.T) {
	bin := binary(t)
	fixture := newFixture(t)

	got := invoke(t, bin, fixture.env, "review", "--pr", "42", "--repo", "acme/widget")

	if got.status != 0 {
		t.Fatalf("status = %d\nstdout: %q\nstderr: %q", got.status, got.stdout, got.stderr)
	}

	// The whole block, byte for byte: the run header's blank line and two
	// ui_say lines (lib/run.sh:1066-1067), three more ui_say, two ui_ok lines
	// with the glyph, the verdict printf with its arrow and trailing blank
	// (lib/run.sh:1317), then the resolve tip and its blank
	// (lib/run.sh:1319-1322). The two-space indent and the `│  ✓ ` prefix are
	// the shell's, measured above.
	want := "\n" +
		"  Reviewing acme/widget#42 — pass 1\n" +
		"  Reviewer: claude, reviewer-model\n" +
		"  Found 1 issue(s) — 1 high, 0 medium, 0 low, of which 0 pre-existing.\n" +
		"  1 at or above min_fix_severity (medium); the rest are reported and left alone.\n" +
		"  Posting them as inline comments on the lines they affect.\n" +
		"│  ✓ posted 1 finding comment(s)\n" +
		"│  ✓ posted a summary comment\n" +
		"  → verdict: issues-remain\n" +
		"\n" +
		"  Nothing was changed in your working tree. To act on these:\n" +
		"    crossrev resolve --pr 42\n" +
		"\n"
	if got.stdout != want {
		t.Errorf("stdout =\n%q\nwant\n%q", got.stdout, want)
	}
	if got.stderr != "" {
		t.Errorf("stderr = %q, want nothing: a clean pass warns about nothing", got.stderr)
	}
}

// The other half of the split: a warning goes to stderr, with the ⚠ and the
// three-space consequence indent, and never to stdout.
//
// The reviewer answering `converged` alongside an actionable finding is the
// cheapest real one to reach (lib/run.sh:1305, which is ui_warn).
func TestALegsWarningGoesToStderr(t *testing.T) {
	bin := binary(t)
	fixture := newFixtureWith(t, convergedPayload)

	got := invoke(t, bin, fixture.env, "review", "--pr", "42", "--repo", "acme/widget")

	if got.status != 0 {
		t.Fatalf("status = %d\nstdout: %q\nstderr: %q", got.status, got.stdout, got.stderr)
	}
	want := "\n⚠  the reviewer returned verdict 'converged' alongside 1 actionable finding\n" +
		"   The actionable count outranks the verdict, so the pass is labelled 'awaiting-resolution' to run the resolve leg.\n\n"
	if !strings.Contains(got.stderr, want) {
		t.Errorf("stderr =\n%q\nwant it to contain\n%q", got.stderr, want)
	}
	if strings.Contains(got.stdout, "outranks the verdict") {
		t.Errorf("the warning also landed on stdout: %q", got.stdout)
	}
}

// fixture is the smallest repository a review leg will run against: a git
// checkout with a config committed on main, the offline stub earlier on PATH,
// and the stub's own configuration written where tests/stub/_stub-env.sh reads
// it. That last part is the point — the binary hands `gh` an allowlist, so the
// route table reaches the stub through the file rather than the environment.
type fixture struct{ env []string }

func newFixture(t *testing.T) fixture { return newFixtureWith(t, fixturePayload) }

func newFixtureWith(t *testing.T, payload string) fixture {
	t.Helper()
	for _, tool := range []string{"git", "jq", "bash"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not installed, and the fixture needs it", tool)
		}
	}
	stub, err := filepath.Abs("../../tests/stub")
	if err != nil || !exists(filepath.Join(stub, "gh")) {
		t.Skip("the offline stub is not in this checkout")
	}

	dir := t.TempDir()
	config := filepath.Join(dir, "cfg")
	state := filepath.Join(dir, "state")
	work := filepath.Join(dir, "repo")
	mkdirs(t, config, state, work)

	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = work
		cmd.Env = append(crexec.Inherit(childEnvironment),
			"HOME="+dir, "GIT_CONFIG_GLOBAL="+filepath.Join(dir, "gitconfig"))
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q", "-b", "main")
	git("config", "user.email", "t@example.com")
	git("config", "user.name", "Test")
	write(t, filepath.Join(work, "app.ts"), "export const ok = 1\n")
	mkdirs(t, filepath.Join(work, ".github"))
	write(t, filepath.Join(work, ".github", "crossrev.yml"), fixtureConfig)
	git("add", "-A")
	git("commit", "-q", "-m", "base")

	base := strings.TrimSpace(gitOutput(t, work, dir, "rev-parse", "HEAD"))

	routes := filepath.Join(dir, "routes")
	write(t, routes, fixtureRoutes(base))
	body := payload
	payload = filepath.Join(dir, "payload.json")
	write(t, payload, body)

	// What the stub reads, in the file tests/stub/_stub-env.sh sources. The
	// allowlist the binary builds carries XDG_CONFIG_HOME, so the child can
	// find it; it does not carry a test-only name, and must not.
	write(t, filepath.Join(config, "crossrev-stub.env"),
		"export CROSSREV_GH_ROUTES="+shellQuote(routes)+"\n"+
			"export CROSSREV_GH_LOG="+shellQuote(filepath.Join(dir, "gh.log"))+"\n"+
			"export CROSSREV_REVIEW_PAYLOAD="+shellQuote(payload)+"\n")

	t.Chdir(work)
	return fixture{env: []string{
		"PATH=" + stub + string(os.PathListSeparator) + os.Getenv("PATH"),
		"HOME=" + dir,
		"XDG_CONFIG_HOME=" + config,
		"XDG_STATE_HOME=" + state,
		"RUNNER_ENVIRONMENT=self-hosted",
	}}
}

const fixtureConfig = `version: 1
mode: local
policy:
  min_fix_severity: medium
  max_passes_per_cycle: 3
  max_files_changed_per_pr: 200
  max_prs_per_day: 25
reviewer:
  harness: claude
  model: reviewer-model
resolver:
  harness: claude
  model: resolver-model
backlog:
  destination: none
`

// One finding, high severity, so the leg posts a comment and a summary and the
// report carries both a ui_ok and the plain lines around it.
const convergedPayload = `{"verdict":"converged","blocked_reason":null,"findings":[{"path":"app.ts","line":1,"side":"RIGHT","severity":"high","category":"correctness","pre_existing":false,"title":"Unchecked value","why":"A failure reads as success","fix":"Check it"}]}`

const fixturePayload = `{"verdict":"issues-remain","blocked_reason":null,"findings":[{"path":"app.ts","line":1,"side":"RIGHT","severity":"high","category":"correctness","pre_existing":false,"title":"Unchecked value","why":"A failure reads as success","fix":"Check it"}]}`

func fixtureRoutes(sha string) string {
	pr := `{"number":42,"title":"Add refresh","body":"b","url":"https://github.com/x",` +
		`"headRefName":"main","headRefOid":"` + sha + `","baseRefName":"main","baseRefOid":"` + sha + `",` +
		`"changedFiles":1,"labels":[],"isCrossRepository":false,"maintainerCanModify":false,` +
		`"isDraft":false,"headRepositoryOwner":{"login":"acme"},"headRepository":{"name":"widget"},"state":"OPEN"}`
	return strings.Join([]string{
		"repo view --json nameWithOwner*\t{\"nameWithOwner\":\"acme/widget\"}",
		"repo view * --json defaultBranchRef*\t{\"defaultBranchRef\":{\"name\":\"main\"}}",
		"api user*\t{\"login\":\"tester\"}",
		"pr view 42 --repo * --json *\t" + pr,
		"*Accept: application/vnd.github.diff*\tdiff --git a/app.ts b/app.ts",
		"api --paginate repos/*/issues/42/comments*\t[]",
		"api --paginate repos/*/pulls/42/comments*\t[]",
		"api --method GET repos/*/issues/comments*\t[]",
		"api --method POST repos/*/issues/42/comments*\t{\"id\":9001}",
		"api --method POST repos/*/pulls/42/comments*\t{\"id\":9002}",
		"api --method PATCH repos/*/issues/comments/*\t{\"id\":9001}",
		"api graphql*\t{\"data\":{\"repository\":{\"pullRequest\":{\"reviewThreads\":{\"nodes\":[]}}}}}",
		"api repos/*/labels/*\t!fail",
		"api --method POST repos/*/labels*\t{}",
		"issue edit*\t{}",
	}, "\n") + "\n"
}

func exists(path string) bool { _, err := os.Stat(path); return err == nil }

func mkdirs(t *testing.T, paths ...string) {
	t.Helper()
	for _, path := range paths {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", path, err)
		}
	}
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func gitOutput(t *testing.T, dir, home string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(crexec.Inherit(childEnvironment), "HOME="+home)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return string(out)
}

// shellQuote is printf %q for the one shape this file produces: a filesystem
// path with no quote in it. Single quotes, so nothing inside is expanded.
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// The run log's last line and the two stderr notices that close a run
// (run_cleanup, lib/run.sh:92-108).
//
// The trap writes `exit code=$rc reason=...` on every path, and on a failure it
// names the run directory so a reader can find the transcripts the failure
// kept. Neither reached the native binary, so a run log ended mid-leg and a
// failed leg said nothing about where its record was.
func TestARunClosesItsLogAndNamesTheRecordOnFailure(t *testing.T) {
	bin := binary(t)

	t.Run("a clean leg records its own exit", func(t *testing.T) {
		fixture := newFixture(t)
		got := invoke(t, bin, fixture.env, "review", "--pr", "42", "--repo", "acme/widget")
		if got.status != 0 {
			t.Fatalf("status = %d\nstderr: %q", got.status, got.stderr)
		}
		if log := lastRunLog(t, fixture); !strings.Contains(log, "exit code=0") {
			t.Errorf("the run log does not close with the exit event:\n%s", log)
		}
	})

	t.Run("a failed leg records the code and names the record", func(t *testing.T) {
		// No payload file for the stub, so the harness exits 1.
		fixture := newFixtureWith(t, fixturePayload)
		if err := os.Remove(filepath.Join(fixture.home(t), "payload.json")); err != nil {
			t.Fatalf("remove the canned payload: %v", err)
		}
		got := invoke(t, bin, fixture.env, "review", "--pr", "42", "--repo", "acme/widget")
		if got.status != 1 {
			t.Fatalf("status = %d, want 1\nstdout: %q\nstderr: %q", got.status, got.stdout, got.stderr)
		}
		if !strings.Contains(got.stderr, "Run log and any kept transcripts:") {
			t.Errorf("the failure does not name where the record is:\n%q", got.stderr)
		}
		if log := lastRunLog(t, fixture); !strings.Contains(log, "exit code=1") {
			t.Errorf("the run log does not carry the failing exit:\n%s", log)
		}
	})
}

// home is the fixture's temporary root, which is the parent of every path it
// wrote.
func (f fixture) home(t *testing.T) string {
	t.Helper()
	for _, entry := range f.env {
		if strings.HasPrefix(entry, "HOME=") {
			return strings.TrimPrefix(entry, "HOME=")
		}
	}
	t.Fatal("the fixture set no HOME")
	return ""
}

// lastRunLog reads run.log out of the one run directory the leg created.
func lastRunLog(t *testing.T, f fixture) string {
	t.Helper()
	var state string
	for _, entry := range f.env {
		if strings.HasPrefix(entry, "XDG_STATE_HOME=") {
			state = strings.TrimPrefix(entry, "XDG_STATE_HOME=")
		}
	}
	matches, err := filepath.Glob(filepath.Join(state, "crossrev", "runs", "acme-widget", "pr-42", "*", "run.log"))
	if err != nil || len(matches) == 0 {
		t.Fatalf("no run log under %s: %v %v", state, matches, err)
	}
	body, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatalf("read %s: %v", matches[0], err)
	}
	return string(body)
}

// The local run lock, run_lock_acquire at lib/run.sh:949 and :1768.
//
// vcs.Repository.AcquireRunLock had no caller, so two terminals could drive the
// same pull request at once and interleave their comments and replies.
func TestALegTakesTheLocalRunLock(t *testing.T) {
	bin := binary(t)
	fixture := newFixture(t)

	gitDir := strings.TrimSpace(gitOutput(t, ".", fixture.home(t), "rev-parse", "--git-dir"))
	if err := os.MkdirAll(filepath.Join(gitDir, "crossrev"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// This test process is alive, so it is a live holder.
	held := fmt.Sprintf("%d on other since now\n", os.Getpid())
	write(t, filepath.Join(gitDir, "crossrev", "pr-42.lock"), held)

	got := invoke(t, bin, fixture.env, "review", "--pr", "42", "--repo", "acme/widget")

	if got.status != 1 {
		t.Fatalf("status = %d, want 1: a live holder is a refusal\nstdout: %q", got.status, got.stdout)
	}
	want := fmt.Sprintf("already holds pull request 42 — %d on other since now", os.Getpid())
	if !strings.Contains(got.stderr, want) {
		t.Errorf("stderr =\n%q\nwant it to name the holder %q", got.stderr, want)
	}
}

// A lock left by a process that is gone is taken over, with a warning that says
// so (lib/run.sh:210-212).
func TestALegTakesOverADeadHoldersLock(t *testing.T) {
	bin := binary(t)
	fixture := newFixture(t)

	gitDir := strings.TrimSpace(gitOutput(t, ".", fixture.home(t), "rev-parse", "--git-dir"))
	if err := os.MkdirAll(filepath.Join(gitDir, "crossrev"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	lock := filepath.Join(gitDir, "crossrev", "pr-42.lock")
	// PID 2^22 is above every Linux and macOS pid_max, so it cannot be alive.
	write(t, lock, "4194304 on other since now\n")

	got := invoke(t, bin, fixture.env, "review", "--pr", "42", "--repo", "acme/widget")

	if got.status != 0 {
		t.Fatalf("status = %d, want 0\nstderr: %q", got.status, got.stderr)
	}
	if !strings.Contains(got.stderr, "no longer running") {
		t.Errorf("taking the lock over was not announced:\n%q", got.stderr)
	}
	// And it is released, so the next run is not blocked by this one.
	if _, err := os.Stat(lock); !os.IsNotExist(err) {
		t.Errorf("the lock survived the run: %v", err)
	}
}
