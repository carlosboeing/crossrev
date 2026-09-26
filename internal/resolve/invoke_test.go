package resolve

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/exec"
	"github.com/carlosboeing/crossrev/internal/harness"
	"github.com/carlosboeing/crossrev/internal/ui"
	"github.com/carlosboeing/crossrev/internal/vcs"
)

// TestInvoke pins write capability, the no-arbitrary-command grant, quarantine
// and worktree before the model, and one semantic retry.
//
// Write flag measured from lib/run.sh:494-495:
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

	t.Run("a harness write to a quarantined path warns when the checkout is restored", func(t *testing.T) {
		e := setup(t)
		e.addReview(t, defaultFindings(), "issues-remain")
		e.runner.onRun = func(spec exec.Spec) {
			// A blind write: CLAUDE.md was quarantined before the harness
			// started, so anything created at its path is discarded.
			if err := os.WriteFile(filepath.Join(spec.Dir, "CLAUDE.md"), []byte("blind\n"), 0o644); err != nil {
				t.Errorf("plant the blind write: %v", err)
			}
		}
		got := e.run(t)
		if got.Err != nil {
			t.Fatalf("Run: %v", got.Err)
		}
		want := "the harness wrote to quarantined path(s): CLAUDE.md\n   Those writes were discarded when the checkout was restored, so any finding reported as fixed by editing them is not fixed and is in no commit. Check those findings by hand."
		if !strings.Contains(ui.Joined(got.Messages), want) {
			t.Errorf("messages = %q, want warning %q", got.Messages, want)
		}
	})

	t.Run("a quarantined write still warns when the invocation never starts", func(t *testing.T) {
		e := setup(t)
		e.addReview(t, defaultFindings(), "issues-remain")
		e.adapter.beforeSpec = func(inv harness.Invocation) {
			if err := os.WriteFile(filepath.Join(inv.Workdir, "CLAUDE.md"), []byte("blind\n"), 0o644); err != nil {
				t.Errorf("plant the blind write: %v", err)
			}
		}
		e.adapter.specErr = errors.New("adapter setup failed")
		got := e.run(t)
		if got.Err == nil {
			t.Fatal("Run succeeded after the adapter spec failed")
		}
		if !strings.Contains(got.Err.Error(), "adapter setup failed") {
			t.Errorf("error = %q, want the spec failure", got.Err)
		}
		want := "the harness wrote to quarantined path(s): CLAUDE.md\n   Those writes were discarded when the checkout was restored, so any finding reported as fixed by editing them is not fixed and is in no commit. Check those findings by hand."
		if !strings.Contains(ui.Joined(got.Messages), want) {
			t.Errorf("messages = %q, want warning %q", got.Messages, want)
		}
	})

	t.Run("missing configured resolver is substituted before the claim", func(t *testing.T) {
		e := setup(t)
		e.addReview(t, defaultFindings(), "issues-remain")
		e.git.show = map[string][]byte{
			e.base.SHA() + ":.github/crossrev.yml": []byte("version: 2\nresolver:\n  harness: codex\n  model: x\n"),
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
			t.Errorf("Messages = %v, want the substitute warning (lib/run.sh:548-549)", got.Messages)
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

func TestResolvePromptFiltersGeneratedContext(t *testing.T) {
	e := setup(t)
	findings := json.RawMessage(`[
		{"id":"1111111111111111","path":"web/package-lock.json","line":1,"side":"RIGHT","severity":"high","category":"correctness","pre_existing":false,"title":"finding 1","why":"w","fix":"f"},
		{"id":"2222222222222222","path":"deleted_gen.go","line":1,"side":"LEFT","severity":"high","category":"correctness","pre_existing":false,"title":"finding 2","why":"w","fix":"f"},
		{"id":"3333333333333333","path":"app.ts","line":1,"side":"RIGHT","severity":"high","category":"correctness","pre_existing":false,"title":"finding 3","why":"w","fix":"f"}
	]`)
	e.addReview(t, findings, "issues-remain")
	e.adapter.payloads = []json.RawMessage{json.RawMessage(`{
		"blocked":false,"blocked_reason":null,"summary":"Fixed.","commit_subject":null,
		"resolutions":[
			{"finding_number":1,"resolution":"fixed","reply":"done","persist":null,"duplicate_of":null},
			{"finding_number":2,"resolution":"fixed","reply":"done","persist":null,"duplicate_of":null},
			{"finding_number":3,"resolution":"fixed","reply":"done","persist":null,"duplicate_of":null}
		]
	}`)}

	diffText := strings.Join([]string{
		"diff --git a/web/package-lock.json b/web/package-lock.json",
		"--- a/web/package-lock.json",
		"+++ b/web/package-lock.json",
		"@@ -1 +1 @@",
		"-oldlock",
		"+newlock",
		"diff --git a/deleted_gen.go b/deleted_gen.go",
		"--- a/deleted_gen.go",
		"+++ /dev/null",
		"@@ -1 +0,0 @@",
		"-// Code generated by protoc. DO NOT EDIT.",
		"diff --git a/deleted_unmentioned_gen.go b/deleted_unmentioned_gen.go",
		"--- a/deleted_unmentioned_gen.go",
		"+++ /dev/null",
		"@@ -1 +0,0 @@",
		"-// Code generated by protoc. DO NOT EDIT.",
		"diff --git a/gen/bundle.min.js b/gen/bundle.min.js",
		"--- a/gen/bundle.min.js",
		"+++ b/gen/bundle.min.js",
		"@@ -1 +1 @@",
		"-oldmin",
		"+newmin",
		"diff --git a/app.ts b/app.ts",
		"--- a/app.ts",
		"+++ b/app.ts",
		"@@ -1 +1 @@",
		"-oldapp",
		"+newapp",
		"diff --git a/ordinary_other.go b/ordinary_other.go",
		"--- a/ordinary_other.go",
		"+++ b/ordinary_other.go",
		"@@ -1 +1 @@",
		"-oldother",
		"+newother",
		"diff --git a/head_only_attr.js b/head_only_attr.js",
		"--- a/head_only_attr.js",
		"+++ b/head_only_attr.js",
		"@@ -1 +1 @@",
		"-oldhead",
		"+newhead",
	}, "\n") + "\n"

	e.forge.diff = []byte(diffText)
	e.git.show = map[string][]byte{
		"deleted_gen.go":             []byte("// Code generated by protoc. DO NOT EDIT.\npackage p\n"),
		"deleted_unmentioned_gen.go": []byte("// Code generated by protoc. DO NOT EDIT.\npackage p\n"),
		"ordinary_other.go":          []byte("package p\n"),
		"head_only_attr.js":          []byte("console.log('head only');\n"),
	}
	e.git.generatedAttrs = map[string]vcs.AttributeDecision{
		"head_only_attr.js": vcs.AttributeUnspecified,
	}

	got := e.run(t)
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if len(e.adapter.invs) == 0 {
		t.Fatal("adapter not invoked")
	}
	prompt := e.adapter.invs[0].Prompt.Text

	// The findings already name the first three paths, so the check is on
	// each diff section's own header rather than the bare path.
	for _, want := range []string{"web/package-lock.json", "deleted_gen.go", "app.ts", "ordinary_other.go", "head_only_attr.js"} {
		if header := "diff --git a/" + want + " b/" + want; !strings.Contains(prompt, header) {
			t.Errorf("prompt missing the diff section %q:\n%s", header, prompt)
		}
	}
	for _, unwanted := range []string{"gen/bundle.min.js", "deleted_unmentioned_gen.go"} {
		if strings.Contains(prompt, unwanted) {
			t.Errorf("prompt unexpectedly contains %q:\n%s", unwanted, prompt)
		}
	}
}

func TestResolvePromptAttributeQueryFailureStopsConstruction(t *testing.T) {
	e := setup(t)
	e.addReview(t, defaultFindings(), "issues-remain")
	e.git.generatedAttrsErr = errors.New("check-attr: base revision corrupt")

	got := e.run(t)
	if got.Err == nil {
		t.Fatal("Run succeeded, want error on failed attribute query")
	}
	if len(e.adapter.invs) > 0 {
		t.Fatal("adapter was invoked despite attribute query failure")
	}
}

// Classification reads the committed blob, never the pull request's own
// checkout: a changed path that is a symlink to /dev/zero would read forever,
// and one pointing outside the repository would be judged by bytes that are
// not the change.
func TestResolvePromptClassifiesTheCommittedBlobNotTheCheckout(t *testing.T) {
	e := setup(t)
	e.addReview(t, defaultFindings(), "issues-remain")
	e.adapter.payloads = []json.RawMessage{oneFindingPayload()}
	e.forge.diff = []byte(strings.Join([]string{
		"diff --git a/zero.ts b/zero.ts",
		"--- a/zero.ts",
		"+++ b/zero.ts",
		"@@ -1 +1 @@",
		"-old",
		"+new",
		"diff --git a/outside.ts b/outside.ts",
		"--- a/outside.ts",
		"+++ b/outside.ts",
		"@@ -1 +1 @@",
		"-old",
		"+new",
	}, "\n") + "\n")
	e.git.show = map[string][]byte{
		"zero.ts":    []byte("export const zero = 0\n"),
		"outside.ts": []byte("export const outside = 1\n"),
	}
	external := filepath.Join(t.TempDir(), "external.ts")
	if err := os.WriteFile(external, []byte("// Code generated by tool. DO NOT EDIT.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	e.git.onAddWorktree = func(dir string) error {
		if err := os.Symlink("/dev/zero", filepath.Join(dir, "zero.ts")); err != nil {
			return err
		}
		return os.Symlink(external, filepath.Join(dir, "outside.ts"))
	}

	got := e.run(t)
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	prompt := e.adapter.invs[0].Prompt.Text
	for _, path := range []string{"zero.ts", "outside.ts"} {
		if header := "diff --git a/" + path + " b/" + path; !strings.Contains(prompt, header) {
			t.Errorf("prompt missing %q: classified from the checkout, not the blob:\n%s", header, prompt)
		}
	}
}

func TestResolvePromptOldGitWarnsOnceAndUsesBuiltins(t *testing.T) {
	e := setup(t)
	e.addReview(t, defaultFindings(), "issues-remain")
	e.adapter.payloads = []json.RawMessage{oneFindingPayload()}
	e.git.generatedAttrsWarn = &vcs.Warning{
		Message: "git 2.39 is older than 2.40, so .gitattributes linguist-generated is not read at the base",
		Hint:    "The built-in generated-file rules still apply. Upgrade git to 2.40 or newer to read repository policy.",
	}
	e.forge.diff = []byte(strings.Join([]string{
		"diff --git a/gen/bundle.min.js b/gen/bundle.min.js",
		"--- a/gen/bundle.min.js",
		"+++ b/gen/bundle.min.js",
		"@@ -1 +1 @@",
		"-old",
		"+new",
		"diff --git a/app.ts b/app.ts",
		"--- a/app.ts",
		"+++ b/app.ts",
		"@@ -1 +1 @@",
		"-old",
		"+new",
	}, "\n") + "\n")

	got := e.run(t)
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if !strings.Contains(ui.Joined(got.Messages), "git 2.39 is older than 2.40") {
		t.Errorf("Messages = %v, want old-git warning", got.Messages)
	}
	if len(e.adapter.invs) == 0 {
		t.Fatal("adapter not invoked")
	}
	prompt := e.adapter.invs[0].Prompt.Text
	if strings.Contains(prompt, "gen/bundle.min.js") {
		t.Errorf("built-in signal did not filter bundle.min.js under old git warning:\n%s", prompt)
	}
	if !strings.Contains(prompt, "app.ts") {
		t.Errorf("prompt missing app.ts:\n%s", prompt)
	}
}

func TestResolvePromptNegatedAttributeWithGeneratedHeaderStays(t *testing.T) {
	e := setup(t)
	e.addReview(t, defaultFindings(), "issues-remain")
	e.adapter.payloads = []json.RawMessage{oneFindingPayload()}

	e.forge.diff = []byte(strings.Join([]string{
		"diff --git a/gen/marked_false.go b/gen/marked_false.go",
		"--- a/gen/marked_false.go",
		"+++ b/gen/marked_false.go",
		"@@ -1 +1 @@",
		"-old",
		"+new",
		"diff --git a/app.ts b/app.ts",
		"--- a/app.ts",
		"+++ b/app.ts",
		"@@ -1 +1 @@",
		"-old",
		"+new",
	}, "\n") + "\n")

	e.git.show = map[string][]byte{
		"gen/marked_false.go": []byte("// Code generated by protoc. DO NOT EDIT.\npackage gen\n"),
	}
	e.git.generatedAttrs = map[string]vcs.AttributeDecision{
		"gen/marked_false.go": vcs.AttributeNegated,
	}

	got := e.run(t)
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	prompt := e.adapter.invs[0].Prompt.Text
	if !strings.Contains(prompt, "gen/marked_false.go") {
		t.Errorf("negated attribute file was filtered out:\n%s", prompt)
	}

	// When backlog points to gen/marked_false.go, existing backlog rule excludes it
	e2 := setup(t)
	e2.addReview(t, defaultFindings(), "issues-remain")
	e2.adapter.payloads = []json.RawMessage{oneFindingPayload()}
	e2.forge.diff = e.forge.diff
	e2.git.show = map[string][]byte{
		"gen/marked_false.go":                   []byte("// Code generated by protoc. DO NOT EDIT.\npackage gen\n"),
		e2.base.SHA() + ":.github/crossrev.yml": []byte("version: 2\nbacklog:\n  destination: repository\n  repository:\n    path: gen/marked_false.go\n"),
	}
	e2.git.generatedAttrs = map[string]vcs.AttributeDecision{
		"gen/marked_false.go": vcs.AttributeNegated,
	}
	got2 := e2.run(t)
	if got2.Err != nil {
		t.Fatalf("Run: %v", got2.Err)
	}
	prompt2 := e2.adapter.invs[0].Prompt.Text
	if strings.Contains(prompt2, "diff --git a/gen/marked_false.go") {
		t.Errorf("backlog rule did not exclude gen/marked_false.go from diff:\n%s", prompt2)
	}
}

// The resolve prompt applies built-in signals at every size, so a small
// Markdown file written one line per paragraph would lose its diff if long
// lines counted as minified. It stays.
func TestResolvePromptKeepsUnwrappedMarkdown(t *testing.T) {
	e := setup(t)
	e.addReview(t, defaultFindings(), "issues-remain")
	e.adapter.payloads = []json.RawMessage{oneFindingPayload()}

	e.forge.diff = []byte(strings.Join([]string{
		"diff --git a/docs/README.md b/docs/README.md",
		"--- a/docs/README.md",
		"+++ b/docs/README.md",
		"@@ -1 +1 @@",
		"-old",
		"+new",
		"diff --git a/app.ts b/app.ts",
		"--- a/app.ts",
		"+++ b/app.ts",
		"@@ -1 +1 @@",
		"-old",
		"+new",
	}, "\n") + "\n")
	paragraph := strings.Repeat("An unwrapped paragraph written by a person. ", 10)
	e.git.show = map[string][]byte{
		"docs/README.md": []byte(paragraph + "\n\n" + paragraph + "\n"),
	}

	got := e.run(t)
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	prompt := e.adapter.invs[0].Prompt.Text
	if !strings.Contains(prompt, "diff --git a/docs/README.md b/docs/README.md") {
		t.Errorf("unwrapped Markdown was filtered out of the resolve diff:\n%s", prompt)
	}
}

func hasFlagPair(args []string, flag, value string) bool {
	for i := 0; i < len(args)-1; i++ {
		if args[i] == flag && args[i+1] == value {
			return true
		}
	}
	return false
}

// The version gate refuses an opencode past its supported major before the
// leg's run child starts (issue #272): the probe is the only child the runner
// ever sees.
func TestOpencodeVersionGateRefusesBeforeTheRun(t *testing.T) {
	e := setup(t)
	e.addReview(t, defaultFindings(), "issues-remain")
	e.adapter = nil // the real adapters build the specs
	e.runner.stdout = []byte("opencode v2.0.15\n")

	got := e.runReq(t, Request{PR: 42, Repo: e.slug, Trigger: TriggerHuman, Harness: "opencode"})
	if got.Err == nil {
		t.Fatal("the leg accepted an opencode 2.x install")
	}
	if !strings.Contains(got.Err.Error(), "opencode 1.x") {
		t.Errorf("err = %q, want the supported range named", got.Err)
	}
	if !strings.Contains(got.Err.Error(), "#272") {
		t.Errorf("err = %q, want issue #272 named", got.Err)
	}
	if len(e.runner.specs) != 1 {
		t.Fatalf("the runner started %d children, want only the version probe: %v", len(e.runner.specs), e.runner.specs)
	}
	if !slices.Equal(e.runner.specs[0].Args, []string{"--version"}) {
		t.Errorf("the only child was the version probe; got %v", e.runner.specs[0].Args)
	}
}

// A supported install passes the gate and reaches the run. The canned stdout is
// not a valid run stream, so the leg fails afterwards on the envelope — this
// asserts the gate let it through, not the run's outcome.
func TestOpencodeVersionGateRunsASupportedInstall(t *testing.T) {
	e := setup(t)
	e.addReview(t, defaultFindings(), "issues-remain")
	e.adapter = nil
	e.runner.stdout = []byte("opencode v1.18.21 (test stub)\n")

	e.runReq(t, Request{PR: 42, Repo: e.slug, Trigger: TriggerHuman, Harness: "opencode"})
	if len(e.runner.specs) != 2 {
		t.Fatalf("the runner started %d children, want the probe and the run: %v", len(e.runner.specs), e.runner.specs)
	}
	if !slices.Equal(e.runner.specs[0].Args, []string{"--version"}) {
		t.Errorf("the first child is the version probe; got %v", e.runner.specs[0].Args)
	}
	if got := e.runner.specs[1].Args[0]; got != "run" {
		t.Errorf("the second child is the run; got %v", e.runner.specs[1].Args)
	}
}
