package review

import (
	"context"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/policy"
	"github.com/carlosboeing/crossrev/internal/prstate"
)

// buildConvergence builds the one convergence input from the current head,
// the current complete generation, the findings and the confirmation state.
// It re-reads coverage rather than trusting the label or marker a previous
// step wrote: a stale green can only come from current bytes.
//
// A nil scope means the frozen single-prompt path, which carries no coverage
// obligation: the second return is false and the caller keeps the legacy
// label rule.
func (l *Leg) buildConvergence(ctx context.Context, loaded Context, marker prstate.Marker, actionable int) (policy.Convergence, bool) {
	if loaded.Scope == nil {
		return policy.Convergence{}, false
	}
	scope := loaded.Scope
	var conv policy.Convergence
	conv.Required = len(scope.Required)
	conv.UnresolvedFixable = actionable
	store := ledgerStoreFor(l)
	if store == nil {
		return conv, true
	}
	comments, err := store.CoverageComments(ctx, loaded.Repo, loaded.PR.Number)
	if err != nil {
		return conv, true
	}
	gen, err := prstate.SelectGeneration(comments, loaded.Author,
		core.RevisionPair{Base: scope.Base, Head: scope.Head}, scope.Engine)
	if err != nil {
		return conv, true
	}
	conv.LedgerCurrent = true
	for _, record := range gen.Records {
		switch record.Type {
		case prstate.CoverageRecordOutstanding:
			conv.Outstanding++
		case prstate.CoverageRecordUnit:
			disp, ok := record.Disposition.Get()
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
