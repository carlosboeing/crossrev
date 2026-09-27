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

// TestChangedLinesDetectsRenamesWhenConfigDisablesThem requires the -U0 read
// to force rename detection the way the enumeration does: under
// diff.renames=false the enumeration still reports one rename, so the term
// source must show the rename sections rather than separate add/delete ones
// that would lose the identifiers removed from the old path.
func TestChangedLinesDetectsRenamesWhenConfigDisablesThem(t *testing.T) {
	git := testGit(t)
	dir := realTempDir(t)
	repo := initRepo(t, git, dir)
	ctx := context.Background()

	write(t, dir, "old.go", "package old\n\nfunc One() {}\nfunc Two() {}\nfunc Three() {}\n")
	mustGit(t, repo, "add", "-A")
	mustGit(t, repo, append(testIdentity, "commit", "-qm", "base")...)
	mustGit(t, repo, "config", "diff.renames", "false")
	baseOut := mustGit(t, repo, "rev-parse", "HEAD")
	base, err := core.NewRevision(baseOut.Text())
	if err != nil {
		t.Fatalf("base revision: %v", err)
	}

	mustGit(t, repo, "mv", "old.go", "new.go")
	write(t, dir, "new.go", "package old\n\nfunc One() {}\nfunc Two() {}\nfunc Three() {}\nfunc Four() {}\n")
	mustGit(t, repo, "add", "-A")
	mustGit(t, repo, append(testIdentity, "commit", "-qm", "head")...)
	headOut := mustGit(t, repo, "rev-parse", "HEAD")
	head, err := core.NewRevision(headOut.Text())
	if err != nil {
		t.Fatalf("head revision: %v", err)
	}

	changes, err := repo.ChangedFiles(ctx, base, head)
	if err != nil {
		t.Fatalf("ChangedFiles: %v", err)
	}
	if len(changes) != 1 || changes[0].OldPath != "old.go" || changes[0].Path != "new.go" {
		t.Fatalf("ChangedFiles under diff.renames=false = %+v, want one old.go to new.go rename", changes)
	}

	diff, err := repo.ChangedLines(ctx, base, head)
	if err != nil {
		t.Fatalf("ChangedLines: %v", err)
	}
	text := string(diff)
	for _, want := range []string{"rename from old.go", "rename to new.go"} {
		if !strings.Contains(text, want) {
			t.Errorf("changed lines under diff.renames=false do not show %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "diff --git a/old.go b/old.go") {
		t.Errorf("changed lines split the rename into separate add/delete sections:\n%s", text)
	}
}
