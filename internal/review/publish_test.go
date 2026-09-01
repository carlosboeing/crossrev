package review_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/exec"
	"github.com/carlosboeing/crossrev/internal/forge"
	"github.com/carlosboeing/crossrev/internal/policy"
	"github.com/carlosboeing/crossrev/internal/prstate"
)

const twoFindings = `[{"path":"app.go","line":2,"side":"RIGHT","severity":"high","category":"correctness","pre_existing":false,"title":"Unchecked fetch response","why":"A failed request looks like a success","fix":"Check response.ok"},{"path":"app.go","line":2,"side":"RIGHT","severity":"low","category":"maintainability","pre_existing":false,"title":"Missing return type","why":"The inferred type is wider than intended","fix":"Annotate it"}]`

func issuesPayload(findings string) string {
	return `{"verdict":"issues-remain","blocked_reason":null,"findings":` + findings + `}`
}

func writeAppGo(t *testing.T, dir string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "app.go"), []byte("context\nadded\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestPublishPostsOneInlineCommentPerFinding(t *testing.T) {
	e := newEnv(t)
	writeAppGo(t, e.dir)
	e.runner.script = []exec.Result{{ExitCode: 0, Stdout: claudeStdout(issuesPayload(twoFindings))}}
	got := runLeg(t, e, e.request(t))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if len(e.forge.reviewPosted) != 2 {
		t.Fatalf("inline posts = %d, want 2", len(e.forge.reviewPosted))
	}
	for i, posted := range e.forge.reviewPosted {
		if posted.Path != "app.go" || posted.Line != 2 || posted.Side != core.SideRight {
			t.Errorf("post %d anchor = %s:%d %s, want app.go:2 RIGHT", i, posted.Path, posted.Line, posted.Side)
		}
		if posted.Commit.SHA() != headSHA {
			t.Errorf("post %d commit = %s, want the loaded head", i, posted.Commit.SHA())
		}
		if !strings.Contains(posted.Body, "<!-- crossrev:f") {
			t.Errorf("post %d body missing a finding marker", i)
		}
	}
}

func TestPublishFallsBackToATopLevelComment(t *testing.T) {
	e := newEnv(t)
	writeAppGo(t, e.dir)
	e.forge.forceFallback = true
	e.runner.script = []exec.Result{{ExitCode: 0, Stdout: claudeStdout(issuesPayload(twoFindings))}}
	got := runLeg(t, e, e.request(t))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if len(e.forge.reviewPosted) != 2 {
		t.Fatalf("attempted posts = %d, want 2", len(e.forge.reviewPosted))
	}
	fallbacks := 0
	for _, body := range e.forge.created {
		if strings.Contains(body, "**app.go:2** (RIGHT)") {
			fallbacks++
		}
	}
	if fallbacks != 2 {
		t.Fatalf("fallback comments = %d, want 2; created = %v", fallbacks, e.forge.created)
	}
	for _, p := range e.forge.placements {
		if p != forge.PlacementFallback {
			t.Errorf("placement = %q, want fallback", p)
		}
	}
	want := "GitHub would not anchor a comment to app.go:2 (RIGHT) on acme/widget#42\n   The finding is posted as a top-level comment naming that location instead, so it is not lost. A finding on a deleted line needs side LEFT."
	if !containsString(got.Messages, want) {
		t.Errorf("messages = %q, want warning %q", got.Messages, want)
	}
}

func TestPublishEditsTheOriginalClaimAndDoesNotRepostIt(t *testing.T) {
	e := newEnv(t)
	writeAppGo(t, e.dir)
	e.runner.script = []exec.Result{{ExitCode: 0, Stdout: claudeStdout(issuesPayload(twoFindings))}}
	got := runLeg(t, e, e.request(t))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if got.ClaimID != 9001 {
		t.Fatalf("ClaimID = %d, want 9001", got.ClaimID)
	}
	claims := 0
	for _, body := range e.forge.created {
		if strings.Contains(body, "**crossrev — reviewing") {
			claims++
		}
	}
	if claims != 1 {
		t.Fatalf("claim creates = %d, want 1 (edit, do not replace)", claims)
	}
	if len(e.forge.editIDs) == 0 {
		t.Fatal("no claim edits")
	}
	for _, id := range e.forge.editIDs {
		if id != 9001 {
			t.Errorf("edited comment %d, want the original claim 9001", id)
		}
	}
	last := e.forge.edits[len(e.forge.edits)-1]
	raw, ok := prstate.DecodeMarker(last)
	if !ok {
		t.Fatalf("last edit has no marker: %s", last)
	}
	marker, err := prstate.ParseMarker(raw)
	if err != nil {
		t.Fatal(err)
	}
	if marker.State != core.PassComplete {
		t.Errorf("final state = %q, want complete", marker.State)
	}
}

func TestPublishCompletesOnlyAfterComments(t *testing.T) {
	e := newEnv(t)
	writeAppGo(t, e.dir)
	e.runner.script = []exec.Result{{ExitCode: 0, Stdout: claudeStdout(issuesPayload(twoFindings))}}
	got := runLeg(t, e, e.request(t))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	reviewAt, completeAt := -1, -1
	for i, op := range e.forge.ops {
		if op == "review-comment" && reviewAt < 0 {
			reviewAt = i
		}
	}
	for i, body := range e.forge.edits {
		raw, ok := prstate.DecodeMarker(body)
		if !ok {
			continue
		}
		marker, err := prstate.ParseMarker(raw)
		if err != nil {
			continue
		}
		if marker.State == core.PassComplete {
			// Map this edit onto ops: count comment-edit occurrences.
			n := 0
			for j, op := range e.forge.ops {
				if op == "comment-edit" {
					if n == i {
						completeAt = j
						break
					}
					n++
				}
			}
			break
		}
	}
	if reviewAt < 0 {
		t.Fatal("no review-comment op")
	}
	if completeAt < 0 {
		t.Fatal("no complete edit")
	}
	if completeAt < reviewAt {
		t.Fatalf("complete at op %d before comments at %d: %v", completeAt, reviewAt, e.forge.ops)
	}
}

