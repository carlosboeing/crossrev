package preflight_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/exec"
	"github.com/carlosboeing/crossrev/internal/preflight"
	"github.com/carlosboeing/crossrev/internal/ui"
)

// The five core tools, in the order lib/preflight.sh:86 probes them.
var corePath = []string{"git", "gh", "jq", "yq", "openssl"}

// The hint names a fix for the platform the operator is actually on
// (lib/preflight.sh:8-34). Measured from the shell with
// `_install_hint <tool>` on Darwin.
func TestInstallHintOnDarwin(t *testing.T) {
	c := &preflight.Checker{Harness: document(t), OS: "Darwin"}
	for tool, want := range map[string]string{
		"git":      "xcode-select --install",
		"gh":       "brew install gh",
		"jq":       "brew install jq",
		"yq":       "brew install yq",
		"openssl":  "already present on macOS; otherwise brew install openssl",
		"claude":   "https://claude.com/claude-code",
		"codex":    "https://chatgpt.com/codex",
		"agy":      "https://antigravity.google",
		"grok":     "https://x.ai/cli",
		"opencode": "https://opencode.ai",
		// A harness the descriptor names and does not drive has no install
		// hint of its own, so it falls to the generic sentence.
		"gemini": "install gemini",
		"nope":   "install nope",
	} {
		if got := c.InstallHint(tool); got != want {
			t.Errorf("InstallHint(%q) = %q, want %q", tool, got, want)
		}
	}
}

// Everything that is not Darwin takes the other arm (lib/preflight.sh:22-32).
func TestInstallHintElsewhere(t *testing.T) {
	c := &preflight.Checker{Harness: document(t), OS: "Linux"}
	for tool, want := range map[string]string{
		"git":     "your package manager, e.g. apt install git",
		"gh":      "https://github.com/cli/cli#installation",
		"jq":      "https://jqlang.github.io/jq/download/",
		"yq":      "https://github.com/mikefarah/yq#install",
		"openssl": "your package manager, e.g. apt install openssl",
		"claude":  "https://claude.com/claude-code",
		"nope":    "install nope",
	} {
		if got := c.InstallHint(tool); got != want {
			t.Errorf("InstallHint(%q) = %q, want %q", tool, got, want)
		}
	}
}

// The zero Checker asks the platform it is running on rather than nothing at
// all, which is `uname -s` at lib/preflight.sh:10. Without this the OS field's
// default is never exercised and every hint above proves only what the helper
// filled in.
func TestInstallHintTakesThePlatformWhenNoneIsNamed(t *testing.T) {
	c := &preflight.Checker{Harness: document(t)}
	want := "your package manager, e.g. apt install git"
	if runtime.GOOS == "darwin" {
		want = "xcode-select --install"
	}
	if got := c.InstallHint("git"); got != want {
		t.Errorf("InstallHint(git) = %q, want %q", got, want)
	}
}

// The whole core report, byte for byte, against a machine where everything is
// installed and gh holds a personal token (lib/preflight.sh:83-136).
func TestCheckCoreReportsEveryTool(t *testing.T) {
	r := coreVersions(newRecorder())
	r.answer("gh api user --jq .login", "carlosboeing\n", 0)
	c, buf := checker(t, r, onPath(corePath...))

	if !c.Check(context.Background(), preflight.NeedCore) {
		t.Errorf("Check = false, want true")
	}
	want := "\n◇  Requirements\n" +
		"│  ✓ git 2.50.1\n" +
		"│  ✓ gh 2.97.0 — authenticated as carlosboeing\n" +
		"│  ✓ jq 1.8.1\n" +
		"│  ✓ yq v4.53.3\n" +
		"│  ✓ openssl 3.6.3\n"
	if got := buf.String(); got != want {
		t.Errorf("report =\n%q\nwant\n%q", got, want)
	}
}

// openssl is asked with its own subcommand, because the build on GitHub's
// hosted runners rejects --version outright (lib/preflight.sh:59).
func TestCheckProbesOpensslWithItsOwnSubcommand(t *testing.T) {
	r := coreVersions(newRecorder())
	r.answer("gh api user --jq .login", "carlosboeing\n", 0)
	c, buf := checker(t, r, onPath(corePath...))
	c.Check(context.Background(), preflight.NeedCore)

	for _, argv := range r.argvs() {
		if argv == "openssl --version" {
			t.Errorf("openssl was probed with --version: %v", r.argvs())
		}
	}
	if _, found := r.specFor("openssl version"); !found {
		t.Errorf("openssl version was never run: %v", r.argvs())
	}
	if !strings.Contains(buf.String(), "✓ openssl 3.6.3") {
		t.Errorf("report did not name openssl's version:\n%s", buf)
	}
}

