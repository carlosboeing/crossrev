package forge_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/forge"
	"github.com/carlosboeing/crossrev/internal/prstate"
)

const (
	countAuthor = "crossrev-acme[bot]"
	countRepo   = "acme/widget"
)

var countCutoff = time.Unix(1_700_000_000, 0).UTC()

func slug(t *testing.T) core.Slug {
	t.Helper()
	s, err := core.ParseSlug(countRepo)
	if err != nil {
		t.Fatalf("parsing %q: %v", countRepo, err)
	}
	return s
}

// markerComment builds one issue comment carrying a pass marker, the way
// GitHub returns it.
func markerComment(t *testing.T, author string, issue int, leg string, state string, ts int64) forge.IssueComment {
	t.Helper()
	payload := map[string]any{"v": 1, "leg": leg, "pass": 1, "ts": ts}
	if state != "" {
		payload["state"] = state
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshalling a marker: %v", err)
	}
	return forge.IssueComment{
		AuthorLogin: author,
		IssueURL:    fmt.Sprintf("https://api.github.com/repos/%s/issues/%d", countRepo, issue),
		Body:        "Summary.\n\n<!-- crossrev: " + string(raw) + " -->",
	}
}

func markers(t *testing.T, leg string, state string, ts int64) []prstate.Marker {
	t.Helper()
	payload := map[string]any{"v": 1, "leg": leg, "pass": 1, "ts": ts}
	if state != "" {
		payload["state"] = state
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshalling a marker: %v", err)
	}
	m, err := prstate.ParseMarker(raw)
	if err != nil {
		t.Fatalf("parsing a marker: %v", err)
	}
	return []prstate.Marker{m}
}

func request(t *testing.T, cap int, current []prstate.Marker) forge.DailyCount {
	t.Helper()
	return forge.DailyCount{
		Repo:           slug(t),
		Author:         countAuthor,
		Cutoff:         countCutoff,
		Cap:            cap,
		CurrentPR:      42,
		CurrentMarkers: current,
	}
}

func TestPRsReviewedTodayCountsDistinctPullRequests(t *testing.T) {
	after := countCutoff.Unix() + 60
	f := &fakeForge{pages: [][]forge.IssueComment{{
		markerComment(t, countAuthor, 7, "review", "complete", after),
		// The same pull request twice counts once.
		markerComment(t, countAuthor, 7, "review", "complete", after+1),
		markerComment(t, countAuthor, 8, "review", "", after),
		// The resolve leg of a cycle does not consume a second unit.
		markerComment(t, countAuthor, 9, "resolve", "complete", after),
		// A declined review consumed nothing.
		markerComment(t, countAuthor, 10, "review", "declined", after),
		// Outside the window.
		markerComment(t, countAuthor, 11, "review", "complete", countCutoff.Unix()-1),
		// Somebody else's marker is not trusted.
		markerComment(t, "someone-else", 12, "review", "complete", after),
		// The pull request being reviewed right now is excluded.
		markerComment(t, countAuthor, 42, "review", "complete", after),
	}}}

	got, err := forge.PRsReviewedToday(context.Background(), f, request(t, 0, nil))
	if err != nil {
		t.Fatalf("PRsReviewedToday: %v", err)
	}
	if got != 2 {
		t.Errorf("count = %d, want 2", got)
	}
	if len(f.asked) != 1 || f.asked[0] != 1 {
		t.Errorf("pages read = %v, want [1]", f.asked)
	}
	if !f.since[0].Equal(countCutoff) {
		t.Errorf("cutoff passed = %v, want %v", f.since[0], countCutoff)
	}
}

// A suffix match on the issue URL must not treat pull request 142 as 42.
func TestPRsReviewedTodayExcludesOnlyTheCurrentPullRequest(t *testing.T) {
	after := countCutoff.Unix() + 60
	f := &fakeForge{pages: [][]forge.IssueComment{{
		markerComment(t, countAuthor, 42, "review", "complete", after),
		markerComment(t, countAuthor, 142, "review", "complete", after),
	}}}

	got, err := forge.PRsReviewedToday(context.Background(), f, request(t, 0, nil))
	if err != nil {
		t.Fatalf("PRsReviewedToday: %v", err)
	}
	if got != 1 {
		t.Errorf("count = %d, want 1", got)
	}
}

func TestPRsReviewedTodayReturnsZeroWhenThisPullRequestIsAlreadyInTheWindow(t *testing.T) {
	f := &fakeForge{pages: [][]forge.IssueComment{{
		markerComment(t, countAuthor, 7, "review", "complete", countCutoff.Unix()+60),
	}}}

	got, err := forge.PRsReviewedToday(context.Background(), f,
		request(t, 0, markers(t, "review", "complete", countCutoff.Unix()+1)))
	if err != nil {
		t.Fatalf("PRsReviewedToday: %v", err)
	}
	if got != 0 {
		t.Errorf("count = %d, want 0", got)
	}
	if len(f.asked) != 0 {
		t.Errorf("pages read = %v, want none", f.asked)
	}
}

func TestPRsReviewedTodayIgnoresOwnMarkersThatDoNotCount(t *testing.T) {
	cases := []struct {
		name    string
		markers []prstate.Marker
	}{
		{"resolve", markers(t, "resolve", "complete", countCutoff.Unix()+1)},
		{"declined", markers(t, "review", "declined", countCutoff.Unix()+1)},
		{"before the cutoff", markers(t, "review", "complete", countCutoff.Unix()-1)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := &fakeForge{pages: [][]forge.IssueComment{{
				markerComment(t, countAuthor, 7, "review", "complete", countCutoff.Unix()+60),
			}}}
			got, err := forge.PRsReviewedToday(context.Background(), f, request(t, 0, c.markers))
			if err != nil {
				t.Fatalf("PRsReviewedToday: %v", err)
			}
			if got != 1 {
				t.Errorf("count = %d, want 1", got)
			}
		})
	}
}

