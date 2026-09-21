package cycle_test

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/cycle"
	"github.com/carlosboeing/crossrev/internal/forge"
	"github.com/carlosboeing/crossrev/internal/prstate"
	"github.com/carlosboeing/crossrev/internal/prstate/storetest"
	"github.com/carlosboeing/crossrev/internal/ui"
)

// statusLedgerHead is the head the ledger cases run at, the same revision the
// measured fixtures use.
const statusLedgerHead = "2c4a46cb321db01826d116b5ef2add6b0284d68c"

// statusProducer is the producer the ledger cases run as: the reviewer the
// status fixture configuration resolves.
func statusProducer() prstate.Producer {
	return prstate.Producer{Harness: "claude", Model: "reviewer-model"}
}

func statusSlotRef(t *testing.T) prstate.SlotRef {
	t.Helper()
	slug, err := core.ParseSlug(statusRepo)
	if err != nil {
		t.Fatalf("slug: %v", err)
	}
	return prstate.SlotRef{Repo: slug, Number: statusPR, Slot: prstate.DefaultSlot}
}

// statusPublishGeneration publishes one complete generation the way the
// review leg does, and answers the handle the pass marker records.
func statusPublishGeneration(t *testing.T, store *storetest.FakeStore, base, head string, paths []string, records []prstate.Record) prstate.Handle {
	t.Helper()
	baseRev, err := core.NewRevision(base)
	if err != nil {
		t.Fatalf("base revision: %v", err)
	}
	headRev, err := core.NewRevision(head)
	if err != nil {
		t.Fatalf("head revision: %v", err)
	}
	gen := prstate.Generation{
		Gen:         1,
		Revision:    core.RevisionPair{Base: baseRev, Head: headRev},
		Engine:      core.FileEngineVersion,
		Slot:        prstate.DefaultSlot,
		Producer:    statusProducer(),
		Form:        prstate.GenerationFull,
		Paths:       paths,
		Records:     records,
		ScopeReport: prstate.ScopeReport{ExaminedScope: "every changed file at the head revision"},
	}
	handle, err := store.PublishGeneration(context.Background(), statusSlotRef(t), prstate.Handle{}, gen)
	if err != nil {
		t.Fatalf("PublishGeneration: %v", err)
	}
	return handle
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
// review leg leaves when its pass converges, carrying the given coverage
// fields.
func statusConvergedMarkerComment(t *testing.T, head string, mutate func(*prstate.Marker)) forge.IssueComment {
	t.Helper()
	marker := prstate.Marker{
		Version:  core.MarkerVersion,
		Leg:      core.LegReview,
		Pass:     1,
		State:    core.PassComplete,
		TS:       1786999940,
		DoneTS:   prstate.Some(int64(1786999970)),
		RunID:    prstate.Some("x"),
		HeadSHA:  prstate.Some(head),
		Harness:  prstate.Some("claude"),
		Verdict:  prstate.Some(string(core.VerdictConverged)),
		Findings: []byte("[]"),
	}
	if mutate != nil {
		mutate(&marker)
	}
	body, err := marker.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	return forge.IssueComment{ID: 9001, AuthorLogin: statusAuthor, Body: "Summary." + body}
}

// statusNameHandle mutates a marker to name the published generation,
// the way a review pass leaves its checkpoint behind.
func statusNameHandle(handle prstate.Handle) func(*prstate.Marker) {
	return func(m *prstate.Marker) {
		m.RecordCoverage(handle)
	}
}

// statusLoadLedger loads a report for a converged-labelled pull request
// carrying the given conversation comments at the fixture base and head,
// with the given ledger store behind the forge client.
func statusLoadLedger(t *testing.T, comments []forge.IssueComment, ledger prstate.LedgerStore) cycle.Report {
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
			ledger:   ledger,
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
// alone: the handle on a v2 marker is a reference, and a ledger that no
// longer holds the named generation — lost, written for another revision,
// retired, or holding records the convergence predicate refuses — cannot
// satisfy the coverage obligation the marker promises.
func TestStatusConvergenceReadsTheLedger(t *testing.T) {
	paths := []string{"app.go"}
	covered := []prstate.Record{statusUnitRecord("app.go", 0, string(core.FileVerdictNoIssue))}

	t.Run("a current complete generation underwrites the converged report", func(t *testing.T) {
		store := storetest.NewFakeStore()
		handle := statusPublishGeneration(t, store, statusBase, statusLedgerHead, paths, covered)
		comments := []forge.IssueComment{
			statusConvergedMarkerComment(t, statusLedgerHead, statusNameHandle(handle)),
		}
		if got := statusLoadLedger(t, comments, store).State; got != core.LoopConverged {
			t.Errorf("state = %q, want %q", got, core.LoopConverged)
		}
	})

	t.Run("a removed ledger leaves only the marker's promise", func(t *testing.T) {
		comments := []forge.IssueComment{
			statusConvergedMarkerComment(t, statusLedgerHead, cutoverNameLostCommit),
		}
		if got := statusLoadLedger(t, comments, storetest.NewFakeStore()).State; got != core.LoopAwaitingReview {
			t.Errorf("state = %q, want %q", got, core.LoopAwaitingReview)
		}
	})

	t.Run("an unverifiable generation is not a complete generation", func(t *testing.T) {
		store := storetest.NewFakeStore()
		handle := statusPublishGeneration(t, store, statusBase, statusLedgerHead, paths, covered)
		store.SetCorrupt(handle.Commit)
		comments := []forge.IssueComment{
			statusConvergedMarkerComment(t, statusLedgerHead, statusNameHandle(handle)),
		}
		if got := statusLoadLedger(t, comments, store).State; got != core.LoopAwaitingReview {
			t.Errorf("state = %q, want %q", got, core.LoopAwaitingReview)
		}
	})

	t.Run("a generation at another base answers for another pull request", func(t *testing.T) {
		store := storetest.NewFakeStore()
		handle := statusPublishGeneration(t, store,
			"1111111111111111111111111111111111111111", statusLedgerHead, paths, covered)
		comments := []forge.IssueComment{
			statusConvergedMarkerComment(t, statusLedgerHead, statusNameHandle(handle)),
		}
		if got := statusLoadLedger(t, comments, store).State; got != core.LoopAwaitingReview {
			t.Errorf("state = %q, want %q", got, core.LoopAwaitingReview)
		}
	})

	t.Run("a could_not_review verdict cannot underwrite convergence", func(t *testing.T) {
		store := storetest.NewFakeStore()
		handle := statusPublishGeneration(t, store, statusBase, statusLedgerHead, paths,
			[]prstate.Record{statusUnitRecord("app.go", 0, string(core.FileVerdictCouldNotReview))})
		comments := []forge.IssueComment{
			statusConvergedMarkerComment(t, statusLedgerHead, statusNameHandle(handle)),
		}
		if got := statusLoadLedger(t, comments, store).State; got != core.LoopAwaitingReview {
			t.Errorf("state = %q, want %q", got, core.LoopAwaitingReview)
		}
	})

	t.Run("an outstanding record cannot underwrite convergence", func(t *testing.T) {
		store := storetest.NewFakeStore()
		handle := statusPublishGeneration(t, store, statusBase, statusLedgerHead, paths,
			[]prstate.Record{prstate.OutstandingRecord(string(core.FileUnitID("app.go")), 0,
				string(core.ChangeModified), core.BodyDigestHex([]byte("body of app.go")), "awaiting review")})
		comments := []forge.IssueComment{
			statusConvergedMarkerComment(t, statusLedgerHead, statusNameHandle(handle)),
		}
		if got := statusLoadLedger(t, comments, store).State; got != core.LoopAwaitingReview {
			t.Errorf("state = %q, want %q", got, core.LoopAwaitingReview)
		}
	})

	t.Run("a v1 marker predates the coverage obligation", func(t *testing.T) {
		raw := []byte(`{"v":1,"leg":"review","pass":1,"state":"complete","ts":1786999940,"done_ts":1786999970,"run_id":"x","head_sha":"` + statusLedgerHead + `","harness":"claude","verdict":"converged","findings":[]}`)
		body, err := prstate.EncodeMarker(raw)
		if err != nil {
			t.Fatalf("EncodeMarker: %v", err)
		}
		comments := []forge.IssueComment{{ID: 9001, AuthorLogin: statusAuthor, Body: "Summary." + body}}
		if got := statusLoadLedger(t, comments, storetest.NewFakeStore()).State; got != core.LoopConverged {
			t.Errorf("state = %q, want %q", got, core.LoopConverged)
		}
	})
}

// TestStatusReportsTheGenerationTheStoreAndTheDegradedFlag pins the other
// half of taking the machine-facing detail out of the summary: the
// generation number, the store it lives in and the degraded flag belong in
// `crossrev status` output, carried from the current review pass marker.
func TestStatusReportsTheGenerationTheStoreAndTheDegradedFlag(t *testing.T) {
	t.Run("a ref generation names its number, its ref and the degraded flag", func(t *testing.T) {
		refName, err := prstate.RefName("refs/crossrev", statusSlotRef(t))
		if err != nil {
			t.Fatalf("RefName: %v", err)
		}
		handle := prstate.Handle{
			Gen:      7,
			Commit:   strings.Repeat("d", 40),
			Location: refName,
			Degraded: true,
		}
		comments := []forge.IssueComment{
			statusConvergedMarkerComment(t, statusLedgerHead, statusNameHandle(handle)),
		}
		report := statusLoadLedger(t, comments, storetest.NewFakeStore())
		if report.CoverageGen != 7 || report.CoverageStore != refName || !report.CoverageDegraded {
			t.Fatalf("coverage = (%d, %q, %v), want (7, %q, true)",
				report.CoverageGen, report.CoverageStore, report.CoverageDegraded, refName)
		}
		var buf bytes.Buffer
		cycle.Render(&ui.IO{Out: &buf}, report)
		page := buf.String()
		for _, want := range []string{
			"generation   7",
			"store        " + refName,
			"degraded     yes",
		} {
			if !strings.Contains(page, want) {
				t.Errorf("page lacks %q:\n%s", want, page)
			}
		}
	})

	t.Run("a whole marker generation omits the degraded line", func(t *testing.T) {
		store := storetest.NewFakeMarkerStore()
		handle := statusPublishGeneration(t, store, statusBase, statusLedgerHead,
			[]string{"app.go"},
			[]prstate.Record{statusUnitRecord("app.go", 0, string(core.FileVerdictNoIssue))})
		comments := []forge.IssueComment{
			statusConvergedMarkerComment(t, statusLedgerHead, statusNameHandle(handle)),
		}
		report := statusLoadLedger(t, comments, store)
		if report.CoverageGen != 1 || report.CoverageStore != prstate.HandleMarker || report.CoverageDegraded {
			t.Fatalf("coverage = (%d, %q, %v), want (1, %q, false)",
				report.CoverageGen, report.CoverageStore, report.CoverageDegraded, prstate.HandleMarker)
		}
		var buf bytes.Buffer
		cycle.Render(&ui.IO{Out: &buf}, report)
		page := buf.String()
		for _, want := range []string{"generation   1", "store        marker"} {
			if !strings.Contains(page, want) {
				t.Errorf("page lacks %q:\n%s", want, page)
			}
		}
		if strings.Contains(page, "degraded") {
			t.Errorf("a whole generation prints a degraded line:\n%s", page)
		}
	})
}
