package prstate_test

import (
	"testing"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/prstate"
)

// mustMarkers parses one generated case's marker array.
func mustMarkers(t *testing.T, raw string) []prstate.Marker {
	t.Helper()
	markers, err := prstate.ParseMarkers([]byte(raw))
	if err != nil {
		t.Fatalf("parsing %s: %v", raw, err)
	}
	return markers
}

// The three pass readers are generated from the Bash case tables in
// tests/test-state.sh, so the expected values below were produced by the shell
// rather than written by hand.
func TestParityStatePass(t *testing.T) {
	for _, c := range parityStatePassCases {
		t.Run(c.desc, func(t *testing.T) {
			if got := prstate.Pass(mustMarkers(t, c.markers)); got != c.want {
				t.Errorf("got %d want %d", got, c.want)
			}
		})
	}
}

func TestParityStateMaxPass(t *testing.T) {
	for _, c := range parityStateMaxPassCases {
		t.Run(c.desc, func(t *testing.T) {
			if got := prstate.MaxPass(mustMarkers(t, c.markers)); got != c.want {
				t.Errorf("got %d want %d", got, c.want)
			}
		})
	}
}

func TestParityStateCurrentReviewPass(t *testing.T) {
	for _, c := range parityStateCurrentReviewPassCases {
		t.Run(c.desc, func(t *testing.T) {
			if got := prstate.CurrentReviewPass(mustMarkers(t, c.markers)); got != c.want {
				t.Errorf("got %d want %d", got, c.want)
			}
		})
	}
}

// state_current_pass_complete asks for one (pass, leg) pair
// (lib/state.sh:287-291).
func TestCurrentPassComplete(t *testing.T) {
	markers := mustMarkers(t, `[{"leg":"review","pass":1,"state":"complete"},{"leg":"resolve","pass":1,"state":"started"}]`)
	if !prstate.CurrentPassComplete(markers, 1, core.LegReview) {
		t.Error("the completed review pass reads as incomplete")
	}
	if prstate.CurrentPassComplete(markers, 1, core.LegResolve) {
		t.Error("the started resolve pass reads as complete")
	}
	if prstate.CurrentPassComplete(markers, 2, core.LegReview) {
		t.Error("a pass with no marker reads as complete")
	}
}

// state_marker_for takes the newest marker for a (pass, leg), whatever its
// state (lib/state.sh:301-305).
func TestMarkerForTakesTheNewestWhateverItsState(t *testing.T) {
	markers := mustMarkers(t, `[{"leg":"review","pass":2,"state":"started","run_id":"1"},{"leg":"review","pass":2,"state":"complete","run_id":"2"}]`)
	got, ok := prstate.MarkerFor(markers, 2, core.LegReview)
	if !ok {
		t.Fatal("no marker found")
	}
	if got.RunID.Value() != "2" {
		t.Errorf("got run_id %q, want the newest", got.RunID.Value())
	}
	if _, ok := prstate.MarkerFor(markers, 3, core.LegReview); ok {
		t.Error("a pass with no marker returned one")
	}
}

// state_is_new_revision compares the pull request head against the last
// non-declined review marker's head_sha (lib/state.sh:344-349).
func TestIsNewRevision(t *testing.T) {
	markers := mustMarkers(t, `[{"leg":"review","pass":1,"state":"complete","head_sha":"aaa111"}]`)
	if prstate.IsNewRevision(markers, "aaa111") {
		t.Error("the same head SHA read as a new revision")
	}
	if !prstate.IsNewRevision(markers, "bbb222") {
		t.Error("a different head SHA did not read as a new revision")
	}
	if !prstate.IsNewRevision(nil, "aaa111") {
		t.Error("a pull request with no review marker is always a new revision")
	}
}

// A declined marker records a pass that never ran, so revision detection must
// not take its head_sha as reviewed (lib/state.sh:260-268).
func TestIsNewRevisionSkipsADeclinedMarker(t *testing.T) {
	markers := mustMarkers(t, `[{"leg":"review","pass":1,"state":"declined","head_sha":"aaa111"}]`)
	if !prstate.IsNewRevision(markers, "aaa111") {
		t.Error("a declined marker's head SHA counted as reviewed")
	}
}

