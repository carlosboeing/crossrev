package preflight_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/preflight"
)

// doctorChecker is a Checker standing in a clean checkout with a clean state
// directory, so a case only has to describe what it wants to be wrong.
func doctorChecker(t *testing.T, r *recorder, look func(string) (string, error), yaml string) (*preflight.Checker, *strings.Builder) {
	t.Helper()
	t.Setenv("GITHUB_ACTIONS", "")
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	var buf strings.Builder
	c, _ := checker(t, r, look)
	c.IO.Out = &buf
	c.IO.Err = &buf
	c.Dir = t.TempDir()
	c.Config = configFor(t, yaml)
	return c, &buf
}

const defaultPairing = "version: \"2\"\nrunner: github-hosted\nreviewer:\n  harness: codex\nresolver:\n  harness: claude\n"

// The whole assembled report of `crossrev doctor` on a machine where everything
// is installed and configured (bin/crossrev:162-179).
func TestDoctorOnAWorkingMachine(t *testing.T) {
	r := coreVersions(newRecorder())
	r.answer("gh api user --jq .login", "carlosboeing\n", 0)
	r.answer("claude --version", "2.1.258 (Claude Code)\n", 0)
	r.answer("codex --version", "codex-cli 0.152.1\n", 0)
	r.answer("gh repo view --json viewerPermission --jq .viewerPermission", "WRITE\n", 0)
	c, buf := doctorChecker(t, r, onPath("git", "gh", "claude", "codex"), defaultPairing)

	if code := c.Doctor(context.Background()); code != 0 {
		t.Errorf("Doctor = %d, want 0", code)
	}
	want := "\n◇  Requirements\n" +
		"│  ✓ git 2.50.1\n" +
		"│  ✓ gh 2.97.0 — authenticated as carlosboeing\n" +
		"│  ✓ claude 2.1.258 — known good (2.1.237-2.1.281)\n" +
		"│  ✓ codex 0.152.1 — unverified, outside the recorded range (0.159.2)\n" +
		"│  ○ agy — not found, optional\n" +
		"│  ○ grok — not found, optional\n" +
		"│  ○ opencode — not found, optional\n" +
		"\n◇  Pairings on runner: github-hosted\n" +
		"│  ✓ reviewer — codex by subscription, kept warm by the refresher workflow\n" +
		"│  ✓ resolver — claude by subscription\n" +
		"\n◇  Review reads\n" +
		"  claude — served reads, command block verified at pin 2.1.237\n" +
		"  codex — served reads, command block verified at pin 0.159.2\n" +
		"  agy — supplied reads, no tripwire: agy emits no tool events to watch\n" +
		"  grok — supplied reads, tripwire UNVERIFIED at pin 1.0.5 (review_isolation_unverified)\n" +
		"  opencode — supplied reads, no tripwire: the five read tools are denied through the isolation config with no command record to watch\n" +
		"\n◇  Coverage ledger\n" +
		"│  store auto (the default): tries git refs first, and falls back to the marker comment when a ref write is refused\n" +
		"│  namespace refs/crossrev — one ref per pull request per reviewer, refs/crossrev/pr/<number>/<slot>/coverage\n" +
		"│  on_overflow degrade (the default): a marker comment past 64 KiB sheds its predecessor, then compacts the current generation to counts, then halts\n" +
		"│  ✓ token can push to this repository (contents: write)\n" +
		"│     This proves the permission, not the namespace: an organisation ruleset restricts ref creation, and only an attempted ref write discovers one.\n" +
		"│  reviewer reviewer1 on codex\n" +
		"└  Everything CrossRev needs is installed.\n\n"
	if got := buf.String(); got != want {
		t.Errorf("report =\n%q\nwant\n%q", got, want)
	}
}

// jq, yq and openssl are not doctor's to require: the binary reads YAML and
// JSON itself and signs nothing, so a PATH holding none of the three passes
// and still prints the pairing report.
func TestDoctorPassesWithoutJqYqAndOpenssl(t *testing.T) {
	r := coreVersions(newRecorder())
	r.answer("gh api user --jq .login", "carlosboeing\n", 0)
	r.answer("claude --version", "2.1.258 (Claude Code)\n", 0)
	r.answer("gh repo view --json viewerPermission --jq .viewerPermission", "WRITE\n", 0)
	c, buf := doctorChecker(t, r, onPath("git", "gh", "claude"), defaultPairing)

	if code := c.Doctor(context.Background()); code != 0 {
		t.Errorf("Doctor = %d, want 0", code)
	}
	report := buf.String()
	if !strings.Contains(report, "\n◇  Pairings on runner: github-hosted\n") {
		t.Errorf("the pairing report was skipped without yq:\n%s", report)
	}
	for _, tool := range []string{"jq", "yq", "openssl"} {
		if strings.Contains(report, tool) {
			t.Errorf("the report still names %s, which doctor no longer requires:\n%s", tool, report)
		}
	}
}

