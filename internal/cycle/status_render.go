package cycle

import (
	"fmt"
	"strings"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/ui"
)

// statusFooter is the last line of the page (lib/run.sh:3102). It says where
// the words came from, because the reader's next question after a header they
// did not expect is whether the terminal and the pull request agree.
const statusFooter = "State is read from the pull request itself, so this is the same view a workflow gets."

// statusLegColumn is the width the leg name is padded to before the
// description, `printf '%-9s'` at lib/run.sh:3209. Nine holds `resolve` with
// two spaces after it, so every description on the page starts at one column.
const statusLegColumn = 9

// statusPassColumn is the width the pass number is padded to in the gutter,
// `printf '%-2s '` at lib/run.sh:3094: two for the number and one space, which
// leaves a two-digit pass its column without moving the glyph.
const statusPassColumn = 2

// Render prints a Report as `crossrev status` prints it (lib/run.sh:3053-3103).
//
// Every decision was made in Load. This lays them out, and the split is what
// lets the page be asserted byte for byte against the shell without a pull
// request behind it: the same Report renders the same bytes.
func Render(out *ui.IO, report Report) {
	out.SectionState(fmt.Sprintf("%s#%d", report.Repo, report.PR),
		string(report.State), report.Colour, report.Note)

	out.Gap()
	out.Head("PULL REQUEST")
	out.Line("title      " + report.Title)
	// Omitted rather than printed empty (lib/run.sh:3071). A pull request read
	// from an endpoint that does not answer `url` has no link to give, and a
	// label with nothing after it reads as a failure to fetch one.
	if report.URL != "" {
		out.Line("url        " + report.URL)
	}
	out.Line(fmt.Sprintf("head       %s on %s, %d file(s)",
		statusAbbreviate(report.HeadSHA), report.HeadBranch, report.ChangedFiles))
	out.Line("labels     " + statusLabelList(report.Labels))
	// Only when it is one: a line reading "draft no" on every other pull
	// request would say nothing (lib/run.sh:3080).
	if report.Draft {
		out.Line("draft      yes — no workflow runs a leg on it")
	}

	out.Gap()
	out.Head("LOOP")
	out.Line(fmt.Sprintf("mode       %s, markers by %s", report.Mode, report.Author))
	out.Line("passes     " + statusPassesLine(report))
	// Backlog.String() is the one line cfg_resolve_backlog prints, which is
	// what lib/run.sh:3085 interpolates: the destination, plus the layout and
	// path only where a repository backlog has them.
	out.Line("deferred   " + report.Backlog.String())

	// Omitted entirely rather than printed with nothing under it
	// (lib/run.sh:3087-3097). A heading with an empty body reads as a bug, and
	// the passes line above already says none yet.
	if report.MaxPass > 0 {
		out.Gap()
		out.Head("PASSES")
		for _, row := range report.Rows {
			out.Row(statusGutter(row), row.Step, statusLegLabel(row.Leg)+row.Description)
		}
	}

	out.Gap()
	out.Head("NEXT")
	for _, line := range report.Next {
		if line.Command {
			out.Cmd(line.Text)
		} else {
			out.Line(line.Text)
		}
	}
	out.End(statusFooter)
}

// statusLabelList is `${CTX_LABELS:-none}` at lib/run.sh:3073, over the
// space-joined list ctx_load builds at lib/run.sh:277.
func statusLabelList(labels []string) string {
	if len(labels) == 0 {
		return "none"
	}
	return strings.Join(labels, " ")
}

// statusPassesLine is the three-way pass wording at lib/run.sh:3078-3084.
//
// Past the cap is its own arm rather than an overflowing "4 of 3", because a
// pass beyond the cap is the state a halt is about and the reader has to see
// which number was exceeded.
func statusPassesLine(report Report) string {
	switch {
	case report.Pass == 0:
		return fmt.Sprintf("none yet, up to %d", report.MaxPassesPerCycle)
	case report.Pass > report.MaxPassesPerCycle:
		return fmt.Sprintf("%d (past the cycle cap of %d)", report.Pass, report.MaxPassesPerCycle)
	default:
		return fmt.Sprintf("%d of %d", report.Pass, report.MaxPassesPerCycle)
	}
}

// statusGutter is what sits left of the glyph: the pass number on the review
// row and three spaces on the resolve row under it (lib/run.sh:3094-3095).
//
// The number is printed once per pass rather than on both rows, so the two legs
// of one pass read as one block.
func statusGutter(row LegRow) string {
	if row.Leg != core.LegReview {
		return strings.Repeat(" ", statusPassColumn+1)
	}
	return fmt.Sprintf("%-*d ", statusPassColumn, row.Pass)
}

// statusLegLabel is the left column of a leg row, padded so the descriptions
// line up (lib/run.sh:3209).
func statusLegLabel(leg core.Leg) string {
	return fmt.Sprintf("%-*s", statusLegColumn, leg)
}
