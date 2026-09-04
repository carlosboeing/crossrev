package resolve

// The resolve half of the cross-leg integration proof. The review leg's
// durable marker bytes are loaded from the frozen fixture the review package's
// integration test pins as its output — internal/review/testdata/
// review-pass-1.marker — and fed to the resolve leg as a pull-request comment
// body, the way GitHub delivers it in production. Every expectation below is
// derived from those bytes: nothing about the review pass is re-declared here,
// and no prstate.Marker value crosses from one leg to the other in memory.
//
// The behaviour pinned here is what the Bash suites prove for the shell
// implementation: selection from the recorded marker and the claim order of
// tests/test-persist.sh and tests/test-recovery.sh, the reply and summary
// shapes of tests/test-presentation.sh, the commit and push of
// tests/test-worktree.sh, and the label transitions of tests/test-policy.sh.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/exec"
	"github.com/carlosboeing/crossrev/internal/forge"
	"github.com/carlosboeing/crossrev/internal/policy"
	"github.com/carlosboeing/crossrev/internal/prstate"
	"github.com/carlosboeing/crossrev/internal/vcs"
)

// reviewHandoff is the serialised pass-1 review marker, frozen as the review
// leg wrote it. internal/review/integration_test.go proves the review leg
// emits these exact bytes; this test proves the resolve leg acts on them.
const reviewHandoff = "../review/testdata/review-pass-1.marker"

type handoffFinding struct {
	id            string
	threadID      string
	rootCommentID int64
	path          string
	line          int
}

// readHandoff loads the review marker off disk and derives everything the
// resolve leg should act on: the head it reviewed and the findings it
// recorded, with the threads they landed on.
func readHandoff(t *testing.T) (prstate.Marker, core.Revision, []handoffFinding) {
	t.Helper()
	raw, err := os.ReadFile(reviewHandoff)
	if err != nil {
		t.Fatalf("read %s: %v", reviewHandoff, err)
	}
	body := strings.TrimRight(string(raw), "\n")
	payload, ok := prstate.DecodeMarker(body)
	if !ok {
		t.Fatalf("%s carries no marker", reviewHandoff)
	}
	marker, err := prstate.ParseMarker(payload)
	if err != nil {
		t.Fatalf("ParseMarker: %v", err)
	}
	if marker.Leg != core.LegReview || marker.State != core.PassComplete {
		t.Fatalf("fixture is a %s pass in state %q, want a complete review", marker.Leg, marker.State)
	}
	head, err := core.NewRevision(marker.HeadSHA.Value())
	if err != nil {
		t.Fatalf("fixture head_sha: %v", err)
	}
	var findings []map[string]json.RawMessage
	if err := marker.DecodeFindings(&findings); err != nil {
		t.Fatalf("DecodeFindings: %v", err)
	}
	out := make([]handoffFinding, 0, len(findings))
	for _, f := range findings {
		root, err := strconv.ParseInt(jsonString(f["root_comment_id"]), 10, 64)
		if err != nil {
			t.Fatalf("finding root_comment_id: %v", err)
		}
		line, err := strconv.Atoi(jsonString(f["line"]))
		if err != nil {
			t.Fatalf("finding line: %v", err)
		}
		out = append(out, handoffFinding{
			id:            jsonString(f["id"]),
			threadID:      jsonString(f["thread_id"]),
			rootCommentID: root,
			path:          jsonString(f["path"]),
			line:          line,
		})
	}
	return marker, head, out
}

