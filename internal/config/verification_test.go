package config_test

import (
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/config"
	"github.com/carlosboeing/crossrev/internal/initcmd"
)

// With nothing configured no check is required and the wait is ten minutes.
func TestVerificationDefaults(t *testing.T) {
	got := loadYAML(t, "version: 2\n").Verification()
	if len(got.RequiredChecks) != 0 {
		t.Errorf("required checks = %v, want none", got.RequiredChecks)
	}
	if got.WaitMinutes != 10 {
		t.Errorf("wait minutes = %d, want 10", got.WaitMinutes)
	}
}

// Each item is NAME or NAME@APP as a string, or a name/app mapping. The
// app defaults to github-actions.
func TestVerificationRequiredChecksRead(t *testing.T) {
	loaded := loadYAML(t, "version: 2\nverification:\n  required_checks:\n    - build\n    - test@my-app\n    - name: lint\n    - name: e2e\n      app: other-app\n")
	got := loaded.Verification().RequiredChecks
	want := []config.RequiredCheck{
		{Name: "build", App: "github-actions"},
		{Name: "test", App: "my-app"},
		{Name: "lint", App: "github-actions"},
		{Name: "e2e", App: "other-app"},
	}
	if len(got) != len(want) {
		t.Fatalf("required checks = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("required checks = %v, want %v", got, want)
		}
	}
}

// An explicitly empty list is none required, not a refusal.
func TestVerificationEmptyRequiredChecksReadAsNone(t *testing.T) {
	got := loadYAML(t, "version: 2\nverification:\n  required_checks: []\n").Verification()
	if len(got.RequiredChecks) != 0 {
		t.Errorf("required checks = %v, want none", got.RequiredChecks)
	}
}

// Empty names, duplicates, CrossRev's own jobs and misshapen items are
// refused at load.
func TestVerificationRequiredChecksRefusals(t *testing.T) {
	for _, tc := range []struct {
		name string
		yaml string
	}{
		{name: "empty item", yaml: "version: 2\nverification:\n  required_checks: ['']\n"},
		{name: "empty name", yaml: "version: 2\nverification:\n  required_checks: ['@my-app']\n"},
		{name: "empty app", yaml: "version: 2\nverification:\n  required_checks: ['build@']\n"},
		{name: "mapping without a name", yaml: "version: 2\nverification:\n  required_checks:\n    - app: my-app\n"},
		{name: "mapping with an empty name", yaml: "version: 2\nverification:\n  required_checks:\n    - name: ''\n"},
		{name: "mapping with an empty app", yaml: "version: 2\nverification:\n  required_checks:\n    - name: build\n      app: ''\n"},
		{name: "duplicate strings", yaml: "version: 2\nverification:\n  required_checks: [build, build]\n"},
		{name: "duplicate across forms", yaml: "version: 2\nverification:\n  required_checks: [build, build@github-actions]\n"},
		{name: "duplicate mappings", yaml: "version: 2\nverification:\n  required_checks:\n    - name: build\n    - name: build\n      app: github-actions\n"},
		{name: "review job", yaml: "version: 2\nverification:\n  required_checks: ['crossrev review / review']\n"},
		{name: "resolve job", yaml: "version: 2\nverification:\n  required_checks:\n    - name: 'crossrev resolve / resolve'\n"},
		{name: "non-list", yaml: "version: 2\nverification:\n  required_checks: build\n"},
		{name: "non-string item", yaml: "version: 2\nverification:\n  required_checks: [build, 7]\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := load(t, tc.yaml); err == nil {
				t.Fatalf("loaded %s, want a refusal", tc.name)
			} else if !strings.Contains(err.Error(), "verification.required_checks") {
				t.Errorf("err = %q, want it to name verification.required_checks", err)
			}
		})
	}
}

// The wait reads 0 to 30 and defaults to 10.
func TestVerificationWaitMinutesReads(t *testing.T) {
	for _, tc := range []struct {
		name string
		yaml string
		want int
	}{
		{name: "absent", yaml: "version: 2\n", want: 10},
		{name: "zero", yaml: "version: 2\nverification:\n  wait_minutes: 0\n", want: 0},
		{name: "thirty", yaml: "version: 2\nverification:\n  wait_minutes: 30\n", want: 30},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := loadYAML(t, tc.yaml).Verification().WaitMinutes; got != tc.want {
				t.Errorf("wait minutes = %d, want %d", got, tc.want)
			}
		})
	}
}

// Anything outside 0 to 30, and anything not a whole number, is refused.
func TestVerificationWaitMinutesRefusals(t *testing.T) {
	for _, tc := range []struct {
		name string
		yaml string
	}{
		{name: "negative", yaml: "version: 2\nverification:\n  wait_minutes: -1\n"},
		{name: "past thirty", yaml: "version: 2\nverification:\n  wait_minutes: 31\n"},
		{name: "words", yaml: "version: 2\nverification:\n  wait_minutes: ten\n"},
		{name: "fraction", yaml: "version: 2\nverification:\n  wait_minutes: 10.5\n"},
		{name: "boolean", yaml: "version: 2\nverification:\n  wait_minutes: true\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := load(t, tc.yaml); err == nil {
				t.Fatalf("loaded %s, want a refusal", tc.name)
			} else if !strings.Contains(err.Error(), "verification.wait_minutes") {
				t.Errorf("err = %q, want it to name verification.wait_minutes", err)
			}
		})
	}
}

// A verification key that holds no keys is refused the way every other
// container key is.
func TestVerificationRefusesANonMapping(t *testing.T) {
	_, err := load(t, "version: 2\nverification: build\n")
	if err == nil {
		t.Fatal("a verification key holding a string loaded")
	}
	if !strings.Contains(err.Error(), "verification") {
		t.Errorf("err = %q, want it to name verification", err)
	}
}

// The refused self-gating names are the check runs CrossRev's own
// workflows publish: each generated workflow's name over its leg job.
// The literals live here because this package may not import the
// generator; this test binds them to its templates instead, so a rename
// on either side fails here rather than gating a review on itself.
func TestOwnCheckNamesMatchTheGeneratedWorkflows(t *testing.T) {
	review := workflowCheck(t, initcmd.ReviewWorkflowTemplate())
	resolve := workflowCheck(t, initcmd.ResolveWorkflowTemplate())
	want := []string{review, resolve}
	if got := config.OwnCheckNames(); len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("OwnCheckNames() = %v, want %v from the generated workflows", got, want)
	}
}

// workflowCheck reads one generated workflow's check name: its `name:`
// over the first job id under `jobs:`, which is the leg job. GitHub
// names the check run "workflow / job" where the job sets no name.
func workflowCheck(t *testing.T, template []byte) string {
	t.Helper()
	workflow := ""
	jobs := false
	for _, line := range strings.Split(string(template), "\n") {
		if strings.HasPrefix(line, "name: ") {
			workflow = strings.TrimPrefix(line, "name: ")
			continue
		}
		if line == "jobs:" {
			jobs = true
			continue
		}
		if jobs && strings.HasPrefix(line, "  ") && !strings.HasPrefix(line, "   ") {
			if trimmed := strings.TrimSpace(line); strings.HasSuffix(trimmed, ":") && !strings.Contains(trimmed, " ") {
				return workflow + " / " + strings.TrimSuffix(trimmed, ":")
			}
		}
	}
	t.Fatalf("no leg job found in the generated workflow")
	return ""
}
