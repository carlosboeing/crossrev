package config_test

import (
	"context"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/config"
	"github.com/carlosboeing/crossrev/internal/core"
)

func trackerOf(t *testing.T, document string) (string, bool) {
	t.Helper()
	tree := files{"": {"AGENTS.md": document}}
	tracker, found, err := config.ProjectMapTracker(context.Background(), core.Revision{}, tree.show())
	if err != nil {
		t.Fatalf("ProjectMapTracker: %v", err)
	}
	return tracker, found
}

func TestProjectMapTrackerReadsTheField(t *testing.T) {
	tests := []struct {
		name     string
		document string
		want     string
		found    bool
	}{
		{"a plain field", "# x\n\n## Project Map\n\n- **Tracker**: GitHub Issues\n", "GitHub Issues", true},
		{"a Project Context heading", "## Project Context\n\n- **Tracker**: none\n", "none", true},
		{"three hashes still open a section", "### Project Map\n\n- **Tracker**: none\n", "none", true},
		{"the heading is matched case-insensitively", "## PROJECT MAP\n\n- **Tracker**: none\n", "none", true},
		{"the field name is matched case-insensitively", "## Project Map\n\n- **tracker**: none\n", "none", true},
		{"a gloss is dropped", "## Project Map\n\n- **Tracker**: none (ROADMAP.md is the source of truth)\n", "none", true},
		{"a glossed path keeps the path", "## Project Map\n\n- **Tracker**: docs/BACKLOG.md (newest first)\n", "docs/BACKLOG.md", true},
		{"a sub-heading does not close the section", "## Project Map\n\n### Detail\n\n- **Tracker**: none\n", "none", true},
		{"a two-hash heading closes it", "## Project Map\n\n## Other\n\n- **Tracker**: none\n", "", false},
		// POSIX [[:space:]] holds the carriage return, so awk's
		// /^##[[:space:]]/ closes the section on a bare `##` before a CRLF
		// break (lib/config.sh:388), and the sed trims the CR off the value
		// (lib/config.sh:398). Trimming space and tab alone leaves `none\r`,
		// which stops matching `none`.
		{"a carriage return is trimmed off the value", "## Project Map\r\n\r\n- **Tracker**: none\r\n", "none", true},
		{"a carriage return closes the section", "## Project Map\n##\r\n- **Tracker**: none\n", "", false},
		{"a field outside any section is not read", "- **Tracker**: none\n", "", false},
		{"no Project Map at all", "# x\n\nnothing here\n", "", false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tracker, found := trackerOf(t, test.document)
			if found != test.found {
				t.Fatalf("found = %v, want %v", found, test.found)
			}
			if tracker != test.want {
				t.Errorf("tracker = %q, want %q", tracker, test.want)
			}
		})
	}
}

// The files are read in the order lib/config.sh:380 names them, and the first
// one holding a Tracker wins.
func TestProjectMapTrackerReadsTheThreeFilesInOrder(t *testing.T) {
	tree := files{"": {
		"CLAUDE.md": "## Project Map\n\n- **Tracker**: from CLAUDE\n",
		"GEMINI.md": "## Project Map\n\n- **Tracker**: from GEMINI\n",
	}}
	tracker, found, err := config.ProjectMapTracker(context.Background(), core.Revision{}, tree.show())
	if err != nil {
		t.Fatalf("ProjectMapTracker: %v", err)
	}
	if !found || tracker != "from CLAUDE" {
		t.Errorf("tracker = %q found = %v, want CLAUDE.md to win", tracker, found)
	}
}

// A Tracker field that strips to nothing does not end the search either. The
// Bash returns only when the stripped value is non-empty, and otherwise moves
// to the next file (lib/config.sh:399-401).
func TestATrackerThatStripsToNothingFallsThroughToTheNextFile(t *testing.T) {
	tree := files{"": {
		"AGENTS.md": "## Project Map\n\n- **Tracker**: (see the board)\n",
		"CLAUDE.md": "## Project Map\n\n- **Tracker**: GitHub Issues\n",
	}}
	tracker, found, err := config.ProjectMapTracker(context.Background(), core.Revision{}, tree.show())
	if err != nil {
		t.Fatalf("ProjectMapTracker: %v", err)
	}
	if !found || tracker != "GitHub Issues" {
		t.Errorf("tracker = %q found = %v, want CLAUDE.md to decide", tracker, found)
	}

	// And with nothing else to read, nothing is declared at all, so the
	// resolution falls to the sniff rather than to an empty declaration.
	only := files{"": {
		"AGENTS.md": "## Project Map\n\n- **Tracker**: (see the board)\n",
		"TODO.md":   "",
	}}
	if got := resolveBacklog(t, only, "", core.Revision{}); got != "repository file TODO.md" {
		t.Errorf("resolved to %q, want the sniff to decide", got)
	}
}

