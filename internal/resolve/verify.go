package resolve

import (
	"context"

	"github.com/carlosboeing/crossrev/internal/verify"
)

// settlementEvidence reads the required-check evidence once for the settle.
// With no required checks it answers none_required without touching the
// forge, so an unconfigured settle costs no call and changes no bytes. The
// resolve leg never waits: it judges what one read reports, and a gate that
// is still outstanding holds the settle off converged until the review leg
// looks again.
func (l *Leg) settlementEvidence(ctx context.Context, s *session) verify.Evidence {
	if len(s.settings.RequiredChecks) == 0 {
		return verify.Evidence{State: verify.None}
	}
	runs, err := l.Forge.CheckRuns(ctx, s.repo, s.pr.HeadRefOid)
	return verify.Evaluate(s.settings.RequiredChecks, runs, err)
}
