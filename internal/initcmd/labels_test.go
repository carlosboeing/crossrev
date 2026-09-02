package initcmd_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/forge"
	"github.com/carlosboeing/crossrev/internal/initcmd"
	"github.com/carlosboeing/crossrev/internal/ui"
)

// fakeLabels is the forge's label write. It answers from the colours a
// fakeGitHub already carries, so a test states one inventory rather than two,
// and records every declaration as the argv-shaped line the shell's stub logs.
type fakeLabels struct {
	colours map[string]string
	fail    map[string]bool

	calls []string
}

func (f *fakeLabels) LabelEnsure(_ context.Context, repo core.Slug, label forge.Label) (forge.LabelState, error) {
	f.calls = append(f.calls, fmt.Sprintf("%s %s %s %s", repo, label.Name, label.Colour, label.Description))
	if f.fail[label.Name] {
		return "", fmt.Errorf("could not create the label '%s' on %s", label.Name, repo)
	}
	current := f.colours[label.Name]
	switch {
	case current == "":
		if f.colours == nil {
			f.colours = map[string]string{}
		}
		f.colours[label.Name] = label.Colour
		return forge.LabelCreated, nil
	case current == strings.ToLower(label.Colour):
		return forge.LabelExists, nil
	default:
		f.colours[label.Name] = label.Colour
		return forge.LabelRecoloured, nil
	}
}

// issueSinkConfig is tests/test-init.sh's config_with_issue_sink, byte for
// byte, so the label counts here are the ones that suite measures.
const issueSinkConfig = `version: 1
mode: automated
policy:
  min_fix_severity: medium
  max_passes_per_cycle: 3
  max_files_changed_per_pr: 200
  max_prs_per_day: 25
reviewer:
  harness: claude
  model: reviewer-model
resolver:
  harness: claude
  model: resolver-model
backlog:
  destination: github_issues
  github_issues:
    tracking_label: crossrev-review
    labels: [bug]
    create_missing_labels: true
    comment_on_existing_issue: false
`

// planned resolves one plan and hands back the request it was resolved from,
// with the output buffer reset so a caller asserts on execution alone.
func planned(t *testing.T, configuration string, adjust func(*initcmd.Request)) (initcmd.Plan, initcmd.Request, *ui.IO, *strings.Builder) {
	t.Helper()
	req := request(t, configuration)
	if adjust != nil {
		adjust(&req)
	}
	plan, err := initcmd.Resolve(context.Background(), req)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	var buffer strings.Builder
	out := &ui.IO{Out: &buffer, Err: &buffer, Palette: ui.Plain()}
	req.Out = out
	return plan, req, out, &buffer
}

// TestExecuteCreatesEveryLoopLabelAndCountsWhatItDid pins the Labels section of
// _init_execute against the block the shell prints for the suite's own fixture:
// seven created, one already there in the declared colour (lib/init.sh:439-455).
func TestExecuteCreatesEveryLoopLabelAndCountsWhatItDid(t *testing.T) {
	labels := &fakeLabels{colours: map[string]string{
		"crossrev/stop": "cf222e",
		"bug":           "d4c5f9",
	}}
	plan, req, _, buffer := planned(t, issueSinkConfig, func(r *initcmd.Request) {
		r.GitHub.(*fakeGitHub).colours = map[string]string{
			"crossrev/stop": "cf222e",
			"bug":           "d4c5f9",
		}
	})
	if err := plan.EnsureLabels(context.Background(), req, initcmd.Execution{Labels: labels}); err != nil {
		t.Fatalf("Labels: %v", err)
	}

	want := "\n◇  Labels\n" +
		"│  ✓ created 7 and found 1 already on acme/widget for the loop\n" +
		"│  ✓ created 1 and found 1 already for filed issues\n"
	if got := buffer.String(); got != want {
		t.Errorf("the Labels block is\n%q\nwant\n%q", got, want)
	}
}

