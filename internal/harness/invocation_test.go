package harness_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/exec"
	"github.com/carlosboeing/crossrev/internal/harness"
)

// A document is present when the orchestrator wrote it, which is the
// `[[ -n "$schema_file" ]]` every adapter tests.
func TestFilePresenceIsThePath(t *testing.T) {
	if (harness.File{}).Present() {
		t.Error("a document nobody wrote is not present")
	}
	if (harness.File{Text: "{}"}).Present() {
		t.Error("text with no path is not a document the adapters see")
	}
	if !(harness.File{Path: "/tmp/schema.json"}).Present() {
		t.Error("a path is what says the document exists")
	}
}

// The string "null" counts as unset, because that is what jq prints for a
// missing key and what every adapter's guard tests for.
func TestEndpointNamedTreatsNullAsUnset(t *testing.T) {
	for _, tt := range []struct {
		name  string
		named bool
	}{
		{name: "", named: false},
		{name: "null", named: false},
		{name: "an-endpoint", named: true},
	} {
		if got := (harness.Endpoint{Name: tt.name}).Named(); got != tt.named {
			t.Errorf("Endpoint{Name: %q}.Named() = %t, want %t", tt.name, got, tt.named)
		}
	}
}

// A model or effort of "null" is not a value either, so the flag is omitted.
func TestNullModelAndEffortAreOmitted(t *testing.T) {
	inv := invocation(t, "claude", false)
	inv.Model, inv.Effort = "null", "null"

	spec, err := claudeAdapter(t).Spec(inv)
	if err != nil {
		t.Fatalf("building the spec: %v", err)
	}
	for _, flag := range []string{"--model", "--effort"} {
		if strings.Contains(strings.Join(spec.Args, " "), flag) {
			t.Errorf("%s was passed for a value of \"null\": %v", flag, spec.Args)
		}
	}
}

// The envelope marshals to the object lib/run.sh reads by key.
func TestEnvelopeJSONShape(t *testing.T) {
	adapter := claudeAdapter(t)
	inv := invocation(t, "claude", false)

	failure := adapter.Envelope(inv, exec.Result{ExitCode: 1})
	encoded, err := json.Marshal(failure)
	if err != nil {
		t.Fatalf("marshalling the failure envelope: %v", err)
	}
	want := `{"ok":false,"payload":null,"harness":"claude","endpoint":null,"model_reported":null,` +
		`"effort_reported":null,"tokens":null,"usage":null,` +
		`"error":"claude exited 1 with no output on either stream"}`
	if string(encoded) != want {
		t.Errorf("failure envelope = %s,\n              want %s", encoded, want)
	}

	// A failure whose message is empty still carries a STRING rather than a
	// null, because `jq -cn --arg e "$msg"` writes one (lib/adapters/codex.sh:100).
	empty := codexAdapter(t).Envelope(invocation(t, "codex", false), exec.Result{ExitCode: 2})
	encodedEmpty, err := json.Marshal(empty)
	if err != nil {
		t.Fatalf("marshalling: %v", err)
	}
	if !strings.Contains(string(encodedEmpty), `"error":""`) {
		t.Errorf("an empty message is the empty string, not null: %s", encodedEmpty)
	}
}

// --- the divergence guard ----------------------------------------------------

