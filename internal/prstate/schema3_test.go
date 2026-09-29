package prstate_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/prstate"
)

// The manifest carries the null reads envelope when the pass served
// nothing: the reads key is always present on a v3 manifest. It follows
// the verification envelope, and writers emit nothing else.
func TestSchemaV3ManifestCarriesNullReadsEnvelope(t *testing.T) {
	gen := validFixtureGeneration(t, prstate.GenerationFull)
	manifest, records, err := prstate.EncodeGenerationV3(gen)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if !strings.Contains(string(manifest), `"reads":null`) {
		t.Fatalf("manifest carries no null reads envelope:\n%s", manifest)
	}
	if _, err := prstate.DecodeGenerationV3(manifest, records); err != nil {
		t.Fatalf("decode: %v", err)
	}
}

// A populated reads envelope round-trips: legs that read through the
// served tool publish the envelope in the manifest, and the same schema
// reads it back. A malformed envelope is still refused.
func TestSchemaV3DecodesPopulatedReadsEnvelope(t *testing.T) {
	gen := validFixtureGeneration(t, prstate.GenerationFull)
	envelope := prstate.NewReadsEnvelope("served", "served", "", 2, 2, 512, 0, false)
	raw, err := json.Marshal(envelope)
	if err != nil {
		t.Fatalf("encode envelope: %v", err)
	}
	gen.Reads = prstate.Some(json.RawMessage(raw))
	manifest, records, err := prstate.EncodeGenerationV3(gen)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if !strings.Contains(string(manifest), `"reads":{"declared_mode":"served"`) {
		t.Fatalf("manifest carries no populated reads envelope:\n%s", manifest)
	}
	back, err := prstate.DecodeGenerationV3(manifest, records)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	backRaw, ok := back.Reads.Get()
	if !ok {
		t.Fatal("decoded generation carries no reads envelope")
	}
	backEnvelope, err := prstate.DecodeReadsEnvelope(backRaw)
	if err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if !reflect.DeepEqual(envelope, backEnvelope) {
		t.Fatalf("reads envelope does not round-trip\n want: %+v\n  got: %+v", envelope, backEnvelope)
	}
	malformed := strings.Replace(string(manifest), `"budget_exhausted":false`, `"budget_exhausted":"never"`, 1)
	if _, err := prstate.DecodeGenerationV3([]byte(malformed), records); err == nil {
		t.Fatal("manifest with a malformed reads envelope decoded")
	}
}

// Split-file ranges and parts round-trip through v3: the merged record of
// a file reviewed in three slices keeps the union of the slices' ranges
// and the part count beside the digest over their bytes.
func TestSuppliedRangesAndPartsRoundTripV3(t *testing.T) {
	gen := validFixtureGeneration(t, prstate.GenerationFull)
	supplied, ok := gen.Records[0].Supplied.Get()
	if !ok {
		t.Fatal("fixture record carries no supplied input")
	}
	supplied.Form = prstate.SuppliedFormHunksContext
	supplied.Ranges = core.SuppliedRanges{
		Base: []core.LineSpan{{Start: 1, End: 50}, {Start: 200, End: 230}},
		Head: []core.LineSpan{{Start: 1, End: 55}, {Start: 205, End: 240}},
	}
	supplied.Parts = 3
	gen.Records[0].Supplied = prstate.Some(supplied)
	manifest, records, err := prstate.EncodeGenerationV3(gen)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	back, err := prstate.DecodeGenerationV3(manifest, records)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !reflect.DeepEqual(gen, back) {
		t.Fatalf("split-file ranges and parts do not round-trip\n want: %+v\n  got: %+v", gen, back)
	}
}

// A v2 pair still decodes under v3, migrated: no reads envelope, no
// ranges, one part. The engine bump retires it right after — file-v2 never
// answers for hunk-v1 — so the migration only has to read well enough to
// retire, never well enough to reuse.
func TestSchemaV3DecodesV2Pair(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "v2_migration.json"))
	if err != nil {
		t.Fatalf("read v2 fixture: %v", err)
	}
	var pair struct {
		Manifest json.RawMessage `json:"manifest"`
		Records  json.RawMessage `json:"records"`
	}
	if err := json.Unmarshal(raw, &pair); err != nil {
		t.Fatalf("decode v2 fixture envelope: %v", err)
	}
	gen, err := prstate.DecodeGenerationV3(pair.Manifest, pair.Records)
	if err != nil {
		t.Fatalf("v2 pair refused: %v", err)
	}
	if gen.Engine != "file-v2" {
		t.Errorf("migrated engine = %q, want file-v2", gen.Engine)
	}
	supplied, ok := gen.Records[0].Supplied.Get()
	if !ok {
		t.Fatal("migrated unit record carries no supplied input")
	}
	if len(supplied.Ranges.Base) != 0 || len(supplied.Ranges.Head) != 0 {
		t.Errorf("migrated ranges = %+v, want empty", supplied.Ranges)
	}
	if supplied.Parts != 1 {
		t.Errorf("migrated parts = %d, want 1", supplied.Parts)
	}
}