// A marker's head_sha is a plain string, not a validated revision: the shipped
// fixtures and every existing pull request carry abbreviated values, and a
// decoder that refused them would drop real markers.
func TestMarkerHeadSHAAcceptsAnAbbreviatedValue(t *testing.T) {
	markers := mustMarkers(t, `[{"leg":"review","pass":1,"state":"complete","head_sha":"9f3c1ab"}]`)
	if markers[0].HeadSHA.Value() != "9f3c1ab" {
		t.Errorf("got %q", markers[0].HeadSHA.Value())
	}
}

// M1. Marker is returned by value, but its four payloads are json.RawMessage
// and a slice header copies the backing array. A caller editing what it was
// handed must not be able to reach the marker list it came from.
func TestMarkerForDoesNotShareItsPayloads(t *testing.T) {
	markers := mustMarkers(t, `[{"leg":"review","pass":1,"findings":[{"id":"a"}],"tokens":{"in":1},"usage":{"u":1},"resolutions":[]}]`)
	got, ok := prstate.MarkerFor(markers, 1, core.LegReview)
	if !ok {
		t.Fatal("no marker found")
	}
	before := string(markers[0].Findings)
	got.Findings[2] = 'X'
	got.Tokens[1] = 'X'
	got.Usage[1] = 'X'
	got.Resolutions[0] = ' '
	if string(markers[0].Findings) != before {
		t.Errorf("editing the returned findings reached the list: %s", markers[0].Findings)
	}
	if string(markers[0].Tokens) != `{"in":1}` || string(markers[0].Usage) != `{"u":1}` {
		t.Errorf("tokens %s usage %s", markers[0].Tokens, markers[0].Usage)
	}
	if string(markers[0].Resolutions) != `[]` {
		t.Errorf("resolutions %s", markers[0].Resolutions)
	}
}

// M6. The `last == ""` arm is the whole reason the shell writes
// `[[ -z "$last" || ... ]]` at lib/state.sh:348: with no marker to compare
// against, every revision is new, including the empty one.
func TestIsNewRevisionWithNoMarkerAndNoHead(t *testing.T) {
	if !prstate.IsNewRevision(nil, "") {
		t.Error("a pull request with no review marker read as already reviewed")
	}
}

// M5. Opt distinguishes three states, and Value alone cannot: a present null
// and a present empty string both answer "". Get is what a caller reads when
// the difference matters.
func TestOptValueAndGet(t *testing.T) {
	set := prstate.Some("codex")
	if got := set.Value(); got != "codex" {
		t.Errorf("Value on a set option is %q", got)
	}
	if got, ok := set.Get(); !ok || got != "codex" {
		t.Errorf("Get on a set option is %q, %v", got, ok)
	}
	null := prstate.Null[string]()
	if got := null.Value(); got != "" {
		t.Errorf("Value on a null option is %q", got)
	}
	if got, ok := null.Get(); ok || got != "" {
		t.Errorf("Get on a null option is %q, %v", got, ok)
	}
	var absent prstate.Opt[string]
	if got, ok := absent.Get(); ok || got != "" {
		t.Errorf("Get on an absent option is %q, %v", got, ok)
	}
	empty := prstate.Some("")
	if got, ok := empty.Get(); !ok || got != "" {
		t.Errorf("Get on a present empty string is %q, %v", got, ok)
	}
}

// M5. DecodeFindings is the review leg's route into the payload the resolve leg
// reads, and a marker with no findings leaves the destination alone.
func TestDecodeFindings(t *testing.T) {
	markers := mustMarkers(t, `[{"leg":"review","pass":1,"findings":[{"id":"aaaa000000000001","title":"a < b"}]},{"leg":"review","pass":2}]`)
	var findings []struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	}
	if err := markers[0].DecodeFindings(&findings); err != nil {
		t.Fatalf("decoding findings: %v", err)
	}
	if len(findings) != 1 || findings[0].ID != "aaaa000000000001" || findings[0].Title != "a < b" {
		t.Errorf("got %+v", findings)
	}
	untouched := findings
	if err := markers[1].DecodeFindings(&untouched); err != nil {
		t.Fatalf("decoding an empty findings payload: %v", err)
	}
	if len(untouched) != 1 {
		t.Errorf("a marker with no findings overwrote the destination: %+v", untouched)
	}
}
