package forge

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/prstate"
)

// commentsPerPage is what RepoIssueComments asks GitHub for, and so what a
// short page is measured against.
const commentsPerPage = 100

// DailyCount is what the daily backstop needs to know before it reads anything.
type DailyCount struct {
	Repo core.Slug
	// Author is whose markers count. A marker is an HTML comment and anyone
	// who can comment can write one, so author identity is the only signal
	// GitHub controls and nobody can forge (lib/state.sh:16-22).
	Author string
	// Cutoff is the start of the rolling window.
	Cutoff time.Time
	// Cap is the configured limit. Zero means no limit.
	Cap int
	// CurrentPR is the pull request under review, which is excluded from the
	// count.
	CurrentPR int
	// CurrentMarkers are the markers already on that pull request.
	CurrentMarkers []prstate.Marker
}

// PRsReviewedToday counts distinct pull requests other than the current one
// that carry a trusted review marker inside the rolling window
// (lib/state.sh:351-419).
//
// A review-and-resolve cycle counts once, because only the review marker
// participates. If the current pull request is already in the window,
// reviewing it again consumes no new unit and the answer is zero without
// reading the repository-wide list at all.
//
// # It paginates to exhaustion
//
// This is the one place the Go port answers differently from the shell, and it
// answers with the number. lib/state.sh:366 loops `for page in 1 2 3 4 5 6 7 8
// 9 10` and then warns that the count covers only what it inspected, so a busy
// repository past a thousand comments in the window reported a total it knew
// was short. Here the read follows pagination until a page comes back shorter
// than a full one, so the count is exact and there is no warning to print. The
// bound that remains is the caller's context: a page that never comes back
// short is a cancellation, not a page counter.
//
// # A whole page at a time
//
// The cap is tested after a page is folded in rather than after each comment.
// The per-comment form returned the instant the count reached the cap, so it
// could never report more than the cap itself; folding the page in first means
// the cap is what gets printed rather than the overshoot (lib/state.sh:407-409).
//
// # A failed read rounds down
//
// An unreadable page answers zero and reports why. The backstop rounds down
// rather than stopping a healthy automatic review early, so the caller warns
// and carries on with zero — which is what lib/state.sh:367-372 does. Printing
// that warning is the caller's, because this package has no reporter and the
// text is the shell's.
//
// # A timestamp that is not a number
//
// A marker whose `ts` is a JSON string counts in the shell and not here. jq
// orders every number below every string, so `"123" > $cutoff` is true whatever
// the cutoff; prstate.Marker decodes field by field and a type mismatch leaves
// the field at its zero (internal/prstate/pass.go:167-200), so the marker fails
// the window test and is skipped.
//
// It is left as it is rather than reproduced. Reaching it takes the trusted
// author writing a malformed marker — the App writes `ts` with jq's `now |
// floor`, so nothing CrossRev ships produces one — and the difference rounds
// the count down, which is the direction lib/state.sh:367-372 already documents
// as the intended one for a read that cannot answer. Reproducing it would mean
// decoding `ts` a second time as raw JSON and implementing jq's total ordering
// across types for one comparison, because the shared marker type is an int64
// and the count may not have its own. That is a jq implementation detail
// carried into Go to make one malformed marker count for more.
func PRsReviewedToday(ctx context.Context, f Forge, req DailyCount) (int, error) {
	for _, m := range req.CurrentMarkers {
		if countsAsReview(m.Leg, m.State, m.TS, req.Cutoff) {
			return 0, nil
		}
	}

	// The URLs already counted, in the order they were seen. A slice rather
	// than a set because the count is small — it stops at the cap — and the
	// order is what makes a failure readable.
	var seen []string
	suffix := "/" + strconv.Itoa(req.CurrentPR)

	for page := 1; ; page++ {
		comments, err := f.RepoIssueComments(ctx, req.Repo, req.Cutoff, page)
		if err != nil {
			return 0, err
		}

		for _, c := range comments {
			if c.AuthorLogin != req.Author {
				continue
			}
			marker, ok := reviewMarkerOf(c.Body)
			if !ok {
				continue
			}
			if !countsAsReview(marker.Leg, marker.State, marker.TS, req.Cutoff) {
				continue
			}
			// The pull request a repository-wide comment belongs to is the
			// issue_url it carries, and the current one does not count.
			if c.IssueURL == "" || strings.HasSuffix(c.IssueURL, suffix) {
				continue
			}
			if !slices.Contains(seen, c.IssueURL) {
				seen = append(seen, c.IssueURL)
			}
		}

		if req.Cap > 0 && len(seen) >= req.Cap {
			return req.Cap, nil
		}
		if len(comments) < commentsPerPage {
			return len(seen), nil
		}
	}
}

// countsAsReview is the one test both halves apply: a review leg, not
// declined, stamped inside the window.
func countsAsReview(leg core.Leg, state core.PassState, ts int64, cutoff time.Time) bool {
	return leg == core.LegReview && !state.Declined() && ts > cutoff.Unix()
}

// reviewMarkerOf pulls the pass marker out of one comment body.
//
// It reads leg, state and ts only, which is why lib/state.sh:378-383 says this
// read never touches a migrated key. It goes through the shared decoder anyway:
// the extraction rule is the thing that has to match — per line, the last
// opening delimiter and the last closing one after it, so a body carrying two
// markers concatenates to invalid JSON and is skipped — and a second copy of
// that rule is how two readers drift apart. The migration the decoder also
// applies renames keys this function does not read, so it cannot change the
// answer.
func reviewMarkerOf(body string) (prstate.Marker, bool) {
	raw, ok := prstate.DecodeMarker(body)
	if !ok {
		return prstate.Marker{}, false
	}
	marker, err := prstate.ParseMarker(raw)
	if err != nil {
		return prstate.Marker{}, false
	}
	return marker, true
}
