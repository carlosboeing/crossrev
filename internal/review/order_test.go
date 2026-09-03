package review

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/harness"
	"github.com/carlosboeing/crossrev/internal/prstate"
)

// TestEnrichmentKeepsTheModelsKeyOrder pins the byte order of an enriched
// finding. Bash builds one as `$f + {id:…, anchor:…, thread_id:null,
// root_comment_id:null, resolution:null, tracked_as:null}` (lib/run.sh:1184-1186),
// and jq's + keeps the left operand's keys in their own order, then appends the
// new ones in the order written. Measured:
//
//	$ echo '{"path":"a.go","line":2}' | jq -c '. + {anchor:"e04c", id:"94ae"}'
//	{"path":"a.go","line":2,"anchor":"e04c","id":"94ae"}
//
// Go's encoding/json sorts a map's keys instead, so a map here would write
// anchor first and path sixth. These bytes go into the marker on every pull
// request, so the order is not cosmetic.
func TestEnrichmentKeepsTheModelsKeyOrder(t *testing.T) {
	const payload = `{"findings":[{"path":"app.go","line":2,"side":"RIGHT",` +
		`"severity":"high","category":"correctness","pre_existing":false,` +
		`"title":"T","why":"W","fix":"F"}]}`

	out, _, err := enrichFindings([]byte(payload), nil, t.TempDir())
	if err != nil {
		t.Fatalf("enrichFindings: %v", err)
	}
	var got []harness.Node
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("findings = %d, want 1", len(got))
	}
	want := []string{
		"path", "line", "side", "severity", "category", "pre_existing",
		"title", "why", "fix",
		"id", "anchor", "thread_id", "root_comment_id", "resolution", "tracked_as",
	}
	if keys := got[0].Keys(); strings.Join(keys, ",") != strings.Join(want, ",") {
		t.Errorf("key order\n got %v\nwant %v", keys, want)
	}
}

// TestReviewFootnoteKeepsTheTrailingSpace covers the branch the resolve leg's
// tests already cover and the review leg's did not: a gap sentence present and
// the cost footnote empty. Bash writes `${gaps:+$gaps }` before a footnote that
// may be empty (lib/run.sh:1542), so the rendered text ends with one space
// inside the <sub> tag. A mutation dropping the `+ " "` here survived the whole
// suite before this test existed.
func TestReviewFootnoteKeepsTheTrailingSpace(t *testing.T) {
	// model set and model_reported absent is what produces the gap sentence;
	// no usage and no billing is what leaves harness.Footnote empty.
	m := prstate.Marker{
		Harness: prstate.Some("claude"),
		Model:   prstate.Some("claude-opus-4-6"),
	}
	got := runDetails(m, "review")
	const want = "<sub>claude does not report which model answered, so the model above is the one CrossRev requested. </sub>"
	if !strings.Contains(got, want) {
		t.Errorf("run details footnote\n got %q\nwant it to contain %q", got, want)
	}
}
