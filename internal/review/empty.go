package review

import (
	"context"
	"fmt"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/policy"
	"github.com/carlosboeing/crossrev/internal/prstate"
	"github.com/carlosboeing/crossrev/internal/ui"
)

// emptyBlockedReason is the marker's reason for a pull request that changes
// no files. CrossRev writes it rather than a model, so `crossrev status` and
// every later reader get the same sentence on every run and every harness.
const emptyBlockedReason = "the pull request's head is identical to its base, so it changes no files and there is nothing to review"

// finishEmptyRun settles a pass whose pull request changes no files. No
// harness is invoked: there is no diff to send and no required file to
// account for, so the answer is CrossRev's to give rather than a model's.
//
// The pass records blocked, which is the existing terminal state for a pass
// that could not do its work, and the summary says so in words a reader can
// act on. It never records converged: policy.Converged refuses a required
// count of zero (internal/policy/convergence.go:50), and a green label on a
// pull request nothing reviewed is the failure the coverage engine exists to
// prevent.
func (l *Leg) finishEmptyRun(ctx context.Context, req Request, loaded Context, ad admission, cap int, claimID int64, marker prstate.Marker, out *Result) (Result, publishState) {
	marker.Verdict = prstate.Some(string(core.VerdictBlocked))
	marker.BlockedReason = prstate.Some(emptyBlockedReason)
	marker.DoneTS = prstate.Some(l.now().Unix())
	marker.Unanchored = prstate.Some(0)
	marker.State = core.PassComplete

	minFix := loaded.Config.Get(".policy.min_fix_severity")
	if minFix == "" {
		minFix = string(core.SeverityMedium)
	}
	summary := SummaryBody(nil, marker, RenderContext{
		Repo:      loaded.Repo.String(),
		PR:        req.PR,
		MinFix:    minFix,
		MaxPass:   cap,
		NoChanges: true,
		BaseRef:   loaded.PR.BaseRefName,
		HeadRef:   loaded.PR.HeadRefName,
	})

	// ui_say: the state, before the comment lands, so a failure below still
	// leaves the operator knowing why the leg stopped.
	out.Messages = append(out.Messages, ui.Say(fmt.Sprintf(
		"%s#%d changes no files — stopping without calling a reviewer", loaded.Repo, req.PR)))

	if err := l.editClaim(ctx, loaded.Repo, claimID, summary, marker); err != nil {
		out.Outcome = OutcomeError
		out.Err = err
		out.Marker = marker
		return *out, publishState{}
	}
	out.Marker = marker
	// The record is durable from here, so nothing below may rewrite it.
	state := publishState{settled: true}
	// ui_ok (lib/run.sh:1299).
	out.Messages = append(out.Messages, ui.OK("posted a summary comment"))

	warns, err := l.applyPassLabels(ctx, req, loaded, ad.pass, policy.PassHalted)
	out.Messages = append(out.Messages, warns...)
	if err != nil {
		out.Outcome = OutcomeError
		out.Err = err
		return *out, state
	}

	out.Outcome = OutcomeHalted
	out.Reason = emptyBlockedReason
	return *out, state
}
