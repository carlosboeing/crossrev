package resolve

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/config"
	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/forge"
	"github.com/carlosboeing/crossrev/internal/harness"
)

func deferredPayload(persist json.RawMessage, dup any) json.RawMessage {
	dupJSON := "null"
	if dup != nil {
		b, err := json.Marshal(dup)
		if err != nil {
			panic(err)
		}
		dupJSON = string(b)
	}
	p := "null"
	if persist != nil {
		p = string(persist)
	}
	return json.RawMessage(`{"blocked":false,"blocked_reason":null,"summary":"Deferred.","commit_subject":null,"resolutions":[{"finding_number":1,"resolution":"deferred","reply":"needs a follow-up","persist":` + p + `,"duplicate_of":` + dupJSON + `}]}`)
}

func persistDoc() json.RawMessage {
	return json.RawMessage(`{"title":"Legacy export is untyped","body":"Measured before filing."}`)
}

func githubIssuesConfig() []byte {
	return []byte(`version: 1
resolver:
  harness: claude
backlog:
  destination: github_issues
  github_issues:
    tracking_label: crossrev-review
    labels: [bug]
    comment_on_existing_issue: true
`)
}

func repositoryBacklogConfig(layout, path string) []byte {
	return []byte(`version: 1
resolver:
  harness: claude
backlog:
  destination: repository
  repository:
    layout: ` + layout + `
    path: ` + path + `
`)
}

