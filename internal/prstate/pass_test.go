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
// (lib/state.sh:288-293).
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
// state (lib/state.sh:297-302).
func TestMarkerForTakesTheNewestWhateverItsState(t *testing.T) {
	markers := mustMarkers(t, `[{"leg":"review","pass":2,"state":"started","run_id":"1"},{"leg":"review","pass":2,"state":"complete","run_id":"2"}]`)
	got, ok := prstate.MarkerFor(markers, 2, core.LegReview)
	if !ok {
		t.Fatal("no marker found")
	}
	if got.RunID != "2" {
		t.Errorf("got run_id %q, want the newest", got.RunID)
	}
	if _, ok := prstate.MarkerFor(markers, 3, core.LegReview); ok {
		t.Error("a pass with no marker returned one")
	}
}

// state_is_new_revision compares the pull request head against the last
// non-declined review marker's head_sha (lib/state.sh:344-350).
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
	if markers[0].HeadSHA != "9f3c1ab" {
		t.Errorf("got %q", markers[0].HeadSHA)
	}
}
