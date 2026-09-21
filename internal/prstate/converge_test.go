package prstate_test

import (
	"encoding/json"
	"testing"

	"github.com/carlosboeing/crossrev/internal/prstate"
)

// v2MarkerWith builds a complete v2 review marker carrying extra keys, which
// is the shape a marker in flight has: settled, at version 2, with whatever
// coverage claim the writer left on it.
func v2MarkerWith(t *testing.T, extra string) prstate.Marker {
	t.Helper()
	raw := `{"v":2,"leg":"review","pass":1,"state":"complete","ts":1700000000`
	if extra != "" {
		raw += "," + extra
	}
	raw += `}`
	m, err := prstate.ParseMarker(json.RawMessage(raw))
	if err != nil {
		t.Fatalf("parsing a v2 marker: %v", err)
	}
	return m
}

func TestAPreChangeV2MarkerStillConverges(t *testing.T) {
	if !prstate.MarkerConverges(v2MarkerWith(t, `"coverage_manifest_id":5708392785`)) {
		t.Fatal("a marker in flight stopped converging on its own field")
	}
}

func TestAPostChangeMarkerConvergesOnTheNewFields(t *testing.T) {
	const sha = "9f3c1abdeadbeef9f3c1abdeadbeef9f3c1abd"
	m := v2MarkerWith(t, `"coverage_gen":3,"coverage_ref":"refs/crossrev/pr/42/reviewer1/coverage","coverage_commit":"`+sha+`"`)
	if !prstate.MarkerConverges(m) {
		t.Fatal("a marker written after the change does not converge")
	}
}

func TestAV2MarkerWithNoCoverageClaimStillRefusesGreen(t *testing.T) {
	if prstate.MarkerConverges(v2MarkerWith(t, "")) {
		t.Fatal("a v2 marker promising coverage it never recorded underwrote green")
	}
}
