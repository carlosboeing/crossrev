package ghexec_test

import (
	"context"
	"testing"

	"github.com/carlosboeing/crossrev/internal/exec"
	"github.com/carlosboeing/crossrev/internal/forge"
	"github.com/carlosboeing/crossrev/internal/forge/ghexec"
)

// Read with a bare `gh api` and no --jq, so the call is byte-identical to the
// existence check it replaces (lib/github.sh:266-269).
func TestLabelColourArgv(t *testing.T) {
	c, r := client(t, out(`{"name":"bug","color":"D73A4A"}`))

	if got := c.LabelColour(context.Background(), testSlug(t), "bug"); got != "d73a4a" {
		t.Errorf("colour = %q, want it lowercased", got)
	}
	r.wantArgs(t, 0, "api", "repos/acme/widget/labels/bug")
}

func TestLabelColourAnswersEmptyForALabelThatIsNotThere(t *testing.T) {
	c, _ := client(t, bad())
	if got := c.LabelColour(context.Background(), testSlug(t), "bug"); got != "" {
		t.Errorf("colour = %q, want empty", got)
	}
}

func TestLabelEnsureCreatesAMissingLabel(t *testing.T) {
	c, r := client(t, bad(), out("{}"))

	got, err := c.LabelEnsure(context.Background(), testSlug(t), forge.Label{
		Name: "crossrev/converged", Colour: "1a7f37", Description: "crossrev loop state",
	})
	if err != nil {
		t.Fatalf("LabelEnsure: %v", err)
	}
	if got != forge.LabelCreated {
		t.Errorf("state = %q, want created", got)
	}
	r.wantArgs(t, 1, "api", "--method", "POST", "repos/acme/widget/labels",
		"-f", "name=crossrev/converged", "-f", "color=1a7f37", "-f", "description=crossrev loop state")
}

func TestLabelEnsureReportsAFailedCreation(t *testing.T) {
	c, _ := client(t, bad(), bad())
	if _, err := c.LabelEnsure(context.Background(), testSlug(t), forge.Label{Name: "x", Colour: "1a7f37"}); err == nil {
		t.Error("a refused creation reported success")
	}
}

func TestLabelEnsureLeavesALabelAlreadyDeclaredAsAsked(t *testing.T) {
	c, r := client(t, out(`{"color":"1A7F37"}`))

	got, err := c.LabelEnsure(context.Background(), testSlug(t), forge.Label{Name: "crossrev/converged", Colour: "1a7f37"})
	if err != nil {
		t.Fatalf("LabelEnsure: %v", err)
	}
	if got != forge.LabelExists {
		t.Errorf("state = %q, want exists", got)
	}
	if len(r.specs) != 1 {
		t.Errorf("gh was invoked %v, want the read alone", r.argvs())
	}
}

// Recolouring is what makes the six loop colours need no migration.
func TestLabelEnsureRecolours(t *testing.T) {
	c, r := client(t, out(`{"color":"5319e7"}`), out("{}"))

	got, err := c.LabelEnsure(context.Background(), testSlug(t), forge.Label{Name: "crossrev/converged", Colour: "1a7f37"})
	if err != nil {
		t.Fatalf("LabelEnsure: %v", err)
	}
	if got != forge.LabelRecoloured {
		t.Errorf("state = %q, want recoloured", got)
	}
	r.wantArgs(t, 1, "api", "--method", "PATCH", "repos/acme/widget/labels/crossrev/converged",
		"-f", "color=1a7f37")
}

// A failed recolour is cosmetic: the label still drives the chain.
func TestLabelEnsureWarnsOnAFailedRecolour(t *testing.T) {
	var warned []string
	rec := &recorder{results: []exec.Result{out(`{"color":"5319e7"}`), bad()}}
	c := ghexec.New(rec, passthrough{}, ghexec.WithWarn(func(summary, _ string) { warned = append(warned, summary) }))

	got, err := c.LabelEnsure(context.Background(), testSlug(t), forge.Label{Name: "crossrev/converged", Colour: "1a7f37"})
	if err != nil {
		t.Fatalf("LabelEnsure: %v", err)
	}
	if got != forge.LabelExists {
		t.Errorf("state = %q, want exists", got)
	}
	if len(warned) != 1 {
		t.Errorf("warnings = %v, want one", warned)
	}
}

// The colour a caller leaves empty is the one lib/github.sh:283 defaults to.
func TestLabelEnsureDefaultsTheColour(t *testing.T) {
	c, r := client(t, bad(), out("{}"))
	if _, err := c.LabelEnsure(context.Background(), testSlug(t), forge.Label{Name: "bug"}); err != nil {
		t.Fatalf("LabelEnsure: %v", err)
	}
	r.wantArgs(t, 1, "api", "--method", "POST", "repos/acme/widget/labels",
		"-f", "name=bug", "-f", "color=ededed", "-f", "description=")
}

func TestPullRequestLabelAddArgv(t *testing.T) {
	c, r := client(t)
	if err := c.PullRequestLabelAdd(context.Background(), testSlug(t), 42, "crossrev/pass-1"); err != nil {
		t.Fatalf("PullRequestLabelAdd: %v", err)
	}
	r.wantArgs(t, 0, "api", "--method", "POST", "repos/acme/widget/issues/42/labels",
		"-f", "labels[]=crossrev/pass-1")
}

// The chain is label-driven, so this one is fatal rather than cosmetic.
func TestPullRequestLabelAddReportsARefusal(t *testing.T) {
	c, _ := client(t, bad())
	if err := c.PullRequestLabelAdd(context.Background(), testSlug(t), 42, "crossrev/pass-1"); err == nil {
		t.Error("a refused label reported success")
	}
}

func TestPullRequestLabelRemoveArgv(t *testing.T) {
	c, r := client(t, bad())
	c.PullRequestLabelRemove(context.Background(), testSlug(t), 42, "crossrev/awaiting-review")
	r.wantArgs(t, 0, "api", "--method", "DELETE",
		"repos/acme/widget/issues/42/labels/crossrev/awaiting-review")
}