func TestIntegrationResolvePassConsumesTheReviewMarker(t *testing.T) {
	review, head, findings := readHandoff(t)
	if len(findings) != 2 {
		t.Fatalf("handoff findings = %d, want 2", len(findings))
	}
	pass := review.Pass

	payload, err := os.ReadFile("testdata/resolve-pass-1-payload.json")
	if err != nil {
		t.Fatalf("read resolve payload fixture: %v", err)
	}

	e := setup(t)
	e.head = head
	e.git.head = head
	e.git.staged = true
	e.forge.pr.HeadRefOid = head
	e.forge.pr.Labels = []forge.Label{
		{Name: policy.LabelPassPrefix + strconv.Itoa(pass)},
		{Name: policy.LabelAwaitingResolution},
	}
	// The comment stream carries the review pass exactly as the review leg
	// left it: summary text with the marker embedded, authored by the trusted
	// account. The marker bytes are the fixture, unparsed until the leg's own
	// comment scan reads them.
	e.forge.comments = []forge.IssueComment{{
		ID:          9001,
		AuthorLogin: e.forge.viewer,
		CreatedAt:   e.now.Add(-time.Minute).Format(time.RFC3339),
		Body:        "## crossrev review — pass 1\n\n2 findings.\n" + strings.TrimRight(mustRead(t, reviewHandoff), "\n"),
	}}
	// The threads the review leg's inline comments opened, keyed by the
	// finding ids recorded in the marker.
	e.forge.threads = nil
	for _, f := range findings {
		e.forge.threads = append(e.forge.threads, forge.ReviewThread{
			ID:            f.threadID,
			Path:          f.path,
			Line:          f.line,
			RootCommentID: f.rootCommentID,
			FindingIDs:    []core.FindingID{mustFindingID(t, f.id)},
		})
	}
	e.adapter.payloads = []json.RawMessage{json.RawMessage(strings.TrimSpace(string(payload)))}

	// The worktree holds repository-provided harness configuration (the fake
	// worktree carries a CLAUDE.md): the sandbox must quarantine it while the
	// resolver runs and restore it before the commit
	// (tests/test-permissions.sh, tests/test-worktree.sh).
	worktree, err := vcs.WorktreeDir(e.slug, 42)
	if err != nil {
		t.Fatalf("WorktreeDir: %v", err)
	}
	quarantinedDuringRun := false
	e.runner.onRun = func(exec.Spec) {
		if _, err := os.Stat(filepath.Join(worktree, "CLAUDE.md")); os.IsNotExist(err) {
			quarantinedDuringRun = true
		}
	}

	got := e.run(t)
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if got.Outcome != OutcomeComplete {
		t.Fatalf("Outcome = %q, want complete (%s)", got.Outcome, got.Message)
	}
	if got.Pass != pass {
		t.Fatalf("Pass = %d, want %d — the pass the review marker recorded", got.Pass, pass)
	}

	if !quarantinedDuringRun {
		t.Error("CLAUDE.md was still in the worktree while the resolver ran")
	}
	if body, err := os.ReadFile(filepath.Join(worktree, "CLAUDE.md")); err != nil || string(body) != "injected\n" {
		t.Errorf("worktree CLAUDE.md after the leg = %q, %v — the sandbox did not restore it before the commit", body, err)
	}
	if _, err := os.Stat(filepath.Join(worktree, ".crossrev-quarantine")); !os.IsNotExist(err) {
		t.Errorf("quarantine residue left behind: %v", err)
	}

	// Selection came from the marker bytes: the resolutions name the finding
	// ids that exist only inside the serialised marker.
	var resolutions []map[string]json.RawMessage
	if err := json.Unmarshal(got.Resolutions, &resolutions); err != nil {
		t.Fatalf("resolutions: %v", err)
	}
	if len(resolutions) != 2 {
		t.Fatalf("resolutions = %d, want 2", len(resolutions))
	}
	for i, f := range findings {
		if id := jsonString(resolutions[i]["finding_id"]); id != f.id {
			t.Errorf("resolution %d finding_id = %q, want %q from the marker bytes", i, id, f.id)
		}
	}

	// The resolver was told the finding identities the marker recorded.
	if len(e.adapter.invs) != 1 {
		t.Fatalf("harness invocations = %d, want 1", len(e.adapter.invs))
	}
	promptText := e.adapter.invs[0].Prompt.Text
	for _, f := range findings {
		if !strings.Contains(promptText, f.id) {
			t.Errorf("resolve prompt does not carry finding id %s from the marker", f.id)
		}
	}

	// Forge call order: the claim is created before the harness runs and
	// edited as the pass progresses; each thread gets its reply before it is
	// resolved; the review pass's own comment is updated after every thread
	// is settled (tests/test-persist.sh).
	order := e.forge.order
	if n := countOp(order, "CommentCreate"); n != 1 {
		t.Fatalf("CommentCreate calls = %d, want 1 (the claim)", n)
	}
	assertBefore(t, order, "CommentCreate", "ReviewReply")
	var replyResolve []string
	for _, op := range order {
		if op == "ReviewReply" || op == "ThreadResolve" {
			replyResolve = append(replyResolve, op)
		}
	}
	if strings.Join(replyResolve, ",") != "ReviewReply,ThreadResolve,ReviewReply,ThreadResolve" {
		t.Errorf("reply/resolve interleave = %v, want reply-then-resolve per finding", replyResolve)
	}

	// The claim body and its progression (tests/test-presentation.sh).
	if !strings.HasPrefix(e.forge.created[0].Body, "**crossrev — resolving pass 1**") {
		t.Errorf("claim body = %q", e.forge.created[0].Body)
	}
	claimID := int64(9101)
	if len(e.forge.edits) != 5 {
		t.Fatalf("claim edits = %d, want 5 (recorded, pushed, review update, summary, complete)", len(e.forge.edits))
	}
	for _, id := range []int{0, 1, 3, 4} {
		if e.forge.edits[id].CommentID != claimID {
			t.Errorf("edit %d targets comment %d, want the resolve claim %d", id, e.forge.edits[id].CommentID, claimID)
		}
	}
	if !strings.Contains(e.forge.edits[0].Body, "Resolutions recorded; committing and replying now.") {
		t.Errorf("first claim edit = %q", e.forge.edits[0].Body)
	}
	if !strings.Contains(e.forge.edits[1].Body, "replying to each thread now") {
		t.Errorf("second claim edit = %q", e.forge.edits[1].Body)
	}

	// Replies land on the threads the marker's findings name, with the
	// resolver's words (tests/test-presentation.sh).
	if len(e.forge.replies) != 2 {
		t.Fatalf("replies = %d, want 2", len(e.forge.replies))
	}
	for i, f := range findings {
		reply := e.forge.replies[i]
		if reply.RootCommentID != f.rootCommentID {
			t.Errorf("reply %d root = %d, want %d from the marker bytes", i, reply.RootCommentID, f.rootCommentID)
		}
		if !strings.Contains(reply.Body, jsonString(resolutions[i]["reply"])) {
			t.Errorf("reply %d body = %q, want the resolver's reply text", i, reply.Body)
		}
	}

	// Both threads resolve: the fix because the commit landed, the skip
	// because a skip settles without one.
	if len(e.forge.resolved) != 2 {
		t.Fatalf("threads resolved = %v, want both finding threads", e.forge.resolved)
	}
	for i, f := range findings {
		if e.forge.resolved[i] != f.threadID {
			t.Errorf("resolved thread %d = %q, want %q from the marker bytes", i, e.forge.resolved[i], f.threadID)
		}
	}

	// The commit carries the resolver's subject and the fixed finding; the
	// push goes to the pull request's own head branch (tests/test-worktree.sh).
	if e.git.commitCalls != 1 || e.git.pushCalls != 1 {
		t.Fatalf("commit/push calls = %d/%d, want 1/1", e.git.commitCalls, e.git.pushCalls)
	}
	if subject, _, _ := strings.Cut(e.git.commitOpts.Message, "\n"); subject != "fix: guard the fetch response" {
		t.Errorf("commit subject = %q", subject)
	}
	if !strings.Contains(e.git.commitOpts.Message, "Unchecked fetch response") {
		t.Errorf("commit body does not name the fixed finding: %q", e.git.commitOpts.Message)
	}
	if e.git.commitOpts.RunHooks {
		t.Error("commit ran the repository's hooks on the default git.hooks: skip")
	}
	if e.git.pushRemote != "origin" || e.git.pushBranch != "feature" {
		t.Errorf("push = %s %s, want origin feature", e.git.pushRemote, e.git.pushBranch)
	}

	// The review pass's comment is updated in place — the same comment id the
	// review leg created — with its findings now carrying their resolutions.
	reviewEdit := e.forge.edits[2]
	if reviewEdit.CommentID != 9001 {
		t.Fatalf("review update targets comment %d, want the review claim 9001", reviewEdit.CommentID)
	}
	updated := decodeMarkerFromBody(t, reviewEdit.Body)
	if updated.Leg != core.LegReview || updated.Pass != pass || updated.State != core.PassComplete {
		t.Errorf("updated review marker = %s pass %d %q, want the complete review pass %d", updated.Leg, updated.Pass, updated.State, pass)
	}
	var updatedFindings []map[string]json.RawMessage
	if err := updated.DecodeFindings(&updatedFindings); err != nil {
		t.Fatalf("updated findings: %v", err)
	}
	wantResolution := []string{"fixed", "skipped"}
	for i, f := range updatedFindings {
		if r := jsonString(f["resolution"]); r != wantResolution[i] {
			t.Errorf("updated finding %d resolution = %q, want %q", i, r, wantResolution[i])
		}
	}

	// The final marker on the resolve claim is the pass of record.
	final := decodeMarkerFromBody(t, e.forge.edits[4].Body)
	if final.Leg != core.LegResolve || final.Pass != pass {
		t.Errorf("final marker leg/pass = %s/%d, want resolve/%d", final.Leg, final.Pass, pass)
	}
	if final.State != core.PassComplete {
		t.Errorf("final state = %q, want complete", final.State)
	}
	if sha := final.CommitSHA.Value(); sha != "cccccccccccccccccccccccccccccccccccccccc" {
		t.Errorf("commit_sha = %q, want the pushed commit", sha)
	}
	if h := final.HeadSHA.Value(); h != head.SHA() {
		t.Errorf("head_sha = %q, want the head the review marker recorded", h)
	}
	if s := final.Summary.Value(); s != "Fixed the unchecked response; left the return-type annotation alone." {
		t.Errorf("summary = %q", s)
	}
	var finalResolutions []map[string]json.RawMessage
	if err := final.DecodeResolutions(&finalResolutions); err != nil {
		t.Fatalf("final resolutions: %v", err)
	}
	for i, f := range findings {
		if id := jsonString(finalResolutions[i]["finding_id"]); id != f.id {
			t.Errorf("final resolution %d finding_id = %q, want %q", i, id, f.id)
		}
	}

	// The loop hands back to the reviewer: the pass pill stays, the state
	// label moves to awaiting-review (tests/test-policy.sh).
	if !containsLabel(e.forge.removedLabels, policy.LabelAwaitingResolution) {
		t.Errorf("labels removed = %v, want awaiting-resolution gone", e.forge.removedLabels)
	}
	if !containsLabel(e.forge.addedLabels, policy.LabelPassPrefix+strconv.Itoa(pass)) {
		t.Errorf("labels added = %v, want pass-%d", e.forge.addedLabels, pass)
	}
	if !containsLabel(e.forge.addedLabels, policy.LabelAwaitingReview) {
		t.Errorf("labels added = %v, want awaiting-review", e.forge.addedLabels)
	}
	if containsLabel(e.forge.addedLabels, policy.LabelStop) {
		t.Error("crossrev/stop applied with nothing escalated")
	}
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(raw)
}

func decodeMarkerFromBody(t *testing.T, body string) prstate.Marker {
	t.Helper()
	raw, ok := prstate.DecodeMarker(body)
	if !ok {
		t.Fatalf("body carries no marker: %q", body)
	}
	marker, err := prstate.ParseMarker(raw)
	if err != nil {
		t.Fatalf("ParseMarker: %v", err)
	}
	return marker
}

func assertBefore(t *testing.T, order []string, first, later string) {
	t.Helper()
	a, b := -1, -1
	for i, op := range order {
		if op == first && a < 0 {
			a = i
		}
		if op == later && b < 0 {
			b = i
		}
	}
	if a < 0 || b < 0 || a > b {
		t.Errorf("%s at %d, %s at %d in %v — want %s first", first, a, later, b, order, first)
	}
}

func containsLabel(labels []string, want string) bool {
	for _, l := range labels {
		if l == want {
			return true
		}
	}
	return false
}