// The two endpoint variables are process-scoped, so a leg that leaks them
// silently redirects the OTHER leg too and the loop completes normally with the
// cross-model property gone.
func TestAssertEnvCleanNamesEveryLeakedVariable(t *testing.T) {
	tests := []struct {
		name    string
		env     []string
		leaked  string
		refused bool
	}{
		{name: "a clean environment", env: []string{"PATH=/usr/bin"}},
		{name: "an empty value is not set", env: []string{"ANTHROPIC_BASE_URL="}},
		{name: "the base URL", env: []string{"ANTHROPIC_BASE_URL=https://x"}, leaked: "ANTHROPIC_BASE_URL", refused: true},
		{name: "the token", env: []string{"ANTHROPIC_AUTH_TOKEN=t"}, leaked: "ANTHROPIC_AUTH_TOKEN", refused: true},
		{
			name:    "both, in the order the shell lists them",
			env:     []string{"ANTHROPIC_AUTH_TOKEN=t", "ANTHROPIC_BASE_URL=https://x"},
			leaked:  "ANTHROPIC_BASE_URL ANTHROPIC_AUTH_TOKEN",
			refused: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := harness.AssertEnvClean(tt.env)
			if !tt.refused {
				if err != nil {
					t.Fatalf("err = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatal("a leaked endpoint variable was accepted")
			}
			if !errors.Is(err, harness.ErrEndpointLeaked) {
				t.Fatalf("err = %v, want ErrEndpointLeaked", err)
			}
			var refusal *harness.Refusal
			if !errors.As(err, &refusal) {
				t.Fatal("the error is not a Refusal")
			}
			want := "these endpoint variables are set in the environment CrossRev inherited: " + tt.leaked
			if refusal.Reason != want {
				t.Errorf("Reason = %q, want %q", refusal.Reason, want)
			}
			// The refusal never prints the value.
			if strings.Contains(refusal.Reason+refusal.Action, "https://x") ||
				strings.Contains(refusal.Reason+refusal.Action, "=t") {
				t.Error("the refusal quotes the value it is refusing")
			}
		})
	}
}

// Layer one's other half, cross-checked against legs_configured_difference.
func TestConfiguredDifferenceFreezesTheShellAnswers(t *testing.T) {
	// legs_configured_difference lived in lib/legs.sh. The five answers below
	// are what it printed, measured at the native cutover; the shell is
	// removed, so they are frozen here.
	cases := []struct {
		name     string
		reviewer harness.LegSettings
		resolver harness.LegSettings
		want     string
	}{
		{name: "identical legs",
			reviewer: harness.LegSettings{Harness: "claude", Endpoint: "vendor", Model: "m"},
			resolver: harness.LegSettings{Harness: "claude", Endpoint: "vendor", Model: "m"},
			want:     "same"},
		{name: "another harness",
			reviewer: harness.LegSettings{Harness: "claude", Endpoint: "vendor", Model: "m"},
			resolver: harness.LegSettings{Harness: "codex", Endpoint: "vendor", Model: "m"},
			want:     "different"},
		{name: "another endpoint",
			reviewer: harness.LegSettings{Harness: "claude", Endpoint: "vendor", Model: "m"},
			resolver: harness.LegSettings{Harness: "claude", Endpoint: "an-endpoint", Model: "m"},
			want:     "different"},
		{name: "another model",
			reviewer: harness.LegSettings{Harness: "claude", Endpoint: "vendor", Model: "m"},
			resolver: harness.LegSettings{Harness: "claude", Endpoint: "vendor", Model: "n"},
			want:     "different"},
		{name: "two empty settings",
			reviewer: harness.LegSettings{},
			resolver: harness.LegSettings{},
			want:     "same"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if got := harness.ConfiguredDifference(tt.reviewer, tt.resolver); got != tt.want {
				t.Errorf("Go  = %q\nwant = %q", got, tt.want)
			}
		})
	}
}

// Layer two: do not halt merely because a harness reports no model — that would
// disqualify the codex adapter for a field Codex does not emit.
func TestAssertModelsDivergedOnlyHaltsOnAProvenConvergence(t *testing.T) {
	tests := []struct {
		name       string
		configured string
		reviewer   string
		resolver   string
		refused    bool
	}{
		{name: "one model was asked for", configured: "same", reviewer: "m", resolver: "m"},
		{name: "the reviewer reports none", configured: "different", reviewer: "", resolver: "m"},
		{name: "the resolver reports none", configured: "different", reviewer: "m", resolver: ""},
		{name: "a null report is no report", configured: "different", reviewer: "null", resolver: "m"},
		{name: "they differ", configured: "different", reviewer: "m", resolver: "n"},
		{name: "the same model answered each", configured: "different", reviewer: "m", resolver: "m", refused: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := harness.AssertModelsDiverged(tt.configured, tt.reviewer, tt.resolver)
			if !tt.refused {
				if err != nil {
					t.Fatalf("err = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, harness.ErrModelsConverged) {
				t.Fatalf("err = %v, want ErrModelsConverged", err)
			}
			var refusal *harness.Refusal
			if !errors.As(err, &refusal) {
				t.Fatal("the error is not a Refusal")
			}
			want := "both legs were configured to differ but the same model answered each: m"
			if refusal.Reason != want {
				t.Errorf("Reason = %q, want %q", refusal.Reason, want)
			}
		})
	}
}

// _same_model lived in lib/run.sh. The table below is what it answered,
// measured at the native cutover; the shell is removed, so the answers are
// frozen here. TestSameModelHoldsInBothDirections pins the containment rule
// itself; this pins the edges, including the empty and case-folded ones.
func TestSameModelFreezesTheShellAnswers(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"claude-opus-5", "claude-opus-5", true},
		{"claude-opus-5", "claude-opus-5-20260101", true},
		{"opus", "claude-opus-4-5-20251101", true},
		{"claude-opus-5", "claude-sonnet-5", false},
		{"", "claude-opus-5", false},
		{"claude-opus-5", "", false},
		{"", "", false},
		{"CLAUDE-OPUS-5", "claude-opus-5", true},
		{"claude-opus-5", "CLAUDE-OPUS-5-20260101", true},
	}
	for _, tt := range cases {
		t.Run(tt.a+" against "+tt.b, func(t *testing.T) {
			if got := harness.SameModel(tt.a, tt.b); got != tt.want {
				t.Errorf("Go  = %v\nwant = %v", got, tt.want)
			}
		})
	}
}

