package review

import (
	"context"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/policy"
	"github.com/carlosboeing/crossrev/internal/prstate"
)

// buildConvergence builds the one convergence input from the current head,
// the generation the marker names, the findings and the confirmation state.
// It re-reads coverage rather than trusting the label or marker a previous
// step wrote: a stale green can only come from current bytes.
//
// A nil scope means the frozen single-prompt path, which carries no coverage
// obligation: the second return is false and the caller keeps the legacy
// label rule. Every other failure — a store this leg cannot open, a corrupt
// claim, no claim at all, a lost, corrupt or unreadable ledger, a retired
// generation — answers the obligation unmet, never green.
func (l *Leg) buildConvergence(ctx context.Context, loaded Context, marker prstate.Marker, actionable int, producer prstate.Producer) (policy.Convergence, bool) {
	if loaded.Scope == nil {
		return policy.Convergence{}, false
	}
	scope := loaded.Scope
	var conv policy.Convergence
	conv.Required = len(scope.Required)
	conv.UnresolvedFixable = actionable
	store, _, err := l.ledgerFor(loaded)
	if err != nil {
		return conv, true
	}
	h, claimed, err := marker.CoverageHandle()
	if err != nil {
		// A claim that is not a valid handle is corrupt state and
		// fails closed, never "no coverage".
		return conv, true
	}
	if !claimed {
		// No claim means no generation backs this marker — or a legacy
		// manifest id whose comment is never read again, which is the
		// lost-ledger row. Either way the obligation is unmet.
		return conv, true
	}
	gen, err := store.ReadGeneration(ctx, slotRefFor(loaded), h)
	if lost, failClosed := coverageOutcome(err); lost || failClosed {
		// A lost ledger re-reviews, which from this gate reads as
		// unmet; corruption and transient failures fail closed the
		// same way. None of them is absence.
		return conv, true
	}
	if !prstate.GenerationCurrent(gen, core.RevisionPair{Base: scope.Base, Head: scope.Head}, scope.Engine, producer) {
		return conv, true
	}
	conv.LedgerCurrent = true
	for _, record := range gen.Records {
		switch record.Type {
		case prstate.CoverageRecordOutstanding:
			conv.Outstanding++
		case prstate.CoverageRecordUnit:
			disp, ok := record.Verdict.Get()
			if !ok || disp == "" {
				conv.Outstanding++
				continue
			}
			conv.Covered++
			if disp == "could_not_review" {
				conv.CouldNotReview++
			}
		}
	}
	conv.ScopeReported = gen.ScopeReport.ExaminedScope != ""
	pair := repairConfirmation(loaded.Markers, scope.Head)
	conv.ConfirmationRequired = pair.set
	if pair.set {
		base, _ := marker.ConfirmationBaseSHA.Get()
		head, _ := marker.ConfirmationHeadSHA.Get()
		conv.ConfirmationComplete = base == pair.base.SHA() && head == pair.head.SHA() && base != "" && head != ""
	}
	return conv, true
}
