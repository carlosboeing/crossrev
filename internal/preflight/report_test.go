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

const defaultPairing = "version: \"1\"\nrunner: github-hosted\nreviewer:\n  harness: codex\nresolver:\n  harness: claude\n"

// The whole assembled report of `crossrev doctor` on a machine where everything
// is installed and configured (bin/crossrev:162-179).
func TestDoctorOnAWorkingMachine(t *testing.T) {
	r := coreVersions(newRecorder())
	r.answer("gh api user --jq .login", "carlosboeing\n", 0)
	r.answer("claude --version", "2.1.258 (Claude Code)\n", 0)
	r.answer("codex --version", "codex-cli 0.152.1\n", 0)
	c, buf := doctorChecker(t, r, onPath("git", "gh", "jq", "yq", "openssl", "claude", "codex"), defaultPairing)

	if code := c.Doctor(context.Background()); code != 0 {
		t.Errorf("Doctor = %d, want 0", code)
	}
	want := "\n◇  Requirements\n" +
		"│  ✓ git 2.50.1\n" +
		"│  ✓ gh 2.97.0 — authenticated as carlosboeing\n" +
		"│  ✓ jq 1.8.1\n" +
		"│  ✓ yq v4.53.3\n" +
		"│  ✓ openssl 3.6.3\n" +
		"│  ✓ claude 2.1.258\n" +
		"│  ✓ codex 0.152.1\n" +
		"│  ○ agy — not found, optional\n" +
		"│  ○ grok — not found, optional\n" +
		"│  ○ opencode — not found, optional\n" +
		"\n◇  Pairings on runner: github-hosted\n" +
		"│  ✓ reviewer — codex by subscription, kept warm by the refresher workflow\n" +
		"│  ✓ resolver — claude by subscription\n" +
		"└  Everything CrossRev needs is installed.\n\n"
	if got := buf.String(); got != want {
		t.Errorf("report =\n%q\nwant\n%q", got, want)
	}
}

// A missing tool changes the closing line and the exit code, and nothing else
// about the report (bin/crossrev:175-179).
func TestDoctorFailsOnAMissingTool(t *testing.T) {
	r := coreVersions(newRecorder())
	r.answer("gh api user --jq .login", "carlosboeing\n", 0)
	r.answer("claude --version", "2.1.258\n", 0)
	c, buf := doctorChecker(t, r, onPath("git", "gh", "jq", "openssl", "claude"), defaultPairing)

	if code := c.Doctor(context.Background()); code != 1 {
		t.Errorf("Doctor = %d, want 1", code)
	}
	report := buf.String()
	if !strings.HasSuffix(report, "└  Fix what is marked ✗ above, then run this again.\n\n") {
		t.Errorf("report did not close with the fix line:\n%s", report)
	}
	// yq is what reads the config in the shell, so without it the pairing
	// report is skipped rather than printed from a config nothing could read.
	if strings.Contains(report, "Pairings on runner") {
		t.Errorf("pairings were reported without yq:\n%s", report)
	}
}

// A stranded quarantine fails doctor on its own, and is reported between the
// requirements and the pairings.
func TestDoctorFailsOnAStrandedQuarantine(t *testing.T) {
	r := coreVersions(newRecorder())
	r.answer("gh api user --jq .login", "carlosboeing\n", 0)
	r.answer("claude --version", "2.1.258\n", 0)
	c, buf := doctorChecker(t, r, onPath("git", "gh", "jq", "yq", "openssl", "claude"), defaultPairing)
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
	c, buf := doctorChecker(t, r, onPath("git", "gh", "jq", "yq", "openssl", "agy"),
		"version: \"1\"\nrunner: github-hosted\nreviewer:\n  harness: agy\nresolver:\n  harness: claude\n")
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
	c, buf := doctorChecker(t, r, onPath("git", "gh", "jq", "yq", "openssl", "claude"), defaultPairing)
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
// an unset key is the default (lib/config.sh:303 reads it through `// empty`,
// and the defaults supply github-hosted).
func TestDoctorReportsPairingsAgainstTheConfiguredRunner(t *testing.T) {
	r := coreVersions(newRecorder())
	r.answer("gh api user --jq .login", "carlosboeing\n", 0)
	r.answer("agy --version", "1.1.23\n", 0)
	c, buf := doctorChecker(t, r, onPath("git", "gh", "jq", "yq", "openssl", "agy"),
		"version: \"1\"\nrunner: self-hosted\nreviewer:\n  harness: agy\nresolver:\n  harness: claude\n")

	if code := c.Doctor(context.Background()); code != 0 {
		t.Errorf("Doctor = %d, want 0", code)
	}
	if !strings.Contains(buf.String(), "\n◇  Pairings on runner: self-hosted\n") {
		t.Errorf("report =\n%s", buf)
	}
}