// A tool that runs and says nothing version-shaped is not a tool that is
// missing, and installing it again is the one thing that will not help
// (lib/preflight.sh:127-131).
func TestCheckSeparatesSilentFromMissing(t *testing.T) {
	r := coreVersions(newRecorder())
	r.answer("gh api user --jq .login", "carlosboeing\n", 0)
	r.answer("yq --version", "yq: error while loading shared libraries\n", 1)
	c, buf := checker(t, r, onPath(corePath...))

	if c.Check(context.Background(), preflight.NeedCore) {
		t.Errorf("Check = true, want false")
	}
	report := buf.String()
	if !strings.Contains(report, "│  ✗ yq — installed, but it did not report a version. Check that it runs.\n") {
		t.Errorf("report did not say the tool is installed:\n%s", report)
	}
	if strings.Contains(report, "yq — not found") {
		t.Errorf("report sent the reader to install what is already there:\n%s", report)
	}
}

// The distinction the case above rests on: genuinely missing still reads as
// missing, and still names the install command (lib/preflight.sh:132-134).
func TestCheckReportsAMissingToolWithItsInstallHint(t *testing.T) {
	r := coreVersions(newRecorder())
	r.answer("gh api user --jq .login", "carlosboeing\n", 0)
	c, buf := checker(t, r, onPath("git", "gh", "jq", "openssl"))

	if c.Check(context.Background(), preflight.NeedCore) {
		t.Errorf("Check = true, want false")
	}
	if !strings.Contains(buf.String(), "│  ✗ yq — not found. Install with: brew install yq\n") {
		t.Errorf("report did not name the fix:\n%s", buf)
	}
	for _, argv := range r.argvs() {
		if strings.HasPrefix(argv, "yq ") {
			t.Errorf("a tool that is not on PATH was still run: %v", r.argvs())
		}
	}
}

// Installed is not the same as usable, and which endpoint proves a token works
// depends on what kind of token gh holds (lib/preflight.sh:96-123).
func TestCheckAsksGhForIdentityAtTheEndpointThatSuitsTheToken(t *testing.T) {
	for _, tt := range []struct {
		name      string
		reachable map[string]reply
		runner    string
		wantOK    bool
		wantLine  string
		wantGone  string
	}{
		{
			name:      "a personal token names its login",
			reachable: map[string]reply{"gh api user --jq .login": {stdout: "carlosboeing\n"}},
			wantOK:    true,
			wantLine:  "│  ✓ gh 2.97.0 — authenticated as carlosboeing\n",
		},
		{
			name:      "an App installation token passes without a user",
			reachable: map[string]reply{"gh api installation/repositories --jq .total_count": {stdout: "2\n"}},
			wantOK:    true,
			wantLine:  "│  ✓ gh 2.97.0 — authenticated as a GitHub App installation\n",
			wantGone:  "not authenticated",
		},
		{
			name:      "a token that reaches only rate_limit still passes",
			reachable: map[string]reply{"gh api rate_limit": {stdout: "{}\n"}},
			wantOK:    true,
			wantLine:  "│  ✓ gh 2.97.0 — authenticated\n",
			wantGone:  "authenticated as ",
		},
		{
			name:     "a token that reaches nothing fails, and locally the fix is a login",
			wantOK:   false,
			wantLine: "│  ✗ gh — installed but not authenticated. Run: gh auth login\n",
		},
		{
			name:     "on a runner the same failure names the credential instead",
			runner:   "true",
			wantOK:   false,
			wantLine: "│  ✗ gh — installed, but the token it was given was refused. Check the app-token the workflow passes, and that the App is still installed on this repository.\n",
			wantGone: "gh auth login",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			// A GitHub runner exports GITHUB_ACTIONS into every step and this
			// suite runs there too, so a case meaning "not on a runner" says so
			// rather than assuming the variable is absent.
			t.Setenv("GITHUB_ACTIONS", tt.runner)

			r := coreVersions(newRecorder())
			for key, answer := range tt.reachable {
				r.answer(key, answer.stdout, answer.code)
			}
			c, buf := checker(t, r, onPath(corePath...))

			if got := c.Check(context.Background(), preflight.NeedCore); got != tt.wantOK {
				t.Errorf("Check = %v, want %v", got, tt.wantOK)
			}
			report := buf.String()
			if !strings.Contains(report, tt.wantLine) {
				t.Errorf("report =\n%s\nwant a line %q", report, tt.wantLine)
			}
			if tt.wantGone != "" && strings.Contains(report, tt.wantGone) {
				t.Errorf("report still says %q:\n%s", tt.wantGone, report)
			}
		})
	}
}

