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

// The manifest carries the null reads envelope: the reservation this slice
// makes so a later slice can populate reads without a second schema bump.
// It follows the verification envelope, and writers emit nothing else.
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

// A non-null reads envelope is refused: the reservation is null in this
// release, so a populated one is a newer writer's, not this one's.
func TestSchemaV3RefusesPopulatedReadsEnvelope(t *testing.T) {
	gen := validFixtureGeneration(t, prstate.GenerationFull)
	manifest, records, err := prstate.EncodeGenerationV3(gen)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	altered := strings.Replace(string(manifest), `"reads":null`, `"reads":{"status":"passed"}`, 1)
	if _, err := prstate.DecodeGenerationV3([]byte(altered), records); err == nil {
		t.Fatal("manifest with a populated reads envelope decoded")
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
