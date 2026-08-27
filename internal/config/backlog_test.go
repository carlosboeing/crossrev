package config_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/config"
	"github.com/carlosboeing/crossrev/internal/core"
)

func resolveBacklog(t *testing.T, tree files, repoYAML string, base core.Revision) string {
	t.Helper()
	if repoYAML != "" {
		if tree[""] == nil {
			tree[""] = map[string]string{}
		}
		tree[""][".github/crossrev.yml"] = repoYAML
	}
	loaded := mustLoad(t, core.Revision{}, tree)
	resolved, err := loaded.ResolveBacklog(context.Background(), base, loaded.Get(".backlog.destination"))
	if err != nil {
		t.Fatalf("ResolveBacklog: %v", err)
	}
	return resolved.String()
}

// Nothing declared and nothing installed resolves to none. `auto` falls to none
// rather than creating a location nobody asked for (lib/config.sh:465).
func TestAutoFallsToNone(t *testing.T) {
	if got := resolveBacklog(t, files{"": {}}, "", core.Revision{}); got != "none" {
		t.Errorf("resolved to %q, want %q", got, "none")
	}
}

// Tier 2, the sniff, in the order lib/config.sh:446-458 probes it.
func TestTheSniffKeepsItsOrder(t *testing.T) {
	tests := []struct {
		name    string
		present []string
		want    string
	}{
		{"a Backlog config at the root", []string{"backlog.config.yml"}, "repository folder backlog/tasks"},
		{"a Backlog config in a folder", []string{"backlog/config.yml"}, "repository folder backlog/tasks"},
		{"a Backlog config in a dot folder", []string{".backlog/config.yml"}, "repository folder backlog/tasks"},
		{"a root BACKLOG.md", []string{"BACKLOG.md"}, "repository file BACKLOG.md"},
		{"a root TODO.md", []string{"TODO.md"}, "repository file TODO.md"},
		{"BACKLOG.md before TODO.md", []string{"TODO.md", "BACKLOG.md"}, "repository file BACKLOG.md"},
		{"a folder convention before a file one", []string{"BACKLOG.md", "backlog.config.yml"}, "repository folder backlog/tasks"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tree := files{"": {}}
			for _, path := range test.present {
				tree[""][path] = ""
			}
			if got := resolveBacklog(t, tree, "", core.Revision{}); got != test.want {
				t.Errorf("resolved to %q, want %q", got, test.want)
			}
		})
	}
}

// A configured path wins outright (lib/config.sh:392-395).
func TestAConfiguredPathWins(t *testing.T) {
	tree := files{"": {"BACKLOG.md": ""}}
	repo := "version: 1\nbacklog:\n  destination: repository\n  repository:\n    path: docs/deferred\n"
	if got := resolveBacklog(t, tree, repo, core.Revision{}); got != "repository folder docs/deferred" {
		t.Errorf("resolved to %q", got)
	}
}

// A repository key is explicit only when it appears in the repository config,
// not when it arrives through the merged defaults (lib/config.sh:396). With
// neither key stated the sniff decides both.
func TestAnExplicitRepositoryDestinationSniffsBothLayouts(t *testing.T) {
	tree := files{"": {"BACKLOG.md": ""}}
	repo := "version: 1\nbacklog:\n  destination: repository\n"
	if got := resolveBacklog(t, tree, repo, core.Revision{}); got != "repository file BACKLOG.md" {
		t.Errorf("resolved to %q, want the sniffed file convention", got)
	}
}

// A stated layout constrains the candidates rather than reinterpreting a path
// that belongs to another one, and tier 3 then fires because a repository
// destination was explicitly asked for (lib/config.sh:459-464).
func TestAStatedLayoutConstrainsTheSniff(t *testing.T) {
	folder := files{"": {"BACKLOG.md": ""}}
	repoFolder := "version: 1\nbacklog:\n  destination: repository\n  repository:\n    layout: folder\n"
	if got := resolveBacklog(t, folder, repoFolder, core.Revision{}); got != "repository folder .crossrev/backlog" {
		t.Errorf("an explicit folder layout resolved to %q", got)
	}

	file := files{"": {"backlog/config.yml": ""}}
	repoFile := "version: 1\nbacklog:\n  destination: repository\n  repository:\n    layout: file\n"
	if got := resolveBacklog(t, file, repoFile, core.Revision{}); got != "repository file .crossrev/backlog.md" {
		t.Errorf("an explicit file layout resolved to %q", got)
	}
}

// github_issues and none are answered without probing anything.
func TestTheTwoLiteralDestinations(t *testing.T) {
	tree := files{"": {"BACKLOG.md": ""}}
	if got := resolveBacklog(t, tree, "version: 1\nbacklog:\n  destination: github_issues\n", core.Revision{}); got != "github_issues" {
		t.Errorf("resolved to %q", got)
	}
	tree = files{"": {"BACKLOG.md": ""}}
	if got := resolveBacklog(t, tree, "version: 1\nbacklog:\n  destination: none\n", core.Revision{}); got != "none" {
		t.Errorf("resolved to %q", got)
	}
}

// The sniff reads the base revision when there is one, like every other policy
// read (lib/config.sh:440-444).
func TestTheSniffReadsTheBaseRevision(t *testing.T) {
	base := revision(t, baseSHA)
	tree := files{
		"":      {"TODO.md": ""},
		baseSHA: {"BACKLOG.md": ""},
	}
	if got := resolveBacklog(t, tree, "", base); got != "repository file BACKLOG.md" {
		t.Errorf("resolved to %q, want the base revision's convention", got)
	}
}

