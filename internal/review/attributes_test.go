package review_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/exec"
	"github.com/carlosboeing/crossrev/internal/review"
	"github.com/carlosboeing/crossrev/internal/ui"
	"github.com/carlosboeing/crossrev/internal/vcs"
)

// A path the base tree marks linguist-generated leaves the required set
// before packing: it is recorded as an exclusion with its reason, never read
// for evidence, and the rest of the pass runs.
func TestReviewExcludesBaseTreeGeneratedPaths(t *testing.T) {
	e := newEnv(t)
	writeRequiredHead(e, "dist/bundle.js", "var bundle = 1\n")
	writeRequiredHead(e, "a.go", "package a\n")
	e.vcs.attrs = map[string]vcs.AttributeDecision{"dist/bundle.js": vcs.AttributeSet}
	e.runner.script = []exec.Result{
		{ExitCode: 0, Stdout: claudeStdout(batchAnswer(t, 1))},
	}
	got := runLeg(t, e, e.request(t))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if got.Outcome != review.OutcomeInvoked {
		t.Fatalf("Outcome = %q, want invoked", got.Outcome)
	}
	if e.vcs.reads["dist/bundle.js"] != 0 {
		t.Errorf("the excluded path's body was read %d times", e.vcs.reads["dist/bundle.js"])
	}
	gens := ledgerGenerations(t, e)
	if len(gens) == 0 {
		t.Fatal("no generations published")
	}
	initial := gens[0]
	if len(initial.Excluded) != 1 {
		t.Fatalf("initial generation exclusions = %v, want the marked path", initial.Excluded)
	}
	exclusion := initial.Excluded[0]
	if exclusion.Path != "dist/bundle.js" {
		t.Errorf("excluded path = %q, want dist/bundle.js", exclusion.Path)
	}
	if exclusion.Reason != "generated: linguist-generated in .gitattributes" {
		t.Errorf("exclusion reason = %q", exclusion.Reason)
	}
	required := 0
	for _, record := range initial.Records {
		if record.Type == "unit" || record.Type == "outstanding" {
			required++
		}
	}
	if required != 1 {
		t.Errorf("required records = %d, want 1 (a.go alone)", required)
	}
}

// A git too old for check-attr --source costs one warning, and the pass runs
// on the built-in rules.
func TestReviewWarnsWhenGitCannotReadBaseAttributes(t *testing.T) {
	e := newEnv(t)
	writeRequiredHead(e, "a.go", "package a\n")
	e.vcs.attrWarn = &vcs.Warning{
		Message: "git 2.39.2 is older than 2.40, so .gitattributes linguist-generated is not read at the base",
		Hint:    "The built-in generated-file rules still apply. Upgrade git to 2.40 or newer to read repository policy.",
	}
	e.runner.script = []exec.Result{
		{ExitCode: 0, Stdout: claudeStdout(batchAnswer(t, 1))},
	}
	got := runLeg(t, e, e.request(t))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	warnings := 0
	for _, line := range got.Messages {
		if line.Kind != ui.KindWarn {
			continue
		}
		warnings++
		if !strings.Contains(line.Text, "2.39.2") {
			t.Errorf("warning does not name the version: %q", line.Text)
		}
	}
	if warnings != 1 {
		t.Errorf("warnings = %d, want exactly one", warnings)
	}
}

// A check-attr failure that is not "git too old" fails the pass the way a
// failed ChangedFiles enumeration does: reading it as unspecified would
// review paths the repository marked linguist-generated.
func TestReviewFailsWhenTheAttributeReadFails(t *testing.T) {
	e := newEnv(t)
	writeRequiredHead(e, "a.go", "package a\n")
	e.vcs.attrErr = errors.New("git check-attr exited 128: fatal: bad object")
	got := runLeg(t, e, e.request(t))
	if got.Outcome != review.OutcomeError {
		t.Fatalf("Outcome = %q, want error", got.Outcome)
	}
	if got.Err == nil || !strings.Contains(got.Err.Error(), "check-attr") {
		t.Errorf("Err = %v, want the attribute failure", got.Err)
	}
	if e.runner.calls != 0 {
		t.Errorf("the harness ran %d times after the attribute read failed", e.runner.calls)
	}
}
