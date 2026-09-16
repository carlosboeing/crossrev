package cycle_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/cycle"
	"github.com/carlosboeing/crossrev/internal/forge"
	"github.com/carlosboeing/crossrev/internal/prstate"
)

// statusLedgerHead is the head the ledger cases run at, the same revision the
// measured fixtures use.
const statusLedgerHead = "2c4a46cb321db01826d116b5ef2add6b0284d68c"

// statusLedger is the in-memory ledger store a case publishes a generation
// into. Comment ids run from 8001 up so they never collide with the marker
// comments the case adds beside them.
type statusLedger struct {
	next     int64
	comments []prstate.CoverageComment
}

func (l *statusLedger) CoverageComments(context.Context, core.Slug, int) ([]prstate.CoverageComment, error) {
	return l.comments, nil
}

func (l *statusLedger) CoverageComment(_ context.Context, _ core.Slug, id int64) (prstate.CoverageComment, error) {
	for _, c := range l.comments {
		if c.ID == id {
			return c, nil
		}
	}
	return prstate.CoverageComment{}, fmt.Errorf("no such comment %d", id)
}

func (l *statusLedger) CreateCoverageComment(_ context.Context, _ core.Slug, _ int, body string) (int64, error) {
	l.next++
	id := 8000 + l.next
	l.comments = append(l.comments, prstate.CoverageComment{ID: id, Author: statusAuthor, Body: body})
	return id, nil
}

var _ prstate.LedgerStore = (*statusLedger)(nil)

// statusPublishGeneration publishes one complete generation the way the
// review leg does, and answers the conversation comments carrying it and the
// manifest's comment id.
func statusPublishGeneration(t *testing.T, base, head string, paths []string, records []prstate.Record) ([]prstate.CoverageComment, int64) {
	t.Helper()
	baseRev, err := core.NewRevision(base)
	if err != nil {
		t.Fatalf("base revision: %v", err)
	}
	headRev, err := core.NewRevision(head)
	if err != nil {
		t.Fatalf("head revision: %v", err)
	}
	slug, err := core.ParseSlug(statusRepo)
	if err != nil {
		t.Fatalf("slug: %v", err)
	}
	store := &statusLedger{}
	candidate := prstate.Generation{
		Gen:         1,
		Revision:    core.RevisionPair{Base: baseRev, Head: headRev},
		Engine:      core.FileEngineVersion,
		Paths:       paths,
		Records:     records,
		ScopeReport: prstate.ScopeReport{ExaminedScope: "every changed file at the head revision"},
	}
	manifest, stop, err := prstate.PublishGeneration(context.Background(), store, slug, statusPR, candidate, func() error { return nil })
	if err != nil {
		t.Fatalf("PublishGeneration: %v", err)
	}
	if stop.Limit != "" {
		t.Fatalf("PublishGeneration stopped: %+v", stop)
	}
	return store.comments, manifest.CommentID()
}

// statusUnitRecord is one judged file record for the ledger cases.
func statusUnitRecord(path string, pathIndex int, verdict string) prstate.Record {
	return prstate.Record{
		Type:       prstate.CoverageRecordUnit,
		UnitID:     string(core.FileUnitID(path)),
		PathIndex:  pathIndex,
		Kind:       prstate.CoverageGranularityFile,
		Change:     string(core.ChangeModified),
		BodyDigest: core.BodyDigestHex([]byte("body of " + path)),
		Verdict:    prstate.Some(verdict),
	}
}

// statusConvergedMarkerComment is the v2 complete converged review marker the
// review leg leaves when its pass converges, naming the manifest it published.
func statusConvergedMarkerComment(t *testing.T, head string, manifestID int64) forge.IssueComment {
	t.Helper()
	marker := prstate.Marker{
		Version:            core.MarkerVersion,
		Leg:                core.LegReview,
		Pass:               1,
		State:              core.PassComplete,
		TS:                 1786999940,
		DoneTS:             prstate.Some(int64(1786999970)),
		RunID:              prstate.Some("x"),
		HeadSHA:            prstate.Some(head),
		Harness:            prstate.Some("claude"),
		Verdict:            prstate.Some(string(core.VerdictConverged)),
		Findings:           json.RawMessage("[]"),
		CoverageManifestID: prstate.Some(manifestID),
	}
	body, err := marker.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	return forge.IssueComment{ID: 9001, AuthorLogin: statusAuthor, Body: "Summary." + body}
}

// statusLedgerComments renders ledger comments as conversation comments, the
// shape IssueComments answers with.
func statusLedgerComments(ledger []prstate.CoverageComment) []forge.IssueComment {
	out := make([]forge.IssueComment, 0, len(ledger))
	for _, c := range ledger {
		out = append(out, forge.IssueComment{ID: c.ID, AuthorLogin: c.Author, Body: "coverage" + c.Body})
	}
	return out
}