// A read that fails names the path it was reading.
func TestAProjectMapReadFailureNamesThePath(t *testing.T) {
	tree := files{"": {"AGENTS.md": readFails}}
	_, _, err := config.ProjectMapTracker(context.Background(), core.Revision{}, tree.show())
	if err == nil {
		t.Fatal("expected the read failure to be reported")
	}
	if !strings.Contains(err.Error(), "AGENTS.md") {
		t.Errorf("error = %q, want it to name the path", err)
	}
}

// A file with a Project Map but no Tracker does not stop the search.
func TestAFileWithoutATrackerIsSkipped(t *testing.T) {
	tree := files{"": {
		"AGENTS.md": "## Project Map\n\n- **Board**: none\n",
		"CLAUDE.md": "## Project Map\n\n- **Tracker**: GitHub Issues\n",
	}}
	tracker, found, err := config.ProjectMapTracker(context.Background(), core.Revision{}, tree.show())
	if err != nil {
		t.Fatalf("ProjectMapTracker: %v", err)
	}
	if !found || tracker != "GitHub Issues" {
		t.Errorf("tracker = %q found = %v", tracker, found)
	}
}

// Tier 1, the repository's own declaration, and what each value routes to
// (lib/config.sh:444-465).
func TestTheTrackerDecidesTheDestination(t *testing.T) {
	tests := []struct {
		tracker string
		want    string
	}{
		{"GitHub Issues", "github_issues"},
		{"github issues", "github_issues"},
		{"none", "none"},
		{"none (ROADMAP.md is the source of truth)", "none"},
		{"docs/BACKLOG.md", "repository file docs/BACKLOG.md"},
		{"docs/deferred", "repository folder docs/deferred"},
		{"BACKLOG.md", "repository file BACKLOG.md"},
		// A URL is a hosted tracker, not a path, and must be caught before
		// the slash arm turns it into a directory inside the checkout.
		{"https://linear.app/acme/team/ENG", "none"},
		{"http://jira.example/browse/ENG", "none"},
		// Linear, Jira and friends: somewhere real, nothing to write to yet.
		{"Linear", "none"},
		// lib/config.sh:449 is the glob `*github*issue*`, which is ordered.
		// A test for the two substrings in any order says GitHub Issues where
		// the Bash says nothing to write to.
		{"Issues on GitHub", "none"},
		{"github issue tracker", "github_issues"},
		// lib/config.sh:456 matches `*/*|*.md` on the lowercased value and
		// then branches on the original at lib/config.sh:457, which is
		// case-sensitive. `docs/TODO.MD` is a folder there.
		{"docs/TODO.MD", "repository folder docs/TODO.MD"},
		{"NOTES.MD", "repository folder NOTES.MD"},
	}
	for _, test := range tests {
		t.Run(test.tracker, func(t *testing.T) {
			tree := files{"": {"AGENTS.md": "## Project Map\n\n- **Tracker**: " + test.tracker + "\n"}}
			if got := resolveBacklog(t, tree, "", core.Revision{}); got != test.want {
				t.Errorf("resolved to %q, want %q", got, test.want)
			}
		})
	}
}

// A declaration beats a sniffed convention, and a URL that decides nothing
// still falls through to the sniff.
func TestADeclarationBeatsASniff(t *testing.T) {
	tree := files{"": {
		"AGENTS.md": "## Project Map\n\n- **Tracker**: GitHub Issues\n",
		"TODO.md":   "",
	}}
	if got := resolveBacklog(t, tree, "", core.Revision{}); got != "github_issues" {
		t.Errorf("resolved to %q", got)
	}

	tree = files{"": {
		"AGENTS.md": "## Project Map\n\n- **Tracker**: https://linear.app/acme\n",
		"TODO.md":   "",
	}}
	if got := resolveBacklog(t, tree, "", core.Revision{}); got != "repository file TODO.md" {
		t.Errorf("a URL tracker resolved to %q, want the sniff to decide", got)
	}
}