// A harness that reports no model does not halt the run.
//
// AssertModelsDiverged's own comment says this in bold terms and names the
// codex adapter, which emits no model field at all. Dropping the `wanted`
// guard survived the suite: two legs that both report nothing then compare
// "" against "", find them equal, and halt a run where nothing converged.
//
// The other half is the case the guard must NOT swallow: two legs configured
// to differ, both reporting the same model, is the substitution the
// cross-model design exists to catch.
func TestAssertModelsDivergedIgnoresAnUnreportedModel(t *testing.T) {
	tests := []struct {
		name       string
		configured string
		reviewer   string
		resolver   string
		wantHalt   bool
	}{
		{name: "neither leg reports a model", configured: harness.ConfiguredDifferent,
			reviewer: "", resolver: ""},
		{name: "the reviewer reports none", configured: harness.ConfiguredDifferent,
			reviewer: "", resolver: "claude-opus-4-5"},
		{name: "the resolver reports none", configured: harness.ConfiguredDifferent,
			reviewer: "gpt-5-codex", resolver: ""},
		{name: "a literal null reads as unreported", configured: harness.ConfiguredDifferent,
			reviewer: "null", resolver: "null"},
		{name: "different models are the healthy case", configured: harness.ConfiguredDifferent,
			reviewer: "gpt-5-codex", resolver: "claude-opus-4-5"},
		{name: "one model was asked for, so a match is expected", configured: harness.ConfiguredSame,
			reviewer: "claude-opus-4-5", resolver: "claude-opus-4-5"},
		{name: "configured to differ and one model answered both",
			configured: harness.ConfiguredDifferent,
			reviewer:   "claude-opus-4-5", resolver: "claude-opus-4-5", wantHalt: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := harness.AssertModelsDiverged(tt.configured, tt.reviewer, tt.resolver)
			if tt.wantHalt {
				if err == nil {
					t.Fatal("the same model answered both legs and the run was not halted")
				}
				var refusal *harness.Refusal
				if !errors.As(err, &refusal) || refusal.Kind != harness.ErrModelsConverged {
					t.Errorf("err = %v, want an ErrModelsConverged refusal", err)
				}
				return
			}
			if err != nil {
				t.Errorf("the run was halted on %q vs %q: %v", tt.reviewer, tt.resolver, err)
			}
		})
	}
}

// Containment is tested in both directions.
//
// SameModel answers `strings.Contains(got, want) || strings.Contains(want, got)`
// and every existing vector put the shorter string in `want`, so the second
// clause was never the one that answered. An alias configured against a pinned
// id and a pinned id configured against an alias are both real: lib/run.sh
// compares what was asked for with what answered, and either can be the more
// precise of the two.
func TestSameModelHoldsInBothDirections(t *testing.T) {
	pairs := []struct {
		short string
		long  string
	}{
		{short: "opus", long: "claude-opus-4-5-20251101"},
		{short: "claude-opus-4-5", long: "claude-opus-4-5-20251101"},
		{short: "gpt-5", long: "gpt-5-codex"},
	}

	for _, pair := range pairs {
		t.Run(pair.short+" vs "+pair.long, func(t *testing.T) {
			if !harness.SameModel(pair.short, pair.long) {
				t.Errorf("SameModel(%q, %q) = false, want true", pair.short, pair.long)
			}
			if !harness.SameModel(pair.long, pair.short) {
				t.Errorf("SameModel(%q, %q) = false; the shorter name is what was asked for in one direction and what answered in the other",
					pair.long, pair.short)
			}
		})
	}

	// A substitution shares no token, in either order.
	for _, pair := range [][2]string{
		{"claude-opus-4-5", "gpt-5-codex"},
		{"gpt-5-codex", "claude-opus-4-5"},
		{"", "claude-opus-4-5"},
		{"claude-opus-4-5", ""},
	} {
		if harness.SameModel(pair[0], pair[1]) {
			t.Errorf("SameModel(%q, %q) = true, want false", pair[0], pair[1])
		}
	}
}
