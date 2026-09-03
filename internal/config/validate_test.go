package config_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/config"
	"github.com/carlosboeing/crossrev/internal/core"
)

// Every value family is refused with the value named, whatever type the YAML
// gave it. The failure each one prevents is silent in the expensive direction.
func TestEveryRefusedValueFamilyNamesItsValue(t *testing.T) {
	tests := []struct {
		name     string
		document string
		wants    string
	}{
		{"a misspelt severity", "version: 1\npolicy:\n  min_fix_severity: medum\n", "policy.min_fix_severity is 'medum'"},
		{"an unset severity", "version: 1\npolicy:\n  min_fix_severity: null\n", "policy.min_fix_severity is 'unset'"},
		{"a zero pass bound", "version: 1\npolicy:\n  max_passes_per_cycle: 0\n", "policy.max_passes_per_cycle is '0'"},
		{"an unset pass bound", "version: 1\npolicy:\n  max_passes_per_cycle: null\n", "policy.max_passes_per_cycle is 'unset'"},
		{"a negative pass bound", "version: 1\npolicy:\n  max_passes_per_cycle: -1\n", "policy.max_passes_per_cycle is '-1'"},
		{"a spelt-out pass bound", "version: 1\npolicy:\n  max_passes_per_cycle: three\n", "policy.max_passes_per_cycle is 'three'"},
		{"a misspelt hooks switch", "version: 1\ngit:\n  hooks: skipp\n", "git.hooks is 'skipp'"},
		{"a zero retention", "version: 1\nlogs:\n  retention_days: 0\n", "logs.retention_days is '0'"},
		{"an unset retention", "version: 1\nlogs:\n  retention_days: null\n", "logs.retention_days is 'unset'"},
		{"a spelt-out retention", "version: 1\nlogs:\n  retention_days: fortnight\n", "logs.retention_days is 'fortnight'"},
		{"a non-boolean transcript switch", "version: 1\nlogs:\n  keep_transcripts: maybe\n", "logs.keep_transcripts is 'maybe'"},
		{"a numeric transcript switch", "version: 1\nlogs:\n  keep_transcripts: 1\n", "logs.keep_transcripts is '1'"},
		{"an unknown backlog destination", "version: 1\nbacklog:\n  destination: elsewhere\n", "backlog.destination is 'elsewhere'"},
		{"an unknown backlog layout", "version: 1\nbacklog:\n  repository:\n    layout: flat\n", "backlog.repository.layout is 'flat'"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tree := files{"": {".github/crossrev.yml": test.document}}
			if got := refusalFrom(t, core.Revision{}, tree).Message; !strings.Contains(got, test.wants) {
				t.Errorf("message = %q, want it to contain %q", got, test.wants)
			}
		})
	}
}

// The version is checked before the merge, so a future shape is refused rather
// than merged into this one and then refused for a value it never stated
// (lib/config.sh:168-169 runs ahead of the merge at lib/config.sh:177).
func TestTheVersionIsCheckedBeforeTheValueFamilies(t *testing.T) {
	tree := files{"": {".github/crossrev.yml": "version: 99\npolicy:\n  min_fix_severity: medum\n"}}
	if got := refusalFrom(t, core.Revision{}, tree).Message; !strings.Contains(got, "declares version 99") {
		t.Errorf("message = %q, want the version refusal first", got)
	}
}

// The value families are refused in the order lib/config.sh:186-190 asserts
// them, so a config with two bad values reports the same one Bash reports.
func TestTheValueFamiliesKeepTheirOrder(t *testing.T) {
	document := "version: 1\npolicy:\n  min_fix_severity: medum\n  max_passes_per_cycle: 0\n" +
		"git:\n  hooks: skipp\nlogs:\n  retention_days: 0\nbacklog:\n  destination: elsewhere\n"
	tree := files{"": {".github/crossrev.yml": document}}
	if got := refusalFrom(t, core.Revision{}, tree).Message; !strings.Contains(got, "policy.min_fix_severity") {
		t.Errorf("message = %q, want min_fix_severity refused first", got)
	}

	document = "version: 1\npolicy:\n  max_passes_per_cycle: 0\ngit:\n  hooks: skipp\n"
	tree = files{"": {".github/crossrev.yml": document}}
	if got := refusalFrom(t, core.Revision{}, tree).Message; !strings.Contains(got, "policy.max_passes_per_cycle") {
		t.Errorf("message = %q, want max_passes_per_cycle refused before git.hooks", got)
	}

	document = "version: 1\ngit:\n  hooks: skipp\nlogs:\n  retention_days: 0\n"
	tree = files{"": {".github/crossrev.yml": document}}
	if got := refusalFrom(t, core.Revision{}, tree).Message; !strings.Contains(got, "git.hooks") {
		t.Errorf("message = %q, want git.hooks refused before logs", got)
	}

	document = "version: 1\nlogs:\n  retention_days: 0\nbacklog:\n  destination: elsewhere\n"
	tree = files{"": {".github/crossrev.yml": document}}
	if got := refusalFrom(t, core.Revision{}, tree).Message; !strings.Contains(got, "logs.retention_days") {
		t.Errorf("message = %q, want logs refused before backlog", got)
	}
}

