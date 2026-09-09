// review.go — the review leg's prompt (lib/prompt.sh:132-198), plus the
// Review Intelligence batch input (plan Task A3).
//
// The frozen sections stay byte-identical: the skill through the untrusted
// notice, the pull-request block, prior findings, open threads, the full
// base-to-head diff, and the output instruction. The batch block below is the
// one intentional addition — numbered required files with readable content or
// explicit access limits, advisory summaries and exclusions — and
// TestReviewIntelligenceSupersedesOnlyTheFrozenPromptSections pins that the
// frozen prompt is a subsequence of what Render writes.

package prompt

import (
	"fmt"
	"strings"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/diff"
)

// Prior is one finding carried in from an earlier pass, as the marker recorded
// it (lib/run.sh:1147).
//
// The number the prompt shows is the row's position, and it is what
// `prior[].finding_number` refers to. The id stays in its own column so it can
// still be quoted in prose; what it is no longer used for is being copied back
// accurately.
type Prior struct {
	ID          Value `json:"id"`
	Path        Value `json:"path"`
	Line        Value `json:"line"`
	Severity    Value `json:"severity"`
	PreExisting Value `json:"pre_existing"`

	// Category, Title, Resolution and TrackedAs each stand in for a field an
	// older marker may not carry: jq's `//` prints "-", "-", "none" and "-".
	// The four above carry no such default, so an absent one prints the word
	// `null` — which is what the Value type reproduces rather than flattening
	// to an empty string or a zero.
	//
	// Line is the field where that matters beyond documentation.
	// lib/validate.sh checks only that a finding's `line` is a number, so a
	// payload writing 1.5 is accepted and the shell prints `app.ts:1.5`. A Go
	// int cannot hold it, and decoding one into this struct used to fail
	// outright — two packages in one commit disagreeing about what a line
	// number is.
	Category   Value `json:"category"`
	Title      Value `json:"title"`
	Resolution Value `json:"resolution"`
	TrackedAs  Value `json:"tracked_as"`
}

// Review is everything the review leg's prompt is assembled from.
//
// Every field is bytes or values the orchestrator already holds. Nothing here
// reads a file, runs a command or reaches the network: the agent fetches
// nothing, and neither does the assembly.
//
// Batch is the Review Intelligence input for this call: numbered required
// files with readable content or explicit access limits, advisory summaries
// and exclusions. It is empty for the frozen parity-era prompt, which carries
// the full diff alone. A3 renders the batch; C1 wires the batch loop that
// fills it.
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

	// Batch holds the numbered required files this call must account for.
	// Nil means the frozen prompt: no batch block is rendered.
	Batch []BatchUnit

	// Advisory holds untouched context files with the rule that found each.
	// Nil means none is rendered.
	Advisory []AdvisoryRef

	// Excluded holds paths removed from the required denominator with their
	// reason. Nil means none is rendered.
	Excluded []ExclusionRef

	// Confirmation holds the B-to-C repair delta: what the resolver changed
	// between the reviewed head and the repaired head. Nil means an initial
	// clean review with nothing to confirm. A set delta renders ahead of
	// the full scope as required confirmation input.
	Confirmation []byte
}

// BatchUnit is one numbered required file: its change, its evidence revision,
// and either readable bytes or an explicit access limit — never both, never
// neither. The number is the unit's 1-based position in the batch, and it is
// what `coverage[].unit_number` refers back to.
type BatchUnit struct {
	// Path is the current path, OldPath the previous path for a rename or a
	// deletion.
	Path    string
	OldPath string
	// Change is how the path changed between the two revisions.
	Change core.ChangeKind
	// ContentRevision is where the evidence bytes were read: the base for a
	// deletion, the head for every other kind.
	ContentRevision core.Revision
	// Body is the readable evidence bytes. Nil when the unit is unavailable
	// or binary.
	Body []byte
	// Available reports whether readable bytes were read.
	Available bool
	// Binary reports a NUL byte in the evidence, git's own binary signal.
	Binary bool
	// Reason names the access limit when the unit has no readable bytes:
	// unreadable, binary or otherwise unavailable content stays a required
	// obligation with this visible limit.
	Reason string
	// NumberedDiff is the gutter-numbered diff for this file's own lines, or
	// nil when none applies.
	NumberedDiff []byte
}

// AdvisoryRef is one untouched path offered as uncertain context, with the
// rule that found it: search for a fixed-string hit, convention for an
// adjacent-test naming match.
type AdvisoryRef struct {
	Path string
	Rule string
	Term string
}

