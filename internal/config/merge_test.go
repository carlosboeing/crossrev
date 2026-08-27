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
// through jq as the text jq wrote after yq wrote it. The literals below are the
// ones where the readings differ, measured against `yq -o=json -I=0 '.' | jq -c`
// directly.
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
		{"1_0.5", "10.5"},
		// An exponent is where yq's text and jq's part company. jq keeps the
		// decimal and prints it in the to-scientific-string form, so the
		// exponent is signed, the case is fixed, and a small enough one is
		// written out in full instead.
		{"1e3", "1E+3"},
		{"1E3", "1E+3"},
		{"1E+3", "1E+3"},
		{"1e+3", "1E+3"},
		{"1e0003", "1E+3"},
		{"1.0e3", "1.0E+3"},
		{"1.5e3", "1.5E+3"},
		{"-1e3", "-1E+3"},
		{"6.02e23", "6.02E+23"},
		{"1e100", "1E+100"},
		{"1e1", "1E+1"},
		{"1e-3", "0.001"},
		{"1E-3", "0.001"},
		{"-1e-3", "-0.001"},
		{"1e-5", "0.00001"},
		{"1e-6", "0.000001"},
		{"1e-7", "1E-7"},
		{"1e0", "1"},
		{"0e0", "0"},
		{"-0e0", "-0"},
		{"0.5e1", "5"},
		// The digits past a float64 are kept, because the rewrite is textual.
		{"1.23456789012345678e3", "1234.56789012345678"},
		// Underscores are not JSON, so the literal is not the one kept: yq
		// writes the number back out rather than dropping the underscore.
		{"1_0e3", "10000.0"},
		{"+1e3", "1000.0"},
		{"5.e3", "5000.0"},
		{".5e3", "500.0"},
		// yq resolves the exact literal `-0` as a float; longer spellings of a
		// negative zero stay integers.
		{"-0", "-0.0"},
		{"-00", "0"},
		{"-0_0", "0"},
		{"-000", "0"},
		{"1000000.0", "1000000.0"},
		{"0.0001", "0.0001"},
		{"3.14", "3.14"},
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

