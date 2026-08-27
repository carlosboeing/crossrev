package config_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/config"
	"github.com/carlosboeing/crossrev/internal/core"
)

// `false`, zero and absent are three states, not one. Collapsing them is the
// failure lib/config.sh:229-231 exists to prevent: jq's alternative operator
// treats false as empty, so a lenient read of `keep_transcripts: false` reports
// the key unset and refuses every config.
func TestFalseIsNotAbsent(t *testing.T) {
	tree := files{"": {".github/crossrev.yml": "version: 1\nlogs:\n  keep_transcripts: false\n"}}
	loaded := mustLoad(t, core.Revision{}, tree)

	// cfg_get and cfg_get_json both run the value through jq's alternative
	// operator, so both read `false` as empty (lib/config.sh:303-304). That is
	// exactly why the validator reads this one key another way, and why the
	// merge itself has to keep the value.
	if got := loaded.Get(".logs.keep_transcripts"); got != "" {
		t.Errorf("Get on false = %q, want the empty string jq's // gives", got)
	}
	if got := string(loaded.GetJSON(".logs.keep_transcripts")); got != "null" {
		t.Errorf("GetJSON on false = %s, want the null jq's // gives", got)
	}
	merged, err := loaded.MergedJSON()
	if err != nil {
		t.Fatalf("MergedJSON: %v", err)
	}
	if !strings.Contains(string(merged), `"keep_transcripts":false`) {
		t.Errorf("the merge lost keep_transcripts: %s", merged)
	}
	// The state that must not collapse: a stated false loads, and an absent
	// key takes the same default, but neither is refused as unset.
	absent := mustLoad(t, core.Revision{}, files{"": {".github/crossrev.yml": "version: 1\n"}})
	if got := string(mustJSON(t, absent)); !strings.Contains(got, `"keep_transcripts":false`) {
		t.Errorf("an absent keep_transcripts did not take its default: %s", got)
	}
}

// Zero survives the merge for a key nothing validates. `max_files_changed_per_pr: 0`
// is the documented "no cap" value, and a read that collapsed it into absent
// would restore the default of 200.
func TestZeroIsNotAbsent(t *testing.T) {
	tree := files{"": {".github/crossrev.yml": "version: 1\npolicy:\n  max_files_changed_per_pr: 0\n"}}
	loaded := mustLoad(t, core.Revision{}, tree)

	if got := loaded.Get(".policy.max_files_changed_per_pr"); got != "0" {
		t.Errorf("max_files_changed_per_pr = %q, want %q", got, "0")
	}
	if got := string(loaded.GetJSON(".policy.max_files_changed_per_pr")); got != "0" {
		t.Errorf("max_files_changed_per_pr as JSON = %s, want 0", got)
	}
}

// A key CrossRev does not know is carried into the merge rather than refused.
// jq's `*` keeps it, and no broad unknown-field refusal exists in Bash to add
// here.
func TestUnknownKeysSurviveTheMerge(t *testing.T) {
	tree := files{"": {".github/crossrev.yml": "version: 1\nnot_a_key_crossrev_knows: 42\npolicy:\n  invented: true\n"}}
	loaded := mustLoad(t, core.Revision{}, tree)

	if got := loaded.Get(".not_a_key_crossrev_knows"); got != "42" {
		t.Errorf("the unknown top-level key = %q, want %q", got, "42")
	}
	if got := loaded.Get(".policy.invented"); got != "true" {
		t.Errorf("the unknown nested key = %q, want %q", got, "true")
	}
	if got := loaded.Get(".policy.min_fix_severity"); got != "medium" {
		t.Errorf("the unknown key displaced a default: min_fix_severity = %q", got)
	}
}