// The three gh questions are asked cheapest first and stop at the first answer
// (lib/preflight.sh:108-113).
func TestCheckStopsAtTheFirstGhEndpointThatAnswers(t *testing.T) {
	t.Setenv("GITHUB_ACTIONS", "")
	r := coreVersions(newRecorder())
	r.answer("gh api user --jq .login", "carlosboeing\n", 0)
	r.answer("gh api installation/repositories --jq .total_count", "2\n", 0)
	r.answer("gh api rate_limit", "{}\n", 0)
	c, _ := checker(t, r, onPath(corePath...))
	c.Check(context.Background(), preflight.NeedCore)

	for _, argv := range r.argvs() {
		if argv == "gh api installation/repositories --jq .total_count" || argv == "gh api rate_limit" {
			t.Errorf("a later endpoint was asked after the first answered: %v", r.argvs())
		}
	}
}

// An empty answer from `gh api user` is not an identity, so the probe carries
// on (lib/preflight.sh:108).
func TestCheckTreatsAnEmptyLoginAsNoAnswer(t *testing.T) {
	t.Setenv("GITHUB_ACTIONS", "")
	r := coreVersions(newRecorder())
	r.answer("gh api user --jq .login", "\n", 0)
	r.answer("gh api rate_limit", "{}\n", 0)
	c, buf := checker(t, r, onPath(corePath...))

	if !c.Check(context.Background(), preflight.NeedCore) {
		t.Errorf("Check = false, want true")
	}
	if !strings.Contains(buf.String(), "│  ✓ gh 2.97.0 — authenticated\n") {
		t.Errorf("report =\n%s", buf)
	}
}

// The harness set is descriptor-driven, and a harness is optional
// (lib/preflight.sh:138-165).
func TestCheckHarnessReportsEveryDescribedHarness(t *testing.T) {
	t.Setenv("GITHUB_ACTIONS", "")
	r := coreVersions(newRecorder())
	r.answer("gh api user --jq .login", "carlosboeing\n", 0)
	r.answer("claude --version", "2.1.258 (Claude Code)\n", 0)
	r.answer("codex --version", "codex-cli 0.152.1\n", 0)
	// agy runs and will not say what it is, which is deliberately not a
	// harness (lib/preflight.sh:151-155).
	r.answer("agy --version", "agy: broken\n", 0)
	c, buf := checker(t, r, onPath("git", "gh", "jq", "yq", "openssl", "claude", "codex", "agy"))

	if !c.Check(context.Background(), preflight.NeedHarness) {
		t.Errorf("Check = false, want true")
	}
	want := "\n◇  Requirements\n" +
		"│  ✓ git 2.50.1\n" +
		"│  ✓ gh 2.97.0 — authenticated as carlosboeing\n" +
		"│  ✓ jq 1.8.1\n" +
		"│  ✓ yq v4.53.3\n" +
		"│  ✓ openssl 3.6.3\n" +
		"│  ✓ claude 2.1.258\n" +
		"│  ✓ codex 0.152.1\n" +
		"│  ○ agy — installed, but it did not report a version\n" +
		"│  ○ grok — not found, optional\n" +
		"│  ○ opencode — not found, optional\n"
	if got := buf.String(); got != want {
		t.Errorf("report =\n%q\nwant\n%q", got, want)
	}
}

// No harness at all is a failure, and the message names the set
// (lib/preflight.sh:160-163).
func TestCheckHarnessFailsWhenNoneIsInstalled(t *testing.T) {
	t.Setenv("GITHUB_ACTIONS", "")
	r := coreVersions(newRecorder())
	r.answer("gh api user --jq .login", "carlosboeing\n", 0)
	c, buf := checker(t, r, onPath(corePath...))

	if c.Check(context.Background(), preflight.NeedHarness) {
		t.Errorf("Check = true, want false")
	}
	want := "│  ✗ no harness CLI found — CrossRev needs at least one of claude, codex, agy, grok and opencode\n"
	if !strings.Contains(buf.String(), want) {
		t.Errorf("report =\n%s\nwant a line %q", buf, want)
	}
}

