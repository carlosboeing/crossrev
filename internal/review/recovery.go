package review

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/harness"
	"github.com/carlosboeing/crossrev/internal/policy"
	"github.com/carlosboeing/crossrev/internal/prstate"
	"github.com/carlosboeing/crossrev/internal/ui"
)

func hasRecordedFindings(m prstate.Marker) bool {
	if !m.Verdict.Present() || m.Verdict.IsNull() || m.Verdict.Value() == "" {
		return false
	}
	if len(m.Findings) == 0 || string(m.Findings) == "null" || string(m.Findings) == "[]" {
		return false
	}
	return true
}

func resumeMessage(pass int, findings json.RawMessage) string {
	return fmt.Sprintf("Resuming pass %d — the previous attempt recorded %d finding(s).", pass, findingCount(findings))
}

func findingCount(raw json.RawMessage) int {
	if len(raw) == 0 || string(raw) == "null" {
		return 0
	}
	var arr []json.RawMessage
	if err := json.Unmarshal(raw, &arr); err != nil {
		return 0
	}
	return len(arr)
}

// reportFatal records a leg that died on the pull request it had claimed
// (_run_report_fatal at lib/run.sh:131-146, through
// _run_report_invoke_failure at lib/run.sh:725-769).
//
// The shell mounts this on the EXIT trap rather than on ui_die, because ui_die
// is not the only way a leg dies: an unguarded failure under `set -e` and a
// SIGKILL to a child both arrive there too, and all three leave the same
// misleading marker — `started`, with a null blocked_reason, which
// `crossrev status` reads as an abandoned leg it cannot explain. Here the
// deferred call in Run is that trap: every path out of the leg goes through it,
// because no package under internal/ exits.
//
// Guarded on the claim still being open. A leg that finished has written
// `complete`, and reporting a completed leg as blocked because something after
// it failed would replace an accurate record with a wrong one — the reason
// run_leg_settled clears the snapshot the moment the complete edit lands
// (lib/run.sh:160-163).
func (l *Leg) reportFatal(ctx context.Context, req Request, loaded Context, marker prstate.Marker, claimID int64, cause error) {
	if claimID == 0 || cause == nil {
		return
	}
	reason, _ := refusalHalves(cause)
	now := l.now().Unix()
	marker.State = core.PassComplete
	marker.DoneTS = prstate.Some(now)
	marker.Verdict = prstate.Some(string(core.VerdictBlocked))
	marker.BlockedReason = prstate.Some(reason)

	cap := 0
	minFix := ""
	if loaded.Config != nil {
		cap = atoi(loaded.Config.Get(".policy.max_passes_per_cycle"))
		minFix = loaded.Config.Get(".policy.min_fix_severity")
	}
	body := SummaryBody(parseFindings(marker.Findings), marker, RenderContext{
		Repo:    loaded.Repo.String(),
		PR:      req.PR,
		MinFix:  minFix,
		MaxPass: cap,
	})
	// Best effort on every write below, which is what `|| true` is at
	// lib/run.sh:750, :759 and :761: the harness error is the cause the
	// operator needs, and a failure to record it must not replace it.
	_ = l.editClaim(ctx, loaded.Repo, claimID, body, marker)
	_, _ = l.applyPassLabels(ctx, req, loaded, marker.Pass, policy.PassHalted)
}

// refusalHalves is the reason half of whatever refusal ended the leg, which is
// what ui_die puts in CROSSREV_DIE_REASON.
func refusalHalves(err error) (reason, action string) {
	var fatal *ui.FatalError
	if errors.As(err, &fatal) {
		return fatal.Reason, fatal.Action
	}
	var refusal *harness.Refusal
	if errors.As(err, &refusal) {
		return refusal.Reason, refusal.Action
	}
	return err.Error(), ""
}