// The operator file affects named endpoints only. Anything else in it is read
// for its version and then ignored, because policy is repository-specific and
// belongs in the repository (lib/config.sh:4-8, 174-184).
func TestTheOperatorFileAffectsNamedEndpointsOnly(t *testing.T) {
	operatorPath := config.OperatorPath()
	tree := files{"": {
		".github/crossrev.yml": "version: 1\npolicy:\n  max_passes_per_cycle: 5\n",
		operatorPath: "version: 1\npolicy:\n  max_passes_per_cycle: 9\nmode: automated\n" +
			"endpoints:\n  mine:\n    base_url: http://local/\n    token_env: TOKEN\n",
	}}
	loaded := mustLoad(t, core.Revision{}, tree)

	if got := loaded.Get(".policy.max_passes_per_cycle"); got != "5" {
		t.Errorf("the operator file changed policy: max_passes_per_cycle = %q, want %q", got, "5")
	}
	if got := loaded.Get(".mode"); got != "local" {
		t.Errorf("the operator file changed mode: %q, want %q", got, "local")
	}
	if got := loaded.Get(".endpoints.mine.base_url"); got != "http://local/" {
		t.Errorf("the operator endpoint is missing: %q", got)
	}
}

// The endpoint merge is recursive, like every other jq `*`. An operator file
// that repoints one field of an endpoint keeps the repository's other fields.
func TestTheEndpointMergeIsRecursive(t *testing.T) {
	operatorPath := config.OperatorPath()
	tree := files{"": {
		".github/crossrev.yml": "version: 1\nendpoints:\n  kimi:\n    base_url: https://public.example/\n    token_env: KIMI_API_KEY\n",
		operatorPath:           "version: 1\nendpoints:\n  kimi:\n    base_url: http://mine.local/\n",
	}}
	endpoint, err := mustLoad(t, core.Revision{}, tree).Endpoint("kimi")
	if err != nil {
		t.Fatalf("Endpoint: %v", err)
	}
	if endpoint.BaseURL != "http://mine.local/" {
		t.Errorf("base_url = %q, want the operator's", endpoint.BaseURL)
	}
	if endpoint.TokenEnv != "KIMI_API_KEY" {
		t.Errorf("token_env = %q, want the repository's", endpoint.TokenEnv)
	}
}

// yq resolves an integer before it prints and leaves a float as written, and a
// refusal quotes what jq read back. `retention_days: 5.0` must therefore be
// refused rather than rounded into an accepted 5.
func TestNumbersArriveAsTheTextJqReads(t *testing.T) {
	tests := []struct {
		name     string
		document string
		path     string
		want     string
	}{
		{"hexadecimal", "version: 1\npolicy:\n  max_prs_per_day: 0x10\n", ".policy.max_prs_per_day", "16"},
		{"leading zeros", "version: 1\npolicy:\n  max_prs_per_day: 007\n", ".policy.max_prs_per_day", "7"},
		{"underscores", "version: 1\npolicy:\n  max_prs_per_day: 1_000\n", ".policy.max_prs_per_day", "1000"},
		{"a float keeps its text", "version: 1\npolicy:\n  max_prs_per_day: 5.0\n", ".policy.max_prs_per_day", "5.0"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			loaded := mustLoad(t, core.Revision{}, files{"": {".github/crossrev.yml": test.document}})
			if got := loaded.Get(test.path); got != test.want {
				t.Errorf("%s = %q, want %q", test.path, got, test.want)
			}
		})
	}
}

// go-yaml's own resolution is not yq's, and the Bash reads every value back
// through jq as the text yq wrote. The literals below are the ones where the
// two differ, measured against `yq -o=json -I=0 '.'` directly.
func TestNumbersAreResolvedTheWayYqResolvesThem(t *testing.T) {
	tests := []struct {
		literal string
		want    string
	}{
		// A leading zero is decimal to yq and octal to go-yaml.
		{"0777", "777"},
		{"007", "7"},
		{"00", "0"},
		{"-07", "-7"},
		{"0_1", "1"},
		// A leading zero the octal reading cannot hold is a float, whose
		// literal is not valid JSON and has to be written back out.
		{"08", "8.0"},
		{"09", "9.0"},
		{"018", "18.0"},
		{"0800", "800.0"},
		{".5", "0.5"},
		{"-.5", "-0.5"},
		{"+0.0", "0.0"},
		{"08.0", "8.0"},
		{"0_8", "8.0"},
		// A literal already carrying a decimal point or an exponent is kept
		// as written, because a refusal quotes it.
		{"5.0", "5.0"},
		{"5.00", "5.00"},
		{"1e3", "1e3"},
		{"1E3", "1E3"},
		{"1.0e3", "1.0e3"},
		{"1_0.5", "10.5"},
		// The bases yq names, and the sign it drops.
		{"0x10", "16"},
		{"0xFF", "255"},
		{"0XFF", "255"},
		{"0o17", "15"},
		{"+5", "5"},
		{"1_000", "1000"},
	}
	for _, test := range tests {
		t.Run(test.literal, func(t *testing.T) {
			document := "version: 1\npolicy:\n  max_prs_per_day: " + test.literal + "\n"
			loaded := mustLoad(t, core.Revision{}, files{"": {".github/crossrev.yml": document}})
			if got := loaded.Get(".policy.max_prs_per_day"); got != test.want {
				t.Errorf("max_prs_per_day = %q, want %q", got, test.want)
			}
			if merged := mustJSON(t, loaded); !json.Valid(merged) {
				t.Errorf("the merge is not JSON, which is what config show prints: %s", merged)
			}
		})
	}
}