// jq is what reads the descriptor in the shell, so without it the harness probe
// says it was skipped rather than reporting every harness as missing
// (lib/preflight.sh:140-141).
func TestCheckHarnessSkipsTheProbeWithoutJq(t *testing.T) {
	t.Setenv("GITHUB_ACTIONS", "")
	r := coreVersions(newRecorder())
	r.answer("gh api user --jq .login", "carlosboeing\n", 0)
	c, buf := checker(t, r, onPath("git", "gh", "yq", "openssl", "claude"))

	// jq itself is missing, so the check fails on that and not on the harness.
	if c.Check(context.Background(), preflight.NeedHarness) {
		t.Errorf("Check = true, want false")
	}
	report := buf.String()
	if !strings.Contains(report, "│  ○ harness check skipped — install jq to probe installed harnesses\n") {
		t.Errorf("report =\n%s", report)
	}
	if strings.Contains(report, "no harness CLI found") {
		t.Errorf("a skipped probe still reported a verdict:\n%s", report)
	}
}

// A version probe starts a harness CLI, which is the process that reads
// attacker-controlled text, so it never receives a forge credential
// (ADR 0001, lib/adapters/claude.sh:72).
func TestCheckWithholdsAForgeCredentialFromAVersionProbe(t *testing.T) {
	t.Setenv("GITHUB_ACTIONS", "")
	r := coreVersions(newRecorder())
	r.answer("gh api user --jq .login", "carlosboeing\n", 0)
	r.answer("claude --version", "2.1.258\n", 0)

	io, _ := capture()
	c := &preflight.Checker{
		IO:       io,
		Runner:   r,
		Env:      []string{"PATH=/stub", "GH_TOKEN=ghp_secret", "GITHUB_TOKEN=ghs_secret"},
		LookPath: onPath("git", "gh", "jq", "yq", "openssl", "claude"),
		Harness:  document(t),
		OS:       "Darwin",
	}
	c.Check(context.Background(), preflight.NeedHarness)

	for _, key := range []string{"git --version", "openssl version", "claude --version"} {
		spec, found := r.specFor(key)
		if !found {
			t.Fatalf("%s was never run: %v", key, r.argvs())
		}
		for _, name := range exec.ForgeCredentialNames() {
			for _, entry := range spec.Env {
				if strings.HasPrefix(entry, name+"=") {
					t.Errorf("%s received %s", key, name)
				}
			}
		}
	}

	// The identity probe is the orchestrator asking GitHub who it is, and it
	// needs the credential to get an answer.
	spec, found := r.specFor("gh api user --jq .login")
	if !found {
		t.Fatalf("gh api user was never run: %v", r.argvs())
	}
	if !strings.Contains(strings.Join(spec.Env, " "), "GH_TOKEN=ghp_secret") {
		t.Errorf("the gh identity probe lost the credential: %v", spec.Env)
	}
}

// Both output streams are folded into one capture, because a tool that
// complains on stderr still has to be read (lib/preflight.sh:59-60).
func TestCheckReadsAToolThatWritesToStderr(t *testing.T) {
	t.Setenv("GITHUB_ACTIONS", "")
	r := coreVersions(newRecorder())
	r.answer("gh api user --jq .login", "carlosboeing\n", 0)
	c, _ := checker(t, r, onPath(corePath...))
	c.Check(context.Background(), preflight.NeedCore)

	spec, found := r.specFor("git --version")
	if !found {
		t.Fatalf("git --version was never run")
	}
	if spec.Streams != exec.StreamsCombined {
		t.Errorf("Streams = %v, want StreamsCombined", spec.Streams)
	}
	if spec.Stdin != nil && len(spec.Stdin) != 0 {
		t.Errorf("Stdin = %q, want no input", spec.Stdin)
	}
}

// Only the first line is read, and only the first version-shaped token in it
// (lib/preflight.sh:62).
func TestCheckReadsTheFirstVersionShapedTokenOfTheFirstLine(t *testing.T) {
	t.Setenv("GITHUB_ACTIONS", "")
	for _, tt := range []struct{ raw, want string }{
		{"git version 2.50.1 (Apple Git-155)\n", "git 2.50.1"},
		{"jq-1.8.1\n", "git 1.8.1"},
		{"yq (mikefarah/yq) version v4.53.3\n", "git v4.53.3"},
		{"OpenSSL 3.0.2 15 Mar 2022\n", "git 3.0.2"},
		{"2.1.258 (Claude Code)\n", "git 2.1.258"},
		{"codex-cli 0.147.0\n", "git 0.147.0"},
		// A second line carrying a version does not rescue a first line
		// without one, because head -1 has already thrown it away.
		{"warning: config is stale\n1.2.3\n", ""},
	} {
		r := coreVersions(newRecorder())
		r.answer("git --version", tt.raw, 0)
		r.answer("gh api user --jq .login", "carlosboeing\n", 0)
		c, buf := checker(t, r, onPath(corePath...))
		c.Check(context.Background(), preflight.NeedCore)

		if tt.want == "" {
			if !strings.Contains(buf.String(), "✗ git — installed, but it did not report a version") {
				t.Errorf("%q reported a version:\n%s", tt.raw, buf)
			}
			continue
		}
		if !strings.Contains(buf.String(), "✓ "+tt.want+"\n") {
			t.Errorf("%q reported\n%s\nwant %q", tt.raw, buf, tt.want)
		}
	}
}

