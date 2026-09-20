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

func fixtureGeneration(t *testing.T, form string) prstate.Generation {
	return storetest.FixtureGeneration(t, form)
}
