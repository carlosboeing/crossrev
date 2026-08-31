package review_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/forge"
	"github.com/carlosboeing/crossrev/internal/prstate"
)

func startedClaimWithFindings(t *testing.T, id1, id2 core.FindingID) prstate.Marker {
	t.Helper()
	raw := fmt.Sprintf(`{"v":1,"leg":"review","pass":1,"state":"started","ts":%d,"comment_id":9001,"run_id":%q,"head_sha":%q,"harness":"claude","model":"reviewer-model","verdict":"issues-remain","findings":[{"id":%q,"path":"app.go","line":2,"side":"RIGHT","severity":"high","category":"correctness","pre_existing":false,"title":"Unchecked fetch response","why":"w","fix":"f","anchor":"","thread_id":null,"resolution":null,"tracked_as":null},{"id":%q,"path":"app.go","line":2,"side":"RIGHT","severity":"low","category":"maintainability","pre_existing":false,"title":"Missing return type","why":"w","fix":"f","anchor":"","thread_id":null,"resolution":null,"tracked_as":null}]}`,
		frozenNow.Unix(), runID, headSHA, id1, id2)
	return parseMarker(t, raw)
}

func TestRecoveryDoesNotRerunTheModel(t *testing.T) {
	e := newEnv(t)
	id1 := mustFindingID(t, "aaaaaaaaaaaaaaaa")
	id2 := mustFindingID(t, "bbbbbbbbbbbbbbbb")
	claim := startedClaimWithFindings(t, id1, id2)
	e.forge.comments = []forge.IssueComment{commentWithMarker(t, 9001, claim)}
	e.forge.nextID = 10001
	got := runLeg(t, e, e.request(t))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if len(e.runner.Specs()) != 0 {
		t.Fatalf("harness ran %d times on recovery, want 0", len(e.runner.Specs()))
	}
	joined := strings.Join(got.Messages, "\n")
	if !strings.Contains(joined, "Resuming pass 1") {
		t.Errorf("messages = %q, want Resuming pass 1", joined)
	}
	if !strings.Contains(joined, "already recorded its findings, so the review is not run again") {
		t.Errorf("messages = %q, want the skip-the-model line", joined)
	}
}

func TestRecoverySkipsACommentThatAlreadyCarriesAMarker(t *testing.T) {
	e := newEnv(t)
	id1 := mustFindingID(t, "aaaaaaaaaaaaaaaa")
	id2 := mustFindingID(t, "bbbbbbbbbbbbbbbb")
	claim := startedClaimWithFindings(t, id1, id2)
	e.forge.comments = []forge.IssueComment{commentWithMarker(t, 9001, claim)}
	e.forge.reviewComments = []forge.IssueComment{{
		ID:          11,
		AuthorLogin: author,
		Body:        "already" + prstate.EncodeFindingMarker(id1, 1, core.LegReview),
	}}
	e.forge.nextID = 10001
	got := runLeg(t, e, e.request(t))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if len(e.forge.reviewPosted) != 1 {
		t.Fatalf("recovery posted %d, want 1 (the missing finding)", len(e.forge.reviewPosted))
	}
	if !strings.Contains(e.forge.reviewPosted[0].Body, string(id2)) {
		t.Errorf("posted body = %s, want finding %s", e.forge.reviewPosted[0].Body, id2)
	}
	if strings.Contains(e.forge.reviewPosted[0].Body, string(id1)) {
		t.Error("re-posted the finding that already carried a marker")
	}
	joined := strings.Join(got.Messages, "\n")
	if !strings.Contains(joined, "already on the pull request from an earlier attempt") {
		t.Errorf("messages = %q, want the skip announcement", joined)
	}
}

func TestRecoveryPostsNoneWhenEveryInlineHasLanded(t *testing.T) {
	e := newEnv(t)
	id1 := mustFindingID(t, "aaaaaaaaaaaaaaaa")
	id2 := mustFindingID(t, "bbbbbbbbbbbbbbbb")
	claim := startedClaimWithFindings(t, id1, id2)
	e.forge.comments = []forge.IssueComment{commentWithMarker(t, 9001, claim)}
	e.forge.reviewComments = []forge.IssueComment{
		{ID: 11, AuthorLogin: author, Body: "a" + prstate.EncodeFindingMarker(id1, 1, core.LegReview)},
		{ID: 12, AuthorLogin: author, Body: "b" + prstate.EncodeFindingMarker(id2, 1, core.LegReview)},
	}
	e.forge.nextID = 10001
	got := runLeg(t, e, e.request(t))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if len(e.forge.reviewPosted) != 0 {
		t.Fatalf("recovery posted %d, want 0", len(e.forge.reviewPosted))
	}
	if len(e.forge.editIDs) != 2 {
		t.Fatalf("claim edits = %d, want 2 (summary then complete)", len(e.forge.editIDs))
	}
	for _, id := range e.forge.editIDs {
		if id != 9001 {
			t.Errorf("edited %d, want the original claim 9001", id)
		}
	}
	last := e.forge.edits[len(e.forge.edits)-1]
	raw, ok := prstate.DecodeMarker(last)
	if !ok {
		t.Fatal("complete edit has no marker")
	}
	marker, err := prstate.ParseMarker(raw)
	if err != nil {
		t.Fatal(err)
	}
	if marker.State != core.PassComplete {
		t.Errorf("state = %q, want complete", marker.State)
	}
}

func TestRecoveryPolicyStillComesFromTheBaseRevision(t *testing.T) {
	e := newEnv(t)
	writeBase(e, ".github/crossrev.yml", "version: 1\npolicy:\n  min_fix_severity: high\n")
	writeHead(e, ".github/crossrev.yml", "version: 1\npolicy:\n  min_fix_severity: low\n")
	id1 := mustFindingID(t, "aaaaaaaaaaaaaaaa")
	id2 := mustFindingID(t, "bbbbbbbbbbbbbbbb")
	claim := startedClaimWithFindings(t, id1, id2)
	e.forge.comments = []forge.IssueComment{commentWithMarker(t, 9001, claim)}
	e.forge.nextID = 10001
	e.cfg = mustConfig(t, "version: 1\npolicy:\n  min_fix_severity: high\n")
	got := runLeg(t, e, e.request(t))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if len(e.forge.reviewPosted) != 2 {
		t.Fatalf("posted %d, want 2", len(e.forge.reviewPosted))
	}
	highNote := "At or above this repository's `min_fix_severity` (high)"
	lowNote := "At or above this repository's `min_fix_severity` (low)"
	for _, posted := range e.forge.reviewPosted {
		if strings.Contains(posted.Body, lowNote) {
			t.Fatalf("published a body using the head's min_fix_severity: %s", posted.Body)
		}
	}
	foundHigh := false
	for _, posted := range e.forge.reviewPosted {
		if strings.Contains(posted.Body, highNote) {
			foundHigh = true
		}
	}
	if !foundHigh {
		t.Errorf("no published body used the base min_fix_severity (high); first body: %s", e.forge.reviewPosted[0].Body)
	}
}
