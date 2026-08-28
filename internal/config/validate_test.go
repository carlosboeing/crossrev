package config_test

import (
	"strings"
	"testing"

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
