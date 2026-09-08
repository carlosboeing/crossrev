package vcs_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/carlosboeing/crossrev/internal/core"
)

// TestChangedFilesEnumeratesEveryGitChangeKind builds one history covering
// every change kind the discovery layer must account for, including a path
// with a space that would split under whitespace parsing.
func TestChangedFilesEnumeratesEveryGitChangeKind(t *testing.T) {
	git := testGit(t)
	dir := realTempDir(t)
	repo := initRepo(t, git, dir)
	ctx := context.Background()

	write(t, dir, "keep.go", "package keep\n")
	write(t, dir, "modify.go", "package old\n")
	write(t, dir, "delete.go", "package gone\n")
	write(t, dir, "old.go", "package renamed\n")
	write(t, dir, "modechange", "content\n")
	write(t, dir, filepath.Join("dir", "space name.go"), "package spaced\n")
	mustGit(t, repo, "add", "-A")
	mustGit(t, repo, append(testIdentity, "commit", "-qm", "base")...)
	baseOut := mustGit(t, repo, "rev-parse", "HEAD")
	baseSHA := baseOut.Text()
	base, err := core.NewRevision(baseSHA)
	if err != nil {
		t.Fatalf("base revision: %v", err)
	}

	write(t, dir, "modify.go", "package old\n\nfunc B() {}\n")
	if err := os.Remove(filepath.Join(dir, "delete.go")); err != nil {
		t.Fatalf("remove delete.go: %v", err)
	}
	if err := os.Rename(filepath.Join(dir, "old.go"), filepath.Join(dir, "new.go")); err != nil {
		t.Fatalf("rename old.go: %v", err)
	}
	if err := os.Remove(filepath.Join(dir, "modechange")); err != nil {
		t.Fatalf("remove modechange: %v", err)
	}
	if err := os.Symlink("target", filepath.Join(dir, "modechange")); err != nil {
		t.Fatalf("symlink modechange: %v", err)
	}
	write(t, dir, "added.go", "package added\n")
	if err := os.WriteFile(filepath.Join(dir, "logo.bin"), []byte("GIF89a\x00binary"), 0o644); err != nil {
		t.Fatalf("write logo.bin: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "empty.go"), nil, 0o644); err != nil {
		t.Fatalf("write empty.go: %v", err)
	}
	write(t, dir, filepath.Join("dir", "space name.go"), "package spaced\n\nfunc C() {}\n")
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
	got := map[string]core.FileChange{}
	for _, c := range changes {
		got[c.Path] = c
	}
	want := map[string]core.FileChange{
		"modify.go":            {Path: "modify.go", Kind: core.ChangeModified},
		"delete.go":            {Path: "delete.go", OldPath: "delete.go", Kind: core.ChangeDeleted},
		"new.go":               {Path: "new.go", OldPath: "old.go", Kind: core.ChangeRenamed},
		"modechange":           {Path: "modechange", Kind: core.ChangeTypeChanged},
		"added.go":             {Path: "added.go", Kind: core.ChangeAdded},
		"logo.bin":             {Path: "logo.bin", Kind: core.ChangeAdded},
		"empty.go":             {Path: "empty.go", Kind: core.ChangeAdded},
		"dir/space name.go": {Path: "dir/space name.go", Kind: core.ChangeModified},
	}
	if len(got) != len(want) {
		t.Fatalf("ChangedFiles returned %d changes %v, want %d %v", len(got), got, len(want), want)
	}
	for path, w := range want {
		g, ok := got[path]
		if !ok {
			t.Errorf("missing change for %q", path)
			continue
		}
		if g.Kind != w.Kind || g.OldPath != w.OldPath || g.Path != w.Path {
			t.Errorf("change for %q = %+v, want %+v", path, g, w)
		}
	}
}

func TestChangedFilesOnAnUnchangedRevisionIsEmpty(t *testing.T) {
	git := testGit(t)
	dir := realTempDir(t)
	repo := initRepo(t, git, dir)

	write(t, dir, "keep.go", "package keep\n")
	mustGit(t, repo, "add", "-A")
	mustGit(t, repo, append(testIdentity, "commit", "-qm", "base")...)
	out := mustGit(t, repo, "rev-parse", "HEAD")
	rev, err := core.NewRevision(out.Text())
	if err != nil {
		t.Fatalf("revision: %v", err)
	}
	changes, err := repo.ChangedFiles(context.Background(), rev, rev)
	if err != nil {
		t.Fatalf("ChangedFiles: %v", err)
	}
	if len(changes) != 0 {
		t.Fatalf("ChangedFiles = %v, want empty", changes)
	}
}
