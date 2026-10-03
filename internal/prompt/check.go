// check.go — the cross-model check's prompt.
//
// One model reviewed the pull request and raised findings; a second model
// judges each one before anything posts. The candidates are numbered by
// position, each with the excerpt it faults and the neutral fact of
// whether its anchored lines changed between base and head, and the
// checker answers one decision per number.

package prompt

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// CheckCandidate is one numbered finding the checker judges: the finding
// as the reviewer raised it, the concerns that raised it, and the
// excerpt it is about.
type CheckCandidate struct {
	// Position is the candidate's number, 1-based in prompt order, and
	// what the answer's `position` refers back to.
	Position int
	// Total numbers the prompt: "candidate 1 of 3".
	Total int
	// ID is the finding's stable id, for the record rather than the
	// answer: the checker names positions, never ids.
	ID string
	// Path, Line and Side anchor the finding the way the reviewer
	// anchored it.
	Path string
	Line int
	Side string
	// Severity, Category and PreExisting are the reviewer's values,
	// which the severity and pre_existing corrections may replace.
	Severity    string
	Category    string
	PreExisting bool
	// Concerns names what raised the finding, in fixed order. Empty
	// for a finding recorded before concerns existed.
	Concerns []string
	// Title and Why are the finding's own words.
	Title string
	Why   string
	// Changed is the neutral fact: whether the anchored lines changed
	// between base and head. Unchanged lines can still be broken by
	// the change, so this decides nothing on its own.
	Changed bool
	// ExcerptLabel names the excerpt: the hunk, or the committed
	// lines around the anchor.
	ExcerptLabel string
	// Excerpt is the code the candidate faults: the supplied hunk for
	// an in-scope candidate, the committed lines around the anchor at
	// head for one outside the diff.
	Excerpt []byte
	// SharedWith names the other positions rendered against this same
	// excerpt, when one rendering serves several candidates.
	SharedWith []int
	// SharedFirst names the position whose rendering carries this
	// candidate's excerpt, when another candidate's does. Set only
	// when the excerpt renders elsewhere.
	SharedFirst int
}

// Check is everything the cross-model check's prompt is assembled from.
//
// Every field is bytes or values the orchestrator already holds. Nothing
// here reads a file, runs a command or reaches the network.
type Check struct {
	Meta Meta
	// Candidates are the numbered findings this call judges, in
	// position order.
	Candidates []CheckCandidate
	// Reads is the reads block naming this call's read path: the served
	// read tool or no read tool at all. Empty renders nothing.
	Reads string
}

// Render is the check prompt: the instruction, the numbered candidates
// with their excerpts, the reads block, and the output instruction.
//
// The opening names the check rather than a leg, so a prompt-routed
// harness stub answers from its own route rather than the review's.
func (c Check) Render() []byte {
	var b strings.Builder

	b.WriteString("# Your task\n\n")
	fmt.Fprintf(&b, "You are the cross-model check of CrossRev, judging pass %s's findings on %s pull request #%s.\n\n",
		sub(c.Meta.Pass), sub(c.Meta.Repo), sub(c.Meta.PR))
	b.WriteString("Another model reviewed this pull request and raised the findings below. " +
		"Each is numbered. Judge each one against the code: confirm it, reject it, or fold it " +
		"into another candidate as a duplicate. Only confirmed findings post; a rejection keeps " +
		"a wrong finding off the pull request, so judge strictly and reject what does not hold.\n\n")

	b.WriteString("## How to judge\n\n")
	b.WriteString("Confirm a candidate when the defect is real on the code shown — " +
		"including a real defect this pull request did not introduce. " +
		"Correct `pre_existing` when the reviewer's attribution is wrong rather than " +
		"rejecting the defect: pre-existing defects are still reported. " +
		"Reject a candidate only when the code is correct or its reasoning contradicts " +
		"the excerpt. " +
		"Fold a candidate into another as a duplicate when both fault the same defect, naming " +
		"the survivor; a duplicate of a duplicate is fine, and the chain must end at a confirmed candidate.\n\n")
	b.WriteString("Each candidate carries whether its anchored lines changed between base and head. " +
		"That is a neutral fact, not a verdict: unchanged lines can still be broken by the change, " +
		"and changed lines can still be correct. Before correcting `pre_existing`, explain the " +
		"base-versus-head reading the correction rests on: would the defect survive a revert?\n\n")
	b.WriteString("Correct `severity` only when the reviewer's is wrong on its own scale — " +
		"high for a bug that should block merging, medium for worth fixing, low for minor. " +
		"A correction replaces the value before the finding posts; silence keeps it.\n\n")

	b.WriteString(untrustedNotice)
	b.WriteString("\n")

	for _, candidate := range c.Candidates {
		renderCheckCandidate(&b, candidate)
	}

	// The reads block names the call's read path ahead of the output
	// instruction. Empty renders nothing, so prompts built without a mode
	// keep their bytes exactly.
	if c.Reads != "" {
		b.WriteString(c.Reads)
		b.WriteString("\n")
	}

	b.WriteString("## Output\n\n")
	b.WriteString("Return JSON matching the schema you were given, and nothing else. " +
		"One decision per candidate number — no more, no fewer, no repeated positions. " +
		"Each decision carries its position, confirmed, rejected or duplicate with the " +
		"duplicated position, one line of reason in at most 300 characters, and the severity " +
		"and pre_existing corrections when the reviewer's values are wrong.\n")

	return []byte(b.String())
}

// renderCheckCandidate is one numbered candidate: the finding as raised,
// the neutral changed-lines fact, and the excerpt it faults.
func renderCheckCandidate(b *strings.Builder, candidate CheckCandidate) {
	fmt.Fprintf(b, "## Candidate %d of %d\n\n", candidate.Position, candidate.Total)
	fmt.Fprintf(b, "- Finding: `%s`, %s:%d (%s)\n", candidate.ID, candidate.Path, candidate.Line, candidate.Side)
	fmt.Fprintf(b, "- Severity: %s · Category: %s · Pre-existing: %s\n",
		candidate.Severity, candidate.Category, yesNoValue(candidate.PreExisting))
	if len(candidate.Concerns) > 0 {
		fmt.Fprintf(b, "- Concerns: %s\n", strings.Join(candidate.Concerns, ", "))
	}
	fmt.Fprintf(b, "- Title: %s\n\n", candidate.Title)
	fmt.Fprintf(b, "%s\n\n", candidate.Why)
	fmt.Fprintf(b, "The anchored lines changed between base and head: %s.\n\n", yesNoValue(candidate.Changed))
	if len(candidate.SharedWith) > 0 {
		positions := append([]int{candidate.Position}, candidate.SharedWith...)
		sort.Ints(positions)
		shared := make([]string, 0, len(positions))
		for _, position := range positions {
			shared = append(shared, strconv.Itoa(position))
		}
		fmt.Fprintf(b, "Candidates %s share the excerpt below.\n\n", strings.Join(shared, ", "))
	}
	switch {
	case candidate.SharedFirst > 0:
		fmt.Fprintf(b, "Excerpt: as candidate %d above.\n\n", candidate.SharedFirst)
	case len(candidate.Excerpt) > 0:
		fmt.Fprintf(b, "%s:\n\n````\n%s\n````\n\n", candidate.ExcerptLabel, quoteBytes(candidate.Excerpt))
	default:
		fmt.Fprintf(b, "%s: unavailable.\n\n", candidate.ExcerptLabel)
	}
}

// yesNoValue renders a boolean the way the prior-findings table does.
func yesNoValue(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}