// A missing tool changes the closing line and the exit code, and the pairing
// report still prints, because the config loads without the missing tool
// (bin/crossrev:175-179).
func TestDoctorFailsOnAMissingTool(t *testing.T) {
	r := coreVersions(newRecorder())
	r.answer("gh api user --jq .login", "carlosboeing\n", 0)
	r.answer("claude --version", "2.1.258\n", 0)
	c, buf := doctorChecker(t, r, onPath("gh", "claude"), defaultPairing)

	if code := c.Doctor(context.Background()); code != 1 {
		t.Errorf("Doctor = %d, want 1", code)
	}
	report := buf.String()
	if !strings.HasSuffix(report, "└  Fix what is marked ✗ above, then run this again.\n\n") {
		t.Errorf("report did not close with the fix line:\n%s", report)
	}
	if !strings.Contains(report, "│  ✗ git — not found. Install with: xcode-select --install\n") {
		t.Errorf("report did not name the missing tool:\n%s", report)
	}
	if !strings.Contains(report, "Pairings on runner") {
		t.Errorf("pairings were skipped for a tool the config does not read:\n%s", report)
	}
}

// A stranded quarantine fails doctor on its own, and is reported between the
// requirements and the pairings.
func TestDoctorFailsOnAStrandedQuarantine(t *testing.T) {
	r := coreVersions(newRecorder())
	r.answer("gh api user --jq .login", "carlosboeing\n", 0)
	r.answer("claude --version", "2.1.258\n", 0)
	c, buf := doctorChecker(t, r, onPath("git", "gh", "claude"), defaultPairing)
	if err := os.Mkdir(filepath.Join(c.Dir, ".crossrev-quarantine"), 0o755); err != nil {
		t.Fatal(err)
	}

	if code := c.Doctor(context.Background()); code != 1 {
		t.Errorf("Doctor = %d, want 1", code)
	}
	report := buf.String()
	quarantine := strings.Index(report, "stranded quarantine found at")
	pairings := strings.Index(report, "Pairings on runner")
	if quarantine < 0 || pairings < 0 || quarantine > pairings {
		t.Errorf("the quarantine was not reported before the pairings:\n%s", report)
	}
}

// A pairing the runner cannot serve fails doctor, and the worktree report still
// runs after it (bin/crossrev:171-174).
func TestDoctorFailsOnAnUnservablePairing(t *testing.T) {
	state := t.TempDir()
	r := coreVersions(newRecorder())
	r.answer("gh api user --jq .login", "carlosboeing\n", 0)
	r.answer("agy --version", "1.1.23\n", 0)
	c, buf := doctorChecker(t, r, onPath("git", "gh", "agy"),
		"version: \"2\"\nrunner: github-hosted\nreviewer:\n  harness: agy\nresolver:\n  harness: claude\n")
	t.Setenv("XDG_STATE_HOME", state)
	if err := os.MkdirAll(filepath.Join(state, "crossrev", "worktrees", "acme-widget", "pr-9"), 0o755); err != nil {
		t.Fatal(err)
	}

	if code := c.Doctor(context.Background()); code != 1 {
		t.Errorf("Doctor = %d, want 1", code)
	}
	report := buf.String()
	if !strings.Contains(report, "│  ✗ reviewer — agy by subscription cannot run on a github-hosted runner\n") {
		t.Errorf("the refusal is missing:\n%s", report)
	}
	if !strings.Contains(report, "◇  Tool-owned worktrees\n") {
		t.Errorf("the worktree report did not run after a refused pairing:\n%s", report)
	}
	if !strings.HasSuffix(report, "└  Fix what is marked ✗ above, then run this again.\n\n") {
		t.Errorf("report did not close with the fix line:\n%s", report)
	}
}

// Leftover worktrees are reported and are not a failure: the Bash call is not
// guarded by `|| doctor_ok=1` (bin/crossrev:174).
func TestDoctorReportsWorktreesWithoutFailing(t *testing.T) {
	state := t.TempDir()
	r := coreVersions(newRecorder())
	r.answer("gh api user --jq .login", "carlosboeing\n", 0)
	r.answer("claude --version", "2.1.258\n", 0)
	c, buf := doctorChecker(t, r, onPath("git", "gh", "claude"), defaultPairing)
	t.Setenv("XDG_STATE_HOME", state)
	if err := os.MkdirAll(filepath.Join(state, "crossrev", "worktrees", "acme-widget", "pr-9"), 0o755); err != nil {
		t.Fatal(err)
	}

	if code := c.Doctor(context.Background()); code != 0 {
		t.Errorf("Doctor = %d, want 0", code)
	}
	report := buf.String()
	if !strings.Contains(report, "│  ○ "+state+"/crossrev/worktrees/acme-widget/pr-9\n") {
		t.Errorf("the worktree was not named:\n%s", report)
	}
	if !strings.HasSuffix(report, "└  Everything CrossRev needs is installed.\n\n") {
		t.Errorf("a leftover worktree failed the check:\n%s", report)
	}
}

// The runner the pairings are reported against is the one the config names, and
// an unset key is the default (lib/config.sh:390 reads it through `// empty`,
// and the defaults supply github-hosted).
func TestDoctorReportsPairingsAgainstTheConfiguredRunner(t *testing.T) {
	r := coreVersions(newRecorder())
	r.answer("gh api user --jq .login", "carlosboeing\n", 0)
	r.answer("agy --version", "1.1.23\n", 0)
	c, buf := doctorChecker(t, r, onPath("git", "gh", "agy"),
		"version: \"2\"\nrunner: self-hosted\nreviewer:\n  harness: agy\nresolver:\n  harness: claude\n")

	if code := c.Doctor(context.Background()); code != 0 {
		t.Errorf("Doctor = %d, want 0", code)
	}
	if !strings.Contains(buf.String(), "\n◇  Pairings on runner: self-hosted\n") {
		t.Errorf("report =\n%s", buf)
	}
}
