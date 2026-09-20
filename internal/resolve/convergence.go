package resolve

import (
	"context"

	"github.com/carlosboeing/crossrev/internal/config"
	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/forge"
	"github.com/carlosboeing/crossrev/internal/policy"
	"github.com/carlosboeing/crossrev/internal/prstate"
)

// refLedgerSource is implemented by forge clients that can read coverage
// generations back from git refs.
type refLedgerSource interface {
	RefLedger(namespace string) prstate.LedgerStore
}

// resolveConvergence builds the one convergence input for a no-commit
// settle: the generation the review marker names must exactly account for
// the required set at the pull request's base and head, with no
// outstanding or unexamined record, a reported scope, and any repair delta
// confirmed.
//
// It re-reads coverage rather than trusting the marker a previous step
// wrote. The marker is asked first, before the store is called: no claim
// means no coverage pass ran and the frozen-path settle keeps its legacy
// label — the only source of that answer. A claim that is not a valid
// handle is corrupt state and refuses, never "no coverage". A marker
// carrying only the legacy coverage manifest id is the lost-ledger row:
// comment-era generations are never read again, so the settle re-reviews
// rather than keeping a green label it cannot back.
//
// A named generation that reads but is retired, lost, corrupt, or on an
// unreadable store refuses the same way: a settle the coverage does not
// support stays awaiting-review, never converged. The read-outcome table
// this routing transcribes is stated once, on coverageOutcome in
// internal/review/ledger.go.
func (l *Leg) resolveConvergence(ctx context.Context, s *session) (policy.Convergence, bool) {
	var conv policy.Convergence
	base, head := s.pr.BaseRefOid, s.pr.HeadRefOid
	h, claimed, err := s.review.CoverageHandle()
	if err != nil {
		return conv, true
	}
	if !claimed {
		if _, ok := s.review.CoverageManifestID.Get(); ok {
			return conv, true
		}
		return conv, false
	}
	store, ok := resolveStoreFor(l.Forge, s.cfg, h)
	if !ok {
		return conv, true
	}
	reviewer := s.cfg.Reviewers()[0]
	ref := prstate.SlotRef{Repo: s.repo, Number: s.req.PR, Slot: reviewer.ID}
	producer := prstate.Producer{Harness: reviewer.Harness, Model: reviewer.Model, Effort: reviewer.Effort, Endpoint: reviewer.Endpoint}
	gen, err := store.ReadGeneration(ctx, ref, h)
	if err != nil {
		return conv, true
	}
	if !prstate.GenerationCurrent(gen, core.RevisionPair{Base: base, Head: head}, core.FileEngineVersion, producer) {
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
	conv.Required = len(gen.Paths)
	conv.ScopeReported = gen.ScopeReport.ExaminedScope != ""
	conv.ConfirmationRequired, conv.ConfirmationComplete = resolveConfirmation(s, head)
	return conv, true
}

// resolveStoreFor answers the store the named handle reads through: the
// marker store for a marker handle, which carries its generation inline,
// and the ref store under the configured namespace for a ref handle. It
// reports false when the client cannot read what the marker names, which
// fails the settle closed.
func resolveStoreFor(client forge.Forge, cfg *config.Config, h prstate.Handle) (prstate.LedgerStore, bool) {
	if h.Location == prstate.HandleMarker {
		return prstate.NewMarkerStore(nil, cfg.Coverage().OnOverflow), true
	}
	src, ok := client.(refLedgerSource)
	if !ok || src == nil {
		return nil, false
	}
	store := src.RefLedger(cfg.Coverage().RefNamespace)
	if store == nil {
		return nil, false
	}
	return store, true
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