// `endpoints:` holding a scalar is jq's `string and object cannot be
// multiplied`, and the Bash stops there with nothing loaded. Reading it
// leniently loaded a config with every named endpoint dropped.
func TestANonMappingEndpointsKeyIsRefused(t *testing.T) {
	for _, test := range []struct{ name, document, wants string }{
		{"a string", "version: 1\nendpoints: nope\n", "endpoints is a string, which is not a mapping"},
		{"a list", "version: 1\nendpoints:\n  - a\n", "endpoints is a list, which is not a mapping"},
		{"a number", "version: 1\nendpoints: 5\n", "endpoints is a number, which is not a mapping"},
		{"true", "version: 1\nendpoints: true\n", "endpoints is true or false, which is not a mapping"},
	} {
		t.Run(test.name, func(t *testing.T) {
			tree := files{"": {".github/crossrev.yml": test.document}}
			if got := refusalFrom(t, core.Revision{}, tree).Message; got != test.wants {
				t.Errorf("message = %q, want %q", got, test.wants)
			}
		})
	}
	// `null` and `false` are jq's `// {}`, so both contribute nothing and the
	// run carries on. Measured on both sides.
	for _, document := range []string{"version: 1\nendpoints: null\n", "version: 1\nendpoints: false\n"} {
		loaded := mustLoad(t, core.Revision{}, files{"": {".github/crossrev.yml": document}})
		if got := string(mustJSON(t, loaded)); !strings.Contains(got, `"endpoints":{}`) {
			t.Errorf("endpoints = %s, want an empty object", got)
		}
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
		// `jq -r` pretty-prints a composite, and a refusal quoting one reads
		// it through here.
		{".backlog.github_issues.labels", "[\n  \"a\",\n  \"b\"\n]"},
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

// A nested key holding something other than a mapping is a jq type error, and
// the Bash stops with nothing loaded. `.policy.min_fix_severity` against
// `policy: "x"` is `Cannot index string with string`, not null.
//
// The values were run through `crossrev config show` on both sides. Bash exits
// 5 for every row below; Go loaded three of them at exit 0 and answered two
// more under a refusal naming a key nobody had written.
func TestANestedNonMappingIsRefusedByName(t *testing.T) {
	for _, test := range []struct{ name, document, wants string }{
		{"policy a string", "version: 1\npolicy: \"x\"\n", "policy is a string, which is not a mapping"},
		{"policy a list", "version: 1\npolicy:\n  - a\n", "policy is a list, which is not a mapping"},
		{"git a number", "version: 1\ngit: 5\n", "git is a number, which is not a mapping"},
		{"logs a list", "version: 1\nlogs:\n  - 1\n", "logs is a list, which is not a mapping"},
		{"backlog a string", "version: 1\nbacklog: hello\n", "backlog is a string, which is not a mapping"},
		{"backlog.repository a string", "version: 1\nbacklog:\n  repository: hello\n", "backlog.repository is a string, which is not a mapping"},
		{"a boolean", "version: 1\ngit: true\n", "git is true or false, which is not a mapping"},
	} {
		t.Run(test.name, func(t *testing.T) {
			tree := files{"": {".github/crossrev.yml": test.document}}
			if got := refusalFrom(t, core.Revision{}, tree).Message; got != test.wants {
				t.Errorf("message = %q, want %q", got, test.wants)
			}
		})
	}
}

// null is not the container refusal, because `null.foo` is null in jq rather
// than a type error. It reaches the value assertion underneath instead, and
// what happens there depends on whether that key has a fallback: jq's `*`
// replaces the defaults' mapping with the null, so the key below it is unset.
// Measured on both sides.
func TestANestedNullReachesTheValueAssertion(t *testing.T) {
	for _, test := range []struct{ name, document, wants string }{
		{"policy", "version: 1\npolicy: null\n", "policy.min_fix_severity is 'unset', which is not one of high, medium or low"},
		{"git", "version: 1\ngit:\n", "git.hooks is 'unset', which is not one of skip or run"},
		{"logs", "version: 1\nlogs: null\n", "logs.retention_days is 'unset', which is not a whole number of days above zero"},
	} {
		t.Run(test.name, func(t *testing.T) {
			tree := files{"": {".github/crossrev.yml": test.document}}
			if got := refusalFrom(t, core.Revision{}, tree).Message; got != test.wants {
				t.Errorf("message = %q, want %q", got, test.wants)
			}
		})
	}
	// The two backlog keys each read through a fallback, so a null there loads.
	for _, document := range []string{
		"version: 1\nbacklog: null\n",
		"version: 1\nbacklog:\n  repository: null\n",
	} {
		if got := mustLoad(t, core.Revision{}, files{"": {".github/crossrev.yml": document}}).Get(".mode"); got != "local" {
			t.Errorf("%q did not load: mode = %q", document, got)
		}
	}
}

// Which fault is named is the one the run meets first, because the Bash reaches
// each assertion only when the one above it passed.
func TestTheFirstFaultInTheReadingOrderIsTheOneNamed(t *testing.T) {
	tree := files{"": {".github/crossrev.yml": "version: 1\ngit: 5\npolicy:\n  min_fix_severity: medum\n"}}
	want := "policy.min_fix_severity is 'medum', which is not one of high, medium or low"
	if got := refusalFrom(t, core.Revision{}, tree).Message; got != want {
		t.Errorf("message = %q, want the severity named before the container", got)
	}
}

// A merge key is resolved, not carried. go-yaml resolves an alias and leaves
// `<<` alone, so the anchor's keys landed under a literal `<<` and the default
// underneath survived — a repository sharing a policy block through an anchor
// got one effective policy from Bash and another from Go, both at exit 0.
func TestAMergeKeyIsResolvedTheWayYqResolvesIt(t *testing.T) {
	for _, test := range []struct{ name, document, path, want string }{
		{
			"one source", "version: 1\ndefaults: &d\n  hooks: run\ngit:\n  <<: *d\n",
			".git.hooks", "run",
		},
		{
			"a key written after the merge wins",
			"version: 1\na: &a\n  hooks: run\ngit:\n  <<: *a\n  hooks: skip\n",
			".git.hooks", "skip",
		},
		{
			"a key written before it does not",
			"version: 1\na: &a\n  hooks: run\ngit:\n  hooks: skip\n  <<: *a\n",
			".git.hooks", "run",
		},
		{
			"a chain resolves through its own merge",
			"version: 1\nb: &b\n  hooks: run\na: &a\n  <<: *b\ngit:\n  <<: *a\n",
			".git.hooks", "run",
		},
		{
			"the earliest of a sequence of sources wins",
			"version: 1\na: &a\n  hooks: run\nb: &b\n  hooks: skip\ngit:\n  <<: [*a, *b]\n",
			".git.hooks", "run",
		},
		{
			"a source yq will not follow merges nothing",
			"version: 1\ngit:\n  hooks: run\n  <<:\n    hooks: skip\n",
			".git.hooks", "run",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			loaded := mustLoad(t, core.Revision{}, files{"": {".github/crossrev.yml": test.document}})
			if got := loaded.Get(test.path); got != test.want {
				t.Errorf("%s = %q, want %q", test.path, got, test.want)
			}
			if got := string(mustJSON(t, loaded)); strings.Contains(got, `"<<"`) {
				t.Errorf("a literal merge key reached the merge: %s", got)
			}
		})
	}
}

// A sequence of merge sources is applied last to first, so the keys the
// earliest entry does not set come from the ones after it, in that order.
// Measured against yq, which answers `{"k":"a","pc":3,"pb":2,"pa":1}`.
func TestASequenceOfMergeSourcesKeepsYqsOrder(t *testing.T) {
	document := "version: 1\n" +
		"a: &a {k: a, pa: 1}\nb: &b {k: b, pb: 2}\nc: &c {k: c, pc: 3}\n" +
		"d:\n  <<: [*a, *b, *c]\n"
	loaded := mustLoad(t, core.Revision{}, files{"": {".github/crossrev.yml": document}})
	if got := string(loaded.GetJSON(".d")); got != `{"k":"a","pc":3,"pb":2,"pa":1}` {
		t.Errorf("the merged sequence = %s", got)
	}
}

// A merge key naming something that is not a mapping is an error in yq, so the
// Bash refuses the whole file for it.
func TestAMergeKeyNamingANonMappingRefusesTheFile(t *testing.T) {
	for name, document := range map[string]string{
		"an alias to a list":   "version: 1\na: &a [1, 2]\ngit:\n  <<: *a\n",
		"an alias to a scalar": "version: 1\na: &a 5\ngit:\n  <<: *a\n",
	} {
		t.Run(name, func(t *testing.T) {
			tree := files{"": {".github/crossrev.yml": document}}
			if got := refusalFrom(t, core.Revision{}, tree).Message; got != "could not parse .github/crossrev.yml" {
				t.Errorf("message = %q, want the file refused as unparsable", got)
			}
		})
	}
}

// A mapping key is the scalar's source text, resolved no further. yq writes the
// key `~` rather than `null`, and a key that is itself a list or a mapping is
// the empty string.
func TestAMappingKeyIsTheTextYqWrites(t *testing.T) {
	for _, test := range []struct{ name, document, wants string }{
		{"a tilde", "version: 1\nq:\n  ~: v\n", `"q":{"~":"v"}`},
		{"the word null", "version: 1\nq:\n  null: v\n", `"q":{"null":"v"}`},
		{"a list key", "version: 1\nq:\n  ? [a, b]\n  : v\n", `"q":{"":"v"}`},
		{"a mapping key", "version: 1\nq:\n  ? {a: b}\n  : v\n", `"q":{"":"v"}`},
		{"an integer", "version: 1\nq:\n  5: v\n", `"q":{"5":"v"}`},
		{"a boolean", "version: 1\nq:\n  true: v\n", `"q":{"true":"v"}`},
		{"an unresolved base", "version: 1\nq:\n  0x10: v\n", `"q":{"0x10":"v"}`},
		{"an exponent", "version: 1\nq:\n  1e3: v\n", `"q":{"1e3":"v"}`},
		{"a negative zero", "version: 1\nq:\n  -0: v\n", `"q":{"-0":"v"}`},
		{"an alias key", "version: 1\na: &x 1\nq:\n  *x : v\n", `"q":{"1":"v"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			loaded := mustLoad(t, core.Revision{}, files{"": {".github/crossrev.yml": test.document}})
			if got := string(mustJSON(t, loaded)); !strings.Contains(got, test.wants) {
				t.Errorf("the merge = %s, want it to contain %s", got, test.wants)
			}
		})
	}
}

// A document of nothing but whitespace states no policy, which is what an
// absent file states. yq reads one holding a tab as null and exits 0, and
// go-yaml handed the same bytes reports a character that cannot start a token —
// so the Bash loaded the defaults and Go refused the file.
//
// yq holds a file's leading blank and comment lines aside before it parses, and
// this drops the same region. Tab-indented content is still refused by both,
// and so is a tab on any line below content.
func TestAWhitespaceOnlyDocumentStatesNoPolicy(t *testing.T) {
	for name, document := range map[string]string{
		"a tab":                      "\t\n",
		"spaces and a tab":           "   \n\t\n",
		"spaces":                     "   \n",
		"a carriage return":          "\r\n",
		"a tab before a comment":     "\t# nothing\n",
		"a tab above real content":   "\t\nversion: 1\n",
		"a blank above a comment":    "\t\n# nothing\n",
		"a comment after a tab line": "\t# one\n# two\n",
	} {
		t.Run(name, func(t *testing.T) {
			loaded := mustLoad(t, core.Revision{}, files{"": {".github/crossrev.yml": document}})
			if got := loaded.Get(".policy.min_fix_severity"); got != "medium" {
				t.Errorf("min_fix_severity = %q, want the default", got)
			}
		})
	}
}

// A tab that reaches the parser is an error on both sides.
func TestATabInContentStillRefusesTheFile(t *testing.T) {
	for name, document := range map[string]string{
		"a tab indenting a value":       "version: 1\ngit:\n\thooks: run\n",
		"a tab line below content":      "version: 1\n\t\n",
		"a tab under a leading comment": "# c\n\tversion: 1\n",
	} {
		t.Run(name, func(t *testing.T) {
			tree := files{"": {".github/crossrev.yml": document}}
			if got := refusalFrom(t, core.Revision{}, tree).Message; got != "could not parse .github/crossrev.yml" {
				t.Errorf("message = %q, want the file refused as unparsable", got)
			}
		})
	}
}

// A file holding more than one document is not a mapping. `yq -o=json -I=0`
// writes one line per document and the shape test reads the whole stream, so
// two documents never answer `object`. Taking the first would load a policy the
// other implementation refuses to run at all.
func TestAMultiDocumentFileIsNotAMapping(t *testing.T) {
	for name, document := range map[string]string{
		"two mappings":         "mode: ci\n---\nmode: two\n",
		"a mapping then empty": "mode: ci\n---\n",
		"markers only":         "---\n---\n",
	} {
		t.Run(name, func(t *testing.T) {
			tree := files{"": {".github/crossrev.yml": document}}
			if got := refusalFrom(t, core.Revision{}, tree).Message; got != ".github/crossrev.yml is not a mapping" {
				t.Errorf("message = %q, want the shape refusal", got)
			}
		})
	}
	// One document with a leading marker is still one document.
	loaded := mustLoad(t, core.Revision{}, files{"": {".github/crossrev.yml": "---\nversion: 1\nmode: automated\n"}})
	if got := loaded.Get(".mode"); got != "automated" {
		t.Errorf("mode = %q, want the single document read", got)
	}
}

// A refusal quotes what `jq -r` writes, and `jq -r` pretty-prints a composite.
func TestARefusalQuotesACompositeTheWayJqPrintsIt(t *testing.T) {
	tree := files{"": {".github/crossrev.yml": "version: 1\npolicy:\n  min_fix_severity: [a, b]\n"}}
	want := "policy.min_fix_severity is '[\n  \"a\",\n  \"b\"\n]', which is not one of high, medium or low"
	if got := refusalFrom(t, core.Revision{}, tree).Message; got != want {
		t.Errorf("message = %q, want %q", got, want)
	}
}
