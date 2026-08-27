package core

import (
	"encoding/json"
	"strings"
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

// run_invoke at lib/run.sh:774 hands the adapter a prompt, a schema and a
// workdir, and adapter_claude at lib/adapters/claude.sh:15-17 defaults only
// model, effort, endpoint and write. So the first three are required and the
// rest are not.
func TestNewInvocationRequestRefusesAnIncompleteInvocation(t *testing.T) {
	tests := []struct {
		name    string
		harness HarnessName
		prompt  string
		schema  string
		workdir string
		write   WriteCapability
	}{
		{name: "no harness", harness: "", prompt: "/tmp/p", schema: "/tmp/s", workdir: "/checkout", write: WriteNo},
		{name: "unknown harness", harness: "gemini", prompt: "/tmp/p", schema: "/tmp/s", workdir: "/checkout", write: WriteNo},
		{name: "no prompt", harness: HarnessClaude, prompt: "", schema: "/tmp/s", workdir: "/checkout", write: WriteNo},
		{name: "no schema", harness: HarnessClaude, prompt: "/tmp/p", schema: "", workdir: "/checkout", write: WriteNo},
		{name: "no workdir", harness: HarnessClaude, prompt: "/tmp/p", schema: "/tmp/s", workdir: "", write: WriteNo},
		{name: "no write capability", harness: HarnessClaude, prompt: "/tmp/p", schema: "/tmp/s", workdir: "/checkout", write: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewInvocationRequest(tt.harness, tt.prompt, tt.schema, tt.workdir, "", "", "", tt.write)
			if err == nil {
				t.Fatal("NewInvocationRequest() = nil error, want a refusal")
			}
		})
	}
}

func TestNewInvocationRequestCarriesTheAdapterArguments(t *testing.T) {
	req, err := NewInvocationRequest(HarnessClaude, "/tmp/prompt", "/tmp/schema.json",
		"/checkout", "opus", "high", "", WriteNo)
	if err != nil {
		t.Fatalf("NewInvocationRequest: %v", err)
	}
	want := InvocationRequest{
		Harness:    HarnessClaude,
		PromptPath: "/tmp/prompt",
		SchemaPath: "/tmp/schema.json",
		Workdir:    "/checkout",
		Model:      "opus",
		Effort:     "high",
		Write:      WriteNo,
	}
	if req != want {
		t.Fatalf("NewInvocationRequest() = %+v, want %+v", req, want)
	}
	// Model, effort and endpoint stay optional: the shell defaults all three.
	if _, err := NewInvocationRequest(HarnessCodex, "/tmp/p", "/tmp/s", "/checkout", "", "", "", WriteYes); err != nil {
		t.Fatalf("NewInvocationRequest with no model, effort or endpoint: %v", err)
	}
}

// Every other closed vocabulary in the package has a ParseX. Without these
// three, a marker reader would grow its own validator for the same sets.
func TestParseBillingAcceptsTheFourWrittenValues(t *testing.T) {
	// The empty string is the mode usage_billing_for falls through to, and
	// lib/usage.sh:492 writes it to the marker as null, which reads back as "".
	for _, in := range []string{"", "endpoint", "api", "subscription"} {
		got, err := ParseBilling(in)
		if err != nil || string(got) != in {
			t.Fatalf("ParseBilling(%q) = %q, %v", in, got, err)
		}
	}
	for _, in := range []string{"free", "Endpoint", "vendor"} {
		if _, err := ParseBilling(in); err == nil {
			t.Fatalf("ParseBilling(%q) = nil error, want a refusal", in)
		}
	}
}

func TestParseCostSourceAcceptsTheThreeWrittenValues(t *testing.T) {
	for _, in := range []string{"", "harness", "table"} {
		got, err := ParseCostSource(in)
		if err != nil || string(got) != in {
			t.Fatalf("ParseCostSource(%q) = %q, %v", in, got, err)
		}
	}
	for _, in := range []string{"vendor", "Harness", "prices"} {
		if _, err := ParseCostSource(in); err == nil {
			t.Fatalf("ParseCostSource(%q) = nil error, want a refusal", in)
		}
	}
}