// statusLoadLedger loads a report for a converged-labelled pull request
// carrying the given conversation comments at the fixture base and head.
func statusLoadLedger(t *testing.T, comments []forge.IssueComment) cycle.Report {
	t.Helper()
	head, err := core.NewRevision(statusLedgerHead)
	if err != nil {
		t.Fatalf("head revision: %v", err)
	}
	base, err := core.NewRevision(statusBase)
	if err != nil {
		t.Fatalf("base revision: %v", err)
	}
	slug, err := core.ParseSlug(statusRepo)
	if err != nil {
		t.Fatalf("slug: %v", err)
	}
	s := &cycle.Status{
		Forge: &statusForge{
			pr: forge.PullRequest{
				Number:      statusPR,
				HeadRefOid:  head,
				BaseRefOid:  base,
				State:       "OPEN",
				Labels:      []forge.Label{{Name: "crossrev/converged"}},
				HeadRefName: "feature",
			},
			comments: comments,
		},
		Liveness: statusLife{},
		Now:      func() time.Time { return time.Unix(1787000000, 0) },
		Show:     statusShow(statusCase{MinFixSeverity: "medium", MaxPassesPerCycle: 3}),
	}
	report, err := s.Load(context.Background(), slug, statusPR)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return report
}

// TestStatusConvergenceReadsTheLedger pins that a converged report is
// underwritten by the coverage the marker references, not by the marker
// alone: the manifest id on a v2 marker is a reference, and a ledger that no
// longer holds a current complete generation — removed, written for another
// revision, or holding records the convergence predicate refuses — cannot
// satisfy the coverage obligation the marker promises.
func TestStatusConvergenceReadsTheLedger(t *testing.T) {
	paths := []string{"app.go"}
	covered := []prstate.Record{statusUnitRecord("app.go", 0, string(core.FileVerdictNoIssue))}

	ledger, manifestID := statusPublishGeneration(t, statusBase, statusLedgerHead, paths, covered)

	cases := []struct {
		name    string
		ledger  []prstate.CoverageComment
		want    core.LoopState
	}{
		{
			name:   "a current complete generation underwrites the converged report",
			ledger: ledger,
			want:   core.LoopConverged,
		},
		{
			name:   "a removed ledger leaves only the marker's promise",
			ledger: nil,
			want:   core.LoopAwaitingReview,
		},
		{
			name: "a manifest without its shard is not a complete generation",
			ledger: func() []prstate.CoverageComment {
				var manifests []prstate.CoverageComment
				for _, c := range ledger {
					if _, ok := prstate.DecodeCoverageManifest(c.Body); ok {
						manifests = append(manifests, c)
					}
				}
				return manifests
			}(),
			want: core.LoopAwaitingReview,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			comments := append(statusLedgerComments(tc.ledger),
				statusConvergedMarkerComment(t, statusLedgerHead, manifestID))
			report := statusLoadLedger(t, comments)
			if report.State != tc.want {
				t.Errorf("state = %q, want %q", report.State, tc.want)
			}
		})
	}

	t.Run("a generation at another base answers for another pull request", func(t *testing.T) {
		other, otherManifest := statusPublishGeneration(t,
			"1111111111111111111111111111111111111111", statusLedgerHead, paths, covered)
		comments := append(statusLedgerComments(other),
			statusConvergedMarkerComment(t, statusLedgerHead, otherManifest))
		if got := statusLoadLedger(t, comments).State; got != core.LoopAwaitingReview {
			t.Errorf("state = %q, want %q", got, core.LoopAwaitingReview)
		}
	})

	t.Run("a could_not_review verdict cannot underwrite convergence", func(t *testing.T) {
		unexamined, id := statusPublishGeneration(t, statusBase, statusLedgerHead, paths,
			[]prstate.Record{statusUnitRecord("app.go", 0, string(core.FileVerdictCouldNotReview))})
		comments := append(statusLedgerComments(unexamined),
			statusConvergedMarkerComment(t, statusLedgerHead, id))
		if got := statusLoadLedger(t, comments).State; got != core.LoopAwaitingReview {
			t.Errorf("state = %q, want %q", got, core.LoopAwaitingReview)
		}
	})

	t.Run("an outstanding record cannot underwrite convergence", func(t *testing.T) {
		waiting, id := statusPublishGeneration(t, statusBase, statusLedgerHead, paths,
			[]prstate.Record{prstate.OutstandingRecord(string(core.FileUnitID("app.go")), 0,
				string(core.ChangeModified), core.BodyDigestHex([]byte("body of app.go")), "awaiting review")})
		comments := append(statusLedgerComments(waiting),
			statusConvergedMarkerComment(t, statusLedgerHead, id))
		if got := statusLoadLedger(t, comments).State; got != core.LoopAwaitingReview {
			t.Errorf("state = %q, want %q", got, core.LoopAwaitingReview)
		}
	})

	t.Run("a v1 marker predates the coverage obligation", func(t *testing.T) {
		raw := json.RawMessage(`{"v":1,"leg":"review","pass":1,"state":"complete","ts":1786999940,"done_ts":1786999970,"run_id":"x","head_sha":"` + statusLedgerHead + `","harness":"claude","verdict":"converged","findings":[]}`)
		body, err := prstate.EncodeMarker(raw)
		if err != nil {
			t.Fatalf("EncodeMarker: %v", err)
		}
		comments := []forge.IssueComment{{ID: 9001, AuthorLogin: statusAuthor, Body: "Summary." + body}}
		if got := statusLoadLedger(t, comments).State; got != core.LoopConverged {
			t.Errorf("state = %q, want %q", got, core.LoopConverged)
		}
	})
}
