package review

import (
	"context"
	"errors"
	"strings"

	"github.com/carlosboeing/crossrev/internal/forge"
	"github.com/carlosboeing/crossrev/internal/prstate"
)

// ledgerSelection is the store in force for a pass and why. The store is
// recorded in the pass marker on every publication — a ref name, or
// "marker" — so a later pass reads its predecessor's store rather than
// re-deriving it.
type ledgerSelection struct {
	Store  string // "refs" | "marker"
	Reason string // "configured" | "ref_write_refused" | "ruleset"
}

const (
	ledgerStoreRefs   = "refs"
	ledgerStoreMarker = "marker"

	ledgerReasonConfigured      = "configured"
	ledgerReasonRefWriteRefused = "ref_write_refused"
	ledgerReasonRuleset         = "ruleset"
)

// refLedgerSource is implemented by forge clients that can write coverage
// generations to git refs.
type refLedgerSource interface {
	RefLedger(namespace string) prstate.LedgerStore
}

// ledgerFor returns the store this pass writes through and the selection it
// recorded. Availability is determined by attempting the write, not by
// inferring from configuration: a token's stated permissions and its actual
// ones diverge often enough that guessing is worse than asking. Under `auto`
// a refused ref write falls back to the marker store for the rest of the pass
// and records why; under `refs` it fails loudly, because the operator asked
// for the guarantee and a silent downgrade would break it.
func (l *Leg) ledgerFor(loaded Context) (prstate.LedgerStore, ledgerSelection, error) {
	cov := loaded.Config.Coverage()
	// The run log's Publish is the same filter the comment writer applies
	// on the way out, so the marker store digests the bytes that persist.
	// Nil-safe: a leg with no log still redacts, and only loses the event
	// line about it.
	marker := prstate.NewMarkerStore(nil, cov.OnOverflow, l.Log.Publish)
	switch cov.Store {
	case ledgerStoreMarker:
		// Never touch refs at all, whatever the token permits.
		return marker, ledgerSelection{Store: ledgerStoreMarker, Reason: ledgerReasonConfigured}, nil
	case ledgerStoreRefs:
		refs, ok := refLedgerFor(l.Forge, cov.RefNamespace)
		if !ok {
			return nil, ledgerSelection{}, errors.New("coverage.store is refs but this client cannot write git refs")
		}
		return refs, ledgerSelection{Store: ledgerStoreRefs, Reason: ledgerReasonConfigured}, nil
	default:
		// auto: refs when available, marker when not. The wrapper
		// attempts the ref write and falls back on refusal.
		refs, ok := refLedgerFor(l.Forge, cov.RefNamespace)
		if !ok {
			return marker, ledgerSelection{Store: ledgerStoreMarker, Reason: ledgerReasonRefWriteRefused}, nil
		}
		selection := ledgerSelection{Store: ledgerStoreRefs, Reason: ledgerReasonConfigured}
		return &autoLedger{refs: refs, marker: marker, selection: selection}, selection, nil
	}
}

// refLedgerFor answers the ref store under the configured namespace, or
// false when the client cannot write refs.
func refLedgerFor(client forge.Forge, namespace string) (prstate.LedgerStore, bool) {
	src, ok := client.(refLedgerSource)
	if !ok || src == nil {
		return nil, false
	}
	store := src.RefLedger(namespace)
	if store == nil {
		return nil, false
	}
	return store, true
}

// autoLedger is the `auto` store: it writes through refs until a write is
// refused, then through the marker store for the rest of the pass. Reads
// are location-addressed — a marker handle reads through the marker store
// and a ref handle through the ref store — so a pass that fell back keeps
// reading both halves of its own history.
type autoLedger struct {
	refs      prstate.LedgerStore
	marker    prstate.LedgerStore
	selection ledgerSelection
}

var _ prstate.LedgerStore = (*autoLedger)(nil)

// Selection reports the store currently in force, so the pass can say
// which half of its history a publication landed in.
func (a *autoLedger) Selection() ledgerSelection {
	return a.selection
}

func (a *autoLedger) ReadGeneration(ctx context.Context, ref prstate.SlotRef, handle prstate.Handle) (prstate.Generation, error) {
	if handle.Location == prstate.HandleMarker {
		return a.marker.ReadGeneration(ctx, ref, handle)
	}
	return a.refs.ReadGeneration(ctx, ref, handle)
}

