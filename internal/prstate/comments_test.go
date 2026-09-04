package prstate_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/prstate"
)

// state_markers decodes a whole comment stream in one pass, keeps only the
// comments carrying a marker, attaches each comment's id, migrates the old
// vocabulary and skips a line it cannot read without losing the markers around
// it (lib/state.sh:141-155, pinned by tests/test-state.sh).
func TestMarkersOverACommentStream(t *testing.T) {
	stream := strings.Join([]string{
		`{"id":11,"body":"Pass 1.\n\n<!-- crossrev: {\"v\":1,\"leg\":\"review\",\"pass\":1,\"state\":\"complete\"} -->"}`,
		`not json`,
		`{"id":12,"body":"Pass 1.\n\n<!-- crossrev: {\"v\":1,\"leg\":\"resolve\",\"pass\":1,\"state\":\"complete\",\"dispositions\":[{\"finding_id\":\"f1\",\"disposition\":\"rebutted\"}]} -->"}`,
		`{"id":13,"body":"a human reply with no marker"}`,
	}, "\n")

	markers := prstate.Markers([]byte(stream))
	if len(markers) != 2 {
		t.Fatalf("kept %d markers, want 2", len(markers))
	}
	if markers[0].CommentID() != 11 || markers[1].CommentID() != 12 {
		t.Errorf("comment ids are %d and %d", markers[0].CommentID(), markers[1].CommentID())
	}
	if markers[0].Leg != core.LegReview || markers[1].Leg != core.LegResolve {
		t.Errorf("legs are %q and %q", markers[0].Leg, markers[1].Leg)
	}
	var resolutions []struct {
		FindingID  string `json:"finding_id"`
		Resolution string `json:"resolution"`
	}
	if err := markers[1].DecodeResolutions(&resolutions); err != nil {
		t.Fatalf("decoding resolutions: %v", err)
	}
	if len(resolutions) != 1 || resolutions[0].Resolution != string(core.ResolutionDisputed) {
		t.Errorf("the old vocabulary was not migrated inside the batch: %+v", resolutions)
	}
}

// A line that is valid JSON but not an object is skipped, the way
// `select(type == "object")` skips it.
func TestMarkersSkipsANonObjectLine(t *testing.T) {
	stream := "[1,2]\n\"text\"\n" + `{"id":9,"body":"x<!-- crossrev: {\"leg\":\"review\"} -->"}`
	markers := prstate.Markers([]byte(stream))
	if len(markers) != 1 || markers[0].CommentID() != 9 {
		t.Fatalf("got %d markers", len(markers))
	}
}

