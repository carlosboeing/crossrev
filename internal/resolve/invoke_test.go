package resolve

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/exec"
	"github.com/carlosboeing/crossrev/internal/harness"

	"github.com/carlosboeing/crossrev/internal/ui"
)

// TestInvoke pins write capability, the no-arbitrary-command grant, quarantine
// and worktree before the model, and one semantic retry.
//
// Write flag measured from lib/run.sh:488-489:
//
//	LEG_WRITE=no
//	[[ "$leg" == "resolver" ]] && LEG_WRITE=yes
func TestInvoke(t *testing.T) {
	t.Run("resolver stub receives write permission", func(t *testing.T) {
		e := setup(t)
		e.addReview(t, defaultFindings(), "issues-remain")
		got := e.run(t)
		if got.Err != nil {
			t.Fatalf("Run: %v", got.Err)
		}
		if len(e.adapter.invs) == 0 {
			t.Fatal("adapter was not invoked")
		}
		if !e.adapter.invs[0].Write {
			t.Fatal("resolver invocation Write is false")
		}
		if core.WriteCapabilityFor(core.RoleResolver) != core.WriteYes {
			t.Fatal("WriteCapabilityFor(resolver) is not yes")
		}
	})

	t.Run("claude write spec grants acceptEdits and not arbitrary commands", func(t *testing.T) {
		e := setup(t)
		e.addReview(t, defaultFindings(), "issues-remain")
		doc := mustHarness(t)
		adapter, ok := harness.For(doc, "claude")
		if !ok {
			t.Fatal("no claude adapter")
		}
		got := e.run(t)
		if got.Err != nil {
			t.Fatalf("Run: %v", got.Err)
		}
		if len(e.adapter.invs) == 0 {
			t.Fatal("no invocation recorded")
		}
		inv := e.adapter.invs[0]
		spec, err := adapter.Spec(inv)
		if err != nil {
			t.Fatalf("Spec: %v", err)
		}
		if !hasFlagPair(spec.Args, "--permission-mode", "acceptEdits") {
			t.Fatalf("write spec missing acceptEdits: %v", spec.Args)
		}
		for _, forbidden := range []string{"bypassPermissions", "danger-full-access", "--dangerously"} {
			if containsAny(spec.Args, forbidden) {
				t.Fatalf("write spec granted %q: %v", forbidden, spec.Args)
			}
		}
		if envHas(spec.Env, "GH_TOKEN") || envHas(spec.Env, "GITHUB_TOKEN") ||
			envHas(spec.Env, "GH_ENTERPRISE_TOKEN") || envHas(spec.Env, "GITHUB_ENTERPRISE_TOKEN") {
			t.Fatalf("forge credential reached the model-facing spec: %v", spec.Env)
		}
	})

	t.Run("worktree and quarantine complete before the model starts", func(t *testing.T) {
		e := setup(t)
		e.addReview(t, defaultFindings(), "issues-remain")
		var sawWorktree, sawQuarantine bool
		e.runner.onRun = func(spec exec.Spec) {
			if e.git.worktrees == nil || len(*e.git.worktrees) == 0 {
				t.Error("worktree was not created before the harness process")
			} else {
				sawWorktree = true
			}
			if spec.Dir == "" {
				t.Error("harness Dir is empty")
			}
			if _, err := os.Stat(filepath.Join(spec.Dir, "CLAUDE.md")); err == nil {
				t.Error("CLAUDE.md still in the checkout when the harness started")
			}
			if _, err := os.Stat(filepath.Join(spec.Dir, ".crossrev-quarantine", "CLAUDE.md")); err == nil {
				sawQuarantine = true
			} else {
				t.Errorf("CLAUDE.md was not quarantined before the harness started: %v", err)
			}
		}
		// Plant a file the descriptor quarantines so the move is observable.
		got := e.run(t)
		if got.Err != nil {
			t.Fatalf("Run: %v", got.Err)
		}
		if !sawWorktree {
			t.Fatal("onRun did not observe a worktree")
		}
		if !sawQuarantine {
			t.Fatal("quarantine did not complete before the model started")
		}
		if e.git.worktrees == nil || len(*e.git.worktrees) == 0 {
			t.Fatal("no worktree was created")
		}
	})

	t.Run("semantic omission earns one retry and a second is fatal", func(t *testing.T) {
		e := setup(t)
		e.addReview(t, defaultFindings(), "issues-remain")
		e.adapter.payloads = []json.RawMessage{missingPayload(), oneFindingPayload()}
		got := e.run(t)
		if got.Err != nil {
			t.Fatalf("Run after one retry: %v", got.Err)
		}
		if e.adapter.calls != 2 {
			t.Fatalf("adapter calls = %d, want 2", e.adapter.calls)
		}
		if e.git.restoreCalls == nil || *e.git.restoreCalls == 0 {
			t.Fatal("retry did not restore the captured tree")
		}
	})

	t.Run("shape errors on a schema-native harness do not retry", func(t *testing.T) {
		e := setup(t)
		e.addReview(t, defaultFindings(), "issues-remain")
		e.adapter.payloads = []json.RawMessage{shapePayload(), oneFindingPayload()}
		got := e.run(t)
		if got.Err == nil {
			t.Fatal("shape failure was accepted")
		}
		if e.adapter.calls != 1 {
			t.Fatalf("adapter calls = %d, want 1 (no retry)", e.adapter.calls)
		}
	})

	t.Run("a sandbox restore failure warns before refusing the attempt", func(t *testing.T) {
		e := setup(t)
		e.addReview(t, defaultFindings(), "issues-remain")
		var workdir string
		defer func() {
			if workdir != "" {
				if err := os.Chmod(workdir, 0o700); err != nil {
					t.Errorf("restore workdir permissions: %v", err)
				}
			}
		}()
		e.adapter.beforeSpec = func(inv harness.Invocation) {
			workdir = inv.Workdir
			if err := os.Chmod(workdir, 0o500); err != nil {
				t.Errorf("make sandbox restore fail: %v", err)
			}
		}
		e.adapter.specErr = errors.New("adapter setup failed")
		got := e.run(t)
		if got.Err == nil {
			t.Fatal("Run succeeded after the sandbox restore failed")
		}
		if !strings.Contains(got.Err.Error(), "could not be put back") {
			t.Errorf("error = %q, want the restore refusal", got.Err)
		}
		want := "the rejected attempt's edits could not be put back\n   They are still in the checkout, and a later run would capture them as its own baseline. Check `git status` before re-running the leg."
		if !strings.Contains(ui.Joined(got.Messages), want) {
			t.Errorf("messages = %q, want warning %q", got.Messages, want)
		}
	})

	t.Run("missing configured resolver is substituted before the claim", func(t *testing.T) {
		e := setup(t)
		e.addReview(t, defaultFindings(), "issues-remain")
		e.git.show = map[string][]byte{
			e.base.SHA() + ":.github/crossrev.yml": []byte("version: 1\nresolver:\n  harness: codex\n  model: x\n"),
		}
		e.lookPath = func(name string) (string, error) {
			if name == "claude" {
				return "/usr/bin/claude", nil
			}
			return "", os.ErrNotExist
		}
		got := e.run(t)
		if got.Err != nil {
			t.Fatalf("Run: %v", got.Err)
		}
		if len(e.forge.created) == 0 {
			t.Fatal("no claim")
		}
		if !strings.Contains(e.forge.created[0].Body, `"harness":"claude"`) {
			t.Errorf("claim named the missing harness: %s", e.forge.created[0].Body)
		}
		found := false
		for _, msg := range ui.Texts(got.Messages) {
			if strings.Contains(msg, "is not installed") && strings.Contains(msg, "claude") {
				found = true
			}
		}
		if !found {
			t.Errorf("Messages = %v, want the substitute warning (lib/run.sh:542-543)", got.Messages)
		}
		if len(e.adapter.invs) == 0 || !e.adapter.invs[0].Write {
			t.Fatal("substitute lost write permission")
		}
	})

	t.Run("hosted runner without the secret refuses before a harness process", func(t *testing.T) {
		e := setup(t)
		e.addReview(t, defaultFindings(), "issues-remain")
		t.Setenv("RUNNER_ENVIRONMENT", "github-hosted")
		t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")
		got := e.run(t)
		if got.Err == nil {
			t.Fatal("wanted a missing-secret refusal on a github-hosted runner")
		}
		if !strings.Contains(got.Err.Error(), "CLAUDE_CODE_OAUTH_TOKEN") {
			t.Errorf("err = %q, want it to name CLAUDE_CODE_OAUTH_TOKEN", got.Err)
		}
		if e.runner.specs != nil {
			t.Fatalf("harness started after a missing hosted secret: %d specs", len(e.runner.specs))
		}
	})

	t.Run("AssertEnvClean refuses a leaked ANTHROPIC_BASE_URL", func(t *testing.T) {
		e := setup(t)
		e.addReview(t, defaultFindings(), "issues-remain")
		e.legEnv = []string{"PATH=/usr/bin", "HOME=/tmp", "ANTHROPIC_BASE_URL=https://example.invalid"}
		got := e.run(t)
		if got.Err == nil {
			t.Fatal("wanted AssertEnvClean to refuse a leaked ANTHROPIC_BASE_URL")
		}
		if !strings.Contains(got.Err.Error(), "ANTHROPIC_BASE_URL") {
			t.Errorf("err = %q, want it to name ANTHROPIC_BASE_URL", got.Err)
		}
		if e.runner.specs != nil {
			t.Fatalf("harness started after an endpoint leak: %d specs", len(e.runner.specs))
		}
	})

	t.Run("the spec passed to Run keeps PATH and HOME and omits GH_TOKEN", func(t *testing.T) {
		e := setup(t)
		e.addReview(t, defaultFindings(), "issues-remain")
		e.adapter = nil
		e.legEnv = []string{"PATH=/usr/bin:/bin", "HOME=" + e.workdir, "GH_TOKEN=should-never-reach-the-model"}
		e.runner.stdout = claudeResolveStdout(string(oneFindingPayload()))
		got := e.run(t)
		if got.Err != nil {
			t.Fatalf("Run: %v", got.Err)
		}
		if len(e.runner.specs) == 0 {
			t.Fatal("no harness spec")
		}
		env := e.runner.specs[0].Env
		if name, found := forgeCredentialIn(env); found {
			t.Fatalf("harness env carried %s", name)
		}
		if !envHas(env, "PATH") {
			t.Errorf("spec.Env dropped PATH: %v", env)
		}
		if !envHas(env, "HOME") {
			t.Errorf("spec.Env dropped HOME: %v", env)
		}
	})

	t.Run("opencode schema mismatch retries once", func(t *testing.T) {
		e := setup(t)
		e.addReview(t, defaultFindings(), "issues-remain")
		e.adapter.payloads = []json.RawMessage{shapePayload(), oneFindingPayload()}
		got := e.runReq(t, Request{PR: 42, Repo: e.slug, Trigger: TriggerHuman, Harness: "opencode"})
		if got.Err != nil {
			t.Fatalf("Run: %v", got.Err)
		}
		if e.adapter.calls != 2 {
			t.Fatalf("adapter calls = %d, want 2 (schema_native false retries once)", e.adapter.calls)
		}
	})

	t.Run("duplicate payload earns one retry then a good payload invokes", func(t *testing.T) {
		e := setup(t)
		e.addReview(t, defaultFindings(), "issues-remain")
		e.adapter.payloads = []json.RawMessage{duplicatePayload(), oneFindingPayload()}
		got := e.run(t)
		if got.Err != nil {
			t.Fatalf("Run after one duplicate retry: %v", got.Err)
		}
		if e.adapter.calls != 2 {
			t.Fatalf("adapter calls = %d, want 2", e.adapter.calls)
		}
		if got.Outcome != OutcomeComplete {
			t.Fatalf("Outcome = %q, want complete", got.Outcome)
		}
	})
}

func hasFlagPair(args []string, flag, value string) bool {
	for i := 0; i < len(args)-1; i++ {
		if args[i] == flag && args[i+1] == value {
			return true
		}
	}
	return false
}
