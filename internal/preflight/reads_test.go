package preflight_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/harness"
	"github.com/carlosboeing/crossrev/internal/preflight"
)

// doctor prints each harness's mode and names where the tripwire cannot
// run: agy's missing record and opencode's denied-without-a-record config.
func TestReportReadModesNamesModesAndGaps(t *testing.T) {
	io, buf := capture()
	c := &preflight.Checker{IO: io, Harness: document(t)}
	c.ReportReadModes()
	report := buf.String()
	for _, want := range []string{
		"Review reads",
		"claude — served reads, command block verified at pin 2.1.237",
		"codex — served reads, command block verified at pin 0.148.0",
		"grok — supplied reads, tripwire verified at pin 1.0.5",
		"opencode — supplied reads, no tripwire",
		"agy — supplied reads, no tripwire",
	} {
		if !strings.Contains(report, want) {
			t.Errorf("report misses %q:\n%s", want, report)
		}
	}
}

// A moved pin reads as unverified in the report, before the leg refuses it.
func TestReportReadModesNamesAnUnverifiedPin(t *testing.T) {
	raw := harness.DescriptorJSON()
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		t.Fatalf("decoding the descriptor: %v", err)
	}
	for _, entry := range root["harnesses"].([]any) {
		if entry.(map[string]any)["name"] == "codex" {
			install := entry.(map[string]any)["install"].(map[string]any)
			install["pinned_version"] = "9.9.9"
			install["command"] = "install 9.9.9"
		}
	}
	mutated, _ := json.Marshal(root)
	doc, err := harness.Load(mutated)
	if err != nil {
		t.Fatalf("loading the moved pin: %v", err)
	}
	io, buf := capture()
	c := &preflight.Checker{IO: io, Harness: doc}
	c.ReportReadModes()
	if !strings.Contains(buf.String(), "codex — served reads, command block UNVERIFIED at pin 9.9.9 (review_isolation_unverified)") {
		t.Errorf("report names no unverified codex block:\n%s", buf.String())
	}
}
