package resolve

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/forge"
	"github.com/carlosboeing/crossrev/internal/harness"
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
