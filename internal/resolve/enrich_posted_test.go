package resolve

import (
	"encoding/json"
	"testing"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/harness"
)

func postedNodes(t *testing.T, raw string) []harness.Node {
	t.Helper()
	var out []harness.Node
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("decode findings: %v", err)
	}
	return out
}

// Findings the review leg held back (posted:false) never reach the resolver:
// no thread reply and no top-level comment can name them. Findings without
// the key — markers written before it existed — still arrive.
func TestEnrichFiltersNotPostedFindings(t *testing.T) {
	in := postedNodes(t, `[{`+
		`"id":"aaaaaaaaaaaaaaaa","path":"a.go","line":1,"severity":"low","pre_existing":false,"title":"held nit","posted":false},`+
		`{`+
		`"id":"bbbbbbbbbbbbbbbb","path":"a.go","line":2,"severity":"high","pre_existing":false,"title":"real bug"},`+
		`{`+
		`"id":"cccccccccccccccc","path":"a.go","line":3,"severity":"medium","pre_existing":false,"title":"worth fixing","posted":true},`+
		`{`+
		`"id":"dddddddddddddddd","path":"a.go","line":4,"severity":"high","pre_existing":true,"title":"old bug","posted":null}]`)
	got := enrichFindings(in, nil, core.SeverityMedium)
	if len(got) != 3 {
		t.Fatalf("enriched findings = %d, want 3 (the held one filtered)", len(got))
	}
	for _, f := range got {
		if f.Member("id").StringVal() == "aaaaaaaaaaaaaaaa" {
			t.Error("the held finding reached the resolver input")
		}
	}
	// The surviving findings are renumbered in order, so resolution numbers
	// still match the prompt the resolver answers.
	for i, want := range []string{"bbbbbbbbbbbbbbbb", "cccccccccccccccc", "dddddddddddddddd"} {
		if id := got[i].Member("id").StringVal(); id != want {
			t.Errorf("finding %d id = %q, want %q", i, id, want)
		}
		if n := got[i].Number("number"); n != int64(i+1) {
			t.Errorf("finding %s number = %d, want %d", want, n, i+1)
		}
	}
	if may := got[0].Member("may_fix").StringVal(); may != "true" {
		t.Errorf("high finding may_fix = %q, want true", may)
	}
	if may := got[2].Member("may_fix").StringVal(); may != "false" {
		t.Errorf("pre-existing finding may_fix = %q, want false", may)
	}
}

// A pass whose every finding was held leaves the resolver nothing to verify,
// rather than an error: the held findings are recorded, not actionable.
func TestEnrichWithEveryFindingNotPostedAnswersEmpty(t *testing.T) {
	in := postedNodes(t, `[{"id":"aaaaaaaaaaaaaaaa","severity":"low","pre_existing":false,"title":"held nit","posted":false}]`)
	if got := enrichFindings(in, nil, core.SeverityMedium); len(got) != 0 {
		t.Errorf("enriched findings = %d, want 0", len(got))
	}
}

// The filter answers a copy: the caller's slice still holds the full review
// record, which the marker and summary rewrite needs after the resolver runs.
// Filtering in place would compact the shared backing array and drop the
// held findings from that rewrite.
func TestEnrichFindingsLeavesInputUnchanged(t *testing.T) {
	in := postedNodes(t, `[{`+
		`"id":"aaaaaaaaaaaaaaaa","path":"a.go","line":1,"severity":"low","pre_existing":false,"title":"held nit","posted":false},`+
		`{`+
		`"id":"bbbbbbbbbbbbbbbb","path":"a.go","line":2,"severity":"high","pre_existing":false,"title":"real bug"}]`)
	got := enrichFindings(in, nil, core.SeverityMedium)
	if len(got) != 1 {
		t.Fatalf("enriched findings = %d, want 1", len(got))
	}
	if len(in) != 2 {
		t.Fatalf("input findings = %d, want 2 (the full record)", len(in))
	}
	if id := in[0].Member("id").StringVal(); id != "aaaaaaaaaaaaaaaa" {
		t.Errorf("input[0] id = %q, want the held finding (backing array clobbered)", id)
	}
	if id := in[1].Member("id").StringVal(); id != "bbbbbbbbbbbbbbbb" {
		t.Errorf("input[1] id = %q, want the posted finding", id)
	}
}