func TestPublishWriteStaysFalse(t *testing.T) {
	e := newEnv(t)
	writeAppGo(t, e.dir)
	e.runner.script = []exec.Result{{ExitCode: 0, Stdout: claudeStdout(issuesPayload(twoFindings))}}
	got := runLeg(t, e, e.request(t))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if len(e.runner.Specs()) != 1 {
		t.Fatalf("harness calls = %d, want 1", len(e.runner.Specs()))
	}
	if strings.Contains(strings.Join(e.runner.Specs()[0].Args, " "), "acceptEdits") {
		t.Errorf("review invocation granted writes: %v", e.runner.Specs()[0].Args)
	}
}

func TestPublishLabelsMoveThePassPill(t *testing.T) {
	e := newEnv(t)
	writeAppGo(t, e.dir)
	e.forge.pr.Labels = []forge.Label{
		{Name: policy.LabelPassPrefix + "1"},
		{Name: policy.LabelAwaitingReview},
	}
	e.runner.script = []exec.Result{{ExitCode: 0, Stdout: claudeStdout(issuesPayload(twoFindings))}}
	got := runLeg(t, e, e.request(t))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if !containsString(e.forge.labelsAdded, policy.LabelPassPrefix+"1") {
		t.Errorf("labels added = %v, want pass-1", e.forge.labelsAdded)
	}
	if !containsString(e.forge.labelsAdded, policy.LabelAwaitingResolution) {
		t.Errorf("labels added = %v, want awaiting-resolution", e.forge.labelsAdded)
	}
	if !containsString(e.forge.labelsRemoved, policy.LabelAwaitingReview) {
		t.Errorf("labels removed = %v, want awaiting-review", e.forge.labelsRemoved)
	}
}

func TestPublishLabelsShedHigherPassPillsOnAReset(t *testing.T) {
	e := newEnv(t)
	writeAppGo(t, e.dir)
	e.forge.pr.Labels = []forge.Label{
		{Name: policy.LabelPassPrefix + "2"},
		{Name: policy.LabelPassPrefix + "3"},
		{Name: policy.LabelConverged},
	}
	e.runner.script = []exec.Result{{ExitCode: 0, Stdout: claudeStdout(issuesPayload(twoFindings))}}
	got := runLeg(t, e, e.request(t))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if !containsString(e.forge.labelsAdded, policy.LabelPassPrefix+"1") {
		t.Errorf("labels added = %v, want pass-1", e.forge.labelsAdded)
	}
	if !containsString(e.forge.labelsRemoved, policy.LabelPassPrefix+"2") {
		t.Errorf("labels removed = %v, want pass-2", e.forge.labelsRemoved)
	}
	if !containsString(e.forge.labelsRemoved, policy.LabelPassPrefix+"3") {
		t.Errorf("labels removed = %v, want pass-3", e.forge.labelsRemoved)
	}
	if !containsString(e.forge.labelsRemoved, policy.LabelConverged) {
		t.Errorf("labels removed = %v, want converged", e.forge.labelsRemoved)
	}
}

func containsString(got []string, want string) bool {
	for _, s := range got {
		if s == want {
			return true
		}
	}
	return false
}

func TestPublishAttachesThreadIDsBeforeTheSummary(t *testing.T) {
	e := newEnv(t)
	writeAppGo(t, e.dir)
	e.runner.script = []exec.Result{{ExitCode: 0, Stdout: claudeStdout(issuesPayload(twoFindings))}}
	got := runLeg(t, e, e.request(t))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	last := e.forge.edits[len(e.forge.edits)-1]
	raw, ok := prstate.DecodeMarker(last)
	if !ok {
		t.Fatal("last edit has no marker")
	}
	marker, err := prstate.ParseMarker(raw)
	if err != nil {
		t.Fatal(err)
	}
	var findings []map[string]json.RawMessage
	if err := marker.DecodeFindings(&findings); err != nil {
		t.Fatal(err)
	}
	if len(findings) != 2 {
		t.Fatalf("findings = %d, want 2", len(findings))
	}
	for i, f := range findings {
		if string(f["thread_id"]) == "null" || string(f["thread_id"]) == "" {
			t.Errorf("finding %d thread_id = %s, want a GraphQL id", i, f["thread_id"])
		}
		if string(f["root_comment_id"]) == "null" || string(f["root_comment_id"]) == "" {
			t.Errorf("finding %d root_comment_id = %s, want a numeric id", i, f["root_comment_id"])
		}
	}
}

func TestPublishLabelWarningContainsFullText(t *testing.T) {
	e := newEnv(t)
	writeAppGo(t, e.dir)
	e.forge.labelAddErr = os.ErrPermission
	e.runner.script = []exec.Result{{ExitCode: 0, Stdout: claudeStdout(issuesPayload(twoFindings))}}
	got := runLeg(t, e, e.request(t))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	wantWarning := "could not apply the label 'crossrev/pass-1' to acme/widget#42\n   Locally that is cosmetic, because this process drives both legs itself. In automated mode it would stall the chain, which is what `crossrev init` creates the labels for."
	found := false
	for _, msg := range got.Messages {
		if msg == wantWarning {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("messages = %q, want warning %q", got.Messages, wantWarning)
	}
}