// The two guards stay reachable for a caller passing a literal the resolver
// does not know, even though a configured value is refused at load
// (lib/config.sh:372-376).
func TestTheResolverStillGuardsALiteralItDoesNotKnow(t *testing.T) {
	loaded := mustLoad(t, core.Revision{}, files{"": {}})
	if _, err := loaded.ResolveBacklog(context.Background(), core.Revision{}, "elsewhere"); err == nil {
		t.Fatal("expected a refusal for an unknown destination")
	} else if refusal, ok := err.(*config.Refusal); !ok {
		t.Fatalf("expected a *config.Refusal, got %T", err)
	} else if refusal.Message != "backlog.destination is 'elsewhere', which CrossRev does not recognise" {
		t.Errorf("message = %q", refusal.Message)
	}
}

// --- the backlog path guard -------------------------------------------------

func guardRefusal(t *testing.T, root, path string) *config.Refusal {
	t.Helper()
	err := config.AssertPathInsideRepo(root, path)
	if err == nil {
		t.Fatalf("AssertPathInsideRepo(%q, %q) allowed the path", root, path)
	}
	refusal, ok := err.(*config.Refusal)
	if !ok {
		t.Fatalf("expected a *config.Refusal, got %T: %v", err, err)
	}
	return refusal
}

// The cases lib/config.sh:480-505 already decides, which the divergence must
// leave exactly as they are.
func TestThePathGuardKeepsItsLexicalDecisions(t *testing.T) {
	root := t.TempDir()

	for _, allowed := range []string{"backlog/tasks", "sub/../sibling", ".", "", "docs/BACKLOG.md"} {
		if err := config.AssertPathInsideRepo(root, allowed); err != nil {
			t.Errorf("AssertPathInsideRepo(%q) refused: %v", allowed, err)
		}
	}

	if got := guardRefusal(t, root, "/etc").Message; got != "the backlog path '/etc' is absolute" {
		t.Errorf("an absolute path: %q", got)
	}
	// Traversal that lands somewhere real above the checkout resolves, so it
	// is refused by the arm that can name where it landed. Only a path that
	// pops past `/` leaves the resolution empty and reaches the other arm
	// (lib/config.sh:498-504).
	if got := guardRefusal(t, root, "../../etc").Message; got != "the backlog path '../../etc' resolves outside the repository" {
		t.Errorf("traversal above the checkout: %q", got)
	}
	if got := guardRefusal(t, root, "sub/../../outside").Message; got != "the backlog path 'sub/../../outside' resolves outside the repository" {
		t.Errorf("traversal that re-enters and then leaves: %q", got)
	}
	climbs := strings.Repeat("../", 40) + "etc"
	if got := guardRefusal(t, root, climbs).Message; got != "the backlog path '"+climbs+"' escapes the repository" {
		t.Errorf("traversal past the filesystem root: %q", got)
	}
	if got := guardRefusal(t, "", "backlog/tasks").Message; got != "not inside a git repository" {
		t.Errorf("no repository root: %q", got)
	}
}

// Issue 128, and the first of the two deliberate divergences approved for this
// port: a symlink inside the checkout pointing outside it used to pass, because
// the Bash guard resolves lexically and asks the filesystem nothing. The value
// ends in a file write, so the write would land outside the repository.
func TestThePathGuardRefusesASymlinkOutOfTheRepository(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "notes")); err != nil {
		t.Fatalf("create the symlink: %v", err)
	}

	refusal := guardRefusal(t, root, "notes")
	if refusal.Message != "the backlog path 'notes' resolves outside the repository" {
		t.Errorf("message = %q", refusal.Message)
	}
	if !strings.Contains(refusal.Hint, "must stay inside the checkout") {
		t.Errorf("hint = %q", refusal.Hint)
	}

	// The same link one level down, where the escape is the ancestor rather
	// than the path itself and nothing below it exists yet.
	if got := guardRefusal(t, root, "notes/deferred").Message; got != "the backlog path 'notes/deferred' resolves outside the repository" {
		t.Errorf("through a symlinked ancestor: %q", got)
	}
}

// A symlink that stays inside the checkout is still allowed. The guard bounds
// where a write lands, not how the path is spelled.
func TestThePathGuardAllowsASymlinkInsideTheRepository(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "real"), 0o755); err != nil {
		t.Fatalf("create the target: %v", err)
	}
	if err := os.Symlink(filepath.Join(root, "real"), filepath.Join(root, "linked")); err != nil {
		t.Fatalf("create the symlink: %v", err)
	}
	for _, allowed := range []string{"linked", "linked/tasks"} {
		if err := config.AssertPathInsideRepo(root, allowed); err != nil {
			t.Errorf("AssertPathInsideRepo(%q) refused: %v", allowed, err)
		}
	}
}

// The common case is a path nothing has created yet, and it must not be refused
// merely for not existing.
func TestThePathGuardAllowsAPathThatDoesNotExistYet(t *testing.T) {
	root := t.TempDir()
	if err := config.AssertPathInsideRepo(root, "deep/not/created/yet"); err != nil {
		t.Errorf("AssertPathInsideRepo refused a path that does not exist: %v", err)
	}
}
