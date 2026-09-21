package resolve

import (
	"context"
	"errors"
	"testing"

	"github.com/carlosboeing/crossrev/internal/config"
	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/policy"
	"github.com/carlosboeing/crossrev/internal/prstate"
	"github.com/carlosboeing/crossrev/internal/prstate/storetest"
)

// cutoverSession is the settle session the cutover tests read convergence
// with: the fixture pull request at the fixture revision pair, the default
// configuration, and the given review marker under settle.
func cutoverSession(t *testing.T, e *testEnv, review prstate.Marker) *session {
	t.Helper()
	cfg, err := config.Load(context.Background(), e.base,
		func(context.Context, core.Revision, string) ([]byte, config.FileStatus, error) {
			return nil, config.NotFound, nil
		})
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	return &session{
		req:    Request{PR: 42, Repo: e.slug, Trigger: TriggerHuman},
		repo:   e.slug,
		pr:     e.forge.pr,
		cfg:    cfg,
		author: "tester",
		pass:   1,
		review: review,
	}
}

// cutoverReviewMarker is the complete review marker under settle, carrying
// the given coverage fields at the fixture head.
func cutoverReviewMarker(t *testing.T, e *testEnv, mutate func(*prstate.Marker)) prstate.Marker {
	t.Helper()
	m := prstate.Marker{
		Version: core.MarkerVersion,
		Leg:     core.LegReview,
		Pass:    1,
		State:   core.PassComplete,
		TS:      e.now.Unix() - 60,
		RunID:   prstate.Some("review-run"),
		HeadSHA: prstate.Some(e.head.SHA()),
		Harness: prstate.Some("codex"),
		Verdict: prstate.Some("converged"),
	}
	if mutate != nil {
		mutate(&m)
	}
	return m
}

