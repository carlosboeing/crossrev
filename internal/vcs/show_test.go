package vcs_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/vcs"
)

// Policy is read from the pull request's base revision, never its head
// (ADR 0003, lib/config.sh:87-93). The property is that changing the file in
// the working tree changes nothing about what a base-revision read returns.
func TestShowReadsTheRevisionAndNotTheWorkingTree(t *testing.T) {
	ctx := context.Background()
	git := testGit(t)
	repo := initRepo(t, git, filepath.Join(realTempDir(t), "clone"))

	base := commitFile(t, repo, ".github/crossrev.yml", "version: 1\nmode: local\n", "base policy")
	// The pull request raises the pass budget and repoints an endpoint. Read
	// from the head this would take effect on the review of itself.
	write(t, repo.Dir(), ".github/crossrev.yml", "version: 1\nmax_passes_per_cycle: 99\n")

	content, status, err := repo.Show(ctx, base, ".github/crossrev.yml")
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if status != vcs.IsFile {
		t.Fatalf("status = %v, want IsFile", status)
	}
	if got, want := string(content), "version: 1\nmode: local\n"; got != want {
		t.Errorf("content = %q, want %q", got, want)
	}

	// And the zero revision is the working tree, which is what init and doctor
	// read (lib/config.sh:134-136).
	content, status, err = repo.Show(ctx, core.Revision{}, filepath.Join(repo.Dir(), ".github/crossrev.yml"))
	if err != nil {
		t.Fatalf("Show at the working tree: %v", err)
	}
	if status != vcs.IsFile {
		t.Fatalf("status = %v, want IsFile", status)
	}
	if got, want := string(content), "version: 1\nmax_passes_per_cycle: 99\n"; got != want {
		t.Errorf("content = %q, want %q", got, want)
	}
}

func TestShowStatuses(t *testing.T) {
	ctx := context.Background()
	git := testGit(t)
	root := realTempDir(t)
	repo := initRepo(t, git, filepath.Join(root, "clone"))

	write(t, repo.Dir(), "empty.yml", "")
	write(t, repo.Dir(), "dir/inside.yml", "version: 1\n")
	write(t, repo.Dir(), "binary.bin", "\x00\x01\x02no trailing newline")
	base := commitFile(t, repo, "app.ts", "export const ok = 1\n", "init")

	tests := []struct {
		name    string
		path    string
		status  vcs.FileStatus
		content string
	}{
		// A file that exists at the base revision and holds nothing states no
		// policy, which is the same answer as no file — but it is a different
		// read, and the status has to keep them apart (lib/config.sh:105-107).
		{name: "an empty file is found", path: "empty.yml", status: vcs.IsFile, content: ""},
		// `[[ -f ]]` and `git show` disagree about a directory, and the shell
		// keeps the disagreement: at a revision the tree is found and then
		// refused for its shape (lib/config.sh:93-95).
		{name: "a tree is found rather than skipped", path: "dir", status: vcs.IsOther},
		{name: "a path that is not there", path: "nowhere.yml", status: vcs.NotFound},
		{name: "bytes are not touched", path: "binary.bin", status: vcs.IsFile, content: "\x00\x01\x02no trailing newline"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			content, status, err := repo.Show(ctx, base, tt.path)
			if err != nil {
				t.Fatalf("Show: %v", err)
			}
			if status != tt.status {
				t.Fatalf("status = %v, want %v", status, tt.status)
			}
			if string(content) != tt.content {
				t.Errorf("content = %q, want %q", string(content), tt.content)
			}
		})
	}
}

// A working-tree read is a plain filesystem read, and the path it is given is
// not always repository-relative: the operator configuration layer is an
// absolute path outside the checkout, and resolving it against the repository
// would drop the layer without a word (lib/config.sh:166).
func TestShowWorkingTreeReadsOutsideTheCheckout(t *testing.T) {
	ctx := context.Background()
	git := testGit(t)
	root := realTempDir(t)
	repo := initRepo(t, git, filepath.Join(root, "clone"))

	operator := write(t, root, "xdg/crossrev/config.yml", "endpoints:\n  kimi:\n    base_url: http://localhost\n")

	content, status, err := repo.Show(ctx, core.Revision{}, operator)
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if status != vcs.IsFile {
		t.Fatalf("status = %v, want IsFile", status)
	}
	if len(content) == 0 {
		t.Error("the operator layer read as empty")
	}

	// A directory in the working tree is absent to `[[ -f ]]`, so the next
	// configuration path is tried (lib/config.sh:37).
	if _, status, err = repo.Show(ctx, core.Revision{}, filepath.Join(root, "xdg")); err != nil {
		t.Fatalf("Show over a directory: %v", err)
	} else if status != vcs.IsOther {
		t.Errorf("status = %v, want IsOther", status)
	}

	if _, status, err = repo.Show(ctx, core.Revision{}, filepath.Join(root, "no-such-file")); err != nil {
		t.Fatalf("Show over a missing path: %v", err)
	} else if status != vcs.NotFound {
		t.Errorf("status = %v, want NotFound", status)
	}
}

// A revision the object database does not hold is NotFound rather than an
// error, which is `git show … || return 1` at lib/config.sh:95.
func TestShowAtAnUnknownRevision(t *testing.T) {
	ctx := context.Background()
	git := testGit(t)
	repo := initRepo(t, git, filepath.Join(realTempDir(t), "clone"))
	commitFile(t, repo, "app.ts", "x\n", "init")

	unknown, err := core.NewRevision("0123456789abcdef0123456789abcdef01234567")
	if err != nil {
		t.Fatalf("NewRevision: %v", err)
	}
	_, status, err := repo.Show(ctx, unknown, "app.ts")
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if status != vcs.NotFound {
		t.Errorf("status = %v, want NotFound", status)
	}
}

// The three constants must keep the order config.FileStatus declares, because
// the tier-3 wiring converts one to the other by value and nothing else checks.
func TestFileStatusOrder(t *testing.T) {
	if vcs.NotFound != 0 || vcs.IsFile != 1 || vcs.IsOther != 2 {
		t.Errorf("FileStatus = %d/%d/%d, want 0/1/2", vcs.NotFound, vcs.IsFile, vcs.IsOther)
	}
}