func TestParseWriteCapabilityAcceptsOnlyNoAndYes(t *testing.T) {
	for _, in := range []string{"no", "yes"} {
		got, err := ParseWriteCapability(in)
		if err != nil || string(got) != in {
			t.Fatalf("ParseWriteCapability(%q) = %q, %v", in, got, err)
		}
	}
	// The empty string is not the Bash default: lib/run.sh:488 writes the word
	// `no`, so an unset capability is a bug rather than a permission.
	for _, in := range []string{"", "true", "Yes", "readonly"} {
		if _, err := ParseWriteCapability(in); err == nil {
			t.Fatalf("ParseWriteCapability(%q) = nil error, want a refusal", in)
		}
	}
}

// lib/usage.sh:520 sums the four cache buckets and nothing else.
func TestUsageCachedSumsTheFourCacheBuckets(t *testing.T) {
	u := Usage{
		InputFresh:        1,
		CacheRead:         2,
		CacheWrite5m:      4,
		CacheWrite1h:      8,
		CacheWriteUnsplit: 16,
		Output:            32,
	}
	if got := u.Cached(); got != 30 {
		t.Fatalf("Cached() = %d, want 30", got)
	}
	if got := (Usage{}).Cached(); got != 0 {
		t.Fatalf("Cached() = %d on a zero record, want 0", got)
	}
}

