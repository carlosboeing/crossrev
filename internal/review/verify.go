package review

import (
	"context"
	"fmt"
	"slices"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/policy"
	"github.com/carlosboeing/crossrev/internal/ui"
	"github.com/carlosboeing/crossrev/internal/verify"
)

// waitInterval is the re-read cadence while required checks are outstanding.
// Thirty seconds against a ten-minute default wait is twenty re-reads, and
// three reads a tick — labels, head, evidence — is cheap against the API.
const waitInterval = verify.WaitInterval

// verificationEvidence reads the required-check evidence once for head. With
// no required checks it answers none_required without touching the forge, so
// an unconfigured gate costs no call and changes no bytes.
func (l *Leg) verificationEvidence(ctx context.Context, loaded Context, settings legSettings, head core.Revision) verify.Evidence {
	if len(settings.requiredChecks) == 0 {
		return verify.Evidence{State: verify.None}
	}
	runs, err := l.Forge.CheckRuns(ctx, loaded.Repo, head)
	return verify.Evaluate(settings.requiredChecks, runs, err)
}

// waitForVerification re-reads pending or missing evidence until it turns
// terminal, the wait runs out, or an early stop fires: crossrev/stop, a head
// change, or cancellation. It answers the last evidence read, which the
// caller converges or halts on like any other.
//
// The clock and the sleep are both the leg's, so a test advances time
// without waiting out thirty seconds a tick.
func (l *Leg) waitForVerification(ctx context.Context, req Request, loaded Context, settings legSettings, head core.Revision, current verify.Evidence) verify.Evidence {
	return verify.Wait(ctx, settings.checkWait, current, l.now, l.sleep,
		func() bool {
			return slices.Contains(l.Forge.PullRequestLabels(ctx, loaded.Repo, req.PR), policy.LabelStop)
		},
		func() bool {
			pr, err := l.Forge.PullRequest(ctx, loaded.Repo, req.PR)
			return err == nil && pr.HeadRefOid.SHA() != head.SHA()
		},
		func() verify.Evidence { return l.verificationEvidence(ctx, loaded, settings, head) })
}

// verificationDebt names the blocking evidence for a halted pass: the halt
// word, then what blocks. A failure names each failed check with its
// conclusion and run URL; pending and missing name each check still
// outstanding; an unreadable enumeration names why it could not be trusted.
func verificationDebt(ev verify.Evidence) string {
	return verify.Debt(ev)
}

// verificationHaltLines is what the operator reads when required checks halt
// a pass: the debt and the next step. Restart clears the halt and re-applies
// the awaiting label, so the checks report first and the re-driven pass
// judges them fresh.
func verificationHaltLines(pr int, debt string) []ui.Line {
	return []ui.Line{
		ui.Say("Required checks block convergence: " + debt + "."),
		ui.Say(fmt.Sprintf("When they have reported, run `crossrev restart --pr %d` to drive the pass again.", pr)),
	}
}