// A literal yq cannot read is an error there, so the Bash refuses the whole
// file for it (lib/config.sh:36-38, 43-46).
func TestALiteralYqCannotReadRefusesTheFile(t *testing.T) {
	for name, literal := range map[string]string{
		"a binary literal":        "0b101",
		"a zero binary literal":   "0b0",
		"an uppercase octal":      "0O17",
		"an integer beyond int64": "9223372036854775808",
		"an infinity":             ".inf",
		"a not-a-number":          ".nan",
	} {
		t.Run(name, func(t *testing.T) {
			document := "version: 1\npolicy:\n  max_prs_per_day: " + literal + "\n"
			tree := files{"": {".github/crossrev.yml": document}}
			if got := refusalFrom(t, core.Revision{}, tree).Message; got != "could not parse .github/crossrev.yml" {
				t.Errorf("message = %q, want the file refused as unparsable", got)
			}
		})
	}
}

// The value families see the text yq wrote, so `08` is refused as the 8.0 it
// resolves to rather than accepted as three digits (lib/config.sh:225, 262).
func TestALeadingZeroFloatIsRefusedAsTheShellRefusesIt(t *testing.T) {
	for _, test := range []struct{ document, wants string }{
		{"version: 1\nlogs:\n  retention_days: 08\n", "logs.retention_days is '8.0'"},
		{"version: 1\npolicy:\n  max_passes_per_cycle: 08\n", "policy.max_passes_per_cycle is '8.0'"},
	} {
		tree := files{"": {".github/crossrev.yml": test.document}}
		if got := refusalFrom(t, core.Revision{}, tree).Message; !strings.Contains(got, test.wants) {
			t.Errorf("message = %q, want it to contain %q", got, test.wants)
		}
	}
	// And a leading-zero integer is decimal, so the bound is the one written.
	tree := files{"": {".github/crossrev.yml": "version: 1\npolicy:\n  max_passes_per_cycle: 0777\n"}}
	if got := mustLoad(t, core.Revision{}, tree).Get(".policy.max_passes_per_cycle"); got != "777" {
		t.Errorf("max_passes_per_cycle = %q, want %q", got, "777")
	}
}

// jq would raise a type error on `endpoints:` holding a scalar. Go declines to
// crash over it and merges nothing instead, which is what objectAt documents.
func TestAScalarEndpointsKeyMergesNothing(t *testing.T) {
	tree := files{"": {".github/crossrev.yml": "version: 1\nendpoints: nope\n"}}
	loaded := mustLoad(t, core.Revision{}, tree)
	if got := loaded.Merged.Object("endpoints").Len(); got != 0 {
		t.Errorf("the merge kept %d endpoints, want none", got)
	}
	if got := string(mustJSON(t, loaded)); !strings.Contains(got, `"endpoints":{}`) {
		t.Errorf("endpoints = %s, want an empty object", got)
	}
}

