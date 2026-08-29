package harness_test

import (
	"encoding/json"
	"errors"
	osexec "os/exec"
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
func TestConfiguredDifferenceMatchesTheShell(t *testing.T) {
	for _, tool := range []string{"bash"} {
		if _, err := osexec.LookPath(tool); err != nil {
			t.Skipf("%s is not on PATH, so the shell side cannot be run", tool)
		}
	}

	// Nothing is spliced into this script: the six settings arrive as arguments.
	const script = `
set -uo pipefail
ROOT="$1"; shift
export ROOT
# shellcheck source=/dev/null
source "$ROOT/lib/ui.sh"
# shellcheck source=/dev/null
source "$ROOT/lib/legs.sh"
legs_configured_difference "$@"
`

	cases := []struct{ reviewer, resolver harness.LegSettings }{
		{harness.LegSettings{"claude", "vendor", "m"}, harness.LegSettings{"claude", "vendor", "m"}},
		{harness.LegSettings{"claude", "vendor", "m"}, harness.LegSettings{"codex", "vendor", "m"}},
		{harness.LegSettings{"claude", "vendor", "m"}, harness.LegSettings{"claude", "an-endpoint", "m"}},
		{harness.LegSettings{"claude", "vendor", "m"}, harness.LegSettings{"claude", "vendor", "n"}},
		{harness.LegSettings{}, harness.LegSettings{}},
	}
	for _, tt := range cases {
		name := tt.reviewer.Harness + "/" + tt.reviewer.Endpoint + "/" + tt.reviewer.Model +
			" against " + tt.resolver.Harness + "/" + tt.resolver.Endpoint + "/" + tt.resolver.Model
		t.Run(name, func(t *testing.T) {
			cmd := osexec.Command("bash", "-c", script, "bash", repoRoot,
				tt.reviewer.Harness, tt.reviewer.Endpoint, tt.reviewer.Model,
				tt.resolver.Harness, tt.resolver.Endpoint, tt.resolver.Model)
			out, err := cmd.Output()
			if err != nil {
				t.Fatalf("running legs_configured_difference: %v", err)
			}
			if got := harness.ConfiguredDifference(tt.reviewer, tt.resolver); got != string(out) {
				t.Errorf("Go = %q, Bash = %q", got, string(out))
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

// _same_model, run rather than described.
//
// The function is extracted from lib/run.sh by name rather than sourcing the
// whole file, which defines several hundred functions and runs top-level guards.
// An extraction that failed would exit 2 rather than pass vacuously.
func TestSameModelMatchesTheShell(t *testing.T) {
	if _, err := osexec.LookPath("bash"); err != nil {
		t.Skip("bash is not on PATH, so the shell side cannot be run")
	}

	// Nothing is spliced into this script: the two names arrive as arguments.
	const script = `
set -uo pipefail
ROOT="$1"
eval "$(sed -n '/^_same_model() {$/,/^}$/p' "$ROOT/lib/run.sh")"
declare -F _same_model >/dev/null || { printf 'the function could not be extracted\n' >&2; exit 2; }
if _same_model "$2" "$3"; then printf 'same'; else printf 'differ'; fi
`

	cases := [][2]string{
		{"claude-opus-5", "claude-opus-5"},
		{"claude-opus-5", "claude-opus-5-20260101"},
		{"opus", "claude-opus-4-5-20251101"},
		{"claude-opus-5", "claude-sonnet-5"},
		{"", "claude-opus-5"},
		{"claude-opus-5", ""},
		{"", ""},
		{"CLAUDE-OPUS-5", "claude-opus-5"},
		{"claude-opus-5", "CLAUDE-OPUS-5-20260101"},
	}
	for _, tt := range cases {
		t.Run(tt[0]+" against "+tt[1], func(t *testing.T) {
			cmd := osexec.Command("bash", "-c", script, "bash", repoRoot, tt[0], tt[1])
			out, err := cmd.Output()
			if err != nil {
				t.Fatalf("running _same_model: %v", err)
			}
			got := "differ"
			if harness.SameModel(tt[0], tt[1]) {
				got = "same"
			}
			if got != string(out) {
				t.Errorf("Go = %q, Bash = %q", got, string(out))
			}
		})
	}
}
