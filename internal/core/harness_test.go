package core

import (
	"encoding/json"
	"testing"
)

// lib/harnesses.json lists the five drivable harnesses in this order.
func TestHarnessNamesAreTheFiveDescriptorsInDeclarationOrder(t *testing.T) {
	want := []string{"claude", "codex", "agy", "grok", "opencode"}
	got := HarnessNames()
	if len(got) != len(want) {
		t.Fatalf("HarnessNames() = %v, want %v", got, want)
	}
	for i := range want {
		if string(got[i]) != want[i] {
			t.Fatalf("HarnessNames() = %v, want %v", got, want)
		}
	}
	for _, in := range want {
		if _, err := ParseHarnessName(in); err != nil {
			t.Fatalf("ParseHarnessName(%q): %v", in, err)
		}
	}
	for _, in := range []string{"", "gemini", "Claude", "cursor"} {
		if _, err := ParseHarnessName(in); err == nil {
			t.Fatalf("ParseHarnessName(%q) = nil error, want a refusal", in)
		}
	}
}

// lib/run.sh:488 sets LEG_WRITE=no and lib/run.sh:489 raises it to yes for the
// resolver alone. The comment above it says the derivation is deliberately not
// configurable, so the only route to a write capability is the leg role.
func TestWriteCapabilityIsDerivedFromTheLegAndNothingElse(t *testing.T) {
	if got := WriteCapabilityFor(RoleReviewer); got != WriteNo {
		t.Fatalf("WriteCapabilityFor(reviewer) = %q, want no", got)
	}
	if got := WriteCapabilityFor(RoleResolver); got != WriteYes {
		t.Fatalf("WriteCapabilityFor(resolver) = %q, want yes", got)
	}
	// Anything that is not the resolver reads no, matching the Bash default.
	if got := WriteCapabilityFor(LegRole("watchdog")); got != WriteNo {
		t.Fatalf("WriteCapabilityFor(watchdog) = %q, want no", got)
	}
	if WriteNo != "no" || WriteYes != "yes" {
		t.Fatalf("write capabilities are %q and %q, want no and yes", WriteNo, WriteYes)
	}
}

// lib/usage.sh:363 treats an endpoint named `vendor` as no endpoint at all.
func TestEndpointVendorIsTheSentinelForNoNamedEndpoint(t *testing.T) {
	if EndpointVendor != "vendor" {
		t.Fatalf("EndpointVendor = %q, want vendor", EndpointVendor)
	}
}

// lib/usage.sh:363-374 produces exactly these billing modes, plus the empty
// string where the credential descriptor cannot name one.
func TestBillingVocabulary(t *testing.T) {
	want := []string{"endpoint", "api", "subscription"}
	got := BillingModes()
	if len(got) != len(want) {
		t.Fatalf("BillingModes() = %v, want %v", got, want)
	}
	for i := range want {
		if string(got[i]) != want[i] {
			t.Fatalf("BillingModes() = %v, want %v", got, want)
		}
	}
	if BillingNone != "" {
		t.Fatalf("BillingNone = %q, want the empty string", BillingNone)
	}
}

// lib/usage.sh:493 writes `harness` and lib/usage.sh:471 writes `table`.
func TestCostSourceVocabulary(t *testing.T) {
	if CostSourceHarness != "harness" || CostSourceTable != "table" {
		t.Fatalf("cost sources are %q and %q", CostSourceHarness, CostSourceTable)
	}
	if CostSourceNone != "" {
		t.Fatalf("CostSourceNone = %q, want the empty string", CostSourceNone)
	}
}

// usage_zero at lib/usage.sh:31 declares the token buckets, and
// usage_with_total at lib/usage.sh:38 sums exactly six of them.
func TestUsageTokenTotalSumsTheSixBuckets(t *testing.T) {
	u := Usage{
		InputFresh:        1,
		CacheRead:         2,
		CacheWrite5m:      4,
		CacheWrite1h:      8,
		CacheWriteUnsplit: 16,
		Output:            32,
	}
	if got := u.TokenTotal(); got != 63 {
		t.Fatalf("TokenTotal() = %d, want 63", got)
	}
	// Reasoning is reported beside the buckets and is not one of them.
	reasoning := int64(64)
	u.Reasoning = &reasoning
	if got := u.TokenTotal(); got != 63 {
		t.Fatalf("TokenTotal() = %d after setting Reasoning, want 63", got)
	}
}

func TestUsageOptionalFieldsAreDistinguishableFromZero(t *testing.T) {
	var u Usage
	if u.Reasoning != nil || u.CostUSD != nil || u.PriceTable != nil {
		t.Fatal("a zero Usage reports a value for a field the harness did not send")
	}
	cost := 0.0
	u.CostUSD = &cost
	if u.CostUSD == nil || *u.CostUSD != 0 {
		t.Fatal("a reported cost of exactly zero must survive as a reported value")
	}
}

func TestInvocationRequestCarriesTheAdapterArguments(t *testing.T) {
	req := InvocationRequest{
		Harness:    HarnessClaude,
		PromptPath: "/tmp/prompt",
		SchemaPath: "/tmp/schema.json",
		Workdir:    "/checkout",
		Model:      "opus",
		Effort:     "high",
		Endpoint:   "",
		Write:      WriteNo,
	}
	if req.Harness != "claude" || req.Write != WriteNo {
		t.Fatalf("InvocationRequest = %+v", req)
	}
}

// The adapters return the envelope shape lib/run.sh:840 reads: `ok`, `payload`
// and `error`, with the telemetry fields attached afterwards.
func TestEnvelopeDecodesTheAdapterFailureShape(t *testing.T) {
	const raw = `{"ok":false,"payload":null,"error":"the adapter returned nothing at all"}`
	var env Envelope
	if err := json.Unmarshal([]byte(raw), &env); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if env.OK {
		t.Fatal("OK = true for a failure envelope")
	}
	if env.Error != "the adapter returned nothing at all" {
		t.Fatalf("Error = %q", env.Error)
	}
	if env.ModelReported != nil || env.Tokens != nil || env.Usage != nil {
		t.Fatal("absent telemetry decoded as present")
	}
}

func TestEnvelopeKeepsAbsentAndNullTelemetryApart(t *testing.T) {
	const raw = `{"ok":true,"payload":{"verdict":"converged"},"model_reported":"","tokens":0}`
	var env Envelope
	if err := json.Unmarshal([]byte(raw), &env); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if env.ModelReported == nil || *env.ModelReported != "" {
		t.Fatal("a reported empty model must decode as present and empty")
	}
	if env.Tokens == nil || *env.Tokens != 0 {
		t.Fatal("a reported token count of zero must decode as present and zero")
	}
	if string(env.Payload) != `{"verdict":"converged"}` {
		t.Fatalf("Payload = %s", env.Payload)
	}
}
