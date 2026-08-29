package harness_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/carlosboeing/crossrev/internal/harness"
)

// parityFixtureDir is the single package-relative route to the frozen Bash
// oracle. Go reads that file and never writes it, and no test here invokes the
// capture script: a Go run that could recapture would freeze Go's answer rather
// than Bash's.
const parityFixtureDir = "../../tests/fixtures/parity"

// usageOracle is the file this package answers for. It freezes usage_zero,
// usage_with_total, the five parsers, usage_price_key, usage_price,
// usage_billing_for, usage_format_cost and usage_footnote, and
// tests/test-parity.sh holds the Bash side of the same contract.
const usageOracle = "usage.json"

type usageFixture struct {
	Captured struct {
		Platform         string `json:"platform"`
		TrImplementation string `json:"tr_implementation"`
		Locale           string `json:"locale"`
	} `json:"captured"`
	Function          string          `json:"function"`
	PriceTableVersion string          `json:"price_table_version"`
	Zero              json.RawMessage `json:"zero"`
	ZeroWithTotal     json.RawMessage `json:"zero_with_total"`
	ParseCases        []struct {
		Name   string `json:"name"`
		Parser string `json:"parser"`
		Input  string `json:"input"`
		Record string `json:"record"`
	} `json:"parse_cases"`
	PriceKeyCases []struct {
		Reported string `json:"reported"`
		Key      string `json:"key"`
	} `json:"price_key_cases"`
	PriceCases []struct {
		Name   string `json:"name"`
		Model  string `json:"model"`
		Usage  string `json:"usage"`
		Priced string `json:"priced"`
	} `json:"price_cases"`
	BillingCases []struct {
		Name               string `json:"name"`
		Harness            string `json:"harness"`
		Endpoint           string `json:"endpoint"`
		AnthropicAPIKeySet bool   `json:"anthropic_api_key_set"`
		Billing            string `json:"billing"`
	} `json:"billing_cases"`
	FormatCostCases []struct {
		Value     string `json:"value"`
		Formatted string `json:"formatted"`
	} `json:"format_cost_cases"`
	FootnoteCases []struct {
		CostSource string `json:"cost_source"`
		Billing    string `json:"billing"`
		Footnote   string `json:"footnote"`
	} `json:"footnote_cases"`
}

func usageFixtureFile(t *testing.T) usageFixture {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(parityFixtureDir, usageOracle))
	if err != nil {
		t.Fatalf("reading the %s oracle: %v", usageOracle, err)
	}
	var fixture usageFixture
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatalf("decoding the %s oracle: %v", usageOracle, err)
	}
	return fixture
}

// An oracle with no provenance cannot be re-derived, and a divergence found
// against it could not be attributed.
func TestUsageOracleCarriesProvenance(t *testing.T) {
	fixture := usageFixtureFile(t)
	if fixture.Captured.Platform == "" || fixture.Captured.TrImplementation == "" || fixture.Captured.Locale == "" {
		t.Errorf("incomplete provenance: %+v", fixture.Captured)
	}
	if fixture.Function == "" {
		t.Error("the oracle names no Bash function")
	}
}

// The record's key order is part of the contract, because tests/test-parity.sh
// compares the two zero records as strings rather than as values.
func TestZeroRecordsMatchTheOracleByteForByte(t *testing.T) {
	fixture := usageFixtureFile(t)

	for _, tt := range []struct {
		name string
		got  harness.Usage
		want json.RawMessage
	}{
		{name: "usage_zero", got: harness.Zero(), want: fixture.Zero},
		{name: "usage_with_total", got: harness.Zero().WithTotal(), want: fixture.ZeroWithTotal},
	} {
		t.Run(tt.name, func(t *testing.T) {
			encoded, err := json.Marshal(tt.got)
			if err != nil {
				t.Fatalf("marshalling the record: %v", err)
			}
			var compact bytes.Buffer
			if err := json.Compact(&compact, tt.want); err != nil {
				t.Fatalf("compacting the oracle: %v", err)
			}
			if !bytes.Equal(encoded, compact.Bytes()) {
				t.Errorf("record = %s, want %s", encoded, compact.Bytes())
			}
		})
	}
}

// Every parse vector the capture recorded, against the parser it named.
func TestParsersMatchTheOracle(t *testing.T) {
	fixture := usageFixtureFile(t)
	if len(fixture.ParseCases) == 0 {
		t.Fatal("the oracle records no parse cases, so this proves nothing")
	}

	parsers := map[string]func([]byte) *harness.Usage{
		"claude":   harness.ParseClaude,
		"codex":    harness.ParseCodexEvents,
		"grok":     harness.ParseGrok,
		"agy":      harness.ParseAgy,
		"opencode": harness.ParseOpencodeExport,
	}
	seen := map[string]bool{}

	for _, want := range fixture.ParseCases {
		t.Run(want.Name, func(t *testing.T) {
			parse, known := parsers[want.Parser]
			if !known {
				t.Fatalf("the oracle names the parser %q, which this package does not have", want.Parser)
			}
			seen[want.Parser] = true
			assertRecord(t, parse([]byte(want.Input)), want.Record)
		})
	}

	// A parser with no vector would pass this whole test by not being called.
	for name := range parsers {
		if !seen[name] {
			t.Errorf("the oracle carries no vector for the %s parser", name)
		}
	}
}

