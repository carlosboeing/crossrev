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
