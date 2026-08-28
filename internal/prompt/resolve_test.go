package prompt_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/prompt"
)

// resolveOracle is prompt_resolve.json: the inputs the Bash function was called
// with, and the bytes it wrote.
type resolveOracle struct {
	Captured struct {
		Platform         string `json:"platform"`
		TrImplementation string `json:"tr_implementation"`
		Locale           string `json:"locale"`
	} `json:"captured"`
	Function string `json:"function"`
	Inputs   struct {
		Skill      string            `json:"skill"`
		Diff       string            `json:"diff"`
		Meta       prompt.Meta       `json:"meta"`
		Findings   []prompt.Finding  `json:"findings"`
		Threads    []prompt.Thread   `json:"threads"`
		Candidates prompt.Candidates `json:"candidates"`
	} `json:"inputs"`
	Prompt string `json:"prompt"`
}

func loadResolveOracle(t *testing.T) resolveOracle {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(parityFixtureDir, "prompt_resolve.json"))
	if err != nil {
		t.Fatalf("reading the prompt_resolve oracle: %v", err)
	}
	var o resolveOracle
	if err := json.Unmarshal(raw, &o); err != nil {
		t.Fatalf("decoding the prompt_resolve oracle: %v", err)
	}
	return o
}

// quarantinedInTheFixture is what `_sandbox_paths | paste -sd, -` answered when
// the oracle was captured: every path lib/harnesses.json names, sorted and made
// unique by jq. It is an input to the prompt rather than something the prompt
// package can read, because the descriptor lives behind an effect boundary
// internal/prompt does not cross.
var quarantinedInTheFixture = []string{
	".agents", ".claude", ".clauderc", ".codex", ".cursor", ".gemini",
	".github/copilot-instructions.md", ".grok", ".mcp.json", ".opencode",
	"AGENT.md", "AGENTS.md", "Agents.md", "CLAUDE.local.md", "CLAUDE.md",
	"Claude.md", "GEMINI.md", "agents.md", "claude.md", "opencode.json",
	"opencode.jsonc",
}

func resolveFromOracle(o resolveOracle) prompt.Resolve {
	return prompt.Resolve{
		Skill:            []byte(o.Inputs.Skill),
		Diff:             []byte(o.Inputs.Diff),
		Meta:             o.Inputs.Meta,
		Findings:         o.Inputs.Findings,
		Threads:          o.Inputs.Threads,
		Candidates:       o.Inputs.Candidates,
		QuarantinedPaths: quarantinedInTheFixture,
	}
}

// TestResolveMatchesTheFrozenPrompt is the second half of surface 11.
func TestResolveMatchesTheFrozenPrompt(t *testing.T) {
	o := loadResolveOracle(t)
	if o.Function != "prompt_resolve" {
		t.Fatalf("the oracle names %q rather than prompt_resolve", o.Function)
	}

	got := resolveFromOracle(o).Render()
	if string(got) != o.Prompt {
		t.Fatalf("prompt_resolve is not byte-identical (%d bytes against %d):\n%s",
			len(got), len(o.Prompt), firstDifference(got, []byte(o.Prompt)))
	}
}

// The quarantined paths reach the prompt joined the way `paste -sd, - | sed
// 's/,/, /g'` joins them (lib/prompt.sh:234).
func TestResolveNamesTheQuarantinedPaths(t *testing.T) {
	o := loadResolveOracle(t)
	got := string(resolveFromOracle(o).Render())
	want := "- These paths are **deliberately not in the checkout**: " +
		".agents, .claude, .clauderc, .codex, .cursor, .gemini, " +
		".github/copilot-instructions.md, .grok, .mcp.json, .opencode, " +
		"AGENT.md, AGENTS.md, Agents.md, CLAUDE.local.md, CLAUDE.md, " +
		"Claude.md, GEMINI.md, agents.md, claude.md, opencode.json, opencode.jsonc. "
	if !strings.Contains(got, want) {
		t.Fatalf("the quarantine list is missing or differs")
	}
}

