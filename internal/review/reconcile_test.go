package review_test

import (
	"testing"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/forge"
	"github.com/carlosboeing/crossrev/internal/prstate"
	"github.com/carlosboeing/crossrev/internal/review"
)

func TestReconcileSkipsAFindingAlreadyCarryingItsMarker(t *testing.T) {
	id := mustFindingID(t, "aaaaaaaaaaaaaaaa")
	reviewComments := []forge.IssueComment{{
		ID:          11,
		AuthorLogin: author,
		Body:        "inline" + prstate.EncodeFindingMarker(id, 1, core.LegReview),
	}}
	got := review.PostedFindingIDs(reviewComments, nil, author)
	if len(got) != 1 || got[0] != id {
		t.Fatalf("PostedFindingIDs = %v, want [%s]", got, id)
	}
}

func TestReconcileReadsAFallbackFromIssueComments(t *testing.T) {
	id := mustFindingID(t, "bbbbbbbbbbbbbbbb")
	issue := []forge.IssueComment{{
		ID:          22,
		AuthorLogin: author,
		Body:        "**app.go:2** (RIGHT)\n\nfallback" + prstate.EncodeFindingMarker(id, 1, core.LegReview),
	}}
	posted := review.PostedFindingIDs(nil, issue, author)
	if len(posted) != 1 || posted[0] != id {
		t.Fatalf("PostedFindingIDs = %v, want [%s]", posted, id)
	}
	unthreaded := review.UnthreadedFindingIDs(issue, author, 1)
	if len(unthreaded) != 1 || unthreaded[0] != id {
		t.Fatalf("UnthreadedFindingIDs = %v, want [%s]", unthreaded, id)
	}
}

func TestReconcileIgnoresAnotherAuthorAndTheResolveLeg(t *testing.T) {
	reviewID := mustFindingID(t, "aaaaaaaaaaaaaaaa")
	resolveID := mustFindingID(t, "cccccccccccccccc")
	comments := []forge.IssueComment{
		{ID: 1, AuthorLogin: "someone-else", Body: "x" + prstate.EncodeFindingMarker(reviewID, 1, core.LegReview)},
		{ID: 2, AuthorLogin: author, Body: "y" + prstate.EncodeFindingMarker(resolveID, 1, core.LegResolve)},
	}
	if got := review.PostedFindingIDs(comments, comments, author); len(got) != 0 {
		t.Fatalf("PostedFindingIDs = %v, want none", got)
	}
	if got := review.UnthreadedFindingIDs(comments, author, 1); len(got) != 0 {
		t.Fatalf("UnthreadedFindingIDs = %v, want none", got)
	}
}

func TestReconcileUnthreadedIsNarrowedToThePass(t *testing.T) {
	id := mustFindingID(t, "dddddddddddddddd")
	issue := []forge.IssueComment{{
		ID:          3,
		AuthorLogin: author,
		Body:        "old" + prstate.EncodeFindingMarker(id, 1, core.LegReview),
	}}
	if got := review.UnthreadedFindingIDs(issue, author, 2); len(got) != 0 {
		t.Fatalf("pass 2 counted %v, want none", got)
	}
	if got := review.UnthreadedFindingIDs(issue, author, 1); len(got) != 1 {
		t.Fatalf("pass 1 counted %v, want [%s]", got, id)
	}
}

func mustFindingID(t *testing.T, s string) core.FindingID {
	t.Helper()
	id, err := prstate.ParseFindingID(s)
	if err != nil {
		t.Fatal(err)
	}
	return id
}
