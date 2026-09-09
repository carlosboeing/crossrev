package resolve

import (
	"context"
	"strings"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/policy"
	"github.com/carlosboeing/crossrev/internal/prstate"
)

// resolveConvergence builds the one convergence input for a no-commit
// settle: the current complete generation at the pull request's base and
// head must exactly account for the required set, with no outstanding or
// unexamined record, a reported scope, and any repair delta confirmed.
//
// It re-reads coverage rather than trusting the marker a previous step
// wrote. A resolve run with no ledger store, no current generation, or no
// review coverage at this head refuses: a settle the coverage does not
// support stays awaiting-review, never converged.
// hasCoverage reports whether any coverage generation exists at this head:
// a coverage pass always leaves its initial outstanding generation, so
// absence proves no coverage pass ran here and the frozen-path settle
// keeps its legacy label. Corrupt state is never absence: it fails closed.
func (l *Leg) resolveConvergence(ctx context.Context, s *session) (policy.Convergence, bool) {
	var conv policy.Convergence
	store, ok := l.Forge.(prstate.LedgerStore)
	if !ok || store == nil {
		return conv, false
	}
	base, head := s.pr.BaseRefOid, s.pr.HeadRefOid
	comments, err := store.CoverageComments(ctx, s.repo, s.req.PR)
	if err != nil {
		return conv, true
	}
	gen, err := prstate.SelectGeneration(comments, s.author,
		core.RevisionPair{Base: base, Head: head}, core.FileEngineVersion)
	if err != nil {
		if isAbsence(err) {
			return conv, false
		}
		return conv, true
	}
	conv.LedgerCurrent = true
	required := map[string]bool{}
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
	conv.Required = len(gen.Paths)
	for _, path := range gen.Paths {
		required[path] = true
	}
	_ = required
	conv.ScopeReported = gen.ScopeReport.ExaminedScope != ""
	conv.ConfirmationRequired, conv.ConfirmationComplete = resolveConfirmation(s, head)
	return conv, true
}

// isAbsence reports the one SelectGeneration failure that is not a refusal:
// no complete generation exists at this revision pair.
func isAbsence(err error) bool {
	return err != nil && strings.Contains(err.Error(), "no complete generation at")
}

// resolveConfirmation reads the repair-delta obligation off the review
// marker under settle: a B/C pair naming this head means the repair was
// confirmed during review, so the settle inherits it complete. No pair
// means no repair was under confirmation.
func resolveConfirmation(s *session, head core.Revision) (required, complete bool) {
	base, ok := s.review.ConfirmationBaseSHA.Get()
	if !ok || base == "" {
		return false, false
	}
	confirmed, ok := s.review.ConfirmationHeadSHA.Get()
	if !ok || confirmed == "" {
		return true, false
	}
	if confirmed != head.SHA() {
		return true, false
	}
	return true, true
}
