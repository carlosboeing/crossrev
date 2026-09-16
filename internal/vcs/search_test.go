package vcs_test

import (
	"context"
	"fmt"
	"sort"
	"testing"

	"github.com/carlosboeing/crossrev/internal/core"
)

// TestExactSearchReportsTooCommonAtTwoHundred commits 205 files holding one
// fixed-string term and requires the 200-hit cap: the capped call reports
// too_common with exactly 200 hits, while a limit above the total does not.
func TestExactSearchReportsTooCommonAtTwoHundred(t *testing.T) {
	git := testGit(t)
	dir := realTempDir(t)
	repo := initRepo(t, git, dir)
	ctx := context.Background()

	const term = "sharedhelper_thing"
	const files = 205
	for i := 0; i < files; i++ {
		write(t, dir, fmt.Sprintf("pkg/file%03d.go", i), fmt.Sprintf("package pkg\n\n// %s filler %d\n", term, i))
	}
	write(t, dir, "pkg/rare.go", "package pkg\n\nfunc LoneUnrelatedSymbol() {}\n")
	mustGit(t, repo, "add", "-A")
	mustGit(t, repo, append(testIdentity, "commit", "-qm", "head")...)
	headOut := mustGit(t, repo, "rev-parse", "HEAD")
	head, err := core.NewRevision(headOut.Text())
	if err != nil {
		t.Fatalf("head revision: %v", err)
	}

	hits, tooCommon, err := repo.ExactSearch(ctx, head, term, 200)
	if err != nil {
		t.Fatalf("ExactSearch: %v", err)
	}
	if !tooCommon {
		t.Error("ExactSearch over 205 hits with limit 200 reports too_common false, want true")
	}
	if len(hits) != 200 {
		t.Errorf("ExactSearch returned %d hits, want exactly 200", len(hits))
	}
	if !sort.SliceIsSorted(hits, func(i, j int) bool { return hits[i].Path < hits[j].Path }) {
		t.Error("ExactSearch hits are not sorted by path")
	}

	roomy, tooCommon, err := repo.ExactSearch(ctx, head, term, 206)
	if err != nil {
		t.Fatalf("ExactSearch: %v", err)
	}
	if tooCommon {
		t.Error("ExactSearch over 205 hits with limit 206 reports too_common true, want false")
	}
	if len(roomy) != files {
		t.Errorf("ExactSearch with room returned %d hits, want %d", len(roomy), files)
	}

	rare, tooCommon, err := repo.ExactSearch(ctx, head, "LoneUnrelatedSymbol", 200)
	if err != nil {
		t.Fatalf("ExactSearch: %v", err)
	}
	if tooCommon {
		t.Error("ExactSearch over one hit reports too_common true, want false")
	}
	if len(rare) != 1 || rare[0].Path != "pkg/rare.go" {
		t.Errorf("ExactSearch rare term = %v, want [pkg/rare.go]", rare)
	}
}

func TestExactSearchIsFixedString(t *testing.T) {
	git := testGit(t)
	dir := realTempDir(t)
	repo := initRepo(t, git, dir)
	ctx := context.Background()

	write(t, dir, "literal.go", "package p\n\nvar s = \"a.b\"\n")
	write(t, dir, "lookalike.go", "package p\n\nvar s = \"aXb\"\n")
	write(t, dir, "flag.go", "package p\n\n// -flag literal\n")
	mustGit(t, repo, "add", "-A")
	mustGit(t, repo, append(testIdentity, "commit", "-qm", "head")...)
	headOut := mustGit(t, repo, "rev-parse", "HEAD")
	head, err := core.NewRevision(headOut.Text())
	if err != nil {
		t.Fatalf("head revision: %v", err)
	}

	hits, tooCommon, err := repo.ExactSearch(ctx, head, "a.b", 200)
	if err != nil {
		t.Fatalf("ExactSearch: %v", err)
	}
	if tooCommon {
		t.Error("ExactSearch reports too_common over two hits, want false")
	}
	if len(hits) != 1 || hits[0].Path != "literal.go" {
		t.Errorf("ExactSearch fixed-string a.b = %v, want [literal.go]", hits)
	}

	flag, _, err := repo.ExactSearch(ctx, head, "-flag", 200)
	if err != nil {
		t.Fatalf("ExactSearch: %v", err)
	}
	if len(flag) != 1 || flag[0].Path != "flag.go" {
		t.Errorf("ExactSearch leading-dash term = %v, want [flag.go]", flag)
	}
}

func TestExactSearchNoMatchesIsEmpty(t *testing.T) {
	git := testGit(t)
	dir := realTempDir(t)
	repo := initRepo(t, git, dir)

	write(t, dir, "keep.go", "package keep\n")
	mustGit(t, repo, "add", "-A")
	mustGit(t, repo, append(testIdentity, "commit", "-qm", "head")...)
	headOut := mustGit(t, repo, "rev-parse", "HEAD")
	head, err := core.NewRevision(headOut.Text())
	if err != nil {
		t.Fatalf("head revision: %v", err)
	}

	hits, tooCommon, err := repo.ExactSearch(context.Background(), head, "nothing_names_this", 200)
	if err != nil {
		t.Fatalf("ExactSearch: %v", err)
	}
	if tooCommon {
		t.Error("ExactSearch with no matches reports too_common true, want false")
	}
	if len(hits) != 0 {
		t.Errorf("ExactSearch with no matches = %v, want empty", hits)
	}
}

func TestExactSearchRejectsEmptyTermAndLimit(t *testing.T) {
	git := testGit(t)
	dir := realTempDir(t)
	repo := initRepo(t, git, dir)

	write(t, dir, "keep.go", "package keep\n")
	mustGit(t, repo, "add", "-A")
	mustGit(t, repo, append(testIdentity, "commit", "-qm", "head")...)
	headOut := mustGit(t, repo, "rev-parse", "HEAD")
	head, err := core.NewRevision(headOut.Text())
	if err != nil {
		t.Fatalf("head revision: %v", err)
	}
	ctx := context.Background()

	if _, _, err := repo.ExactSearch(ctx, head, "", 200); err == nil {
		t.Error("ExactSearch with an empty term succeeded, want an error")
	}
	if _, _, err := repo.ExactSearch(ctx, head, "keep", 0); err == nil {
		t.Error("ExactSearch with limit 0 succeeded, want an error")
	}
	if _, _, err := repo.ExactSearch(ctx, head, "keep", -1); err == nil {
		t.Error("ExactSearch with a negative limit succeeded, want an error")
	}
}

func TestExactSearchFindsAPathWithASpace(t *testing.T) {
	git := testGit(t)
	dir := realTempDir(t)
	repo := initRepo(t, git, dir)

	write(t, dir, "dir/space name.go", "package spaced\n\nfunc SpacedHelper() {}\n")
	mustGit(t, repo, "add", "-A")
	mustGit(t, repo, append(testIdentity, "commit", "-qm", "head")...)
	headOut := mustGit(t, repo, "rev-parse", "HEAD")
	head, err := core.NewRevision(headOut.Text())
	if err != nil {
		t.Fatalf("head revision: %v", err)
	}

	hits, _, err := repo.ExactSearch(context.Background(), head, "SpacedHelper", 200)
	if err != nil {
		t.Fatalf("ExactSearch: %v", err)
	}
	if len(hits) != 1 || hits[0].Path != "dir/space name.go" {
		t.Errorf("ExactSearch spaced path = %v, want [dir/space name.go]", hits)
	}
}
