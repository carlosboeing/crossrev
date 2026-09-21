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

// cutoverCheckpoint is a completed review marker for the given pass naming
// the given commit: the shape a settled pass leaves its successor.
func cutoverCheckpoint(pass int, commit string) prstate.Marker {
	marker := cutoverMarkerNaming(commit)
	marker.Pass = pass
	marker.State = core.PassComplete
	return marker
}

// TestMarkerForPassSeedsANewPassFromTheLatestCheckpoint pins the new-pass
// ancestry: a pass with no marker of its own starts from the slot's latest
// coverage checkpoint, so its first publication parents onto the previous
// tip instead of rooting an unrelated chain. The seed is coverage only —
// no findings carry across passes.
func TestMarkerForPassSeedsANewPassFromTheLatestCheckpoint(t *testing.T) {
	commit1 := "1111111111111111111111111111111111111111"
	commit2 := "2222222222222222222222222222222222222222"
	corrupt := prstate.Marker{Version: core.MarkerVersion, Leg: core.LegReview, Pass: 1, State: core.PassComplete, CoverageGen: prstate.Some(7)}
	declined := prstate.Marker{Version: core.MarkerVersion, Leg: core.LegReview, Pass: 2, State: core.PassDeclined}
	ownBlank := prstate.Marker{Version: core.MarkerVersion, Leg: core.LegReview, Pass: 2, State: core.PassStarted}
	ownCorrupt := prstate.Marker{Version: core.MarkerVersion, Leg: core.LegReview, Pass: 2, State: core.PassStarted, CoverageGen: prstate.Some(7)}

	cases := []struct {
		name     string
		markers  []prstate.Marker
		pass     int
		wantGen  int
		wantRef  string
		wantErr  bool
		wantPass int
	}{
		{name: "first pass with no markers starts blank", markers: nil, pass: 1, wantGen: 0, wantPass: 1},
		{name: "own marker with a claim is kept", markers: []prstate.Marker{cutoverCheckpoint(2, commit1)}, pass: 2, wantGen: 7, wantRef: commit1, wantPass: 2},
		{name: "new pass seeds from the previous checkpoint", markers: []prstate.Marker{cutoverCheckpoint(1, commit1)}, pass: 2, wantGen: 7, wantRef: commit1, wantPass: 2},
		{name: "newest checkpoint wins", markers: []prstate.Marker{cutoverCheckpoint(1, commit1), cutoverCheckpoint(2, commit2)}, pass: 3, wantGen: 7, wantRef: commit2, wantPass: 3},
		{name: "claimless markers are skipped", markers: []prstate.Marker{cutoverCheckpoint(1, commit1), declined}, pass: 3, wantGen: 7, wantRef: commit1, wantPass: 3},
		{name: "own claimless marker is seeded", markers: []prstate.Marker{cutoverCheckpoint(1, commit1), ownBlank}, pass: 2, wantGen: 7, wantRef: commit1, wantPass: 2},
		{name: "corrupt checkpoint refuses", markers: []prstate.Marker{corrupt}, pass: 2, wantErr: true},
		{name: "own corrupt claim refuses", markers: []prstate.Marker{cutoverCheckpoint(1, commit1), ownCorrupt}, pass: 2, wantErr: true},
		{name: "older corruption is not the new pass's business", markers: []prstate.Marker{corrupt, cutoverCheckpoint(2, commit2)}, pass: 3, wantGen: 7, wantRef: commit2, wantPass: 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			marker, err := markerForPass(tc.markers, tc.pass)
			if tc.wantErr {
				if err == nil {
					t.Fatal("corrupt coverage did not refuse the pass")
				}
				return
			}
			if err != nil {
				t.Fatalf("markerForPass: %v", err)
			}
			if marker.Pass != tc.wantPass {
				t.Fatalf("marker pass = %d, want %d", marker.Pass, tc.wantPass)
			}
			h, claimed, err := marker.CoverageHandle()
			if err != nil {
				t.Fatalf("seeded claim does not parse: %v", err)
			}
			if tc.wantGen == 0 {
				if claimed {
					t.Fatalf("blank pass claims coverage %+v; nothing seeded it", h)
				}
				return
			}
			if !claimed {
				t.Fatal("the pass started with no coverage claim; it would root a new chain")
			}
			if h.Gen != tc.wantGen || h.Commit != tc.wantRef {
				t.Fatalf("seeded handle = gen %d commit %s, want gen %d commit %s", h.Gen, h.Commit, tc.wantGen, tc.wantRef)
			}
			if len(marker.Findings) != 0 {
				t.Fatal("the seed carried findings across passes; ancestry is coverage only")
			}
		})
	}
}

