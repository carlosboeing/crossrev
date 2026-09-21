package cycle_test

import (
	"errors"
	"testing"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/forge"
	"github.com/carlosboeing/crossrev/internal/prstate"
	"github.com/carlosboeing/crossrev/internal/prstate/storetest"
)

func cutoverNameLostCommit(m *prstate.Marker) {
	m.CoverageGen = prstate.Some(7)
	m.CoverageRef = prstate.Some("refs/crossrev/pr/42/reviewer1/coverage")
	m.CoverageCommit = prstate.Some("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	m.CoverageDegraded = prstate.Some(false)
}

// TestStatusRefusesGreenWhenTheLedgerIsLost pins the status reader's lost
// row: the marker names a commit whose objects are gone, so the report
// refuses green. Left alone the reader returns true the moment markers
// stop carrying the legacy manifest id, which stops a check silently
// rather than failing.
func TestStatusRefusesGreenWhenTheLedgerIsLost(t *testing.T) {
	comments := []forge.IssueComment{
		statusConvergedMarkerComment(t, statusLedgerHead, cutoverNameLostCommit),
	}
	report := statusLoadLedger(t, comments, storetest.NewFakeStore())
	if report.State != core.LoopAwaitingReview {
		t.Errorf("state = %q, want %q: a lost ledger kept the converged report", report.State, core.LoopAwaitingReview)
	}
}

// TestEveryReaderFailsClosedOnCorruptionAndOnAnUnreadableStore pins the
// status reader: a corrupt claim and an unreadable store refuse, never
// read as no coverage.
func TestEveryReaderFailsClosedOnCorruptionAndOnAnUnreadableStore(t *testing.T) {
	t.Run("a corrupt claim refuses", func(t *testing.T) {
		comments := []forge.IssueComment{
			statusConvergedMarkerComment(t, statusLedgerHead, func(m *prstate.Marker) {
				// A claim that is not a valid handle: a generation
				// number with no ref behind it. Corrupt state,
				// never "no coverage".
				m.CoverageGen = prstate.Some(7)
			}),
		}
		report := statusLoadLedger(t, comments, storetest.NewFakeStore())
		if report.State != core.LoopAwaitingReview {
			t.Errorf("state = %q, want %q: a corrupt claim kept the converged report", report.State, core.LoopAwaitingReview)
		}
	})

	t.Run("an unreadable store refuses", func(t *testing.T) {
		comments := []forge.IssueComment{
			statusConvergedMarkerComment(t, statusLedgerHead, cutoverNameLostCommit),
		}
		store := storetest.NewFakeStore()
		store.SetUnreadable(errors.New("the store cannot be read"))
		report := statusLoadLedger(t, comments, store)
		if report.State != core.LoopAwaitingReview {
			t.Errorf("state = %q, want %q: an unreadable store kept the converged report", report.State, core.LoopAwaitingReview)
		}
	})
}
