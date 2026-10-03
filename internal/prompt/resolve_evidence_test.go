package prompt_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/prompt"
)

func evidenceInput(t *testing.T) prompt.Resolve {
	t.Helper()
	o := loadResolveOracle(t)
	in := resolveFromOracle(o)
	in.Findings = append([]prompt.Finding(nil), o.Inputs.Findings...)
	return in
}

func siblings(n int) []prompt.Sibling {
	out := make([]prompt.Sibling, 0, n)
	for i := 1; i <= n; i++ {
		out = append(out, prompt.Sibling{Path: fmt.Sprintf("pkg/file%02d.go", i), Line: 10 * i, Term: "parseThing"})
	}
	return out
}

// A finding with neither field renders as it always has: the frozen oracle
// pins the whole prompt, and this pins that the new fields add no line.
func TestResolveEvidenceFieldsAreAbsentByDefault(t *testing.T) {
	got := string(evidenceInput(t).Render())
	for _, banned := range []string{"Recurrence candidate", "Sibling locations"} {
		if strings.Contains(got, banned) {
			t.Errorf("a finding with no evidence printed %q", banned)
		}
	}
}

func TestResolveShowsARecurrenceCandidateWithItsEarlierFix(t *testing.T) {
	in := evidenceInput(t)
	in.Findings[0].Recurrence = &prompt.Recurrence{Pass: 2, FindingID: "abcdef0123456789", Commit: "0123456789abcdef0123456789abcdef01234567"}
	got := string(in.Render())

	for _, want := range []string{
		"- **Recurrence candidate.**",
		"pass 2",
		"`abcdef0123456789`",
		"`0123456`",
		"incomplete",
		"say what you concluded",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("recurrence line is missing %q", want)
		}
	}
	if strings.Count(got, "Recurrence candidate") != 1 {
		t.Errorf("the second finding, which has no earlier fix, carried the line too")
	}
	if strings.Contains(got, "0123456789abcdef0123456789abcdef01234567") {
		t.Error("the recurrence line printed the whole commit rather than its short form")
	}
}

// The skill escalates a point that was disputed and re-raised unchanged. A
// finding that was fixed before is a different case: the reopened-fix check
// replaces the escalate instruction for it.
func TestResolvePriorFixedDoesNotTellTheResolverToEscalate(t *testing.T) {
	in := evidenceInput(t)
	in.Findings[0].PriorResolution = prompt.Str("fixed")
	got := string(in.Render())

	if !strings.Contains(got, "You settled this `fixed` in an earlier pass") {
		t.Fatal("a finding fixed before lost the line that says so")
	}
	if strings.Contains(got, "escalate rather than re-argue") {
		t.Error("the prior-fixed line still carries the escalate-rather-than-re-argue instruction")
	}
}

func TestResolveShowsSiblingsAsAdvisoryAndPartial(t *testing.T) {
	in := evidenceInput(t)
	in.Findings[0].Siblings = siblings(3)
	got := string(in.Render())

	for _, want := range []string{
		"- Sibling locations",
		"partial",
		"`pkg/file01.go:10` (`parseThing`)",
		"`pkg/file03.go:30` (`parseThing`)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("sibling block is missing %q", want)
		}
	}
	if strings.Count(got, "Sibling locations") != 1 {
		t.Errorf("only one finding carries siblings, got %d blocks", strings.Count(got, "Sibling locations"))
	}
}

func TestRenderWithinKeepsEverySiblingWhenThePromptFits(t *testing.T) {
	in := evidenceInput(t)
	in.Findings[0].Siblings = siblings(10)
	full := in.Render()

	got := in.RenderWithin(len(full))
	if string(got) != string(full) {
		t.Fatal("a prompt that fits the limit was shortened")
	}
}

func TestRenderWithinShrinksSiblingsBeforeTheLimit(t *testing.T) {
	in := evidenceInput(t)
	in.Findings[0].Siblings = siblings(10)
	in.Findings[1].Siblings = siblings(10)
	full := in.Render()

	// One byte under the full render: some entries must go, and the answer
	// must fit.
	limit := len(full) - 1
	got := in.RenderWithin(limit)
	if len(got) > limit {
		t.Fatalf("the prompt is %d bytes, over the %d-byte limit", len(got), limit)
	}
	kept := strings.Count(string(got), "(`parseThing`)")
	if kept == 0 || kept >= 20 {
		t.Fatalf("expected fewer entries but not none, kept %d of 20", kept)
	}
}

func TestRenderWithinDropsSiblingsWhenNothingElseFits(t *testing.T) {
	in := evidenceInput(t)
	bare := in.Render()
	in.Findings[0].Siblings = siblings(10)
	in.Findings[1].Siblings = siblings(10)

	got := in.RenderWithin(len(bare))
	if string(got) != string(bare) {
		t.Fatalf("with room for no sibling the prompt is %d bytes, want the bare %d", len(got), len(bare))
	}
	if strings.Contains(string(got), "Sibling locations") {
		t.Error("the sibling heading survived with no entries")
	}
}

// Past the limit even with every sibling gone, the prompt comes back whole so
// the size gate refuses it, rather than this method hiding the overflow.
func TestRenderWithinLeavesAnOverflowToTheSizeGate(t *testing.T) {
	in := evidenceInput(t)
	in.Findings[0].Siblings = siblings(4)
	bare := evidenceInput(t).Render()

	got := in.RenderWithin(len(bare) - 1)
	if len(got) != len(bare) {
		t.Fatalf("got %d bytes, want the sibling-free %d for the gate to measure", len(got), len(bare))
	}
}

func TestRenderDoesNotMutateTheCallersSiblings(t *testing.T) {
	in := evidenceInput(t)
	in.Findings[0].Siblings = siblings(10)
	bare := evidenceInput(t).Render()
	_ = in.RenderWithin(len(bare))
	if len(in.Findings[0].Siblings) != 10 {
		t.Fatalf("RenderWithin shortened the caller's list to %d", len(in.Findings[0].Siblings))
	}
}