// The adapters return the envelope shape lib/run.sh:840 reads. All nine keys
// come from the adapter itself; nothing is attached afterwards.
func TestEnvelopeDecodesTheAdapterFailureShape(t *testing.T) {
	const raw = `{"ok":false,"payload":null,"error":"the adapter returned nothing at all"}`
	var env Envelope
	if err := json.Unmarshal([]byte(raw), &env); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if env.OK {
		t.Fatal("OK = true for a failure envelope")
	}
	if env.Error == nil || *env.Error != "the adapter returned nothing at all" {
		t.Fatalf("Error = %v", env.Error)
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

// The record `usage_zero` writes at lib/usage.sh:31, byte for byte. It is
// embedded verbatim in a pull-request comment body, so a key in the wrong place
// or a "" where the shell writes null is a diff on every pull request forever.
const usageZeroBytes = `{"input_fresh":0,"cache_read":0,"cache_write_5m":0,"cache_write_1h":0,"cache_write_unsplit":0,"output":0,"reasoning":null,"cost_usd":null,"cost_source":null,"price_table":null,"billing":null,"models":null,"derived":[]}`

// usage_with_total at lib/usage.sh:38 is `jq -c '.total = (…)'`, and jq appends
// a new key at the end.
const usageWithTotalBytes = `{"input_fresh":0,"cache_read":0,"cache_write_5m":0,"cache_write_1h":0,"cache_write_unsplit":0,"output":0,"reasoning":null,"cost_usd":null,"cost_source":null,"price_table":null,"billing":null,"models":null,"derived":[],"total":0}`

func TestZeroUsageMarshalsAsUsageZeroWritesIt(t *testing.T) {
	b, err := json.Marshal(Usage{})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if got := string(b); got != usageZeroBytes {
		t.Fatalf("Marshal(Usage{}) =\n\t%s\nwant\n\t%s", got, usageZeroBytes)
	}
}

func TestUsageWithATotalPutsTotalLastLikeJQ(t *testing.T) {
	u := Usage{}
	total := u.TokenTotal()
	u.Total = &total
	b, err := json.Marshal(u)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if got := string(b); got != usageWithTotalBytes {
		t.Fatalf("Marshal(usage with total) =\n\t%s\nwant\n\t%s", got, usageWithTotalBytes)
	}
}

// lib/usage.sh:81 and lib/usage.sh:93 write `derived:[]` and never null, so a
// nil slice has to render as the empty array the shell writes.
func TestDerivedRendersAsAnEmptyArrayAndModelsAsNull(t *testing.T) {
	b, err := json.Marshal(Usage{})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !strings.Contains(string(b), `"derived":[]`) {
		t.Fatalf("Marshal(Usage{}) does not carry \"derived\":[]: %s", b)
	}
	if !strings.Contains(string(b), `"models":null`) {
		t.Fatalf("Marshal(Usage{}) does not carry \"models\":null: %s", b)
	}
	u := Usage{Derived: []string{"input_fresh"}}
	b, err = json.Marshal(u)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !strings.Contains(string(b), `"derived":["input_fresh"]`) {
		t.Fatalf("a populated derived list did not survive: %s", b)
	}
}

// The nine keys every adapter writes, in the order it writes them:
// lib/adapters/claude.sh:120-121 and :154-158, lib/adapters/codex.sh:96-97,
// lib/adapters/agy.sh:105-106 and :137-139.
func TestEnvelopeMarshalsTheFailureShapeAnAdapterWrites(t *testing.T) {
	const want = `{"ok":false,"payload":null,"harness":"claude","endpoint":null,"model_reported":null,"effort_reported":null,"tokens":null,"usage":null,"error":"the adapter returned nothing at all"}`
	msg := "the adapter returned nothing at all"
	b, err := json.Marshal(Envelope{Harness: HarnessClaude, Error: &msg})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if got := string(b); got != want {
		t.Fatalf("Marshal(failure envelope) =\n\t%s\nwant\n\t%s", got, want)
	}
}

func TestEnvelopeMarshalsTheSuccessShapeAnAdapterWrites(t *testing.T) {
	const want = `{"ok":true,"payload":{"verdict":"converged"},"harness":"claude","endpoint":"vendor","model_reported":"claude-opus-4-1","effort_reported":null,"tokens":63,"usage":null,"error":null}`
	endpoint := EndpointVendor
	model := "claude-opus-4-1"
	tokens := int64(63)
	b, err := json.Marshal(Envelope{
		OK:            true,
		Payload:       json.RawMessage(`{"verdict":"converged"}`),
		Harness:       HarnessClaude,
		Endpoint:      &endpoint,
		ModelReported: &model,
		Tokens:        &tokens,
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if got := string(b); got != want {
		t.Fatalf("Marshal(success envelope) =\n\t%s\nwant\n\t%s", got, want)
	}
}

// A populated record, produced by piping usage_zero through the same key
// assignments usage_parse_claude makes and then usage_with_total. Every
// nullable field carries a value here, which the zero record cannot check.
func TestAPopulatedUsageMarshalsAsTheShellWritesIt(t *testing.T) {
	const want = `{"input_fresh":1200,"cache_read":800,"cache_write_5m":64,"cache_write_1h":0,"cache_write_unsplit":0,"output":350,"reasoning":90,"cost_usd":0.4213,"cost_source":"harness","price_table":null,"billing":"subscription","models":[{"id":"claude-opus-4-1","total":2414}],"derived":["input_fresh"],"total":2414}`

	reasoning := int64(90)
	cost := 0.4213
	source := CostSourceHarness
	billing := BillingSubscription
	u := Usage{
		InputFresh:   1200,
		CacheRead:    800,
		CacheWrite5m: 64,
		Output:       350,
		Reasoning:    &reasoning,
		CostUSD:      &cost,
		CostSource:   &source,
		Billing:      &billing,
		Models:       []ModelUsage{{ID: "claude-opus-4-1", Total: 2414}},
		Derived:      []string{"input_fresh"},
	}
	total := u.TokenTotal()
	u.Total = &total

	b, err := json.Marshal(u)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if got := string(b); got != want {
		t.Fatalf("Marshal(populated usage) =\n\t%s\nwant\n\t%s", got, want)
	}

	// And back again, so a record read off a marker is the record that was
	// written to it.
	var back Usage
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	again, err := json.Marshal(back)
	if err != nil {
		t.Fatalf("Marshal after round trip: %v", err)
	}
	if got := string(again); got != want {
		t.Fatalf("round trip =\n\t%s\nwant\n\t%s", got, want)
	}
}
