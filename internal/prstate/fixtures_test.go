package prstate_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// parityFixtureDir is the single package-relative route to the frozen Bash
// oracle. Go reads those files and never writes them, and no test in this
// package invokes the capture script: a Go run that could recapture would
// freeze Go's answer rather than Bash's.
const parityFixtureDir = "../../tests/fixtures/parity"

// loadFixture reads one oracle file and unmarshals it into v.
func loadFixture(t *testing.T, name string, v any) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(parityFixtureDir, name))
	if err != nil {
		t.Fatalf("reading the %s oracle: %v", name, err)
	}
	if err := json.Unmarshal(raw, v); err != nil {
		t.Fatalf("decoding the %s oracle: %v", name, err)
	}
}

// captured is the provenance block every oracle file carries. Task 0.1 records
// the platform, the tr implementation and the locale the Bash run happened
// under, because two of the three decide whether an id is byte-oriented.
type captured struct {
	Platform         string `json:"platform"`
	TrImplementation string `json:"tr_implementation"`
	Locale           string `json:"locale"`
}

// TestParityFixturesCarryProvenance fails if an oracle file lost the block that
// says which machine produced it. A fixture with no provenance cannot be
// re-derived, and a divergence found against it could not be attributed.
func TestParityFixturesCarryProvenance(t *testing.T) {
	for _, name := range prstateOracleFiles {
		var f struct {
			Captured captured `json:"captured"`
			Function string   `json:"function"`
		}
		loadFixture(t, name, &f)
		if f.Captured.Platform == "" || f.Captured.TrImplementation == "" || f.Captured.Locale == "" {
			t.Errorf("%s: incomplete provenance %+v", name, f.Captured)
		}
		if f.Function == "" {
			t.Errorf("%s: names no Bash function", name)
		}
	}
}

// prstateOracleFiles are the four oracle files this package answers for. The
// other five belong to the diff, configuration, label and prompt ports.
var prstateOracleFiles = []string{
	"marker_codec.json",
	"marker_encode.json",
	"state_finding_id.json",
	"state_anchor.json",
}