func cutoverNameLostCommit(m *prstate.Marker) {
	m.CoverageGen = prstate.Some(7)
	m.CoverageRef = prstate.Some("refs/crossrev/pr/42/reviewer1/coverage")
	m.CoverageCommit = prstate.Some("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	m.CoverageDegraded = prstate.Some(false)
}

func cutoverLeg(e *testEnv) *Leg {
	return &Leg{Forge: e.forge}
}

// TestResolveRefusesToKeepAConvergedLabelWhenTheLedgerIsLost pins the
// resolve reader's lost row: the marker names a commit whose objects are
// gone, so the settle is obliged and refuses green. Taking the frozen
// path here would keep a converged label on a pull request nobody fully
// reviewed.
func TestResolveRefusesToKeepAConvergedLabelWhenTheLedgerIsLost(t *testing.T) {
	e := setup(t)
	e.forge.ledger = storetest.NewFakeStore()
	review := cutoverReviewMarker(t, e, cutoverNameLostCommit)

	conv, obliged := cutoverLeg(e).resolveConvergence(context.Background(), cutoverSession(t, e, review))
	if !obliged {
		t.Fatal("a lost ledger took the frozen path and kept the converged label")
	}
	if policy.Converged(conv) {
		t.Fatal("a lost ledger reported green")
	}
}

// TestResolveKeepsTheLegacyLabelWhenNoCoveragePassRan pins the frozen row,
// unchanged by the cutover: a marker with no coverage claim means no
// coverage pass ran, so the settle keeps its legacy label.
func TestResolveKeepsTheLegacyLabelWhenNoCoveragePassRan(t *testing.T) {
	e := setup(t)
	e.forge.ledger = storetest.NewFakeStore()
	review := cutoverReviewMarker(t, e, nil)

	_, obliged := cutoverLeg(e).resolveConvergence(context.Background(), cutoverSession(t, e, review))
	if obliged {
		t.Fatal("a pull request with no coverage pass ran took the obliged path")
	}
}

// TestALegacyManifestIDClaimReReviews pins the legacy row: a marker
// carrying only the comment-era manifest id is the lost-ledger row.
// Comment-era generations are never read again, so the settle re-reviews
// rather than keeping a green label it cannot back.
func TestALegacyManifestIDClaimReReviews(t *testing.T) {
	e := setup(t)
	e.forge.ledger = storetest.NewFakeStore()
	review := cutoverReviewMarker(t, e, func(m *prstate.Marker) {
		m.CoverageManifestID = prstate.Some(int64(9001))
	})

	conv, obliged := cutoverLeg(e).resolveConvergence(context.Background(), cutoverSession(t, e, review))
	if !obliged {
		t.Fatal("a legacy manifest id claim took the frozen path and kept the converged label")
	}
	if policy.Converged(conv) {
		t.Fatal("a legacy manifest id claim reported green")
	}
}

// TestEveryReaderFailsClosedOnCorruptionAndOnAnUnreadableStore pins the
// resolve reader: a corrupt claim and an unreadable store refuse, never
// read as no coverage.
func TestEveryReaderFailsClosedOnCorruptionAndOnAnUnreadableStore(t *testing.T) {
	t.Run("a corrupt claim refuses", func(t *testing.T) {
		e := setup(t)
		e.forge.ledger = storetest.NewFakeStore()
		// A claim that is not a valid handle: a generation number with
		// no ref behind it. Corrupt state, never "no coverage".
		review := cutoverReviewMarker(t, e, func(m *prstate.Marker) {
			m.CoverageGen = prstate.Some(7)
		})

		conv, obliged := cutoverLeg(e).resolveConvergence(context.Background(), cutoverSession(t, e, review))
		if !obliged {
			t.Fatal("a corrupt claim took the frozen path")
		}
		if policy.Converged(conv) {
			t.Fatal("a corrupt claim reported green")
		}
	})

	t.Run("an unreadable store refuses", func(t *testing.T) {
		e := setup(t)
		store := storetest.NewFakeStore()
		store.SetUnreadable(errors.New("the store cannot be read"))
		e.forge.ledger = store
		review := cutoverReviewMarker(t, e, cutoverNameLostCommit)

		conv, obliged := cutoverLeg(e).resolveConvergence(context.Background(), cutoverSession(t, e, review))
		if !obliged {
			t.Fatal("an unreadable store took the frozen path")
		}
		if policy.Converged(conv) {
			t.Fatal("an unreadable store reported green")
		}
	})
}

// TestResolveSettlesAPassThatRanUnderAnOverride pins the settle half of
// the producer contract: the review converged under a harness override
// the configuration does not name, and the settle judges the pass by what
// it ran with rather than retiring its coverage.
func TestResolveSettlesAPassThatRanUnderAnOverride(t *testing.T) {
	e := setup(t)
	store := storetest.NewFakeStore()
	e.forge.ledger = store
	overridden := prstate.Producer{Harness: "opencode"}
	gen := prstate.Generation{
		Gen:      1,
		Revision: core.RevisionPair{Base: e.forge.pr.BaseRefOid, Head: e.forge.pr.HeadRefOid},
		Engine:   core.FileEngineVersion,
		Slot:     prstate.DefaultSlot,
		Producer: overridden,
		Form:     prstate.GenerationFull,
		Paths:    []string{"a.go"},
		Records: []prstate.Record{{
			Type:       prstate.CoverageRecordUnit,
			UnitID:     string(core.FileUnitID("a.go")),
			PathIndex:  0,
			Kind:       prstate.CoverageGranularityFile,
			Change:     string(core.ChangeModified),
			BodyDigest: core.BodyDigestHex([]byte("body of a.go")),
			Verdict:    prstate.Some("no_issue"),
		}},
		ScopeReport: prstate.ScopeReport{ExaminedScope: "read the batch"},
	}
	handle, err := store.PublishGeneration(context.Background(),
		prstate.SlotRef{Repo: e.slug, Number: 42, Slot: prstate.DefaultSlot}, prstate.Handle{}, gen)
	if err != nil {
		t.Fatalf("PublishGeneration: %v", err)
	}
	review := cutoverReviewMarker(t, e, func(m *prstate.Marker) {
		m.Harness = prstate.Some("opencode")
		m.Model = prstate.Null[string]()
		m.RecordCoverage(handle)
	})

	conv, obliged := cutoverLeg(e).resolveConvergence(context.Background(), cutoverSession(t, e, review))
	if !obliged {
		t.Fatal("a current complete generation took the frozen path")
	}
	if !policy.Converged(conv) {
		t.Fatal("the settle retired coverage the review converged under an override")
	}
}