// The whole page is folded in before the cap is tested, so the answer is the
// cap itself rather than the overshoot (lib/state.sh:407-409).
func TestPRsReviewedTodayReportsTheCapAndStops(t *testing.T) {
	after := countCutoff.Unix() + 60
	page := make([]forge.IssueComment, 0, 100)
	for i := range 100 {
		page = append(page, markerComment(t, countAuthor, 100+i, "review", "complete", after))
	}
	f := &fakeForge{pages: [][]forge.IssueComment{page, page}}

	got, err := forge.PRsReviewedToday(context.Background(), f, request(t, 5, nil))
	if err != nil {
		t.Fatalf("PRsReviewedToday: %v", err)
	}
	if got != 5 {
		t.Errorf("count = %d, want the cap 5", got)
	}
	if len(f.asked) != 1 {
		t.Errorf("pages read = %v, want the first only", f.asked)
	}
}

// The ten-page cap is gone: pagination runs until a short page, and the count
// is exact.
func TestPRsReviewedTodayPaginatesPastTenPages(t *testing.T) {
	after := countCutoff.Unix() + 60
	var pages [][]forge.IssueComment
	for p := range 10 {
		page := []forge.IssueComment{markerComment(t, countAuthor, 1000+p, "review", "complete", after)}
		// A full page keeps the read going. The other 99 repeat one pull
		// request already counted, so only the first adds to the total.
		for range 99 {
			page = append(page, markerComment(t, countAuthor, 1000+p, "review", "complete", after))
		}
		pages = append(pages, page)
	}
	pages = append(pages, []forge.IssueComment{
		markerComment(t, countAuthor, 2000, "review", "complete", after),
	})
	f := &fakeForge{pages: pages}

	got, err := forge.PRsReviewedToday(context.Background(), f, request(t, 0, nil))
	if err != nil {
		t.Fatalf("PRsReviewedToday: %v", err)
	}
	if got != 11 {
		t.Errorf("count = %d, want 11", got)
	}
	if len(f.asked) != 11 {
		t.Errorf("pages read = %v, want eleven", f.asked)
	}
}

func TestPRsReviewedTodayReportsAnAPIFailureAsZero(t *testing.T) {
	after := countCutoff.Unix() + 60
	page := make([]forge.IssueComment, 0, 100)
	for i := range 100 {
		page = append(page, markerComment(t, countAuthor, 300+i, "review", "complete", after))
	}
	f := &fakeForge{pages: [][]forge.IssueComment{page}, failOn: 2}

	got, err := forge.PRsReviewedToday(context.Background(), f, request(t, 0, nil))
	if !errors.Is(err, errPage) {
		t.Errorf("error = %v, want the page read failure", err)
	}
	if got != 0 {
		t.Errorf("count = %d, want 0 on a failed read", got)
	}
}

// A body that carries two markers concatenates to invalid JSON and is skipped.
func TestPRsReviewedTodaySkipsABodyWithTwoMarkers(t *testing.T) {
	after := countCutoff.Unix() + 60
	two := markerComment(t, countAuthor, 7, "review", "complete", after)
	extra := markerComment(t, countAuthor, 7, "review", "complete", after)
	two.Body += "\n" + extra.Body

	f := &fakeForge{pages: [][]forge.IssueComment{{
		two,
		markerComment(t, countAuthor, 8, "review", "complete", after),
	}}}

	got, err := forge.PRsReviewedToday(context.Background(), f, request(t, 0, nil))
	if err != nil {
		t.Fatalf("PRsReviewedToday: %v", err)
	}
	if got != 1 {
		t.Errorf("count = %d, want 1", got)
	}
}

func TestPRsReviewedTodaySkipsACommentWithNoMarkerAndNoIssueURL(t *testing.T) {
	after := countCutoff.Unix() + 60
	noURL := markerComment(t, countAuthor, 7, "review", "complete", after)
	noURL.IssueURL = ""

	f := &fakeForge{pages: [][]forge.IssueComment{{
		noURL,
		{AuthorLogin: countAuthor, Body: "no marker here", IssueURL: "https://api.github.com/repos/acme/widget/issues/9"},
	}}}

	got, err := forge.PRsReviewedToday(context.Background(), f, request(t, 0, nil))
	if err != nil {
		t.Fatalf("PRsReviewedToday: %v", err)
	}
	if got != 0 {
		t.Errorf("count = %d, want 0", got)
	}
}

// A marker whose ts is a JSON string counts in jq, which orders every number
// below every string, and not here: the shared decoder leaves a field it cannot
// read at its zero, so the marker fails the window test.
func TestPRsReviewedTodaySkipsAMarkerWhoseTimestampIsAString(t *testing.T) {
	stringTS := forge.IssueComment{
		AuthorLogin: countAuthor,
		IssueURL:    "https://api.github.com/repos/acme/widget/issues/7",
		Body:        `<!-- crossrev: {"v":1,"leg":"review","pass":1,"ts":"1700000060"} -->`,
	}
	f := &fakeForge{pages: [][]forge.IssueComment{{stringTS}}}

	got, err := forge.PRsReviewedToday(context.Background(), f, request(t, 0, nil))
	if err != nil {
		t.Fatalf("PRsReviewedToday: %v", err)
	}
	if got != 0 {
		t.Errorf("count = %d, want 0; the shell counts it and rounding down is the safe direction", got)
	}
}