// TestAutoFallsBackOnRefusal pins the auto path: a permission denial on
// the ref half — including one from the first blob write, before the code
// reaches /git/refs — moves the pass to the marker store instead of
// halting, and records why. A ruleset denial records the policy reason
// rather than the bare one.
func TestAutoFallsBackOnRefusal(t *testing.T) {
	cases := []struct {
		name   string
		denied *prstate.RefWriteRefused
		reason string
	}{
		{
			name:   "scope denial falls back",
			denied: &prstate.RefWriteRefused{Op: "creating manifest blob", Err: errors.New("creating manifest blob: gh exited 1")},
			reason: ledgerReasonRefWriteRefused,
		},
		{
			name:   "ruleset denial records the policy reason",
			denied: &prstate.RefWriteRefused{Op: "updating ref", Ruleset: true, Err: errors.New("updating ref: gh exited 1")},
			reason: ledgerReasonRuleset,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			scope := cutoverScope(t)
			loaded := cutoverLoaded(t, scope)
			refs := storetest.NewFakeStore()
			refs.SetUnreadable(tc.denied)
			leg := &Leg{Forge: &cutoverForge{store: refs}}

			store, selection, err := leg.ledgerFor(loaded)
			if err != nil {
				t.Fatalf("ledgerFor: %v", err)
			}
			if selection.Store != ledgerStoreRefs {
				t.Fatalf("initial selection = %q, want refs (auto attempts the ref write first)", selection.Store)
			}
			auto, ok := store.(*autoLedger)
			if !ok {
				t.Fatalf("store is %T, want the auto wrapper", store)
			}
			handle, err := store.PublishGeneration(ctx, slotRefFor(loaded), prstate.Handle{}, cutoverGeneration(t, prstate.GenerationFull, scope))
			if err != nil {
				t.Fatalf("a denied ref write halted the pass instead of falling back: %v", err)
			}
			if handle.Location != prstate.HandleMarker {
				t.Fatalf("fallback handle location = %q, want the marker store", handle.Location)
			}
			if _, ok := handle.Payload.Get(); !ok {
				t.Fatal("the fallback handle carries no payload")
			}
			if got := auto.Selection(); got.Store != ledgerStoreMarker || got.Reason != tc.reason {
				t.Fatalf("selection after fallback = %+v, want marker/%s", got, tc.reason)
			}
		})
	}
}

// TestAutoFailsLoudOnTransientFailure pins the other half of the
// contract: an ordinary error is not a refusal, so the pass fails with
// the ref selection still in force rather than landing a generation in
// the marker over a network blip.
func TestAutoFailsLoudOnTransientFailure(t *testing.T) {
	ctx := context.Background()
	scope := cutoverScope(t)
	loaded := cutoverLoaded(t, scope)
	refs := storetest.NewFakeStore()
	refs.SetUnreadable(errors.New("updating ref: gh exited 1"))
	leg := &Leg{Forge: &cutoverForge{store: refs}}

	store, _, err := leg.ledgerFor(loaded)
	if err != nil {
		t.Fatalf("ledgerFor: %v", err)
	}
	auto := store.(*autoLedger)
	if _, err := store.PublishGeneration(ctx, slotRefFor(loaded), prstate.Handle{}, cutoverGeneration(t, prstate.GenerationFull, scope)); err == nil {
		t.Fatal("a transient ref failure fell back instead of failing loudly")
	}
	if got := auto.Selection(); got.Store != ledgerStoreRefs {
		t.Fatalf("selection after a transient failure = %+v, want the ref selection still in force", got)
	}
}
