package prompt_test

import (
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/prompt"
)

func checkFixture() prompt.Check {
	return prompt.Check{
		Meta: prompt.Meta{
			Repo: prompt.Str("acme/widget"),
			PR:   prompt.Num(42),
			Pass: prompt.Num(1),
		},
		Candidates: []prompt.CheckCandidate{
			{
				Position: 1, Total: 2, ID: "94ae87514df00d4e",
				Path: "app.go", Line: 12, Side: "RIGHT",
				Severity: "high", Category: "correctness", PreExisting: false,
				Concerns:     []string{"correctness"},
				Title:        "Unchecked fetch response",
				Why:          "A failed request looks like a success.",
				Changed:      true,
				ExcerptLabel: "The hunk at app.go:12",
				Excerpt:      []byte("@@ -10,3 +10,4 @@\n context\n+added\n context"),
			},
			{
				Position: 2, Total: 2, ID: "743ffc807492ddce",
				Path: "old.go", Line: 3, Side: "RIGHT",
				Severity: "low", Category: "maintainability", PreExisting: true,
				Title:   "Stale comment",
				Why:     "The comment names a removed flag.",
				Changed: false,
				ExcerptLabel: "old.go lines 1-23 at head",
				Excerpt:      []byte("line one\nline two"),
				SharedWith:   []int{1},
			},
		},
		Reads: prompt.ReadsBlock("served"),
	}
}

// The check prompt numbers every candidate with its finding, the neutral
// changed-lines fact, and its excerpt, and instructs one decision per
// number as JSON and nothing else.
func TestCheckPromptNumbersEveryCandidate(t *testing.T) {
	got := string(checkFixture().Render())

	for _, want := range []string{
		"You are the cross-model check of CrossRev, judging pass 1's findings on acme/widget pull request #42.",
		"## Candidate 1 of 2",
		"- Finding: `94ae87514df00d4e`, app.go:12 (RIGHT)",
		"- Severity: high · Category: correctness · Pre-existing: no",
		"- Concerns: correctness",
		"- Title: Unchecked fetch response",
		"A failed request looks like a success.",
		"The anchored lines changed between base and head: yes.",
		"The hunk at app.go:12:",
		"## Candidate 2 of 2",
		"- Severity: low · Category: maintainability · Pre-existing: yes",
		"The anchored lines changed between base and head: no.",
		"Candidates 1, 2 share the excerpt below.",
		"old.go lines 1-23 at head:",
		"unchanged lines can still be broken by the change",
		"would the defect survive a revert?",
		"One decision per candidate number — no more, no fewer, no repeated positions.",
		prompt.ReadsBlock("served"),
	} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt carries no %q:\n%s", want, got)
		}
	}
	// A finding recorded before concerns existed names none.
	if strings.Count(got, "- Concerns:") != 1 {
		t.Errorf("prompt names concerns %d times, want once:\n%s", strings.Count(got, "- Concerns:"), got)
	}
}

// The check confirms a real defect however old it is: a defect the
// pull request did not introduce earns a pre_existing correction,
// never a rejection for its age alone.
func TestCheckPromptConfirmsRealPreExistingDefects(t *testing.T) {
	got := string(checkFixture().Render())
	if strings.Contains(got, "neither touched nor could have broken") {
		t.Error("the prompt rejects findings by attribution rather than validity")
	}
	for _, want := range []string{
		"including a real defect this pull request did not introduce",
		"Correct `pre_existing`",
		"pre-existing defects are still reported",
		"Reject a candidate only when the code is correct",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt carries no %q:\n%s", want, got)
		}
	}
}

// The opening names the check rather than a leg, so a prompt-routed stub
// answers from its own route rather than the review's.
func TestCheckPromptNamesNoLeg(t *testing.T) {
	got := string(checkFixture().Render())
	for _, leg := range []string{"You are the review leg", "You are the resolve leg"} {
		if strings.Contains(got, leg) {
			t.Errorf("prompt contains %q", leg)
		}
	}
}

// Without an excerpt the label says unavailable rather than rendering an
// empty fence the checker could read as empty code.
func TestCheckPromptMarksAMissingExcerpt(t *testing.T) {
	fixture := checkFixture()
	fixture.Candidates[0].Excerpt = nil
	got := string(fixture.Render())
	if !strings.Contains(got, "The hunk at app.go:12: unavailable.") {
		t.Errorf("prompt marks no missing excerpt:\n%s", got)
	}
}

// A candidate whose excerpt renders under another names it rather than
// repeating the fence or crying unavailable.
func TestCheckPromptNamesASharedExcerpt(t *testing.T) {
	fixture := checkFixture()
	fixture.Candidates[1].Excerpt = nil
	fixture.Candidates[1].SharedWith = nil
	fixture.Candidates[1].SharedFirst = 1
	got := string(fixture.Render())
	if !strings.Contains(got, "Excerpt: as candidate 1 above.") {
		t.Errorf("prompt names no shared excerpt:\n%s", got)
	}
	if strings.Contains(got, "old.go lines 1-23 at head: unavailable.") {
		t.Errorf("a shared excerpt reads unavailable:\n%s", got)
	}
}
