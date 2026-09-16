package vcs_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/carlosboeing/crossrev/internal/core"
)

// TestRangeDiffReportsTheResolverOnlyDelta pins the B-to-C repair delta: two
// commits where the second repairs one file and changes a previously clean
// file, and the ranged diff names both with the repair's own bytes.
func TestRangeDiffReportsTheResolverOnlyDelta(t *testing.T) {
	git := testGit(t)
	dir := realTempDir(t)
	repo := initRepo(t, git, dir)
	ctx := context.Background()

	write(t, dir, "repair.go", "package repair\n\nfunc Broken() int { return 0 }\n")
	write(t, dir, "clean.go", "package clean\n")
	mustGit(t, repo, "add", "-A")
	mustGit(t, repo, append(testIdentity, "commit", "-qm", "reviewed")...)
	reviewedOut := mustGit(t, repo, "rev-parse", "HEAD")
	reviewed, err := core.NewRevision(reviewedOut.Text())
	if err != nil {
		t.Fatalf("reviewed revision: %v", err)
	}

	write(t, dir, "repair.go", "package repair\n\nfunc Fixed() int { return 1 }\n")
	write(t, dir, "clean.go", "package clean\n\nfunc Touched() {}\n")
	mustGit(t, repo, "add", "-A")
	mustGit(t, repo, append(testIdentity, "commit", "-qm", "repaired")...)
	repairedOut := mustGit(t, repo, "rev-parse", "HEAD")
	repaired, err := core.NewRevision(repairedOut.Text())
	if err != nil {
		t.Fatalf("repaired revision: %v", err)
	}

	delta, err := repo.RangeDiff(ctx, reviewed, repaired)
	if err != nil {
		t.Fatalf("RangeDiff: %v", err)
	}
	if len(delta) == 0 {
		t.Fatal("ranged diff is empty, want the repair bytes")
	}
	text := string(delta)
	for _, want := range []string{"repair.go", "clean.go", "Fixed", "Touched"} {
		if !contains(text, want) {
			t.Errorf("ranged diff lacks %q:\n%s", want, text)
		}
	}
	for _, want := range []string{"-func Broken", "+func Fixed"} {
		if !contains(text, want) {
			t.Errorf("ranged diff lacks the unified hunk line %q:\n%s", want, text)
		}
	}
}

// TestRangeDiffReadsNoPolicy pins that the ranged diff is pure history: no
// configuration is read, so a repair cannot rewrite the rules that judge it.
func TestRangeDiffReadsNoPolicy(t *testing.T) {
	git := testGit(t)
	dir := realTempDir(t)
	repo := initRepo(t, git, dir)
	ctx := context.Background()

	write(t, dir, "app.go", "package app\n")
	write(t, dir, ".github/crossrev.yml", "version: 2\nmode: local\n")
	mustGit(t, repo, "add", "-A")
	mustGit(t, repo, append(testIdentity, "commit", "-qm", "base")...)
	baseOut := mustGit(t, repo, "rev-parse", "HEAD")
	base, err := core.NewRevision(baseOut.Text())
	if err != nil {
		t.Fatalf("base revision: %v", err)
	}
	if err := os.Remove(filepath.Join(dir, ".github/crossrev.yml")); err != nil {
		t.Fatalf("remove config: %v", err)
	}
	write(t, dir, "app.go", "package app\n\nfunc Extra() {}\n")
	mustGit(t, repo, "add", "-A")
	mustGit(t, repo, append(testIdentity, "commit", "-qm", "head")...)
	headOut := mustGit(t, repo, "rev-parse", "HEAD")
	head, err := core.NewRevision(headOut.Text())
	if err != nil {
		t.Fatalf("head revision: %v", err)
	}

	delta, err := repo.RangeDiff(ctx, base, head)
	if err != nil {
		t.Fatalf("RangeDiff: %v", err)
	}
	if !contains(string(delta), "Extra") {
		t.Errorf("ranged diff lacks the repair:\n%s", delta)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
