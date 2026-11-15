package resolve

import (
	"encoding/json"
	"github.com/carlosboeing/crossrev/internal/harness"
	"strings"
	"testing"
)

func TestResolvePricesTheReportedModelAndExplainsTheColumns(t *testing.T) {
	e := setup(t)
	e.git.staged = true
	e.addReview(t, defaultFindings(), "issues-remain")
	env := okResolveEnvelope(string(oneFindingPayload()), 1000, 100)
	model := "claude-haiku-4-5"
	env.ModelReported = &model
	total := env.Usage.WithTotal()
	env.Usage = &total
	env.Tokens = total.Total
	e.adapter.envelopes = []harness.Envelope{env}
	got := e.run(t)
	if got.Err != nil {
		t.Fatal(got.Err)
	}
	var usage harness.Usage
	if err := json.Unmarshal(got.Marker.Usage, &usage); err != nil {
		t.Fatal(err)
	}
	if usage.CostUSD == nil || usage.CostSource == nil || *usage.CostSource != "table" {
		t.Fatalf("reported model not priced: %+v", usage)
	}
	details := runDetails(got.Marker, "resolve")
	for _, want := range []string{"| Tokens | Cached | Est. cost |", "Cached is the read/write portion already included in Tokens.", "claude-haiku-4-5", "~$0.00"} {
		if !strings.Contains(details, want) {
			t.Errorf("missing %q: %s", want, details)
		}
	}
	if log := resolveRunLog(t, e); !strings.Contains(log, `"normalization_version":1`) {
		t.Fatal("numeric telemetry not logged")
	}
}