// state_finding_marker writes the id, the pass and the leg in that order,
// behind the finding prefix (lib/state.sh:168-172).
func TestEncodeFindingMarker(t *testing.T) {
	id := prstate.NewFindingID("app.ts", "Fetch timeout missing", prstate.Anchor("abcd1234"))
	got := prstate.EncodeFindingMarker(id, 2, core.LegReview)
	want := "\n\n<!-- crossrev:f {\"id\":\"" + string(id) + "\",\"pass\":2,\"leg\":\"review\"} -->"
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

// _state_finding_ids reads the ids back out of comment bodies, narrowed to one
// leg and optionally one pass, and returns them sorted and deduplicated
// (lib/state.sh:216-222).
func TestFindingIDsFilterByLegAndPass(t *testing.T) {
	bodies := []string{
		"a" + markFor(t, "bbbb", 1, core.LegReview),
		"b" + markFor(t, "aaaa", 1, core.LegReview),
		"c" + markFor(t, "aaaa", 1, core.LegReview),
		"d" + markFor(t, "cccc", 2, core.LegReview),
		"e" + markFor(t, "dddd", 1, core.LegResolve),
	}

	all := prstate.FindingIDs(bodies, core.LegReview, 0)
	if strings.Join(strs(all), ",") != strings.Join(sortedIDs(t, "aaaa", "bbbb", "cccc"), ",") {
		t.Errorf("unfiltered by pass: %v", strs(all))
	}

	one := prstate.FindingIDs(bodies, core.LegReview, 1)
	if strings.Join(strs(one), ",") != strings.Join(sortedIDs(t, "aaaa", "bbbb"), ",") {
		t.Errorf("pass 1 only: %v", strs(one))
	}

	other := prstate.FindingIDs(bodies, core.LegResolve, 0)
	if len(other) != 1 {
		t.Errorf("the resolve leg saw %v", strs(other))
	}
}

// The extraction is per line, taking the last opening delimiter and the last
// closing one after it, exactly as the sed at lib/state.sh:218 does.
func TestFindingIDsTakesTheLastDelimitersOnALine(t *testing.T) {
	first := markFor(t, "aaaa", 1, core.LegReview)
	second := markFor(t, "bbbb", 1, core.LegReview)
	line := "prefix " + strings.TrimPrefix(first, "\n\n") + " middle " + strings.TrimPrefix(second, "\n\n") + " tail"
	got := prstate.FindingIDs([]string{line}, core.LegReview, 0)
	if len(got) != 1 {
		t.Fatalf("got %v", strs(got))
	}
}

func TestFindingIDsIgnoresAStateMarker(t *testing.T) {
	body := "text\n\n<!-- crossrev: {\"leg\":\"review\",\"pass\":1} -->"
	if got := prstate.FindingIDs([]string{body}, core.LegReview, 0); len(got) != 0 {
		t.Errorf("a state marker produced %v", strs(got))
	}
}

// markFor mints a deterministic id from a seed and writes its finding marker.
func markFor(t *testing.T, seed string, pass int, leg core.Leg) string {
	t.Helper()
	return prstate.EncodeFindingMarker(idFor(t, seed), pass, leg)
}

func idFor(t *testing.T, seed string) core.FindingID {
	t.Helper()
	return prstate.NewFindingID(seed+".ts", seed, prstate.Anchor(seed))
}

func sortedIDs(t *testing.T, seeds ...string) []string {
	t.Helper()
	out := make([]string, 0, len(seeds))
	for _, s := range seeds {
		out = append(out, string(idFor(t, s)))
	}
	slices.Sort(out)
	return out
}

func strs(ids []core.FindingID) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, string(id))
	}
	return out
}

// Markers keeps the comment order, which is chronological, because pass
// numbering and revision detection both read the last one.
func TestMarkersKeepsTheCommentOrder(t *testing.T) {
	stream := strings.Join([]string{
		`{"id":1,"body":"<!-- crossrev: {\"leg\":\"review\",\"pass\":1,\"head_sha\":\"aaa\"} -->"}`,
		`{"id":2,"body":"<!-- crossrev: {\"leg\":\"review\",\"pass\":2,\"head_sha\":\"bbb\"} -->"}`,
	}, "\n")
	markers := prstate.Markers([]byte(stream))
	if len(markers) != 2 || markers[0].Pass != 1 || markers[1].Pass != 2 {
		t.Fatalf("got %+v", markers)
	}
	if prstate.IsNewRevision(markers, "bbb") {
		t.Error("the last review marker's head SHA was not the one compared")
	}
}

// A body carrying a marker on each of two lines concatenates to two JSON
// values, which no parser accepts, so that comment contributes nothing and the
// ones around it survive (lib/state.sh:77-81). Two markers on ONE line are a
// different case: the last-opening rule keeps only the second.
func TestMarkersSkipsABodyWithAMarkerOnTwoLines(t *testing.T) {
	stream := strings.Join([]string{
		`{"id":1,"body":"<!-- crossrev: {\"leg\":\"review\",\"pass\":1} -->\n<!-- crossrev: {\"leg\":\"review\",\"pass\":9} -->"}`,
		`{"id":2,"body":"<!-- crossrev: {\"leg\":\"review\",\"pass\":2} -->"}`,
	}, "\n")
	markers := prstate.Markers([]byte(stream))
	if len(markers) != 1 || markers[0].CommentID() != 2 {
		t.Fatalf("got %+v", markers)
	}
}

// Two markers on ONE line leave the second, because the extraction takes the
// last opening delimiter on the line.
func TestMarkersTakesTheSecondOfTwoMarkersOnOneLine(t *testing.T) {
	stream := `{"id":1,"body":"<!-- crossrev: {\"leg\":\"review\",\"pass\":1} --> <!-- crossrev: {\"leg\":\"review\",\"pass\":9} -->"}`
	markers := prstate.Markers([]byte(stream))
	if len(markers) != 1 || markers[0].Pass != 9 {
		t.Fatalf("got %+v", markers)
	}
}

