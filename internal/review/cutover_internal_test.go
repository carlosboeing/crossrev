package review

import (
	"context"
	"errors"
	"testing"

	"github.com/carlosboeing/crossrev/internal/config"
	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/forge"
	"github.com/carlosboeing/crossrev/internal/intel"
	"github.com/carlosboeing/crossrev/internal/policy"
	"github.com/carlosboeing/crossrev/internal/prstate"
	"github.com/carlosboeing/crossrev/internal/prstate/storetest"
)

// cutoverForge is the forge double the cutover reader tests drive the leg
// with. It answers the ledger store the fixture carries, the way the
// production client answers its ref store under the configured namespace.
type cutoverForge struct {
	forge.Forge
	store prstate.LedgerStore
}

// RefLedger answers the ledger store this fixture carries.
func (f *cutoverForge) RefLedger(string) prstate.LedgerStore { return f.store }

const (
	cutoverAuthor = "tester"
	cutoverBase   = "0913bf7b99dcecf746d0e6fcef5a9c1d64aaf3b0"
	cutoverHead   = "2c4a46cb321db01826d116b5ef2add6b0284d68c"
)

// cutoverProducer is the producer the cutover readers run as.
var cutoverProducer = prstate.Producer{Harness: "claude", Model: "reviewer-model"}

// errCutoverUnreadable is the transient store failure the fail-closed cases
// read: neither lost nor corrupt, so nothing may treat it as absence.
var errCutoverUnreadable = errors.New("cutover test: the store cannot be read")

// cutoverScope is one required file at the cutover revision pair.
func cutoverScope(t *testing.T) intel.Scope {
	t.Helper()
	base, err := core.NewRevision(cutoverBase)
	if err != nil {
		t.Fatal(err)
	}
	head, err := core.NewRevision(cutoverHead)
	if err != nil {
		t.Fatal(err)
	}
	return intel.Scope{
		Base:   base,
		Head:   head,
		Engine: core.FileEngineVersion,
		Required: []intel.FileUnit{{
			ID:              core.FileUnitID("a.go"),
			Path:            "a.go",
			Change:          core.ChangeModified,
			ContentRevision: head,
		}},
	}
}

// cutoverLoaded is the leg context the reader tests load the leg with: the
// default configuration over the cutover scope.
func cutoverLoaded(t *testing.T, scope intel.Scope) Context {
	t.Helper()
	cfg, err := config.Load(context.Background(), scope.Base,
		func(context.Context, core.Revision, string) ([]byte, config.FileStatus, error) {
			return nil, config.NotFound, nil
		})
	if err != nil {
		t.Fatal(err)
	}
	slug, err := core.ParseSlug("acme/widget")
	if err != nil {
		t.Fatal(err)
	}
	return Context{
		Repo:   slug,
		PR:     forge.PullRequest{Number: 42, BaseRefOid: scope.Base, HeadRefOid: scope.Head},
		Config: cfg,
		Author: cutoverAuthor,
		Scope:  &scope,
	}
}

// cutoverMarkerNaming returns a pass marker naming the given commit through
// a valid handle: the shape a post-cutover pass leaves behind.
func cutoverMarkerNaming(commit string) prstate.Marker {
	return prstate.Marker{
		Version:          core.MarkerVersion,
		Leg:              core.LegReview,
		Pass:             1,
		State:            core.PassStarted,
		CoverageGen:      prstate.Some(7),
		CoverageRef:      prstate.Some("refs/crossrev/pr/42/reviewer1/coverage"),
		CoverageCommit:   prstate.Some(commit),
		CoverageDegraded: prstate.Some(false),
	}
}

// cutoverPublish writes the given generation to the store and answers the
// marker naming it, the way a pass leaves its checkpoint behind.
func cutoverPublish(t *testing.T, ctx context.Context, loaded Context, store prstate.LedgerStore, gen prstate.Generation) prstate.Marker {
	t.Helper()
	handle, err := store.PublishGeneration(ctx, slotRefFor(loaded), prstate.Handle{}, gen)
	if err != nil {
		t.Fatalf("PublishGeneration: %v", err)
	}
	var marker prstate.Marker
	marker.RecordCoverage(handle)
	return marker
}

// cutoverGeneration is a complete two-file generation at the scope's
// revision pair under the cutover producer: both files judged, scope
// reported, so the readers converge on it.
func cutoverGeneration(t *testing.T, form string, scope intel.Scope) prstate.Generation {
	t.Helper()
	gen := storetest.FixtureGeneration(t, form)
	gen.Revision = core.RevisionPair{Base: scope.Base, Head: scope.Head}
	gen.Engine = scope.Engine
	gen.Producer = cutoverProducer
	return gen
}

// TestReviewRefusesGreenWhenTheLedgerIsLost pins the review reader's lost
// row: the marker names a commit whose objects are gone, so convergence
// refuses green.
func TestReviewRefusesGreenWhenTheLedgerIsLost(t *testing.T) {
	ctx := context.Background()
	scope := cutoverScope(t)
	loaded := cutoverLoaded(t, scope)
	leg := &Leg{Forge: &cutoverForge{store: storetest.NewFakeStore()}}
	marker := cutoverMarkerNaming("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")

	conv, obliged := leg.buildConvergence(ctx, loaded, marker, 0, cutoverProducer)
	if !obliged {
		t.Fatal("a lost ledger took the frozen path")
	}
	if policy.Converged(conv) {
		t.Fatal("a lost ledger reported green")
	}
}

