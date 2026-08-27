package config_test

import (
	"context"
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

// The files are read in the order lib/config.sh:344 names them, and the first
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
// (lib/config.sh:408-429).
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