// TestExecuteDeclaresEveryLabelWithItsOwnColourAndDescription pins the argv the
// shell's stub records: the colour and the description come from the label
// policy per label, never one string across all of them
// (lib/init.sh:445, tests/test-init.sh:107-111).
func TestExecuteDeclaresEveryLabelWithItsOwnColourAndDescription(t *testing.T) {
	labels := &fakeLabels{}
	plan, req, _, _ := planned(t, issueSinkConfig, nil)
	if err := plan.EnsureLabels(context.Background(), req, initcmd.Execution{Labels: labels}); err != nil {
		t.Fatalf("Labels: %v", err)
	}

	want := []string{
		"acme/widget crossrev/pass-1 57606a crossrev: reached pass 1",
		"acme/widget crossrev/pass-2 57606a crossrev: reached pass 2",
		"acme/widget crossrev/pass-3 57606a crossrev: reached pass 3",
		"acme/widget crossrev/awaiting-resolution 8250df crossrev: the review landed, the resolve leg is owed",
		"acme/widget crossrev/awaiting-review 0969da crossrev: a review is owed on this pull request",
		"acme/widget crossrev/converged 1a7f37 crossrev: the loop finished on its own",
		"acme/widget crossrev/halted bc4c00 crossrev: stopped short, a human is needed",
		"acme/widget crossrev/stop cf222e crossrev: apply this to stop the loop",
		"acme/widget crossrev-review d4c5f9 filed by crossrev",
		"acme/widget bug d4c5f9 filed by crossrev",
	}
	if got := strings.Join(labels.calls, "\n"); got != strings.Join(want, "\n") {
		t.Errorf("the declarations are\n%s\nwant\n%s", got, strings.Join(want, "\n"))
	}
}

// TestExecuteRecolourssLabelsMintedInTheOldColour is the --upgrade migration:
// every loop label already there in another colour is recoloured, and the run
// says how many (lib/init.sh:448-455, tests/test-runner.sh has the same case in
// tests/test-init.sh:205-222).
func TestExecuteRecoloursLabelsMintedInTheOldColour(t *testing.T) {
	old := map[string]string{}
	for _, label := range []string{
		"crossrev/pass-1", "crossrev/pass-2", "crossrev/pass-3",
		"crossrev/awaiting-resolution", "crossrev/awaiting-review",
		"crossrev/converged", "crossrev/halted", "crossrev/stop",
	} {
		old[label] = "5319e7"
	}
	old["bug"] = "d73a4a"
	labels := &fakeLabels{colours: old}
	plan, req, _, buffer := planned(t, issueSinkConfig, func(r *initcmd.Request) {
		colours := map[string]string{}
		for name, colour := range old {
			colours[name] = colour
		}
		r.GitHub.(*fakeGitHub).colours = colours
	})
	if err := plan.EnsureLabels(context.Background(), req, initcmd.Execution{Labels: labels}); err != nil {
		t.Fatalf("Labels: %v", err)
	}

	want := "\n◇  Labels\n" +
		"│  ✓ created 0 and found 0 already on acme/widget for the loop\n" +
		"│  ✓ recoloured 8 that were already there, so the label row carries state at a glance\n" +
		"│  ✓ created 1 and found 1 already for filed issues\n"
	if got := buffer.String(); got != want {
		t.Errorf("the Labels block is\n%q\nwant\n%q", got, want)
	}
	for _, call := range labels.calls {
		if strings.HasPrefix(call, "acme/widget bug ") {
			t.Errorf("a backlog label already there was declared again: %q", call)
		}
	}
}

// TestExecuteSaysNothingAboutRecolouringWhenItRecolouredNothing pins the guard
// at lib/init.sh:455: the second line appears only above zero.
func TestExecuteSaysNothingAboutRecolouringWhenItRecolouredNothing(t *testing.T) {
	plan, req, _, buffer := planned(t, issueSinkConfig, nil)
	if err := plan.EnsureLabels(context.Background(), req, initcmd.Execution{Labels: &fakeLabels{}}); err != nil {
		t.Fatalf("Labels: %v", err)
	}
	if strings.Contains(buffer.String(), "recoloured") {
		t.Errorf("a run that recoloured nothing said it did:\n%s", buffer.String())
	}
}

