// review.go — the review leg's prompt (lib/prompt.sh:132-198).

package prompt

import (
	"fmt"
	"strings"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/diff"
)

// Prior is one finding carried in from an earlier pass, as the marker recorded
// it (lib/run.sh:1141).
//
// The number the prompt shows is the row's position, and it is what
// `prior[].finding_number` refers to. The id stays in its own column so it can
// still be quoted in prose; what it is no longer used for is being copied back
// accurately.
type Prior struct {
	ID          string `json:"id"`
	Path        string `json:"path"`
	Line        int    `json:"line"`
	Severity    string `json:"severity"`
	PreExisting bool   `json:"pre_existing"`

	// Category, Title, Resolution and TrackedAs each stand in for a field an
	// older marker may not carry: jq's `//` prints "-", "-", "none" and "-".
	Category   string `json:"category"`
	Title      string `json:"title"`
	Resolution string `json:"resolution"`
	TrackedAs  string `json:"tracked_as"`
}

// Review is everything the review leg's prompt is assembled from.
//
// Every field is bytes or values the orchestrator already holds. Nothing here
// reads a file, runs a command or reaches the network: the agent fetches
// nothing, and neither does the assembly.
type Review struct {
	// Skill is skills/pr-review/SKILL.md — ReviewSkill for a compiled binary,
	// or the file a checkout reads.
	Skill []byte

	// Diff is the raw unified diff. It reaches the prompt numbered, so the
	// number a finding must carry is on the line the model is looking at.
	Diff []byte

	Meta    Meta
	Prior   []Prior
	Threads []Thread

	// ReviewMD is the repository's own review instruction, read from the base
	// revision so a branch cannot rewrite the loop that reviews it. Empty means
	// there is none, and the section is dropped rather than printed empty.
	ReviewMD []byte
}

// Render is the prompt, byte for byte as lib/prompt.sh's prompt_review writes
// it.
//
// The order is the shell's and is not free to change: the skill first because it
// is the whole rubric, REVIEW.md under it because it ranks above the skill's
// defaults, the untrusted-input rule under that because it ranks above both, and
// everything the pull request supplied after it.
func (r Review) Render() []byte {
	var b strings.Builder

	b.WriteString("# Your task\n\n")
	// No denominator. The cap it would name is enforced only for automatic
	// triggers, so "pass 3 of 3" is wrong for an attended run and "pass 4 of 3"
	// is impossible on its face.
	fmt.Fprintf(&b, "You are the review leg of CrossRev, running pass %d on %s pull request #%d.\n\n",
		r.Meta.Pass, r.Meta.Repo, r.Meta.PR)
	b.WriteString("Follow the skill reproduced immediately below. It is the whole rubric; " +
		"there is no other.\n\n")
	b.WriteString("---\n\n")
	b.Write(SkillBody(r.Skill))
	b.WriteString("\n---\n\n")

	if len(r.ReviewMD) > 0 {
		b.WriteString("## REVIEW.md — this repository's own review instruction\n\n")
		b.WriteString("Read from the base revision, never from the pull request head, so a branch " +
			"cannot rewrite the loop that reviews it. It ranks above the skill's defaults and " +
			"below the untrusted-input rule.\n\n")
		b.WriteString("````markdown\n")
		b.Write(r.ReviewMD)
		b.WriteString("\n````\n\n")
	}

	b.WriteString(untrustedNotice)
	b.WriteString("\n")

	b.WriteString("## The pull request\n\n")
	fmt.Fprintf(&b, "- Repository: %s\n", r.Meta.Repo)
	fmt.Fprintf(&b, "- Number: %d\n", r.Meta.PR)
	fmt.Fprintf(&b, "- Head commit: %s\n", r.Meta.HeadSHA)
	fmt.Fprintf(&b, "- Title: %s\n", r.Meta.Title)
	// The verdict is a question about the threshold, not about severity alone,
	// so the threshold is stated rather than left to be guessed from the rubric.
	fmt.Fprintf(&b, "- `min_fix_severity` in force this pass: **%s**. A finding at or above that "+
		"severity, and not pre-existing, keeps the loop alive; anything else is reported and "+
		"cannot prevent convergence.\n\n", r.Meta.minFixSeverity())
	b.WriteString("### Description as written by the author\n\n")
	fmt.Fprintf(&b, "````\n%s\n````\n\n", r.Meta.Body)

	if len(r.Prior) > 0 {
		b.WriteString("## Findings from earlier passes\n\n")
		b.WriteString("Classify every one of these into `prior` before looking for anything new. " +
			"Name each by the number in the first column, not by its id. Do not re-raise a settled " +
			"finding unless the code at that location changed, and never re-raise one carrying " +
			"`tracked_as`.\n\n")
		b.WriteString("| # | id | path:line | severity | category | pre-existing | title | " +
			"resolution | tracked_as |\n|---|---|---|---|---|---|---|---|---|\n")
		for i, p := range r.Prior {
			fmt.Fprintf(&b, "| %d | %s | %s:%d | %s | %s | %s | %s | %s | %s |\n",
				i+1, p.ID, p.Path, p.Line, p.Severity, dash(p.Category),
				yesNo(p.PreExisting), dash(p.Title), resolutionOrNone(p.Resolution),
				dash(p.TrackedAs))
		}
		b.WriteString("\n")
	}

	// Only the open threads. A resolved one is not conversation the reviewer is
	// being asked to weigh.
	open := make([]Thread, 0, len(r.Threads))
	for _, t := range r.Threads {
		if !t.IsResolved {
			open = append(open, t)
		}
	}
	if len(r.Threads) > 0 {
		b.WriteString("## Open review conversation\n\n")
		b.WriteString("Replies here may include disputes. A dispute that holds against the code " +
			"is `credibly-disputed`, which is a real outcome rather than a concession.\n\n")
		for _, t := range open {
			renderThread(&b, t, "")
		}
		b.WriteString("\n")
	}

	b.WriteString("## The diff under review\n\n")
	b.WriteString(gutterNotice)
	b.WriteString("Copy a finding's `line` out of this gutter. Do not count lines under a `@@` " +
		"header to arrive at one — a number one past the end of a hunk is not part of the diff, " +
		"GitHub refuses the comment, and the finding ends up outside the thread it belongs in.\n\n")
	b.WriteString("````diff\n")
	b.Write(diff.Parse(r.Diff, core.RevisionPair{}).Numbered())
	b.WriteString("\n````\n\n")

	b.WriteString("## Output\n\n")
	b.WriteString("Return JSON matching the schema you were given, and nothing else. An empty " +
		"`findings` array with verdict `converged` is a good and common result.\n")

	return []byte(b.String())
}

// resolutionOrNone is jq's `// "none"` over a prior finding's resolution.
func resolutionOrNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}