// TestResumptionRetiresAGenerationWhoseProducerChanged pins that resumption
// checks the producer, not only the revision pair: the revision guard
// passes and only this one refuses. It must hold for the compact form
// too, where the envelope is the only thing carrying the tuple.
func TestResumptionRetiresAGenerationWhoseProducerChanged(t *testing.T) {
	for _, form := range []string{prstate.GenerationFull, prstate.GenerationCompact} {
		t.Run(form, func(t *testing.T) {
			ctx := context.Background()
			scope := cutoverScope(t)
			loaded := cutoverLoaded(t, scope)
			store := storetest.NewFakeStore()
			leg := &Leg{Forge: &cutoverForge{store: store}}
			published := cutoverGeneration(t, form, scope)
			marker := cutoverPublish(t, ctx, loaded, store, published)
			producer := cutoverProducer
			producer.Model = "opus-next" // same revision pair, different producer

			gen, err := leg.currentGeneration(ctx, loaded, store, marker, scope, producer)
			if err != nil {
				t.Fatal(err)
			}
			if len(gen.Records) != 0 {
				t.Fatalf("%s: a generation from another producer was reused", form)
			}
		})
	}
}

// TestEveryReaderFailsClosedOnCorruptionAndOnAnUnreadableStore pins both
// review readers: a corrupt claim and an unreadable store refuse, never
// read as no coverage.
func TestEveryReaderFailsClosedOnCorruptionAndOnAnUnreadableStore(t *testing.T) {
	// A claim that is not a valid handle: a generation number with no
	// ref behind it. Corrupt state, never "no coverage".
	corrupt := prstate.Marker{CoverageGen: prstate.Some(7)}

	t.Run("resumption refuses a corrupt claim", func(t *testing.T) {
		ctx := context.Background()
		scope := cutoverScope(t)
		loaded := cutoverLoaded(t, scope)
		store := storetest.NewFakeStore()
		leg := &Leg{Forge: &cutoverForge{store: store}}

		_, err := leg.currentGeneration(ctx, loaded, store, corrupt, scope, cutoverProducer)
		if err == nil {
			t.Fatal("a corrupt claim read as no coverage")
		}
	})

	t.Run("resumption fails closed on an unreadable store", func(t *testing.T) {
		ctx := context.Background()
		scope := cutoverScope(t)
		loaded := cutoverLoaded(t, scope)
		store := storetest.NewFakeStore()
		store.SetUnreadable(errCutoverUnreadable)
		leg := &Leg{Forge: &cutoverForge{store: store}}
		marker := cutoverMarkerNaming("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")

		_, err := leg.currentGeneration(ctx, loaded, store, marker, scope, cutoverProducer)
		if err == nil {
			t.Fatal("an unreadable store read as no coverage")
		}
	})

	t.Run("convergence refuses a corrupt claim", func(t *testing.T) {
		ctx := context.Background()
		scope := cutoverScope(t)
		loaded := cutoverLoaded(t, scope)
		leg := &Leg{Forge: &cutoverForge{store: storetest.NewFakeStore()}}

		conv, _ := leg.buildConvergence(ctx, loaded, corrupt, 0, cutoverProducer)
		if policy.Converged(conv) {
			t.Fatal("a corrupt claim reported green")
		}
	})

	t.Run("convergence fails closed on an unreadable store", func(t *testing.T) {
		ctx := context.Background()
		scope := cutoverScope(t)
		loaded := cutoverLoaded(t, scope)
		store := storetest.NewFakeStore()
		store.SetUnreadable(errCutoverUnreadable)
		leg := &Leg{Forge: &cutoverForge{store: store}}
		marker := cutoverMarkerNaming("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")

		conv, obliged := leg.buildConvergence(ctx, loaded, marker, 0, cutoverProducer)
		if !obliged {
			t.Fatal("an unreadable store took the frozen path")
		}
		if policy.Converged(conv) {
			t.Fatal("an unreadable store reported green")
		}
	})
}

// TestSubstitutedRefCannotMoveTheConvergenceAnswer is the end-to-end
// substitution case: the marker names commit A while the store also holds
// a well-formed generation at commit B written by another author, and the
// leg's convergence answer is computed from A. The store-level version is
// the ref store's own contract; this one drives the leg.
func TestSubstitutedRefCannotMoveTheConvergenceAnswer(t *testing.T) {
	ctx := context.Background()
	scope := cutoverScope(t)
	head := scope.Head
	scope.Required = append(scope.Required, intel.FileUnit{
		ID:              core.FileUnitID("b.go"),
		Path:            "b.go",
		Change:          core.ChangeModified,
		ContentRevision: head,
	})
	loaded := cutoverLoaded(t, scope)
	store := storetest.NewFakeStore()
	leg := &Leg{Forge: &cutoverForge{store: store}}
	// The substituted generation at commit B: complete, but covering only
	// one file, so an answer computed from it differs observably from one
	// computed from the marker's commit.
	substitute := cutoverGeneration(t, prstate.GenerationFull, scope)
	substitute.Records = substitute.Records[:1]
	if _, err := store.PublishGeneration(ctx, slotRefFor(loaded), prstate.Handle{}, substitute); err != nil {
		t.Fatal(err)
	}
	// The marker names commit A, whose generation covers both files.
	marker := cutoverPublish(t, ctx, loaded, store, cutoverGeneration(t, prstate.GenerationFull, scope))

	conv, obliged := leg.buildConvergence(ctx, loaded, marker, 0, cutoverProducer)
	if !obliged {
		t.Fatal("the substituted read took the frozen path")
	}
	if conv.Covered != 2 {
		t.Fatalf("covered = %d, want 2: the answer was computed from the substituted bytes, not the marker's commit", conv.Covered)
	}
}