// ExclusionRef is one path removed from the required denominator, with the
// reason it was removed.
type ExclusionRef struct {
	Path   string
	Reason string
}

// Render is the prompt, byte for byte as lib/prompt.sh's prompt_review writes
// it, plus the Review Intelligence batch block when the batch input is set.
//
// The order is the shell's and is not free to change: the skill first because it
// is the whole rubric, REVIEW.md under it because it ranks above the skill's
// defaults, the untrusted-input rule under that because it ranks above both, and
// everything the pull request supplied after it. The batch block sits between
// the full diff and the output instruction: the diff stays the anchorable
// whole, and the numbered files are the readable work this call accounts for.
// With no batch input Render writes the frozen bytes exactly, which
// TestReviewMatchesTheFrozenPrompt pins and
// TestReviewIntelligenceSupersedesOnlyTheFrozenPromptSections relies on.
func (r Review) Render() []byte {
	var b strings.Builder

	b.WriteString("# Your task\n\n")
	// No denominator. The cap it would name is enforced only for automatic
	// triggers, so "pass 3 of 3" is wrong for an attended run and "pass 4 of 3"
	// is impossible on its face.
	fmt.Fprintf(&b, "You are the review leg of CrossRev, running pass %s on %s pull request #%s.\n\n",
		sub(r.Meta.Pass), sub(r.Meta.Repo), sub(r.Meta.PR))
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
	fmt.Fprintf(&b, "- Repository: %s\n", sub(r.Meta.Repo))
	fmt.Fprintf(&b, "- Number: %s\n", sub(r.Meta.PR))
	fmt.Fprintf(&b, "- Head commit: %s\n", sub(r.Meta.HeadSHA))
	fmt.Fprintf(&b, "- Title: %s\n", sub(r.Meta.Title))
	// The verdict is a question about the threshold, not about severity alone,
	// so the threshold is stated rather than left to be guessed from the rubric.
	fmt.Fprintf(&b, "- `min_fix_severity` in force this pass: **%s**. A finding at or above that "+
		"severity, and not pre-existing, keeps the loop alive; anything else is reported and "+
		"cannot prevent convergence.\n\n", subAlt(r.Meta.MinFixSeverity, "medium"))
	b.WriteString("### Description as written by the author\n\n")
	fmt.Fprintf(&b, "````\n%s\n````\n\n", subAlt(r.Meta.Body, ""))

	if len(r.Prior) > 0 {
		b.WriteString("## Findings from earlier passes\n\n")
		b.WriteString("Classify every one of these into `prior` before looking for anything new. " +
			"Name each by the number in the first column, not by its id. Do not re-raise a settled " +
			"finding unless the code at that location changed, and never re-raise one carrying " +
			"`tracked_as`.\n\n")
		b.WriteString("| # | id | path:line | severity | category | pre-existing | title | " +
			"resolution | tracked_as |\n|---|---|---|---|---|---|---|---|---|\n")
		for i, p := range r.Prior {
			fmt.Fprintf(&b, "| %d | %s | %s:%s | %s | %s | %s | %s | %s | %s |\n",
				i+1, p.ID, p.Path, p.Line, p.Severity, p.Category.Or("-"),
				yesNo(p.PreExisting.Truthy()), p.Title.Or("-"), p.Resolution.Or("none"),
				p.TrackedAs.Or("-"))
		}
		b.WriteString("\n")
	}

	// Only the open threads. A resolved one is not conversation the reviewer is
	// being asked to weigh.
	//
	// `select(.isResolved == false)` is strict, so a thread carrying no
	// isResolved at all is dropped rather than shown: null is not equal to
	// false. The shipped projection always sets the key, and the filter still
	// decides what a model is shown, so it is reproduced rather than assumed.
	open := make([]Thread, 0, len(r.Threads))
	for _, t := range r.Threads {
		if t.IsResolved.IsFalse() {
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

	// The confirmation delta renders ahead of the full scope: after a
	// repair, the reviewer confirms what the resolver changed before
	// re-judging the whole. Empty on an initial clean review, so the frozen
	// parity-era prompt keeps its bytes exactly.
	b.WriteString(renderConfirmation(r.Confirmation))

	// The batch block sits between the full diff and the output instruction:
	// the diff stays the anchorable whole, and the numbered files are the
	// readable work this call must account for. Empty batch input renders
	// nothing, so the frozen parity-era prompt keeps its bytes exactly.
	b.WriteString(renderBatch(r.Batch, r.Advisory, r.Excluded))

	b.WriteString("## Output\n\n")
	b.WriteString("Return JSON matching the schema you were given, and nothing else. An empty " +
		"`findings` array with verdict `converged` is a good and common result.\n")
	if len(r.Batch) > 0 || len(r.Advisory) > 0 || len(r.Excluded) > 0 {
		b.WriteString("Name every numbered file above in `coverage`, one entry per number — " +
			"no more, no fewer, no duplicates — and state `examined_scope` and `known_limits` " +
			"even when the review found nothing.\n")
	}

	return []byte(b.String())
}

// renderConfirmation is the required repair-delta input: the B-to-C diff
// the resolver produced, rendered ahead of the current full scope. It takes
// no disposition and satisfies no coverage entry: it says what changed since
// the reviewed head, so the reviewer confirms the repair before re-judging
// the whole. Empty renders nothing.
func renderConfirmation(delta []byte) string {
	if len(delta) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("## The repair delta to confirm\n\n")
	b.WriteString("The resolver changed code since the reviewed head. Confirm this delta " +
		"first: it is required input, and the dispositions below still account for " +
		"every current required file.\n\n")
	b.WriteString("````diff\n")
	b.Write(delta)
	b.WriteString("\n````\n\n")
	return b.String()
}

// renderBatch is the Review Intelligence batch input: numbered required files
// with readable content or an explicit access limit, advisory summaries and
// exclusions. It renders nothing when the batch is empty, so the frozen
// parity-era prompt — built with no batch — keeps its bytes exactly.
func renderBatch(units []BatchUnit, advisory []AdvisoryRef, excluded []ExclusionRef) string {
	if len(units) == 0 && len(advisory) == 0 && len(excluded) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("## The files under review\n\n")
	if len(units) > 0 {
		b.WriteString("Account for every numbered file below in `coverage`, one entry per " +
			"number. A file disposition means you examined the supplied content and change, " +
			"not merely its pathname. `not_affected` does not exempt a changed file: it says " +
			"the file was read and needs no change, with evidence saying why.\n\n")
		for i, u := range units {
			renderBatchUnit(&b, i+1, u)
		}
	}
	if len(advisory) > 0 {
		b.WriteString("### Advisory context\n\n")
		b.WriteString("Untouched files offered as uncertain context. They take no disposition " +
			"and satisfy none: a real defect found here is still published as a finding, but " +
			"the required file it was found from keeps its own disposition.\n\n")
		for _, a := range advisory {
			if a.Term != "" {
				fmt.Fprintf(&b, "- `%s` (search `%s`)\n", a.Path, a.Term)
			} else {
				fmt.Fprintf(&b, "- `%s` (convention)\n", a.Path)
			}
		}
		b.WriteString("\n")
	}
	if len(excluded) > 0 {
		b.WriteString("### Excluded paths\n\n")
		b.WriteString("Removed from the required set, visibly, with the reason for each. " +
			"They take no disposition and are not omitted in silence.\n\n")
		for _, e := range excluded {
			fmt.Fprintf(&b, "- `%s` — %s\n", e.Path, e.Reason)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// renderBatchUnit is one numbered required file: its change, its evidence
// revision, and either readable bytes or an explicit access limit.
func renderBatchUnit(b *strings.Builder, number int, u BatchUnit) {
	fmt.Fprintf(b, "### %d. `%s` — %s at `%s`\n\n", number, u.Path, u.Change, u.ContentRevision)
	if u.OldPath != "" && u.OldPath != u.Path {
		fmt.Fprintf(b, "Previously `%s`.\n\n", u.OldPath)
	}
	switch {
	case !u.Available:
		fmt.Fprintf(b, "No readable content: %s. This file stays required: record "+
			"`could_not_review` with the failed fallbacks in `reason`.\n\n", u.Reason)
	case u.Binary:
		fmt.Fprintf(b, "Binary content is not shown. This file stays required: judge it on "+
			"provenance, integrity, and build or reproducibility evidence, and record the "+
			"limit in `known_limits`.\n\n")
	default:
		fmt.Fprintf(b, "````\n%s\n````\n\n", quoteBytes(u.Body))
	}
	if len(u.NumberedDiff) > 0 {
		fmt.Fprintf(b, "Its numbered diff:\n\n````diff\n%s\n````\n\n",
			quoteBytes(u.NumberedDiff))
	}
}

// quoteBytes is the prompt's own trailing-newline rule: the fenced block
// closes on the line after the content, so one trailing newline terminates the
// last content line rather than opening an empty one.
func quoteBytes(body []byte) string {
	return strings.TrimSuffix(string(body), "\n")
}