// A tool that is not on PATH is never started, which is `command -v` at
// lib/preflight.sh:56.
func TestCheckNeverStartsAToolThatIsNotOnPath(t *testing.T) {
	t.Setenv("GITHUB_ACTIONS", "")
	r := coreVersions(newRecorder())
	c, _ := checker(t, r, onPath())
	if c.Check(context.Background(), preflight.NeedCore) {
		t.Errorf("Check = true, want false")
	}
	if got := r.argvs(); len(got) != 0 {
		t.Errorf("processes started on an empty PATH: %v", got)
	}
}

// yq reads YAML and jq cannot, so the refusal says why rather than just naming
// the binary (lib/preflight.sh:296-300).
func TestRequireYq(t *testing.T) {
	c := &preflight.Checker{Harness: document(t), OS: "Darwin", LookPath: onPath("yq")}
	if err := c.RequireYq(); err != nil {
		t.Errorf("RequireYq with yq installed = %v, want nil", err)
	}

	io, buf := capture()
	c = &preflight.Checker{IO: io, Harness: document(t), OS: "Darwin", LookPath: onPath()}
	err := c.RequireYq()
	if err == nil {
		t.Fatalf("RequireYq without yq = nil, want a refusal")
	}
	var fatal *ui.FatalError
	if !errors.As(err, &fatal) {
		t.Fatalf("RequireYq returned %T, want *ui.FatalError", err)
	}
	if fatal.Reason != "yq is not installed, and crossrev's config files are YAML" {
		t.Errorf("reason = %q", fatal.Reason)
	}
	if fatal.Action != "jq cannot read YAML. Install it with: brew install yq" {
		t.Errorf("action = %q", fatal.Action)
	}
	want := "\nerror  yq is not installed, and crossrev's config files are YAML\n" +
		"       jq cannot read YAML. Install it with: brew install yq\n\n"
	if got := buf.String(); got != want {
		t.Errorf("printed\n%q\nwant\n%q", got, want)
	}
}

// TestRequireYqWithNoLookPathUsesTheSharedSearch is the one case here that
// leaves LookPath nil and drives the production search.
//
// Every other case substitutes one through onPath, so without this the
// fallback is untested and whatever it does is invisible from here. The whole
// contract of that search — executable-preferred, the directory that takes the
// fallback slot and loses it, the name carrying a separator — is measured
// against bash once, in internal/exec, and not repeated per caller.
//
// RequireYq is the driver because it reaches installed() and starts no process.
func TestRequireYqWithNoLookPathUsesTheSharedSearch(t *testing.T) {
	root := t.TempDir()
	asDirectory := filepath.Join(root, "d")
	asProgram := filepath.Join(root, "x")
	if err := os.MkdirAll(filepath.Join(asDirectory, "yq"), 0o755); err != nil {
		t.Fatalf("make the directory named like the tool: %v", err)
	}
	if err := os.MkdirAll(asProgram, 0o755); err != nil {
		t.Fatalf("make %s: %v", asProgram, err)
	}
	if err := os.WriteFile(filepath.Join(asProgram, "yq"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write yq: %v", err)
	}

	for _, row := range []struct {
		name  string
		path  string
		found bool
	}{
		{"a directory named like the tool", asDirectory, false},
		{"the executable behind it", asDirectory + string(os.PathListSeparator) + asProgram, true},
		{"an empty PATH", "", false},
	} {
		t.Run(row.name, func(t *testing.T) {
			t.Setenv("PATH", row.path)
			io, _ := capture()
			// LookPath is left nil, which is the whole point.
			c := &preflight.Checker{IO: io, Harness: document(t), OS: "Darwin"}
			err := c.RequireYq()
			if row.found && err != nil {
				t.Errorf("RequireYq with PATH=%q = %v, want nil", row.path, err)
			}
			if !row.found && err == nil {
				t.Errorf("RequireYq with PATH=%q found yq, want a refusal", row.path)
			}
		})
	}
}