// Every accepted value loads, so the refusals bound the wrong values rather
// than the key.
func TestTheAcceptedValuesLoad(t *testing.T) {
	documents := []string{
		"version: 1\npolicy:\n  min_fix_severity: high\n",
		"version: 1\npolicy:\n  min_fix_severity: medium\n",
		"version: 1\npolicy:\n  min_fix_severity: low\n",
		"version: 1\npolicy:\n  max_passes_per_cycle: 1\n",
		"version: 1\ngit:\n  hooks: skip\n",
		"version: 1\ngit:\n  hooks: run\n",
		"version: 1\nlogs:\n  retention_days: 1\n  keep_transcripts: true\n",
		"version: 1\nlogs:\n  retention_days: 30\n  keep_transcripts: false\n",
		"version: 1\nbacklog:\n  destination: github_issues\n",
		"version: 1\nbacklog:\n  destination: repository\n  repository:\n    layout: file\n",
		"version: 1\nbacklog:\n  destination: none\n",
		"version: 1\nbacklog:\n  destination: auto\n",
		// No version key at all: absent is not a mismatch.
		"policy:\n  min_fix_severity: low\n",
	}
	for _, document := range documents {
		mustLoad(t, core.Revision{}, files{"": {".github/crossrev.yml": document}})
	}
}

// An endpoint may not borrow a GitHub credential.
//
// token_env is read with ${!name} and its value handed to the harness process
// as that vendor's own token variable. So `token_env: GH_TOKEN` carries a
// GitHub credential across the boundary the adapters exist to hold, and past
// their strip list — the strip removes GH_TOKEN, and the same value arrives as
// ANTHROPIC_AUTH_TOKEN. Nothing refused it, at any layer (ADR 0001).
//
// All four names, because `gh` reads four: a list holding only the first leaves
// the enterprise pair usable for the same trick.
func TestAnEndpointMayNotNameAForgeCredentialAsItsTokenEnv(t *testing.T) {
	for _, credential := range config.ForgeCredentialNames() {
		t.Run(credential, func(t *testing.T) {
			document := "version: 1\nendpoints:\n  mine:\n    base_url: https://api.example/\n    token_env: " + credential + "\n"
			refusal := refusalFrom(t, core.Revision{}, files{"": {".github/crossrev.yml": document}})

			want := "the endpoint 'mine' names $" + credential + " as its token_env"
			if refusal.Message != want {
				t.Errorf("message = %q, want %q", refusal.Message, want)
			}
			wantHint := "That is a GitHub credential, and CrossRev hands token_env's value to the model process, " +
				"which must hold none. Point token_env at the endpoint's own token."
			if refusal.Hint != wantHint {
				t.Errorf("hint = %q, want %q", refusal.Hint, wantHint)
			}
		})
	}
}

// Refused at load, so it does not wait for a leg that selects it. An endpoint
// nothing points at is still a config asking CrossRev to do the one thing it
// must not — and Load is the moment `config show`, `doctor` and every leg pass
// through, so all three say it before anything runs.
func TestAnEndpointNoLegSelectsIsRefusedToo(t *testing.T) {
	document := "version: 1\nendpoints:\n  unused:\n    base_url: https://api.example/\n    token_env: GH_TOKEN\n"
	if got := refusalFrom(t, core.Revision{}, files{"": {".github/crossrev.yml": document}}).Message; !strings.Contains(got, "the endpoint 'unused'") {
		t.Errorf("message = %q, want the unselected endpoint refused by name", got)
	}
}

