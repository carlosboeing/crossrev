package review_test

import (
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/exec"
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

func bytesContain(b []byte, s string) bool {
	return strings.Contains(string(b), s)
}
