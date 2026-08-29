package harness_test

import (
	"testing"

	"github.com/carlosboeing/crossrev/internal/harness"
)

// The three refusals, each named on its own.
//
// The oracle freezes them as priced records; this says which rule fired, so a
// change that removed one and left another covering the same vector would be
// caught here rather than passing on the arithmetic.
func TestEveryPriceRefusalHasItsOwnReason(t *testing.T) {
	prices := priceTable(t)

	tests := []struct {
		name  string
		model string
		usage harness.Usage
		why   string
	}{
		{
			name: "a bucket whose rate the extract does not list", model: "gpt-5.5",
			usage: harness.Usage{CacheWrite5m: 10},
			why:   "gpt-5.5 lists no cache-write rate at all, and pricing those tokens at zero would understate the leg without saying so",
		},
		{
			name: "an unresolvable cache-write TTL", model: "claude-opus-5",
			usage: harness.Usage{CacheWriteUnsplit: 1000},
			why:   "the 5m and 1h rates differ and nothing says which one the write was",
		},
		{
			name: "a long-context break a cumulative total cannot rule out", model: "gpt-5.5",
			usage: harness.Usage{InputFresh: 272000},
			why:   "the break is per request and the record is a whole run",
		},
		{
			name: "the break counts every input bucket", model: "gpt-5.5",
			usage: harness.Usage{InputFresh: 200000, CacheRead: 72000},
			why:   "cache reads are input too",
		},
		{
			name: "an unlisted model", model: "a-model-nobody-listed",
			usage: harness.Usage{InputFresh: 1000},
			why:   "an unlisted model prices as a refusal rather than a guess",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			priced := prices.Price(tt.usage.WithTotal(), tt.model)
			if priced.CostUSD != nil || priced.CostSource != nil || priced.PriceTable != nil {
				t.Errorf("the record was priced, and it should have refused: %s", tt.why)
			}
		})
	}
}

// The same entries price when the bucket the refusal is about is empty.
func TestARefusalIsAboutTheBucketRatherThanTheEntry(t *testing.T) {
	prices := priceTable(t)

	tests := []struct {
		name  string
		model string
		usage harness.Usage
		cost  float64
	}{
		{name: "gpt-5.5 with no writes", model: "gpt-5.5",
			usage: harness.Usage{InputFresh: 1000, Output: 100}, cost: 0.008},
		{name: "gpt-5.5 under the break", model: "gpt-5.5",
			usage: harness.Usage{InputFresh: 271999}, cost: 1.359995},
		{name: "output alone does not reach the break", model: "gpt-5.5",
			usage: harness.Usage{Output: 400000}, cost: 12},
		{name: "an unsplit write where no 1hr rate is listed", model: "gpt-5.6",
			usage: harness.Usage{CacheWriteUnsplit: 1000}, cost: 0.005},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			priced := prices.Price(tt.usage.WithTotal(), tt.model)
			if priced.CostUSD == nil {
				t.Fatal("the record refused to price")
			}
			if *priced.CostUSD != tt.cost {
				t.Errorf("cost = %v, want %v", *priced.CostUSD, tt.cost)
			}
			if deref(priced.CostSource) != "table" {
				t.Errorf("cost_source = %q, want table", deref(priced.CostSource))
			}
			if deref(priced.PriceTable) != prices.Version() {
				t.Errorf("price_table = %q, want the extract's version", deref(priced.PriceTable))
			}
		})
	}
}

// Reasoning is persisted beside the total and never priced, because every
// harness that reports it nests it inside output already.
func TestReasoningIsNotPriced(t *testing.T) {
	prices := priceTable(t)
	reasoning := int64(400)

	withReasoning := prices.Price(harness.Usage{Output: 500, Reasoning: &reasoning}.WithTotal(), "claude-opus-5")
	without := prices.Price(harness.Usage{Output: 500}.WithTotal(), "claude-opus-5")

	if deref(withReasoning.CostUSD) != deref(without.CostUSD) {
		t.Errorf("reasoning changed the cost: %v against %v", deref(withReasoning.CostUSD), deref(without.CostUSD))
	}
	if deref(withReasoning.Total) != deref(without.Total) {
		t.Error("reasoning was added to the total")
	}
}

// `version` is the one key of the extract whose value is not a rate object, and
// the exact-match rung answers it.
//
// The shell reaches jq's "Cannot index string with string" there and prints
// nothing, so usage_attach's pipeline loses the whole record. Measured:
// `usage_price "$record" version` exits 5 with empty output, and so does
// `usage_attach "$record" claude vendor version`. Losing the record is a fault
// rather than a behaviour, so Go refuses to price and keeps the buckets.
func TestTheVersionKeyRefusesRatherThanLosingTheRecord(t *testing.T) {
	prices := priceTable(t)

	if got := prices.Key("version"); got != "version" {
		t.Fatalf("Key(\"version\") = %q, want version; the divergence below is about that answer", got)
	}
	priced := prices.Price(harness.Usage{InputFresh: 10}.WithTotal(), "version")
	if priced.CostUSD != nil || priced.CostSource != nil {
		t.Error("the version key priced something")
	}
	if priced.InputFresh != 10 || deref(priced.Total) != 10 {
		t.Error("the buckets did not survive")
	}
}

