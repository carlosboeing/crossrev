package vcs_test

import (
	"context"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/core"
)

// TestChangedLinesAnswersMinusUZeroBetweenBaseAndHead requires the advisory
// term source: added and removed lines with no context, across an addition,
// a modification, a deletion and a rename.
func TestChangedLinesAnswersMinusUZeroBetweenBaseAndHead(t *testing.T) {
	git := testGit(t)
	dir := realTempDir(t)
	repo := initRepo(t, git, dir)
	ctx := context.Background()

	write(t, dir, "keep.go", "package keep\n\nfunc Keep() {}\n")
	write(t, dir, "mod.go", "package mod\n\nfunc Before() {}\n")
	write(t, dir, "gone.go", "package gone\n")
	write(t, dir, "old.go", "package old\n")
	mustGit(t, repo, "add", "-A")
	mustGit(t, repo, append(testIdentity, "commit", "-qm", "base")...)
	baseOut := mustGit(t, repo, "rev-parse", "HEAD")
	base, err := core.NewRevision(baseOut.Text())
	if err != nil {
		t.Fatalf("base revision: %v", err)
	}

	write(t, dir, "mod.go", "package mod\n\nfunc After() {}\n")
	write(t, dir, "new.go", "package fresh\n")
	mustGit(t, repo, "rm", "-q", "gone.go")
	mustGit(t, repo, "mv", "old.go", "renamed.go")
	mustGit(t, repo, "add", "-A")
	mustGit(t, repo, append(testIdentity, "commit", "-qm", "head")...)
	headOut := mustGit(t, repo, "rev-parse", "HEAD")
	head, err := core.NewRevision(headOut.Text())
	if err != nil {
		t.Fatalf("head revision: %v", err)
	}

	diff, err := repo.ChangedLines(ctx, base, head)
	if err != nil {
		t.Fatalf("ChangedLines: %v", err)
	}
	text := string(diff)
	for _, want := range []string{
		"-func Before() {}",
		"+func After() {}",
		"+package fresh",
		"-package gone",
		"rename from old.go",
		"rename to renamed.go",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("changed lines do not show %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "func Keep() {}") {
		t.Errorf("changed lines name the untouched file:\n%s", text)
	}
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, " ") {
			t.Errorf("changed lines carry a context line %q, want -U0", line)
		}
	}
}
