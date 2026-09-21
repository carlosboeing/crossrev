package prstate_test

import (
	"encoding/json"
	"testing"

	"github.com/carlosboeing/crossrev/internal/prstate"
	"github.com/carlosboeing/crossrev/internal/prstate/storetest"
)

func TestAMalformedHandleIsCorruptAndNeverNoCoverage(t *testing.T) {
	const sha = "1111111111111111111111111111111111111111"
	for name, h := range map[string]prstate.Handle{
		"ref handle with an inline payload": {Gen: 1, Commit: sha, Location: "refs/crossrev/…", Payload: prstate.Some(json.RawMessage(`{}`))},
		"marker handle with a commit":       {Gen: 1, Commit: sha, Location: prstate.HandleMarker},
		"marker handle with no payload":     {Gen: 1, Location: prstate.HandleMarker},
		"ref handle with no commit":         {Gen: 1, Location: "refs/crossrev/…"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := h.Valid(); err == nil {
				t.Fatal("a malformed handle validated, so it could be read as no coverage")
			}
		})
	}
}

func TestGenerationCurrentRetiresOnProducerChange(t *testing.T) {
	gen := fixtureGeneration(t, prstate.GenerationCompact)
	moved := gen.Producer
	moved.Model = "opus-next"
	if !prstate.GenerationCurrent(gen, gen.Revision, gen.Engine, gen.Producer) {
		t.Fatal("an unchanged producer retired a generation")
	}
	if prstate.GenerationCurrent(gen, gen.Revision, gen.Engine, moved) {
		t.Fatal("a changed model reused a generation; the revision guard passes and only this one refuses")
	}
}

func TestFakeStoreContract(t *testing.T) {
	storetest.Contract(t, "refs", func(t *testing.T) prstate.LedgerStore {
		return storetest.NewFakeStore()
	})
	storetest.Contract(t, "marker", func(t *testing.T) prstate.LedgerStore {
		return storetest.NewFakeMarkerStore()
	})
}

func TestMarkerStoreMeetsTheContract(t *testing.T) {
	storetest.Contract(t, "marker", func(t *testing.T) prstate.LedgerStore {
		return prstate.NewMarkerStore(identityRender, prstate.OverflowDegrade, passthroughFilter)
	})
}

func identityRender(p json.RawMessage) (string, error) {
	return string(p), nil
}

func passthroughFilter(s string) (string, error) {
	return s, nil
}

// TestProducerFor pins where a pass's producer is read from: the marker
// the pass left, which records the resolved settings — including an
// override's wiped model and endpoint — rather than the configuration
// text. A marker that names no producer predates the claim fields and
// falls back to the configured reviewer.
func TestProducerFor(t *testing.T) {
	fallback := prstate.Producer{Harness: "claude", Model: "reviewer-model", Effort: "high", Endpoint: "vendor"}

	t.Run("a pass marker names what it ran with", func(t *testing.T) {
		marker := prstate.Marker{
			Harness:  prstate.Some("opencode"),
			Model:    prstate.Null[string](),
			Effort:   prstate.Some("medium"),
			Endpoint: prstate.Null[string](),
		}
		want := prstate.Producer{Harness: "opencode", Effort: "medium"}
		if got := prstate.ProducerFor(marker, fallback); got != want {
			t.Fatalf("ProducerFor = %+v, want %+v", got, want)
		}
	})

	t.Run("the wire format round-trips the producer", func(t *testing.T) {
		marker := prstate.Marker{
			Harness:  prstate.Some("opencode"),
			Model:    prstate.Null[string](),
			Effort:   prstate.Some("medium"),
			Endpoint: prstate.Null[string](),
		}
		raw, err := json.Marshal(marker)
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}
		var decoded prstate.Marker
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatalf("Unmarshal: %v", err)
		}
		want := prstate.Producer{Harness: "opencode", Effort: "medium"}
		if got := prstate.ProducerFor(decoded, fallback); got != want {
			t.Fatalf("ProducerFor after a round trip = %+v, want %+v", got, want)
		}
	})

	t.Run("a marker without a producer falls back to the configured reviewer", func(t *testing.T) {
		if got := prstate.ProducerFor(prstate.Marker{}, fallback); got != fallback {
			t.Fatalf("ProducerFor = %+v, want the fallback %+v", got, fallback)
		}
	})
}

func fixtureGeneration(t *testing.T, form string) prstate.Generation {
	return storetest.FixtureGeneration(t, form)
}
