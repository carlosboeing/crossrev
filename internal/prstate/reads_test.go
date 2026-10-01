package prstate_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/prstate"
)

// The manifest reads envelope travels with every generation: which mode was
// declared, which one was effective and why, and what the calls cost.
func TestReadsEnvelopeRoundTrip(t *testing.T) {
	envelope := prstate.ReadsEnvelope{
		DeclaredMode:    "served",
		EffectiveMode:   "supplied",
		Reason:          "reads_unavailable",
		Calls:           2,
		Reads:           7,
		Bytes:           4096,
		Refused:         1,
		BudgetExhausted: true,
	}
	raw, err := json.Marshal(envelope)
	if err != nil {
		t.Fatalf("marshalling the envelope: %v", err)
	}
	for _, key := range []string{`"declared_mode"`, `"effective_mode"`, `"reason"`, `"calls"`, `"reads"`, `"bytes"`, `"refused"`, `"budget_exhausted"`} {
		if !strings.Contains(string(raw), key) {
			t.Errorf("envelope %s carries no %s", raw, key)
		}
	}
	back, err := prstate.DecodeReadsEnvelope(raw)
	if err != nil {
		t.Fatalf("decoding the envelope: %v", err)
	}
	if back.DeclaredMode != "served" || back.EffectiveMode != "supplied" || back.Reason != "reads_unavailable" {
		t.Errorf("round trip lost the modes: %+v", back)
	}
	if back.Calls != 2 || back.Reads != 7 || back.Bytes != 4096 || back.Refused != 1 || !back.BudgetExhausted {
		t.Errorf("round trip lost the counts: %+v", back)
	}
}

func TestReadsEnvelopeRefusesTheUnknown(t *testing.T) {
	if _, err := prstate.DecodeReadsEnvelope([]byte(`{"declared_mode":"direct"}`)); err == nil {
		t.Error("an unknown declared_mode decodes")
	}
	if _, err := prstate.DecodeReadsEnvelope([]byte(`{"declared_mode":"served","effective_mode":"direct"}`)); err == nil {
		t.Error("an unknown effective_mode decodes")
	}
	if _, err := prstate.DecodeReadsEnvelope([]byte(`{"declared_mode":"served","calls":-1}`)); err == nil {
		t.Error("a negative count decodes")
	}
}

// reads.json per ref-store generation flags which served reads overlap the
// supplied prompt content.
func TestBuildReadsJSONFlagsOverlaps(t *testing.T) {
	calls := []prstate.ReadsCall{
		{Path: "a.go", Revision: "base", StartLine: 1, EndLine: 10, Bytes: 200, Served: true},
		{Path: "b.go", Revision: "base", StartLine: 1, EndLine: 5, Bytes: 100, Served: true},
	}
	raw, err := prstate.BuildReadsJSON(calls, []string{"a.go"})
	if err != nil {
		t.Fatalf("building reads.json: %v", err)
	}
	var decoded struct {
		Calls []prstate.ReadsCall `json:"calls"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decoding reads.json: %v", err)
	}
	if len(decoded.Calls) != 2 {
		t.Fatalf("reads.json carries %d calls, want 2", len(decoded.Calls))
	}
	if !decoded.Calls[0].OverlapsSupplied {
		t.Error("the a.go read does not flag its overlap with the supplied content")
	}
	if decoded.Calls[1].OverlapsSupplied {
		t.Error("the b.go read flags an overlap it has none of")
	}
}

// A marker carries the envelope even when the halted call publishes
// nothing: the envelope-only entry is the record the call happened.
func TestMarkerCarriesTheReadsEnvelope(t *testing.T) {
	marker := prstate.Marker{Version: 2, Pass: 1}
	envelope := prstate.ReadsEnvelope{DeclaredMode: "served", EffectiveMode: "served", Calls: 1, Reads: 3, Bytes: 512}
	raw, err := json.Marshal(envelope)
	if err != nil {
		t.Fatalf("marshalling the envelope: %v", err)
	}
	marker.Reads = raw
	encoded, err := json.Marshal(marker)
	if err != nil {
		t.Fatalf("marshalling the marker: %v", err)
	}
	if !strings.Contains(string(encoded), `"reads"`) {
		t.Errorf("the marker carries no reads envelope: %s", encoded)
	}
}