// The operator file merges into the same mapping, so it is checked on the same
// pass rather than trusted for being local.
func TestAnOperatorFileEndpointIsRefusedTheSameWay(t *testing.T) {
	operator := "version: 1\nendpoints:\n  local:\n    base_url: http://mine.local/\n    token_env: GITHUB_TOKEN\n"
	tree := files{"": {
		".github/crossrev.yml": "version: 1\n",
		config.OperatorPath():  operator,
	}}
	if got := refusalFrom(t, core.Revision{}, tree).Message; got != "the endpoint 'local' names $GITHUB_TOKEN as its token_env" {
		t.Errorf("message = %q, want the operator-file endpoint refused by name", got)
	}
}

// A name that merely looks like one is a different variable, and refusing it
// would break a real endpoint for no gain. Environment names are case-sensitive.
func TestALowercaseForgeCredentialNameIsADifferentVariable(t *testing.T) {
	document := "version: 1\nendpoints:\n  kimi:\n    base_url: https://api.example/\n    token_env: gh_token\n"
	loaded := mustLoad(t, core.Revision{}, files{"": {".github/crossrev.yml": document}})
	endpoint, err := loaded.Endpoint("kimi")
	if err != nil {
		t.Fatalf("Endpoint: %v", err)
	}
	if endpoint.TokenEnv != "gh_token" {
		t.Errorf("token_env = %q, want it to survive the load as %q", endpoint.TokenEnv, "gh_token")
	}
}

// And the ordinary case still loads, or the check above would pass against a
// port that had stopped reading token_env at all.
func TestAnEndpointWithItsOwnTokenStillLoads(t *testing.T) {
	document := "version: 1\nendpoints:\n  kimi:\n    base_url: https://api.example/\n    token_env: KIMI_API_KEY\n"
	loaded := mustLoad(t, core.Revision{}, files{"": {".github/crossrev.yml": document}})
	endpoint, err := loaded.Endpoint("kimi")
	if err != nil {
		t.Fatalf("Endpoint: %v", err)
	}
	if endpoint.TokenEnv != "KIMI_API_KEY" {
		t.Errorf("token_env = %q, want %q", endpoint.TokenEnv, "KIMI_API_KEY")
	}
}

// A definition that is not a mapping holds no token_env to read. The jq asks
// `(.value | type) == "object"` before it reads the key, so a string definition
// passes this assertion and is refused later, by Endpoint, for the base_url it
// does not have (lib/config.sh:321-323).
func TestANonMappingEndpointDefinitionPassesTheCredentialCheck(t *testing.T) {
	document := "version: 1\nendpoints:\n  ollama: GH_TOKEN\n"
	loaded := mustLoad(t, core.Revision{}, files{"": {".github/crossrev.yml": document}})
	_, err := loaded.Endpoint("ollama")
	refusal, ok := err.(*config.Refusal)
	if !ok {
		t.Fatalf("expected a *config.Refusal, got %T: %v", err, err)
	}
	if want := "the endpoint 'ollama' has no base_url"; refusal.Message != want {
		t.Errorf("message = %q, want %q", refusal.Message, want)
	}
}

// The endpoint check is last in the assert chain, where lib/config.sh:245 puts
// it, so a config wrong twice reports the same fault Bash reports.
func TestTheEndpointCheckIsLastInTheAssertChain(t *testing.T) {
	document := "version: 1\nbacklog:\n  destination: elsewhere\n" +
		"endpoints:\n  mine:\n    base_url: https://api.example/\n    token_env: GH_TOKEN\n"
	if got := refusalFrom(t, core.Revision{}, files{"": {".github/crossrev.yml": document}}).Message; !strings.Contains(got, "backlog.destination") {
		t.Errorf("message = %q, want the backlog refused first", got)
	}
}

// The four names, written out here rather than read from the production list.
// A test that read the list it checks agrees with whatever the list says, and
// the whole point of the four is that none is missed. internal/archtest binds
// this list to the one a model-facing process is stripped of.
func TestForgeCredentialNamesAreTheFourGhReads(t *testing.T) {
	want := []string{"GH_TOKEN", "GITHUB_TOKEN", "GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN"}
	if got := config.ForgeCredentialNames(); !slices.Equal(got, want) {
		t.Errorf("ForgeCredentialNames() = %v, want %v", got, want)
	}
}

// The accessor answers a fresh slice, so a caller that shortens its copy does
// not shorten the boundary for every other caller in the binary.
func TestForgeCredentialNamesCannotBeShortenedByACaller(t *testing.T) {
	taken := config.ForgeCredentialNames()
	clear(taken)
	if got := config.ForgeCredentialNames(); !slices.Contains(got, "GH_TOKEN") {
		t.Errorf("ForgeCredentialNames() = %v after a caller cleared its copy", got)
	}
}