func TestPriceKeysMatchTheOracle(t *testing.T) {
	fixture := usageFixtureFile(t)
	if len(fixture.PriceKeyCases) == 0 {
		t.Fatal("the oracle records no price-key cases, so this proves nothing")
	}
	prices := priceTable(t)

	for _, want := range fixture.PriceKeyCases {
		if got := prices.Key(want.Reported); got != want.Key {
			t.Errorf("Key(%q) = %q, want %q", want.Reported, got, want.Key)
		}
	}
}

// The extract the binary carries has to be the one the vectors were cut from,
// or every priced answer below is measured against a different table.
func TestPriceTableVersionMatchesTheOracle(t *testing.T) {
	fixture := usageFixtureFile(t)
	if got := priceTable(t).Version(); got != fixture.PriceTableVersion {
		t.Errorf("price table version = %q, want %q", got, fixture.PriceTableVersion)
	}
}

func TestTableCostsMatchTheOracle(t *testing.T) {
	fixture := usageFixtureFile(t)
	if len(fixture.PriceCases) == 0 {
		t.Fatal("the oracle records no price cases, so this proves nothing")
	}
	prices := priceTable(t)

	for _, want := range fixture.PriceCases {
		t.Run(want.Name, func(t *testing.T) {
			var record harness.Usage
			if err := json.Unmarshal([]byte(want.Usage), &record); err != nil {
				t.Fatalf("decoding the input record: %v", err)
			}
			priced := prices.Price(record, want.Model)
			assertRecord(t, &priced, want.Priced)
		})
	}
}

func TestBillingModesMatchTheOracle(t *testing.T) {
	fixture := usageFixtureFile(t)
	if len(fixture.BillingCases) == 0 {
		t.Fatal("the oracle records no billing cases, so this proves nothing")
	}
	doc := descriptors(t)

	for _, want := range fixture.BillingCases {
		t.Run(want.Name, func(t *testing.T) {
			got := harness.BillingFor(doc, want.Harness, want.Endpoint, want.AnthropicAPIKeySet)
			if got != want.Billing {
				t.Errorf("BillingFor(%q, %q, api key %t) = %q, want %q",
					want.Harness, want.Endpoint, want.AnthropicAPIKeySet, got, want.Billing)
			}
		})
	}
}

func TestFormatCostMatchesTheOracle(t *testing.T) {
	fixture := usageFixtureFile(t)
	if len(fixture.FormatCostCases) == 0 {
		t.Fatal("the oracle records no format cases, so this proves nothing")
	}
	for _, want := range fixture.FormatCostCases {
		if got := harness.FormatCost(want.Value); got != want.Formatted {
			t.Errorf("FormatCost(%q) = %q, want %q", want.Value, got, want.Formatted)
		}
	}
}

func TestFootnotesMatchTheOracle(t *testing.T) {
	fixture := usageFixtureFile(t)
	if len(fixture.FootnoteCases) == 0 {
		t.Fatal("the oracle records no footnote cases, so this proves nothing")
	}
	for _, want := range fixture.FootnoteCases {
		if got := harness.Footnote(want.CostSource, want.Billing); got != want.Footnote {
			t.Errorf("Footnote(%q, %q) = %q, want %q",
				want.CostSource, want.Billing, got, want.Footnote)
		}
	}
}

// assertRecord compares a parsed record against the oracle's own JSON.
//
// It compares VALUES rather than the two byte strings. jq and encoding/json
// both print a double in its shortest round-tripping form, so the texts agree
// on every vector here — but a future rate could be one where they choose a
// different exponent form for the same double, and the test would then report a
// divergence that is not one. Comparing the decoded trees keeps the check on
// what the record means. Key order is proved separately, byte for byte, by
// TestZeroRecordsMatchTheOracleByteForByte.
func assertRecord(t *testing.T, got *harness.Usage, want string) {
	t.Helper()

	if want == "null" {
		if got != nil {
			encoded, _ := json.Marshal(got)
			t.Fatalf("record = %s, want null", encoded)
		}
		return
	}
	if got == nil {
		t.Fatalf("record = null, want %s", want)
	}

	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshalling the record: %v", err)
	}
	var mine, theirs any
	if err := json.Unmarshal(encoded, &mine); err != nil {
		t.Fatalf("decoding the record: %v", err)
	}
	if err := json.Unmarshal([]byte(want), &theirs); err != nil {
		t.Fatalf("decoding the oracle record: %v", err)
	}
	if !reflect.DeepEqual(mine, theirs) {
		t.Errorf("record = %s,\n         want %s", encoded, want)
	}
}

func priceTable(t *testing.T) harness.Prices {
	t.Helper()
	prices, err := harness.PriceTable()
	if err != nil {
		t.Fatalf("reading the embedded price extract: %v", err)
	}
	return prices
}

func descriptors(t *testing.T) harness.Document {
	t.Helper()
	doc, err := harness.Descriptors()
	if err != nil {
		t.Fatalf("reading the embedded descriptor: %v", err)
	}
	return doc
}

// deref reads an optional field, answering the zero value for a null.
//
// Every optional key of a usage record and an envelope is a pointer, because
// the shell writes null there and null is a third state beside a value and an
// absence. Reading one in a test is a two-line dance without this.
func deref[T any](value *T) T {
	if value == nil {
		var zero T
		return zero
	}
	return *value
}