// Defaults builds a fresh tree per call and deepMerge clones what it starts
// from, so a loaded config can never write back into the defaults a later load
// will read. Clone's own comment states the guarantee and nothing else pins it.
func TestTheMergeNeverAliasesTheDefaults(t *testing.T) {
	tree := files{"": {".github/crossrev.yml": "version: 1\n"}}
	loaded := mustLoad(t, core.Revision{}, tree)
	loaded.Merged.Object("policy").Set("min_fix_severity", "changed")
	loaded.Merged.Object("backlog").Object("github_issues").Set("tracking_label", "changed")

	fresh := config.Defaults()
	if got := fresh.Object("policy").Value("min_fix_severity"); got != "medium" {
		t.Errorf("the merge wrote through to a default: min_fix_severity = %v", got)
	}
	if got := fresh.Object("backlog").Object("github_issues").Value("tracking_label"); got != "crossrev-review" {
		t.Errorf("the merge wrote through to a nested default: %v", got)
	}
	if got := mustLoad(t, core.Revision{}, tree).Get(".policy.min_fix_severity"); got != "medium" {
		t.Errorf("a second load inherited the first load's mutation: %q", got)
	}
}

// A float where a whole number is required is refused, and the refusal quotes
// the text rather than a rounded value.
func TestAFloatWhereAWholeNumberIsRequiredIsRefused(t *testing.T) {
	tree := files{"": {".github/crossrev.yml": "version: 1\nlogs:\n  retention_days: 5.0\n"}}
	if got := refusalFrom(t, core.Revision{}, tree).Message; !strings.Contains(got, "'5.0'") {
		t.Errorf("message = %q, want it to quote 5.0", got)
	}
}

// `version: 1.0` is a mismatch: the comparison is textual, because the key
// exists so that a future shape can be rejected by an old binary
// (lib/config.sh:298).
func TestTheVersionComparisonIsTextual(t *testing.T) {
	tree := files{"": {".github/crossrev.yml": "version: 1.0\n"}}
	if got := refusalFrom(t, core.Revision{}, tree).Message; !strings.Contains(got, "declares version 1.0") {
		t.Errorf("message = %q, want it to quote 1.0", got)
	}
}

// Get renders one value the way `jq -r "<path> // empty"` does at
// lib/config.sh:303: a string bare, an absent or null or false value empty, and
// anything else as its JSON.
func TestGetRendersTheWayJqDoes(t *testing.T) {
	tree := files{"": {".github/crossrev.yml": "version: 1\n" +
		"backlog:\n  github_issues:\n    labels: [a, b]\n" +
		"reviewer:\n  model: null\n"}}
	loaded := mustLoad(t, core.Revision{}, tree)

	tests := []struct {
		path string
		want string
	}{
		{".mode", "local"},
		{".reviewer.model", ""},
		{".logs.keep_transcripts", ""},
		{".policy.max_passes_per_cycle", "3"},
		{".backlog.github_issues.labels", `["a","b"]`},
		{".nothing.here.at.all", ""},
	}
	for _, test := range tests {
		if got := loaded.Get(test.path); got != test.want {
			t.Errorf("Get(%q) = %q, want %q", test.path, got, test.want)
		}
	}

	if got := string(loaded.GetJSON(".reviewer.model")); got != "null" {
		t.Errorf("GetJSON of a null = %s, want null", got)
	}
	if got := string(loaded.GetJSON(".backlog.github_issues.labels")); got != `["a","b"]` {
		t.Errorf("GetJSON of a list = %s", got)
	}
}

// The merge keeps the defaults' key order and appends what the repository adds,
// which is what jq's `*` does and what `crossrev config show` prints.
func TestTheMergeKeepsKeyOrder(t *testing.T) {
	tree := files{"": {".github/crossrev.yml": "version: 1\nzzz_added_last: 1\nmode: automated\n"}}
	merged, err := mustLoad(t, core.Revision{}, tree).MergedJSON()
	if err != nil {
		t.Fatalf("MergedJSON: %v", err)
	}
	text := string(merged)
	if !strings.HasPrefix(text, `{"version":1,"mode":"automated","runner":`) {
		t.Errorf("the merge reordered the defaults: %s", text[:min(80, len(text))])
	}
	if !strings.HasSuffix(text, `,"zzz_added_last":1}`) {
		t.Errorf("a new repository key is not appended last: %s", text)
	}
}

func mustJSON(t *testing.T, loaded *config.Config) []byte {
	t.Helper()
	encoded, err := loaded.MergedJSON()
	if err != nil {
		t.Fatalf("MergedJSON: %v", err)
	}
	return encoded
}