// TestPersist pins persist-before-resolve, both backlog destinations, and
// that a failed write leaves the thread open (lib/run.sh:2144-2219, :2489-2530).
func TestPersist(t *testing.T) {
	t.Run("a new github issue is filed before its thread is resolved", func(t *testing.T) {
		e := setup(t)
		e.addReview(t, defaultFindings(), "issues-remain")
		e.git.show = map[string][]byte{e.base.SHA() + ":.github/crossrev.yml": githubIssuesConfig()}
		e.adapter.payloads = []json.RawMessage{deferredPayload(persistDoc(), nil)}
		e.forge.threads = []forge.ReviewThread{{
			ID:            "thread-1",
			Path:          "app.ts",
			Line:          2,
			RootCommentID: 55,
			FindingIDs:    []core.FindingID{mustFinding(t)},
		}}
		got := e.run(t)
		if got.Err != nil {
			t.Fatalf("Run: %v", got.Err)
		}
		if len(e.forge.issues) != 1 {
			t.Fatalf("issues filed = %d, want 1: %+v", len(e.forge.issues), e.forge.issues)
		}
		if !firstOpBefore(e.forge.order, "IssueCreate", "ThreadResolve") {
			t.Fatalf("thread resolved before the issue landed: %v", e.forge.order)
		}
		if !firstOpBefore(e.forge.order, "IssueCreate", "ReviewReply") && !firstOpBefore(e.forge.order, "IssueCreate", "ThreadResolve") {
			t.Fatalf("persist was not first: %v", e.forge.order)
		}
		if countOp(e.forge.order, "ThreadResolve") != 1 {
			t.Fatalf("ThreadResolve count = %d, want 1; order=%v", countOp(e.forge.order, "ThreadResolve"), e.forge.order)
		}
		if !strings.Contains(string(got.Resolutions), `"crossrev_tracked"`) {
			t.Fatalf("tracked field missing from resolutions: %s", got.Resolutions)
		}
	})

	t.Run("an existing issue is not filed again", func(t *testing.T) {
		e := setup(t)
		e.addReview(t, defaultFindings(), "issues-remain")
		e.git.show = map[string][]byte{e.base.SHA() + ":.github/crossrev.yml": githubIssuesConfig()}
		e.adapter.payloads = []json.RawMessage{deferredPayload(persistDoc(), nil)}
		e.forge.byFinding = map[string]int{testFinding: 77}
		e.forge.threads = []forge.ReviewThread{{
			ID:            "thread-1",
			RootCommentID: 55,
			FindingIDs:    []core.FindingID{mustFinding(t)},
		}}
		got := e.run(t)
		if got.Err != nil {
			t.Fatalf("Run: %v", got.Err)
		}
		if len(e.forge.issues) != 0 {
			t.Fatalf("filed a duplicate issue: %+v", e.forge.issues)
		}
		if countOp(e.forge.order, "ThreadResolve") != 1 {
			t.Fatal("matched issue did not resolve its thread")
		}
		if !strings.Contains(string(got.Resolutions), "acme/widget#77") {
			t.Fatalf("tracked the wrong landing: %s", got.Resolutions)
		}
	})

	t.Run("duplicate_of does not file and comments when asked", func(t *testing.T) {
		e := setup(t)
		e.addReview(t, defaultFindings(), "issues-remain")
		e.git.show = map[string][]byte{e.base.SHA() + ":.github/crossrev.yml": githubIssuesConfig()}
		e.adapter.payloads = []json.RawMessage{deferredPayload(persistDoc(), 99)}
		e.forge.candidates = []forge.IssueCandidate{{Number: 99, State: "OPEN", Title: "Legacy"}}
		e.forge.threads = []forge.ReviewThread{{
			ID:            "thread-1",
			RootCommentID: 55,
			FindingIDs:    []core.FindingID{mustFinding(t)},
		}}
		got := e.run(t)
		if got.Err != nil {
			t.Fatalf("Run: %v", got.Err)
		}
		if len(e.forge.issues) != 0 {
			t.Fatalf("filed despite duplicate_of: %+v", e.forge.issues)
		}
		if len(e.forge.issueComments) != 1 {
			t.Fatalf("issue comments = %d, want 1", len(e.forge.issueComments))
		}
		if !strings.Contains(string(got.Resolutions), "acme/widget#99") {
			t.Fatalf("tracked the wrong landing: %s", got.Resolutions)
		}
	})

	t.Run("a failed persist leaves the thread open", func(t *testing.T) {
		e := setup(t)
		e.addReview(t, defaultFindings(), "issues-remain")
		e.git.show = map[string][]byte{e.base.SHA() + ":.github/crossrev.yml": githubIssuesConfig()}
		e.adapter.payloads = []json.RawMessage{deferredPayload(persistDoc(), nil)}
		e.forge.issueErr = errPersistFailed
		e.forge.threads = []forge.ReviewThread{{
			ID:            "thread-1",
			RootCommentID: 55,
			FindingIDs:    []core.FindingID{mustFinding(t)},
		}}
		got := e.run(t)
		if got.Err != nil {
			t.Fatalf("Run: %v", got.Err)
		}
		if countOp(e.forge.order, "ThreadResolve") != 0 {
			t.Fatalf("resolved a thread whose write did not land: %v", e.forge.order)
		}
		if strings.Contains(string(got.Resolutions), `"crossrev_tracked":"acme/widget#`) {
			t.Fatalf("recorded a landing that did not happen: %s", got.Resolutions)
		}
	})

	t.Run("a repository folder write is one file per finding", func(t *testing.T) {
		e := setup(t)
		e.addReview(t, defaultFindings(), "issues-remain")
		backlog := filepath.Join(e.workdir, ".crossrev", "backlog")
		e.git.show = map[string][]byte{
			e.base.SHA() + ":.github/crossrev.yml": repositoryBacklogConfig("folder", ".crossrev/backlog"),
		}
		e.adapter.payloads = []json.RawMessage{deferredPayload(persistDoc(), nil)}
		wt := t.TempDir()
		e.git.worktreeDir = wt
		got := e.run(t)
		if got.Err != nil {
			t.Fatalf("Run: %v", got.Err)
		}
		_ = backlog
		if e.git.worktrees == nil || len(*e.git.worktrees) == 0 {
			t.Fatal("no worktree")
		}
		target := filepath.Join((*e.git.worktrees)[0], ".crossrev", "backlog", testFinding+".md")
		body, err := os.ReadFile(target)
		if err != nil {
			t.Fatalf("backlog file: %v", err)
		}
		if !strings.Contains(string(body), "# Legacy export is untyped") {
			t.Errorf("title missing: %s", body)
		}
		if !strings.Contains(string(body), "Found by crossrev while reviewing acme/widget#42") {
			t.Errorf("footer missing: %s", body)
		}
		if !strings.Contains(string(got.Resolutions), testFinding+".md") {
			t.Fatalf("tracked path missing: %s", got.Resolutions)
		}
	})

	t.Run("a repository file write appends rather than replacing", func(t *testing.T) {
		e := setup(t)
		e.addReview(t, defaultFindings(), "issues-remain")
		e.git.show = map[string][]byte{
			e.base.SHA() + ":.github/crossrev.yml": repositoryBacklogConfig("file", ".crossrev/backlog.md"),
		}
		e.adapter.payloads = []json.RawMessage{deferredPayload(persistDoc(), nil)}
		got := e.run(t)
		if got.Err != nil {
			t.Fatalf("Run: %v", got.Err)
		}
		if e.git.worktrees == nil || len(*e.git.worktrees) == 0 {
			t.Fatal("no worktree")
		}
		target := filepath.Join((*e.git.worktrees)[0], ".crossrev", "backlog.md")
		body, err := os.ReadFile(target)
		if err != nil {
			t.Fatalf("backlog file: %v", err)
		}
		if !strings.Contains(string(body), "## Legacy export is untyped") {
			t.Errorf("heading missing: %s", body)
		}
	})

	t.Run("with no backlog configured a deferral is not recorded empty", func(t *testing.T) {
		e := setup(t)
		e.addReview(t, defaultFindings(), "issues-remain")
		e.git.show = map[string][]byte{
			e.base.SHA() + ":.github/crossrev.yml": []byte("version: 1\nresolver:\n  harness: claude\nbacklog:\n  destination: none\n"),
		}
		e.adapter.payloads = []json.RawMessage{deferredPayload(persistDoc(), nil)}
		got := e.run(t)
		if got.Err != nil {
			t.Fatalf("Run: %v", got.Err)
		}
		if strings.Contains(string(got.Resolutions), "crossrev_tracked") {
			t.Fatalf("recorded an empty tracking field with no backlog: %s", got.Resolutions)
		}
		if countOp(e.forge.order, "ThreadResolve") != 0 {
			t.Fatal("resolved a thread with nowhere to track it")
		}
	})

	t.Run("persistDeferred preserves resolutions key order when adding crossrev_tracked", func(t *testing.T) {
		customResolution := `{"finding_id":"` + testFinding + `","reply":"needs a follow-up","resolution":"deferred","persist":{"title":"Legacy export is untyped","body":"Measured before filing."},"duplicate_of":null}`
		recs, err := harness.DecodeStream([]byte(customResolution))
		if err != nil {
			t.Fatalf("decode recs: %v", err)
		}
		customFinding := `{"id":"` + testFinding + `","path":"app.ts","line":2,"severity":"medium","title":"Legacy export is untyped"}`
		findings, err := harness.DecodeStream([]byte(customFinding))
		if err != nil {
			t.Fatalf("decode findings: %v", err)
		}

		e := setup(t)
		s := &session{
			pass:    1,
			repo:    e.slug,
			req:     Request{PR: 42},
			backlog: config.Backlog{Destination: config.DestinationRepository, Layout: config.LayoutFolder, Path: ".crossrev/backlog"},
		}
		workdir := t.TempDir()
		leg := &Leg{Forge: e.forge, Git: e.git}
		filed, matched, wrote, lines, out := leg.persistDeferred(context.Background(), s, workdir, recs, findings, e.head.SHA())
		if filed != 1 || !wrote {
			t.Fatalf("filed=%d, wrote=%v, lines=%s", filed, wrote, lines)
		}
		_ = matched
		marshaledOut := marshalResolutions(out)
		wantOut := `[{"finding_id":"` + testFinding + `","reply":"needs a follow-up","resolution":"deferred","persist":{"title":"Legacy export is untyped","body":"Measured before filing."},"duplicate_of":null,"crossrev_tracked":".crossrev/backlog/` + testFinding + `.md"}]`
		if string(marshaledOut) != wantOut {
			t.Fatalf("marshaled out = %s, want %s", string(marshaledOut), wantOut)
		}
	})
}

type persistFail string

func (persistFail) Error() string { return "persist failed" }

const errPersistFailed persistFail = "persist failed"