// The orchestrator-side merge: billing always, and the cost triple rewritten
// when the billing mode forbids one.
func TestAttachMergesBillingAndCost(t *testing.T) {
	prices := priceTable(t)
	doc := descriptors(t)
	harnessCost := 0.5

	tests := []struct {
		name       string
		usage      harness.Usage
		harness    string
		endpoint   string
		model      string
		apiKey     bool
		billing    string
		cost       *float64
		costSource string
	}{
		{
			name:    "a named endpoint discards whatever the adapter reported",
			usage:   harness.Usage{InputFresh: 1000, CostUSD: &harnessCost},
			harness: "claude", endpoint: "an-endpoint", model: "claude-opus-5",
			billing: "endpoint",
		},
		{
			name:    "a harness cost is kept and marked",
			usage:   harness.Usage{InputFresh: 1000, CostUSD: &harnessCost},
			harness: "claude", endpoint: "vendor", model: "claude-opus-5",
			billing: "subscription", cost: &harnessCost, costSource: "harness",
		},
		{
			name:    "no harness cost is table-priced",
			usage:   harness.Usage{InputFresh: 1000},
			harness: "claude", endpoint: "vendor", model: "claude-opus-5",
			billing: "subscription", cost: float64Ptr(0.005), costSource: "table",
		},
		{
			name:    "an API key wins over the subscription for a harness that keeps one",
			usage:   harness.Usage{InputFresh: 1000},
			harness: "claude", endpoint: "vendor", model: "claude-opus-5", apiKey: true,
			billing: "api", cost: float64Ptr(0.005), costSource: "table",
		},
		{
			name:    "a credential CrossRev cannot tell apart gets no claim",
			usage:   harness.Usage{InputFresh: 1000},
			harness: "opencode", endpoint: "vendor", model: "claude-opus-5",
			billing: "", cost: float64Ptr(0.005), costSource: "table",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			merged := prices.Attach(tt.usage.WithTotal(), doc, tt.harness, tt.endpoint, tt.model, tt.apiKey)

			if got := deref(merged.Billing); got != tt.billing {
				t.Errorf("billing = %q, want %q", got, tt.billing)
			}
			if tt.cost == nil {
				if merged.CostUSD != nil {
					t.Errorf("cost_usd = %v, want null", *merged.CostUSD)
				}
				if merged.CostSource != nil || merged.PriceTable != nil {
					t.Error("the whole cost triple is cleared, not just the figure")
				}
				return
			}
			if merged.CostUSD == nil || *merged.CostUSD != *tt.cost {
				t.Errorf("cost_usd = %v, want %v", merged.CostUSD, *tt.cost)
			}
			if got := deref(merged.CostSource); got != tt.costSource {
				t.Errorf("cost_source = %q, want %q", got, tt.costSource)
			}
			// A harness-supplied cost is not a table price, so it names no
			// table (lib/usage.sh:493).
			if tt.costSource == "harness" && merged.PriceTable != nil {
				t.Errorf("price_table = %q, want null for a harness cost", *merged.PriceTable)
			}
			if tt.costSource == "table" && deref(merged.PriceTable) != prices.Version() {
				t.Errorf("price_table = %q, want the extract's version", deref(merged.PriceTable))
			}
		})
	}
}

// Which harnesses keep a vendor API key alive is recorded per harness in
// env_keep, not known by name.
func TestOnlyTheHarnessThatKeepsTheKeyBillsAsAPI(t *testing.T) {
	doc := descriptors(t)

	for _, name := range doc.Names() {
		entry, _ := doc.For(name)
		keeps := false
		for _, kept := range entry.Credential.EnvKeep {
			if kept == "ANTHROPIC_API_KEY" {
				keeps = true
			}
		}
		got := harness.BillingFor(doc, name, "vendor", true)
		if keeps && got != "api" {
			t.Errorf("%s keeps the key and bills as %q", name, got)
		}
		if !keeps && got == "api" {
			t.Errorf("%s does not keep the key and bills as api", name)
		}
	}
}

// The identity is what makes `total` mean the same thing on every row.
func TestTotalIsTheSixBucketsAndNotReasoning(t *testing.T) {
	reasoning := int64(9)
	record := harness.Usage{
		InputFresh: 1, CacheRead: 2, CacheWrite5m: 4, CacheWrite1h: 8,
		CacheWriteUnsplit: 16, Output: 32, Reasoning: &reasoning,
	}.WithTotal()

	if deref(record.Total) != 63 {
		t.Errorf("total = %d, want 63", deref(record.Total))
	}
	if record.Cached() != 30 {
		t.Errorf("Cached() = %d, want the four cache buckets", record.Cached())
	}
}

func float64Ptr(value float64) *float64 { return &value }
