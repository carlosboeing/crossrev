package config_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/carlosboeing/crossrev/internal/config"
)

// The Bash defaults read the pairing out of lib/harnesses.json at runtime
// (lib/config.sh:101-102). Go states it in source, so this test reads the same
// file and fails if the two ever drift.
func TestTheDefaultPairingMatchesTheHarnessTable(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(repoRoot(t), "lib/harnesses.json"))
	if err != nil {
		t.Fatalf("read lib/harnesses.json: %v", err)
	}
	var table struct {
		DefaultPairing struct {
			Reviewer string `json:"reviewer"`
			Resolver string `json:"resolver"`
		} `json:"default_pairing"`
	}
	if err := json.Unmarshal(raw, &table); err != nil {
		t.Fatalf("decode lib/harnesses.json: %v", err)
	}

	defaults := config.Defaults()
	if got := defaults.Object("reviewer").Value("harness"); got != table.DefaultPairing.Reviewer {
		t.Errorf("the default reviewer is %v, and lib/harnesses.json names %q", got, table.DefaultPairing.Reviewer)
	}
	if got := defaults.Object("resolver").Value("harness"); got != table.DefaultPairing.Resolver {
		t.Errorf("the default resolver is %v, and lib/harnesses.json names %q", got, table.DefaultPairing.Resolver)
	}
	if table.DefaultPairing.Reviewer == table.DefaultPairing.Resolver {
		t.Error("the default pairing reviews and resolves with one harness, which is not a cross-model review")
	}
}

// With no config file anywhere, nothing demands an API key and nothing is
// persisted uninvited (lib/config.sh:88-93).
func TestTheDefaultsDemandNoCredential(t *testing.T) {
	defaults := config.Defaults()
	if got := defaults.Object("endpoints").Len(); got != 0 {
		t.Errorf("the defaults define %d endpoints, want none", got)
	}
	if got := defaults.Value("mode"); got != "local" {
		t.Errorf("mode = %v, want local", got)
	}
}
