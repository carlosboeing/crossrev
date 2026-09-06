package archtest_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// The frozen parity vectors are a recording, and a recording is only worth what
// its provenance says. Each fixture names the shell function it was captured
// from and the machine it was captured on, because the alternative to that
// header is a file of numbers nobody can date or place.
//
// Two packages checked their own fixture's header — internal/cred and
// internal/harness — which left sixteen of the eighteen checked by nobody. A
// per-package check also cannot see a fixture no package reads at all, which is
// how one outlived its reader here unnoticed until it was deleted. This walks
// the directory instead, so a new fixture is covered by existing.
func TestEveryParityFixtureSaysWhereItCameFrom(t *testing.T) {
	dir := filepath.Join("..", "..", "tests", "fixtures", "parity")

	names, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		t.Fatalf("reading %s: %v", dir, err)
	}
	if len(names) == 0 {
		t.Fatalf("no parity fixtures under %s, so this test is guarding nothing", dir)
	}

	for _, name := range names {
		t.Run(filepath.Base(name), func(t *testing.T) {
			raw, err := os.ReadFile(name) //nolint:gosec // a path this test globbed
			if err != nil {
				t.Fatalf("reading: %v", err)
			}

			var fixture struct {
				Captured struct {
					Platform         string `json:"platform"`
					TrImplementation string `json:"tr_implementation"`
					Locale           string `json:"locale"`
				} `json:"captured"`
				Function string `json:"function"`
			}
			if err := json.Unmarshal(raw, &fixture); err != nil {
				t.Fatalf("parsing: %v", err)
			}

			// The function is what a reader follows back to the shell the
			// vector was measured from. Without it the file records an answer
			// and not the question.
			if fixture.Function == "" {
				t.Error("no `function`, so nothing says which shell function this froze")
			}
			for field, value := range map[string]string{
				"platform":          fixture.Captured.Platform,
				"tr_implementation": fixture.Captured.TrImplementation,
				"locale":            fixture.Captured.Locale,
			} {
				if value == "" {
					t.Errorf("captured.%s is empty, so the recording cannot be placed", field)
				}
			}
		})
	}
}