func (a *autoLedger) PublishGeneration(ctx context.Context, ref prstate.SlotRef, parent prstate.Handle, candidate prstate.Generation) (prstate.Handle, error) {
	if a.selection.Store == ledgerStoreMarker {
		return a.marker.PublishGeneration(ctx, ref, parent, candidate)
	}
	handle, err := a.refs.PublishGeneration(ctx, ref, parent, candidate)
	if err == nil {
		return handle, nil
	}
	if !isRefWriteRefused(err) {
		return prstate.Handle{}, err
	}
	a.selection = ledgerSelection{Store: ledgerStoreMarker, Reason: refusalReason(err)}
	return a.marker.PublishGeneration(ctx, ref, parent, candidate)
}

// isRefWriteRefused reports whether the error is the typed refusal the ref
// store reports permission and policy denials with, so `auto` can fall
// back on them and fail loudly on everything else. A transient failure is
// not a refusal: falling back on a network blip would land a generation
// in the marker for no reason the operator can see.
func isRefWriteRefused(err error) bool {
	var refused *prstate.RefWriteRefused
	return errors.As(err, &refused)
}

// refusalReason names why the ref write was refused. A ruleset denial is
// the only refusal with its own cause in the error text; everything else
// is a refused write with no further detail.
func refusalReason(err error) string {
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "ruleset") {
		return ledgerReasonRuleset
	}
	return ledgerReasonRefWriteRefused
}

// parentFor answers what a publication parents on: the slot's current trusted
// checkpoint, no parent when the marker names one whose objects are gone, and
// a refusal for any other read failure — a transient error must fail closed
// rather than silently rooting a new chain and orphaning the real one.
//
// The parent is the handle the marker carries as it stands, never the ref's
// current target: another writer can move the ref before publication and
// have their commit adopted, with the resulting tip landing in a trusted
// marker. Currency is not consulted here — a retired generation is still
// the chain's tip, and the next generation parents onto it whatever
// revision or producer retired it.
func (l *Leg) parentFor(ctx context.Context, store prstate.LedgerStore, ref prstate.SlotRef, m prstate.Marker) (prstate.Handle, error) {
	h, claimed, err := m.CoverageHandle()
	if err != nil {
		return prstate.Handle{}, err
	}
	if !claimed {
		return prstate.Handle{}, nil
	}
	if _, err := store.ReadGeneration(ctx, ref, h); err != nil {
		if lost, _ := coverageOutcome(err); lost {
			return prstate.Handle{}, nil
		}
		return prstate.Handle{}, err
	}
	return h, nil
}

// coverageOutcome is the one place the read outcomes are told apart, so an
// eighth row cannot be added to three readers and missed on the fourth.
//
// The table, asked in order — was coverage ever claimed (the marker, before
// the store is called), can the claim be read (the store), is it still
// usable (GenerationCurrent, after the read):
//
//	No coverage claim          | Nothing                   | No coverage pass ran | Frozen path, keep the legacy label
//	A claim, not a valid handle| —                         | Corrupt state        | Fail closed. Never "no coverage"
//	Names a commit             | That commit, ref present  | Normal               | Read it
//	Names a commit             | That commit, ref gone     | Intact               | Read it, and re-create the ref
//	Names a commit             | Nothing readable          | The ledger was lost  | Treat as no coverage. Re-review. Never keep converged
//	Names a commit             | Objects that do not verify| Corrupt              | Fail closed
//	Names a commit             | Store unreadable          | Transient failure    | Fail closed. Not absence
//	Read, at another revision,
//	engine or producer         | —                         | Retired              | Review from zero. Not an error, not absence
//
// A marker carrying only the legacy coverage_manifest_id is the lost-ledger
// row: comment-era generations are never read again.
func coverageOutcome(err error) (lost, failClosed bool) {
	if err == nil {
		return false, false
	}
	if errors.Is(err, prstate.ErrLedgerLost) {
		return true, false
	}
	return false, true
}

// producerOf reads the producer off the leg's resolved settings — what the
// leg actually ran with, not what the configuration text says, for the
// reason ConfiguredDifference reads what each leg ran.
func producerOf(s legSettings) prstate.Producer {
	return prstate.Producer{Harness: s.harness, Model: s.model, Effort: s.effort, Endpoint: s.endpoint}
}

// slotRefFor addresses this pull request's ledger slot: the reviewer's
// canonical slot id, never derived from list position.
func slotRefFor(loaded Context) prstate.SlotRef {
	return prstate.SlotRef{Repo: loaded.Repo, Number: loaded.PR.Number, Slot: loaded.Config.Reviewers()[0].ID}
}
