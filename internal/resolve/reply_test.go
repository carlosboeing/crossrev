package resolve

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/forge"
	"github.com/carlosboeing/crossrev/internal/harness"
	"github.com/carlosboeing/crossrev/internal/prstate"
)

// TestOutsideDiffReplyKeepsResolutionWithoutAThread pins the C2 resolve
// half: a finding anchored outside_diff with no thread gets a top-level
// reply carrying its finding id, and the resolution is still recorded on
// the persisted finding.
func TestOutsideDiffReplyKeepsResolutionWithoutAThread(t *testing.T) {
	outsideFinding := `{"id":"` + testFinding + `","path":"docs/helper.go","line":1,` +
		`"severity":"high","anchor_kind":"outside_diff","anchor_reason":"outside the changed files"}`
	findings, err := harness.DecodeStream([]byte(outsideFinding))
	if err != nil {
		t.Fatalf("decode findings: %v", err)
	}
	resolution := `{"finding_id":"` + testFinding + `","reply":"noted","resolution":"skipped","crossrev_tracked":""}`
	recs, err := harness.DecodeStream([]byte(resolution))
	if err != nil {
		t.Fatalf("decode recs: %v", err)
	}

	e := setup(t)
	s := &session{
		pass:     1,
		repo:     e.slug,
		req:      Request{PR: 42},
		settings: legSettings{Harness: "claude", Model: "claude-3-7-sonnet"},
	}
	leg := &Leg{Forge: e.forge}
	already := map[string]bool{}
	_, _, unthreaded, findingsOut, messages := leg.replyAndResolve(context.Background(), s, recs, findings, nil, "", already, 0)
	if unthreaded != 1 {
		t.Fatalf("unthreaded = %d, want 1 (no thread for an outside-diff finding)", unthreaded)
	}
	if len(e.forge.replies) != 0 {
		t.Fatalf("thread replies = %d, want 0", len(e.forge.replies))
	}
	if len(e.forge.created) != 1 {
		t.Fatalf("top-level replies = %d, want 1", len(e.forge.created))
	}
	var out []map[string]json.RawMessage
	if err := json.Unmarshal(findingsOut, &out); err != nil {
		t.Fatalf("findingsOut decode: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("findingsOut = %d findings, want 1", len(out))
	}
	var resolutionOut string
	_ = json.Unmarshal(out[0]["resolution"], &resolutionOut)
	if resolutionOut != "skipped" {
		t.Errorf("resolution = %q, want skipped recorded without a thread", resolutionOut)
	}
	var kind string
	_ = json.Unmarshal(out[0]["anchor_kind"], &kind)
	if kind != "outside_diff" {
		t.Errorf("anchor_kind = %q, want outside_diff preserved through reply", kind)
	}
	joined := ""
	for _, m := range messages {
		joined += m.Text + "\n"
	}
	if !strings.Contains(joined, "top-level") {
		t.Errorf("messages lack the top-level notice: %q", messages)
	}
	var _ core.FindingID
	var _ forge.ReviewThread
}

// A reposted finding shares its id with an older thread: the reply and
// the resolution belong to the current thread — the latest posted
// comment — so the actionable thread closes instead of the spent one.
func TestReplyAndResolveUsesCurrentThreadForRepostedFinding(t *testing.T) {
	findings, err := harness.DecodeStream([]byte(`{"id":"cccccccccccccccc","path":"a.go","line":1,` +
		`"severity":"medium","title":"raised nit","resolution":null}`))
	if err != nil {
		t.Fatalf("decode findings: %v", err)
	}
	recs, err := harness.DecodeStream([]byte(`{"finding_id":"cccccccccccccccc","reply":"fixed","resolution":"fixed","crossrev_tracked":""}`))
	if err != nil {
		t.Fatalf("decode recs: %v", err)
	}
	e := setup(t)
	oldID := mustFindingID(t, "cccccccccccccccc")
	e.forge.threads = []forge.ReviewThread{
		{ID: "thread-old", Path: "a.go", Line: 1, RootCommentID: 55, FindingIDs: []core.FindingID{oldID}},
		{ID: "thread-new", Path: "a.go", Line: 1, RootCommentID: 77, FindingIDs: []core.FindingID{oldID}},
	}
	s := &session{
		pass:     3,
		repo:     e.slug,
		req:      Request{PR: 42},
		settings: legSettings{Harness: "claude", Model: "claude-3-7-sonnet"},
	}
	leg := &Leg{Forge: e.forge}
	_, _, _, _, _ = leg.replyAndResolve(context.Background(), s, recs, findings, e.forge.threads, "abc1234", map[string]bool{}, 0)
	if len(e.forge.replies) != 1 {
		t.Fatalf("thread replies = %d, want 1", len(e.forge.replies))
	}
	if e.forge.replies[0].RootCommentID != 77 {
		t.Errorf("reply root = %d, want 77 (the current thread, not the pass-1 thread)", e.forge.replies[0].RootCommentID)
	}
	if len(e.forge.resolved) != 1 || e.forge.resolved[0] != "thread-new" {
		t.Errorf("resolved = %v, want [thread-new] (the current thread)", e.forge.resolved)
	}
}

// A mixed-severity duplicate records the resolution on the posted
// occurrence: the held entry keeps posted:false and no resolution, so a
// later pass can still upgrade it, while the posted entry settles.
func TestReplyAndResolveRecordsResolutionOnPostedOccurrence(t *testing.T) {
	findings, err := harness.DecodeStream([]byte(`{"id":"dddddddddddddddd","path":"a.go","line":1,` +
		`"severity":"low","title":"same point","posted":false,"resolution":null}` + "\n" +
		`{"id":"dddddddddddddddd","path":"a.go","line":1,` +
		`"severity":"medium","title":"same point","resolution":null}`))
	if err != nil {
		t.Fatalf("decode findings: %v", err)
	}
	recs, err := harness.DecodeStream([]byte(`{"finding_id":"dddddddddddddddd","reply":"fixed","resolution":"fixed","crossrev_tracked":""}`))
	if err != nil {
		t.Fatalf("decode recs: %v", err)
	}
	e := setup(t)
	s := &session{
		pass:     2,
		repo:     e.slug,
		req:      Request{PR: 42},
		settings: legSettings{Harness: "claude", Model: "claude-3-7-sonnet"},
	}
	leg := &Leg{Forge: e.forge}
	_, _, _, findingsOut, _ := leg.replyAndResolve(context.Background(), s, recs, findings, nil, "abc1234", map[string]bool{}, 0)
	var out []map[string]json.RawMessage
	if err := json.Unmarshal(findingsOut, &out); err != nil {
		t.Fatalf("findingsOut decode: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("findingsOut = %d findings, want 2", len(out))
	}
	var heldRes, postedRes string
	for _, f := range out {
		var posted *bool
		_ = json.Unmarshal(f["posted"], &posted)
		var res string
		_ = json.Unmarshal(f["resolution"], &res)
		if posted != nil && !*posted {
			heldRes = res
		} else {
			postedRes = res
		}
	}
	if heldRes == "fixed" {
		t.Errorf("held occurrence resolution = %q, want none (held entries carry no resolution)", heldRes)
	}
	if postedRes != "fixed" {
		t.Errorf("posted occurrence resolution = %q, want fixed", postedRes)
	}
}

// A mixed pass carries a held finding beside a posted one through
// resolution: the resolver answers only the posted finding, and the marker
// rewrite keeps the held one — posted:false, no resolution — so its
// not_posted prior and its summary count survive the resolve leg.
func TestReplyAndResolveKeepsHeldFindingsInMarkerRewrite(t *testing.T) {
	findings, err := harness.DecodeStream([]byte(`{"id":"aaaaaaaaaaaaaaaa","path":"a.go","line":1,` +
		`"severity":"low","title":"held nit","posted":false,"resolution":null}` + "\n" +
		`{"id":"bbbbbbbbbbbbbbbb","path":"a.go","line":2,` +
		`"severity":"high","title":"real bug","resolution":null}`))
	if err != nil {
		t.Fatalf("decode findings: %v", err)
	}
	recs, err := harness.DecodeStream([]byte(`{"finding_id":"bbbbbbbbbbbbbbbb","reply":"fixed","resolution":"fixed","crossrev_tracked":""}`))
	if err != nil {
		t.Fatalf("decode recs: %v", err)
	}

	e := setup(t)
	s := &session{
		pass:     2,
		repo:     e.slug,
		req:      Request{PR: 42},
		settings: legSettings{Harness: "claude", Model: "claude-3-7-sonnet"},
	}
	leg := &Leg{Forge: e.forge}
	_, _, _, findingsOut, _ := leg.replyAndResolve(context.Background(), s, recs, findings, nil, "abc1234", map[string]bool{}, 0)
	var out []map[string]json.RawMessage
	if err := json.Unmarshal(findingsOut, &out); err != nil {
		t.Fatalf("findingsOut decode: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("findingsOut = %d findings, want 2 (the held one survives)", len(out))
	}
	byID := map[string]map[string]json.RawMessage{}
	for _, f := range out {
		var id string
		_ = json.Unmarshal(f["id"], &id)
		byID[id] = f
	}
	held, ok := byID["aaaaaaaaaaaaaaaa"]
	if !ok {
		t.Fatal("the held finding is missing from the marker rewrite")
	}
	var posted bool
	_ = json.Unmarshal(held["posted"], &posted)
	if posted {
		t.Errorf("held posted = true, want false recorded through resolution")
	}
	if raw, present := held["resolution"]; !present || string(raw) == `"fixed"` {
		t.Errorf("held resolution = %s, want no resolution recorded", string(raw))
	}
	var resolutionOut string
	_ = json.Unmarshal(byID["bbbbbbbbbbbbbbbb"]["resolution"], &resolutionOut)
	if resolutionOut != "fixed" {
		t.Errorf("posted resolution = %q, want fixed", resolutionOut)
	}
}

// A finding posted on pass 1, held on pass 2 and upgraded on pass 3 is
// replied to on its current thread: the pass-1 reply marker must not
// suppress the pass-3 reply to the new comment. Reply dedup is scoped to
// the current pass — an earlier pass answered a spent thread, not this
// one — while a marker from the current pass still keeps retry idempotency.
func TestReplyAndResolveRepliesToUpgradeAfterEarlierReply(t *testing.T) {
	const fid = "eeeeeeeeeeeeeeee"
	oldID := mustFindingID(t, fid)
	pass1, err := harness.DecodeStream([]byte(`{"id":"` + fid + `","path":"a.go","line":1,` +
		`"severity":"low","title":"same nit","resolution":null}`))
	if err != nil {
		t.Fatalf("decode pass-1 findings: %v", err)
	}
	recs1, err := harness.DecodeStream([]byte(`{"finding_id":"` + fid + `","reply":"noted","resolution":"skipped","crossrev_tracked":""}`))
	if err != nil {
		t.Fatalf("decode pass-1 recs: %v", err)
	}
	pass3, err := harness.DecodeStream([]byte(`{"id":"` + fid + `","path":"a.go","line":1,` +
		`"severity":"medium","title":"same nit","resolution":null}`))
	if err != nil {
		t.Fatalf("decode pass-3 findings: %v", err)
	}
	recs3, err := harness.DecodeStream([]byte(`{"finding_id":"` + fid + `","reply":"fixed","resolution":"fixed","crossrev_tracked":""}`))
	if err != nil {
		t.Fatalf("decode pass-3 recs: %v", err)
	}

	e := setup(t)
	leg := &Leg{Forge: e.forge}
	settings := legSettings{Harness: "claude", Model: "claude-3-7-sonnet"}
	e.forge.threads = []forge.ReviewThread{
		{ID: "thread-old", Path: "a.go", Line: 1, RootCommentID: 55, FindingIDs: []core.FindingID{oldID}},
	}
	s1 := &session{pass: 1, repo: e.slug, req: Request{PR: 42}, settings: settings, author: e.forge.viewer}
	already1 := leg.postedFindingIDs(context.Background(), s1)
	_, _, _, _, _ = leg.replyAndResolve(context.Background(), s1, recs1, pass1, e.forge.threads, "abc1234", already1, 0)
	if len(e.forge.replies) != 1 || e.forge.replies[0].RootCommentID != 55 {
		t.Fatalf("pass-1 replies to root 55, got %+v", e.forge.replies)
	}
	// The pass-1 reply lands on the pull request carrying a pass-1 resolve
	// marker, the way ReplyBody writes it. Pass 2 holds the low finding, so
	// it never reaches the resolver and nothing answers it there.
	e.forge.reviewComments = append(e.forge.reviewComments, forge.IssueComment{
		ID:          6001,
		AuthorLogin: e.forge.viewer,
		Body:        "noted" + prstate.EncodeFindingMarker(oldID, 1, core.LegResolve),
	})
	// Pass 3 upgrades to medium and posts a new comment: a new thread.
	e.forge.threads = append(e.forge.threads, forge.ReviewThread{
		ID: "thread-new", Path: "a.go", Line: 1, RootCommentID: 77, FindingIDs: []core.FindingID{oldID},
	})
	s3 := &session{pass: 3, repo: e.slug, req: Request{PR: 42}, settings: settings, author: e.forge.viewer}
	already3 := leg.postedFindingIDs(context.Background(), s3)
	if already3[fid] {
		t.Fatalf("pass-1 reply suppresses the pass-3 upgrade: already[%s] is set from an earlier pass", fid)
	}
	_, _, _, _, _ = leg.replyAndResolve(context.Background(), s3, recs3, pass3, e.forge.threads, "def5678", already3, 0)
	if len(e.forge.replies) != 2 {
		t.Fatalf("replies = %d, want 2 (pass 1 to the old thread, pass 3 to the current one)", len(e.forge.replies))
	}
	if e.forge.replies[1].RootCommentID != 77 {
		t.Errorf("pass-3 reply root = %d, want 77 (the current thread, not the spent pass-1 thread)", e.forge.replies[1].RootCommentID)
	}
	if len(e.forge.resolved) != 2 || e.forge.resolved[1] != "thread-new" {
		t.Errorf("resolved = %v, want [thread-old thread-new] (each resolution on its own current thread)", e.forge.resolved)
	}
}

// The same posted-held-upgraded sequence records each resolution: pass 1
// settles its posted occurrence, and a pass-3 marker carrying both a held
// low entry and its posted medium upgrade settles only the posted one, so
// the held copy stays upgradeable.
func TestReplyAndResolveSequenceSettlesPostedOccurrence(t *testing.T) {
	const fid = "ffffffffffffffff"
	pass1, err := harness.DecodeStream([]byte(`{"id":"` + fid + `","path":"a.go","line":1,` +
		`"severity":"low","title":"same nit","resolution":null}`))
	if err != nil {
		t.Fatalf("decode pass-1 findings: %v", err)
	}
	recs1, err := harness.DecodeStream([]byte(`{"finding_id":"` + fid + `","reply":"noted","resolution":"skipped","crossrev_tracked":""}`))
	if err != nil {
		t.Fatalf("decode pass-1 recs: %v", err)
	}
	pass3, err := harness.DecodeStream([]byte(`{"id":"` + fid + `","path":"a.go","line":1,` +
		`"severity":"low","title":"same nit","posted":false,"resolution":null}` + "\n" +
		`{"id":"` + fid + `","path":"a.go","line":1,` +
		`"severity":"medium","title":"same nit","resolution":null}`))
	if err != nil {
		t.Fatalf("decode pass-3 findings: %v", err)
	}
	recs3, err := harness.DecodeStream([]byte(`{"finding_id":"` + fid + `","reply":"fixed","resolution":"fixed","crossrev_tracked":""}`))
	if err != nil {
		t.Fatalf("decode pass-3 recs: %v", err)
	}

	e := setup(t)
	leg := &Leg{Forge: e.forge}
	settings := legSettings{Harness: "claude", Model: "claude-3-7-sonnet"}
	s1 := &session{pass: 1, repo: e.slug, req: Request{PR: 42}, settings: settings}
	_, _, _, out1, _ := leg.replyAndResolve(context.Background(), s1, recs1, pass1, nil, "abc1234", map[string]bool{}, 0)
	var first []map[string]json.RawMessage
	if err := json.Unmarshal(out1, &first); err != nil {
		t.Fatalf("pass-1 findingsOut decode: %v", err)
	}
	var settled string
	_ = json.Unmarshal(first[0]["resolution"], &settled)
	if settled != "skipped" {
		t.Fatalf("pass-1 resolution = %q, want skipped on the posted occurrence", settled)
	}
	s3 := &session{pass: 3, repo: e.slug, req: Request{PR: 42}, settings: settings}
	_, _, _, out3, _ := leg.replyAndResolve(context.Background(), s3, recs3, pass3, nil, "def5678", map[string]bool{}, 0)
	var out []map[string]json.RawMessage
	if err := json.Unmarshal(out3, &out); err != nil {
		t.Fatalf("pass-3 findingsOut decode: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("findingsOut = %d findings, want 2", len(out))
	}
	var heldRes, postedRes string
	for _, f := range out {
		var posted *bool
		_ = json.Unmarshal(f["posted"], &posted)
		var res string
		_ = json.Unmarshal(f["resolution"], &res)
		if posted != nil && !*posted {
			heldRes = res
		} else {
			postedRes = res
		}
	}
	if heldRes == "fixed" {
		t.Errorf("held occurrence resolution = %q, want none (held entries carry no resolution)", heldRes)
	}
	if postedRes != "fixed" {
		t.Errorf("posted occurrence resolution = %q, want fixed", postedRes)
	}
}

// Thread binding follows the current thread across the sequence: pass 1
// backfills the old root, pass 2 backfills nothing onto the held entry
// even with a thread under its id, and pass 3 backfills the new root, so
// the reply and the resolve land on the actionable thread.
func TestCurrentThreadBindingAcrossHoldAndUpgrade(t *testing.T) {
	const fid = "1111111111111111"
	oldID := mustFindingID(t, fid)
	decode := func(body string) []harness.Node {
		t.Helper()
		fs, err := harness.DecodeStream([]byte(body))
		if err != nil {
			t.Fatalf("decode findings: %v", err)
		}
		return fs
	}
	old := []forge.ReviewThread{
		{ID: "thread-old", Path: "a.go", Line: 1, RootCommentID: 55, FindingIDs: []core.FindingID{oldID}},
	}
	both := append(append([]forge.ReviewThread{}, old...), forge.ReviewThread{
		ID: "thread-new", Path: "a.go", Line: 1, RootCommentID: 77, FindingIDs: []core.FindingID{oldID},
	})

	pass1 := decode(`{"id":"` + fid + `","path":"a.go","line":1,"severity":"low","title":"same nit"}`)
	backfillRoots(pass1, old)
	if got := pass1[0].Member("root_comment_id").StringVal(); got != "55" {
		t.Errorf("pass-1 root = %q, want 55 (the posted comment's thread)", got)
	}
	if got := threadsByFinding(old)[fid].ID; got != "thread-old" {
		t.Errorf("pass-1 thread = %q, want thread-old", got)
	}

	held := decode(`{"id":"` + fid + `","path":"a.go","line":1,"severity":"low","title":"same nit","posted":false}`)
	backfillRoots(held, old)
	if root := held[0].Member("root_comment_id"); root.Present() && !root.IsNull() {
		t.Errorf("held root = %v, want none (held entries carry no thread)", held[0].Member("root_comment_id").StringVal())
	}

	upgraded := decode(`{"id":"` + fid + `","path":"a.go","line":1,"severity":"medium","title":"same nit"}`)
	backfillRoots(upgraded, both)
	if got := upgraded[0].Member("root_comment_id").StringVal(); got != "77" {
		t.Errorf("pass-3 root = %q, want 77 (the upgrade's current thread)", got)
	}
	if by := threadsByFinding(both); by[fid].ID != "thread-new" || by[fid].RootCommentID != 77 {
		t.Errorf("pass-3 thread = %+v, want thread-new at root 77", by[fid])
	}
}
