package prstate_test

import (
	"strings"
	"testing"
	"time"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/prstate"
)

// state_open_claim finds an unfinished claim for the same (pass, leg), and
// only when the newest marker for that pair is still started
// (lib/state.sh:306-313).
func TestOpenClaim(t *testing.T) {
	claim := mustMarkers(t, `[{"leg":"review","pass":2,"state":"started","run_id":"7"}]`)
	got, ok := prstate.OpenClaim(claim, 2, core.LegReview)
	if !ok {
		t.Fatal("the unfinished claim was not found")
	}
	if got.RunID != "7" {
		t.Errorf("got run_id %q", got.RunID)
	}

	settled := mustMarkers(t, `[{"leg":"review","pass":2,"state":"started"},{"leg":"review","pass":2,"state":"complete"}]`)
	if _, ok := prstate.OpenClaim(settled, 2, core.LegReview); ok {
		t.Error("a completed leg left an open claim")
	}
}

// A moved revision is stale whatever the clock says, and the message names both
// abbreviated SHAs (lib/state.sh:325-331).
func TestClaimIsStaleOnAMovedRevision(t *testing.T) {
	claim := mustMarkers(t, `[{"leg":"review","pass":1,"state":"started","ts":1000,"head_sha":"aaaaaaa111"}]`)[0]
	reason, stale := prstate.ClaimIsStale(claim, "bbbbbbb222", time.Unix(1000, 0), prstate.DefaultClaimWindow)
	if !stale {
		t.Fatal("a moved revision did not read as stale")
	}
	want := "it started against aaaaaaa and the pull request is now at bbbbbbb"
	if reason != want {
		t.Errorf("got %q want %q", reason, want)
	}
}

// Past the window is stale, and the message counts whole minutes
// (lib/state.sh:332-336).
func TestClaimIsStalePastTheWindow(t *testing.T) {
	claim := mustMarkers(t, `[{"leg":"review","pass":1,"state":"started","ts":1000,"head_sha":"aaa111"}]`)[0]
	now := time.Unix(1000+3601+59, 0)
	reason, stale := prstate.ClaimIsStale(claim, "aaa111", now, prstate.DefaultClaimWindow)
	if !stale {
		t.Fatal("a claim past the window did not read as stale")
	}
	want := "it was made 61 minutes ago, past the 60-minute window"
	if reason != want {
		t.Errorf("got %q want %q", reason, want)
	}
}

// Exactly at the window is not stale: the Bash comparison is strictly greater.
func TestClaimAtTheWindowIsNotStale(t *testing.T) {
	claim := mustMarkers(t, `[{"leg":"review","pass":1,"state":"started","ts":1000,"head_sha":"aaa111"}]`)[0]
	if _, stale := prstate.ClaimIsStale(claim, "aaa111", time.Unix(1000+3600, 0), prstate.DefaultClaimWindow); stale {
		t.Error("a claim exactly at the window read as stale")
	}
}

// A claim with no ts is never stale on the clock: `ts > 0` guards the
// comparison, so a marker written before the field existed is resumed.
func TestClaimWithNoTimestampIsNotStaleOnTheClock(t *testing.T) {
	claim := mustMarkers(t, `[{"leg":"review","pass":1,"state":"started","head_sha":"aaa111"}]`)[0]
	if _, stale := prstate.ClaimIsStale(claim, "aaa111", time.Unix(1<<32, 0), prstate.DefaultClaimWindow); stale {
		t.Error("a claim with no timestamp read as stale")
	}
}

// A claim with no head_sha is not stale on the revision: the Bash guard is
// `-n "$claim_sha"`.
func TestClaimWithNoHeadSHAIsNotStaleOnTheRevision(t *testing.T) {
	claim := mustMarkers(t, `[{"leg":"review","pass":1,"state":"started","ts":1000}]`)[0]
	if _, stale := prstate.ClaimIsStale(claim, "bbb222", time.Unix(1000, 0), prstate.DefaultClaimWindow); stale {
		t.Error("a claim with no head SHA read as stale")
	}
}

// The window default is the Bash default of 3600 seconds (lib/state.sh:323).
func TestDefaultClaimWindowIsOneHour(t *testing.T) {
	if prstate.DefaultClaimWindow != time.Hour {
		t.Errorf("got %v", prstate.DefaultClaimWindow)
	}
}

// Bash abbreviates with `${sha:0:7}`, which takes a shorter revision whole
// rather than refusing it (lib/state.sh:330).
func TestClaimStaleMessageTakesAShortRevisionWhole(t *testing.T) {
	claim := mustMarkers(t, `[{"leg":"review","pass":1,"state":"started","ts":1000,"head_sha":"abc"}]`)[0]
	reason, stale := prstate.ClaimIsStale(claim, "defg", time.Unix(1000, 0), prstate.DefaultClaimWindow)
	if !stale {
		t.Fatal("a moved revision did not read as stale")
	}
	if reason != "it started against abc and the pull request is now at defg" {
		t.Errorf("got %q", reason)
	}
}

// The revision test runs before the clock test, so a claim that is both moved
// and expired reports the revision (lib/state.sh:329-337).
func TestClaimReportsTheMovedRevisionBeforeTheClock(t *testing.T) {
	claim := mustMarkers(t, `[{"leg":"review","pass":1,"state":"started","ts":1000,"head_sha":"aaaaaaa111"}]`)[0]
	reason, stale := prstate.ClaimIsStale(claim, "bbbbbbb222", time.Unix(1<<32, 0), prstate.DefaultClaimWindow)
	if !stale || !strings.HasPrefix(reason, "it started against") {
		t.Errorf("got %q", reason)
	}
}

// A newest marker that is not started leaves no open claim, however many
// started markers sit under it: `last` runs before `select` (lib/state.sh:312).
func TestOpenClaimReadsTheNewestMarkerOnly(t *testing.T) {
	markers := mustMarkers(t, `[{"leg":"review","pass":1,"state":"started"},{"leg":"review","pass":1,"state":"declined"}]`)
	if _, ok := prstate.OpenClaim(markers, 1, core.LegReview); ok {
		t.Error("a declined newest marker left an open claim")
	}
}