// An id the hash could not have produced is dropped rather than carried into
// core.FindingID, whose invariant is 16 lowercase hexadecimal characters. Bash
// prints whatever `.id` holds, so this is the one place Go is stricter, and it
// is reachable only by writing a marker by hand.
func TestFindingIDsDropsAnIDTheHashCouldNotProduce(t *testing.T) {
	bodies := []string{
		`a <!-- crossrev:f {"id":"NOTHEX","pass":1,"leg":"review"} -->`,
		`b <!-- crossrev:f {"id":"aaaa000000000001","pass":1,"leg":"review"} -->`,
	}
	got := prstate.FindingIDs(bodies, core.LegReview, 0)
	if len(got) != 1 || string(got[0]) != "aaaa000000000001" {
		t.Errorf("got %v", strs(got))
	}
}

// `_state_finding_ids` pipes every extracted payload into one jq process, and
// jq aborts the whole stream on the first parse error, so the shell loses every
// id BELOW an unreadable marker as well as that one. Measured by sourcing
// lib/state.sh: with the bad line first, `_state_finding_ids review ""` printed
// nothing; with the two lines reversed it printed the id.
//
// Go's answer is the one this port keeps. The direction is the reason it is
// safe: Go finds ids the shell misses, and an id already in the set is one the
// leg does not reply to again, so a Go resolve leg skips a reply a shell one
// would repeat rather than posting one twice.
func TestFindingIDsSurviveAnUnreadableMarkerAboveThem(t *testing.T) {
	bodies := []string{
		"bad  <!-- crossrev:f {oops} -->\ngood <!-- crossrev:f {\"id\":\"aaaa000000000001\",\"pass\":1,\"leg\":\"review\"} -->",
	}
	got := prstate.FindingIDs(bodies, core.LegReview, 0)
	if len(got) != 1 || string(got[0]) != "aaaa000000000001" {
		t.Errorf("read %v, want [aaaa000000000001]", got)
	}
}

// A payload that is not an object contributes nothing and the markers around it
// survive, which is what jq does with `2>/dev/null` on the same stream.
func TestFindingIDsSkipsANonObjectPayload(t *testing.T) {
	bodies := []string{
		`x <!-- crossrev:f 7 -->`,
		`b <!-- crossrev:f {"id":"aaaa000000000001","pass":1,"leg":"review"} -->`,
	}
	if got := prstate.FindingIDs(bodies, core.LegReview, 0); len(got) != 1 {
		t.Errorf("got %v", strs(got))
	}
}

// F3. jq keeps a marker whose field carries the wrong JSON type; the typed view
// must not throw the marker away for it. `commit_subject` is model-supplied at
// lib/run.sh:2115, so a future writer changing any field's type would otherwise
// stop every marker on the pull request from decoding, leave Pass at 1 forever,
// and report nothing wrong.
func TestMarkersKeepsAMarkerWithAWrongTypedField(t *testing.T) {
	stream := strings.Join([]string{
		`{"id":1,"body":"<!-- crossrev: {\"v\":1,\"leg\":\"review\",\"pass\":1,\"commit_subject\":7} -->"}`,
		`{"id":2,"body":"<!-- crossrev: {\"v\":1,\"leg\":\"review\",\"pass\":2} -->"}`,
	}, "\n")
	markers := prstate.Markers([]byte(stream))
	if len(markers) != 2 {
		t.Fatalf("kept %d markers, want 2", len(markers))
	}
	if prstate.Pass(markers) != 3 {
		t.Errorf("pass numbering restarted: got %d", prstate.Pass(markers))
	}
	// The field the struct cannot hold reads as absent, and the bytes survive
	// on the raw view.
	if markers[0].CommitSubject.Present() {
		t.Error("a wrong-typed field read as present")
	}
	if !strings.Contains(string(markers[0].Raw()), `"commit_subject":7`) {
		t.Errorf("the original bytes were lost: %s", markers[0].Raw())
	}
}

// lib/state.sh:153 forces an empty read to `[]` rather than nothing, so a
// caller marshalling the result writes an empty array and not a null.
func TestMarkersOverAnEmptyStreamIsAnEmptySlice(t *testing.T) {
	got := prstate.Markers(nil)
	if got == nil {
		t.Error("an empty stream returned a nil slice")
	}
	if len(got) != 0 {
		t.Errorf("got %d markers", len(got))
	}
}