// The three optional sections are dropped rather than printed empty
// (lib/prompt.sh:263, 277, and prompt_commit_convention's empty-base guard).
func TestResolveDropsTheEmptyOptionalSections(t *testing.T) {
	o := loadResolveOracle(t)
	in := resolveFromOracle(o)
	in.Findings = nil
	in.Threads = nil
	in.Candidates = nil
	got := string(in.Render())

	for _, heading := range []string{
		"## Issues that might already cover one of these",
		"## The conversation so far",
		"## This repository's commit convention",
	} {
		if strings.Contains(got, heading) {
			t.Errorf("%q was printed with nothing to put under it", heading)
		}
	}
}

// A resolved thread is shown, and labelled, where the review prompt leaves it
// out entirely (lib/prompt.sh:279).
func TestResolveShowsResolvedThreadsAndLabelsThem(t *testing.T) {
	o := loadResolveOracle(t)
	got := string(resolveFromOracle(o).Render())
	if !strings.Contains(got, "### app.ts:3 (resolved)\n") {
		t.Error("the resolved thread is missing its label, or missing entirely")
	}
	if !strings.Contains(got, "### app.ts:2\n") {
		t.Error("the unresolved thread is missing")
	}
}

// A finding the resolve leg already settled carries the sentence that says so,
// and one it has not seen before carries nothing in its place
// (lib/prompt.sh:258-260).
func TestResolveNamesAPriorResolution(t *testing.T) {
	o := loadResolveOracle(t)
	in := resolveFromOracle(o)
	in.Findings = append([]prompt.Finding(nil), o.Inputs.Findings...)
	in.Findings[0].PriorResolution = "skipped"
	got := string(in.Render())

	want := "- **You settled this `skipped` in an earlier pass.** If it is unchanged and re-raised, " +
		"escalate rather than re-argue.\n"
	if !strings.Contains(got, want) {
		t.Fatalf("wanted %q", want)
	}
	if strings.Count(got, "You settled this") != 1 {
		t.Fatalf("the second finding, which was never settled, carried the sentence too")
	}
}

// The candidate blocks are headed by the finding's number as well as its id, and
// they keep the order the orchestrator built the object in rather than any
// sorted order (lib/prompt.sh:269-273, lib/run.sh:2482).
func TestResolveKeepsTheCandidateOrder(t *testing.T) {
	o := loadResolveOracle(t)
	in := resolveFromOracle(o)
	in.Candidates = prompt.Candidates{
		{FindingID: "cccc000000000003", Issues: []prompt.Issue{{Number: 31, State: "CLOSED", Title: "b"}}},
		{FindingID: "aaaa000000000001", Issues: []prompt.Issue{{Number: 17, State: "OPEN", Title: "a"}}},
	}
	got := string(in.Render())

	second := strings.Index(got, "### candidates for finding 2 (`cccc000000000003`)")
	first := strings.Index(got, "### candidates for finding 1 (`aaaa000000000001`)")
	if second < 0 || first < 0 {
		t.Fatalf("a candidate heading is missing: finding 2 at %d, finding 1 at %d", second, first)
	}
	if second > first {
		t.Fatal("the blocks were reordered; the object's own order is the order the prompt shows")
	}
	if !strings.Contains(got, "- **#31** (CLOSED) b\n") {
		t.Error("the closed candidate's line is missing")
	}
}

// A candidate keyed on a finding id nothing was numbered with prints the null
// that jq's `first` yields (lib/prompt.sh:271).
func TestResolvePrintsNullForACandidateNoFindingClaims(t *testing.T) {
	o := loadResolveOracle(t)
	in := resolveFromOracle(o)
	in.Candidates = prompt.Candidates{
		{FindingID: "dddd000000000004", Issues: []prompt.Issue{{Number: 5, State: "OPEN", Title: "x"}}},
	}
	if !strings.Contains(string(in.Render()), "### candidates for finding null (`dddd000000000004`)") {
		t.Fatal("wanted the null jq prints when no finding carries that id")
	}
}