// TestExecuteRefusesToInventALabelTheRepositoryGovernsItself is
// create_missing_labels: false — a repository that governs its own label set is
// one where inventing labels is worse than stopping (lib/init.sh:467-472).
func TestExecuteRefusesToInventALabelTheRepositoryGovernsItself(t *testing.T) {
	strict := strings.NewReplacer(
		"    create_missing_labels: true", "    create_missing_labels: false",
		"    labels: [bug]", "    labels: [needs-triage]",
	).Replace(issueSinkConfig)

	labels := &fakeLabels{}
	plan, req, _, buffer := planned(t, strict, nil)
	err := plan.EnsureLabels(context.Background(), req, initcmd.Execution{Labels: labels})
	if err == nil {
		t.Fatal("a missing label the repository will not have invented was created anyway")
	}
	fatal := fatalError(t, err)
	wantReason := "the GitHub issues destination needs these labels and create_missing_labels is false: crossrev-review needs-triage"
	if fatal.Reason != wantReason {
		t.Errorf("reason = %q, want %q", fatal.Reason, wantReason)
	}
	wantAction := "The repository asked CrossRev to use existing labels only. Create them by hand, or set backlog.github_issues.create_missing_labels: true."
	if fatal.Action != wantAction {
		t.Errorf("action = %q, want %q", fatal.Action, wantAction)
	}
	if !strings.Contains(buffer.String(), wantReason) {
		t.Errorf("the refusal was not printed:\n%s", buffer.String())
	}
	for _, call := range labels.calls {
		if strings.Contains(call, "needs-triage") {
			t.Errorf("a label create_missing_labels forbids was declared: %q", call)
		}
	}
}

// TestExecuteCarriesAFailedLabelCreationUp pins that a label that will not
// create ends the run rather than being counted (lib/init.sh:445,
// tests/test-init.sh:405-406).
func TestExecuteCarriesAFailedLabelCreationUp(t *testing.T) {
	labels := &fakeLabels{fail: map[string]bool{"crossrev/pass-2": true}}
	plan, req, _, buffer := planned(t, issueSinkConfig, nil)
	err := plan.EnsureLabels(context.Background(), req, initcmd.Execution{Labels: labels})
	if err == nil {
		t.Fatal("a label that would not create was not reported")
	}
	if !strings.Contains(err.Error(), "could not create the label 'crossrev/pass-2'") {
		t.Errorf("error = %v, want the forge's own refusal", err)
	}
	if strings.Contains(buffer.String(), "◇  Labels") {
		t.Errorf("the section was opened over a label that never landed:\n%s", buffer.String())
	}
	if len(labels.calls) != 2 {
		t.Errorf("it kept declaring labels after one failed: %q", labels.calls)
	}
}

// TestExecuteSaysNothingAboutFiledIssuesWhenThereAreNoBacklogLabels pins the
// guard at lib/init.sh:457: the second block belongs to the GitHub issues
// destination alone.
func TestExecuteSaysNothingAboutFiledIssuesWhenThereAreNoBacklogLabels(t *testing.T) {
	none := strings.Replace(issueSinkConfig, `backlog:
  destination: github_issues
  github_issues:
    tracking_label: crossrev-review
    labels: [bug]
    create_missing_labels: true
    comment_on_existing_issue: false
`, "backlog:\n  destination: none\n", 1)

	plan, req, _, buffer := planned(t, none, nil)
	if err := plan.EnsureLabels(context.Background(), req, initcmd.Execution{Labels: &fakeLabels{}}); err != nil {
		t.Fatalf("Labels: %v", err)
	}
	if strings.Contains(buffer.String(), "for filed issues") {
		t.Errorf("a repository with no backlog labels was told about filed issues:\n%s", buffer.String())
	}
}

// TestExecuteRefusesAnUnwiredLabelWriter: a nil port is a wiring fault in the
// composition root, reported as one rather than filled in with a default that
// silently changes nothing.
func TestExecuteRefusesAnUnwiredLabelWriter(t *testing.T) {
	plan, req, _, _ := planned(t, issueSinkConfig, nil)
	err := plan.EnsureLabels(context.Background(), req, initcmd.Execution{})
	if err == nil || !strings.Contains(err.Error(), "Labels") {
		t.Fatalf("err = %v, want a refusal naming the missing port", err)
	}
}

// fatalError reads the two fields ui.Die returns, and fails the test for
// anything else.
func fatalError(t *testing.T, err error) *ui.FatalError {
	t.Helper()
	var fatal *ui.FatalError
	if !errors.As(err, &fatal) {
		t.Fatalf("err = %v, want a *ui.FatalError", err)
	}
	return fatal
}
