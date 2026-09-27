package review_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/exec"
	"github.com/carlosboeing/crossrev/internal/forge"
	"github.com/carlosboeing/crossrev/internal/review"
)

// secondHeadSHA is the head a later push moves the pull request to, so the
// next run admits pass 2 through IsNewRevision rather than recovery.
const secondHeadSHA = "3333333333333333333333333333333333333333"

// seedCompletePassOne plants a finished pass-1 review marker at the old head,
// so the run under test admits pass 2 as a new revision.
func seedCompletePassOne(t *testing.T, e *env, findings string) {
	t.Helper()
	raw := fmt.Sprintf(`{"v":1,"leg":"review","pass":1,"state":"complete","ts":1699950000,"run_id":"x","head_sha":%q,"verdict":"issues-remain","findings":%s}`, oldSHA, findings)
	e.forge.comments = []forge.IssueComment{commentWithMarker(t, 8001, parseMarker(t, raw))}
}

func markerFindings(t *testing.T, raw json.RawMessage) []review.Finding {
	t.Helper()
	var out []review.Finding
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode marker findings: %v", err)
	}
	return out
}

func findingByTitle(findings []review.Finding, title string) review.Finding {
	for _, f := range findings {
		if f.Title == title {
			return f
		}
	}
	return review.Finding{}
}

func lastSummary(e *env) string {
	if len(e.forge.edits) == 0 {
		return ""
	}
	return e.forge.edits[len(e.forge.edits)-1]
}

// On passes after the first, a finding below min_fix_severity is recorded
// with posted:false, skipped by the posting loop, and counted in the
// summary — while a finding at the threshold still posts.
func TestPublishHoldsBelowThresholdFindingsAfterTheFirstPass(t *testing.T) {
	e := newEnv(t)
	writeAppGo(t, e.dir)
	seedCompletePassOne(t, e, `[]`)
	e.runner.script = []exec.Result{{ExitCode: 0, Stdout: claudeStdout(issuesPayload(twoFindings))}}
	got := runLeg(t, e, e.request(t))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if len(e.forge.reviewPosted) != 1 {
		t.Fatalf("inline posts = %d, want 1 (the high finding only)", len(e.forge.reviewPosted))
	}
	if !strings.Contains(e.forge.reviewPosted[0].Body, "Unchecked fetch response") {
		t.Errorf("posted body does not carry the high finding: %q", e.forge.reviewPosted[0].Body)
	}
	stored := markerFindings(t, got.Marker.Findings)
	low := findingByTitle(stored, "Missing return type")
	if low.ID == "" {
		t.Fatal("the low finding is not recorded on the marker")
	}
	if low.Posted == nil || *low.Posted {
		t.Errorf("low finding posted = %v, want explicit false", low.Posted)
	}
	high := findingByTitle(stored, "Unchecked fetch response")
	if high.ID == "" {
		t.Fatal("the high finding is not recorded on the marker")
	}
	if high.Posted != nil {
		t.Errorf("high finding posted = %v, want absent (posted)", *high.Posted)
	}
	if summary := lastSummary(e); !strings.Contains(summary, "1 finding below medium recorded and not posted") {
		t.Errorf("summary = %q, want the held-findings count", summary)
	}
}

// Pass 1 posts everything, including findings below the threshold.
func TestPublishPostsEverythingOnTheFirstPass(t *testing.T) {
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
	if raw := string(got.Marker.Findings); strings.Contains(raw, `"posted":false`) {
		t.Errorf("pass-1 marker holds a finding: %s", raw)
	}
	if summary := lastSummary(e); strings.Contains(summary, "recorded and not posted") {
		t.Errorf("pass-1 summary counts held findings: %q", summary)
	}
}

// A pre-existing finding at or above the threshold still posts on later
// passes; only severity below the threshold is held.
func TestPublishPreExistingFindingsAboveThresholdStillPost(t *testing.T) {
	e := newEnv(t)
	writeAppGo(t, e.dir)
	seedCompletePassOne(t, e, `[]`)
	payload := `{"verdict":"issues-remain","blocked_reason":null,"findings":[` +
		`{"path":"app.go","line":2,"side":"RIGHT","severity":"high","category":"correctness","pre_existing":true,"title":"Old unchecked fetch","why":"w","fix":"f"},` +
		`{"path":"app.go","line":2,"side":"RIGHT","severity":"low","category":"maintainability","pre_existing":false,"title":"Missing return type","why":"w","fix":"f"}]}` +
		``
	e.runner.script = []exec.Result{{ExitCode: 0, Stdout: claudeStdout(payload)}}
	got := runLeg(t, e, e.request(t))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if len(e.forge.reviewPosted) != 1 {
		t.Fatalf("inline posts = %d, want 1 (the pre-existing high finding)", len(e.forge.reviewPosted))
	}
	if !strings.Contains(e.forge.reviewPosted[0].Body, "Old unchecked fetch") {
		t.Errorf("posted body does not carry the pre-existing finding: %q", e.forge.reviewPosted[0].Body)
	}
	stored := markerFindings(t, got.Marker.Findings)
	if pre := findingByTitle(stored, "Old unchecked fetch"); pre.ID == "" || (pre.Posted != nil && !*pre.Posted) {
		t.Errorf("pre-existing high finding is not posted: %+v", pre)
	}
	if low := findingByTitle(stored, "Missing return type"); low.ID == "" || low.Posted == nil || *low.Posted {
		t.Errorf("new low finding is not held: %+v", low)
	}
}

