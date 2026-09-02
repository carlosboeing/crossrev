package review_test

import (
	"context"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/exec"
	"github.com/carlosboeing/crossrev/internal/review"
)

func TestContextLoadsThePullRequestOnce(t *testing.T) {
	e := newEnv(t)
	got := runLeg(t, e, e.request(t))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if e.forge.prCalls != 1 {
		t.Fatalf("PullRequest called %d times, want 1 (lib/github.sh:36-45, lib/run.sh:242)", e.forge.prCalls)
	}
}

func TestContextReadsPolicyAndReviewMDFromTheBaseRevision(t *testing.T) {
	e := newEnv(t)
	e.cfg = nil
	writeBase(e, ".github/crossrev.yml", "version: 1\npolicy:\n  max_passes_per_cycle: 5\n  min_fix_severity: high\nreviewer:\n  harness: claude\n")
	writeHead(e, ".github/crossrev.yml", "version: 1\npolicy:\n  max_passes_per_cycle: 99\nreviewer:\n  harness: claude\n  model: hijacked-model\n")
	writeBase(e, "REVIEW.md", "Flag every use of console.log.\n")
	writeHead(e, "REVIEW.md", "branch-supplied REVIEW.md, never read from the head.\n")
	writeBase(e, ".gitmessage", "type(scope): subject\n")
	writeHead(e, ".gitmessage", "head gitmessage\n")
	writeBase(e, "AGENTS.md", "# x\n\n## Project Map\n\n- **Tracker**: GitHub Issues\n")
	writeHead(e, "AGENTS.md", "# x\n\n## Project Map\n\n- **Tracker**: hijacked\n")

	var prompt string
	e.runner.onSpec = func(spec exec.Spec) {
		if len(spec.Args) > 0 {
			prompt = spec.Args[len(spec.Args)-1]
		}
	}
	req := e.request(t)
	got := runLeg(t, e, req)
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if got.Context.Config == nil {
		t.Fatal("context carried no config")
	}
	if got := got.Context.Config.Get(".policy.max_passes_per_cycle"); got != "5" {
		t.Errorf("max_passes_per_cycle = %q, want the base revision's 5", got)
	}
	if got := got.Context.Config.Get(".reviewer.model"); got == "hijacked-model" {
		t.Error("the head revision's model took effect")
	}
	if !strings.Contains(prompt, "Flag every use of console.log.") {
		t.Errorf("prompt missing the base REVIEW.md")
	}
	if strings.Contains(prompt, "branch-supplied REVIEW.md") {
		t.Error("prompt carried the head REVIEW.md")
	}
	if !bytesContain(got.Context.GitMessage, "type(scope): subject") {
		t.Errorf("gitmessage = %q, want the base revision", got.Context.GitMessage)
	}
	if bytesContain(got.Context.GitMessage, "head gitmessage") {
		t.Error("gitmessage was read from the head")
	}
	if got.Context.ProjectMapTracker != "GitHub Issues" {
		t.Errorf("ProjectMapTracker = %q, want GitHub Issues from the base revision", got.Context.ProjectMapTracker)
	}
}

func TestContextCarriesTheBaseAndHeadPair(t *testing.T) {
	e := newEnv(t)
	got := runLeg(t, e, e.request(t))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if got.Context.PR.BaseRefOid.SHA() != baseSHA {
		t.Errorf("base = %s, want %s", got.Context.PR.BaseRefOid.SHA(), baseSHA)
	}
	if got.Context.PR.HeadRefOid.SHA() != headSHA {
		t.Errorf("head = %s, want %s", got.Context.PR.HeadRefOid.SHA(), headSHA)
	}
}

// The shell keys the trusted author on the MODE, not on who asked for the leg:
// lib/run.sh:309 is state_trusted_author "$CTX_MODE", and CTX_MODE comes from
// the configuration at lib/run.sh:299. lib/state.sh:26 then branches on
// `automated` alone.
func TestContextResolvesAppSlugInAutomatedMode(t *testing.T) {
	e := newEnv(t)
	e.cfg = mustConfig(t, "version: 1\nmode: automated\n")
	t.Setenv("CROSSREV_APP_SLUG", "crossrev")
	req := e.request(t)
	req.Author = ""
	req.Trigger = review.TriggerHuman
	leg := e.leg(t)
	got := leg.Run(context.Background(), req)
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if got.Context.Author != "crossrev[bot]" {
		t.Errorf("Author = %q, want crossrev[bot] from CROSSREV_APP_SLUG (lib/state.sh:35-40)", got.Context.Author)
	}
}

// The mirror of it: an automatic trigger against a `mode: local` repository is
// the invoking user, because lib/state.sh's `*)` arm covers every mode that is
// not `automated`. Keyed on the trigger instead, this refuses with "cannot
// determine which App's markers to trust" after two gh calls.
func TestContextKeysTheTrustedAuthorOnTheModeNotTheTrigger(t *testing.T) {
	e := newEnv(t)
	e.cfg = mustConfig(t, "version: 1\nmode: local\n")
	t.Setenv("CROSSREV_APP_SLUG", "")
	req := e.request(t)
	req.Author = ""
	req.Trigger = review.TriggerAutomatic
	leg := e.leg(t)
	got := leg.Run(context.Background(), req)
	if got.Err != nil {
		t.Fatalf("Run: %v (lib/run.sh:309 keys on the mode)", got.Err)
	}
	if got.Context.Author != author {
		t.Errorf("Author = %q, want the invoking user %q (lib/state.sh:44)", got.Context.Author, author)
	}
}

func TestContextExcludesRepositoryBacklogFromTheDiff(t *testing.T) {
	e := newEnv(t)
	e.cfg = mustConfig(t, "version: 1\nbacklog:\n  destination: repository\n  repository:\n    layout: file\n    path: BACKLOG.md\n")
	e.forge.diff = []byte("diff --git a/app.go b/app.go\n--- a/app.go\n+++ b/app.go\n@@ -1,1 +1,2 @@\n context\n+added\ndiff --git a/BACKLOG.md b/BACKLOG.md\n--- a/BACKLOG.md\n+++ b/BACKLOG.md\n@@ -1,1 +1,2 @@\n old-backlog\n+UNIQUE_BACKLOG_HUNK\n")
	var prompt string
	e.runner.onSpec = func(spec exec.Spec) {
		if len(spec.Args) > 0 {
			prompt = spec.Args[len(spec.Args)-1]
		}
	}
	got := runLeg(t, e, e.request(t))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if got.Context.Backlog.Destination != "repository" {
		t.Errorf("Backlog.Destination = %q, want repository", got.Context.Backlog.Destination)
	}
	if got.Context.Backlog.Path != "BACKLOG.md" {
		t.Errorf("Backlog.Path = %q, want BACKLOG.md", got.Context.Backlog.Path)
	}
	if !strings.Contains(prompt, "added") {
		t.Error("prompt dropped the code diff")
	}
	if strings.Contains(prompt, "UNIQUE_BACKLOG_HUNK") {
		t.Error("prompt carried the repository backlog (lib/run.sh:1130-1132)")
	}
}

func bytesContain(b []byte, s string) bool {
	return strings.Contains(string(b), s)
}