// A held finding raised again at a higher severity posts then: held as low
// on pass 2 (no comment reaches the pull request), then raised as medium on
// pass 3 under the same finding id, which posts.
func TestPublishReRaisedFindingPostsAtHigherSeverity(t *testing.T) {
	e := newEnv(t)
	writeAppGo(t, e.dir)
	seedCompletePassOne(t, e, `[]`)
	lowOnly := `{"verdict":"issues-remain","blocked_reason":null,"findings":[` +
		`{"path":"app.go","line":2,"side":"RIGHT","severity":"low","category":"maintainability","pre_existing":false,"title":"Missing return type","why":"w","fix":"f"}]}`
	e.runner.script = []exec.Result{{ExitCode: 0, Stdout: claudeStdout(lowOnly)}}
	second := runLeg(t, e, e.request(t))
	if second.Err != nil {
		t.Fatalf("pass 2 Run: %v", second.Err)
	}
	if len(e.forge.reviewPosted) != 0 {
		t.Fatalf("pass-2 posts = %d, want 0 (the low finding is held)", len(e.forge.reviewPosted))
	}
	heldID := ""
	for _, f := range markerFindings(t, second.Marker.Findings) {
		if f.Title == "Missing return type" {
			heldID = f.ID
			if f.Posted == nil || *f.Posted {
				t.Fatalf("pass 2 did not hold the low finding: %+v", f)
			}
		}
	}
	if heldID == "" {
		t.Fatal("pass 2 recorded no finding id")
	}

	e.forge.pr.HeadRefOid = mustRev(t, secondHeadSHA)
	raised := `{"verdict":"issues-remain","blocked_reason":null,"findings":[` +
		`{"path":"app.go","line":2,"side":"RIGHT","severity":"medium","category":"maintainability","pre_existing":false,"title":"Missing return type","why":"w","fix":"f"}]}`
	e.runner.script = []exec.Result{{ExitCode: 0, Stdout: claudeStdout(raised)}}
	third := runLeg(t, e, e.request(t))
	if third.Err != nil {
		t.Fatalf("pass 3 Run: %v", third.Err)
	}
	if len(e.forge.reviewPosted) != 1 {
		t.Fatalf("pass-3 posts = %d, want 1 (the re-raised medium finding)", len(e.forge.reviewPosted))
	}
	if !strings.Contains(e.forge.reviewPosted[0].Body, "Missing return type") {
		t.Errorf("pass-3 post does not carry the re-raised finding: %q", e.forge.reviewPosted[0].Body)
	}
	stored := markerFindings(t, third.Marker.Findings)
	raisedFinding := findingByTitle(stored, "Missing return type")
	if raisedFinding.ID != heldID {
		t.Errorf("re-raised id = %q, want the held id %q", raisedFinding.ID, heldID)
	}
	if raisedFinding.Posted != nil {
		t.Errorf("re-raised posted = %v, want absent (posted)", *raisedFinding.Posted)
	}
}

// A held finding reaches the next review's prior table with resolution
// not_posted, while a posted finding from the same pass reads none.
func TestPriorNotPostedResolutionReachesTheNextPrompt(t *testing.T) {
	e := newEnv(t)
	writeAppGo(t, e.dir)
	seedCompletePassOne(t, e, `[{`+
		`"id":"aaaaaaaaaaaaaaaa","path":"app.go","line":2,"side":"RIGHT","severity":"low","category":"maintainability",`+
		`"pre_existing":false,"title":"Missing return type","why":"w","fix":"f","anchor":"","anchor_kind":"line",`+
		`"anchor_reason":null,"thread_id":null,"root_comment_id":null,"resolution":null,"tracked_as":null,"posted":false},`+
		`{`+
		`"id":"bbbbbbbbbbbbbbbb","path":"app.go","line":2,"side":"RIGHT","severity":"medium","category":"correctness",`+
		`"pre_existing":false,"title":"Unchecked fetch response","why":"w","fix":"f","anchor":"","anchor_kind":"line",`+
		`"anchor_reason":null,"thread_id":null,"root_comment_id":null,"resolution":null,"tracked_as":null}]`)
	e.runner.script = []exec.Result{{ExitCode: 0, Stdout: claudeStdout(`{"verdict":"converged","blocked_reason":null,"findings":[]}`)}}
	got := runLeg(t, e, e.request(t))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	specs := e.runner.Specs()
	if len(specs) == 0 {
		t.Fatal("the harness was never run, so no prompt carried the priors")
	}
	promptText := specPrompt(specs[0])
	if !strings.Contains(promptText, "| 1 | aaaaaaaaaaaaaaaa | app.go:2 | low | maintainability | no | Missing return type | not_posted | - |") {
		t.Errorf("prompt has no not_posted row for the held finding:\n%s", promptText)
	}
	if !strings.Contains(promptText, "| 2 | bbbbbbbbbbbbbbbb | app.go:2 | medium | correctness | no | Unchecked fetch response | none | - |") {
		t.Errorf("prompt changed the posted finding's row:\n%s", promptText)
	}
}
