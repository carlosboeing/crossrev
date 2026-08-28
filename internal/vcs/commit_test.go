package vcs_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/vcs"
)

func TestStageAllAndHasStagedChanges(t *testing.T) {
	ctx := context.Background()
	git := testGit(t)
	repo := initRepo(t, git, filepath.Join(realTempDir(t), "clone"))
	commitFile(t, repo, "app.ts", "export const ok = 1\n", "init")

	staged, err := repo.HasStagedChanges(ctx)
	if err != nil {
		t.Fatalf("HasStagedChanges: %v", err)
	}
	if staged {
		t.Error("a clean checkout reported staged changes")
	}

	write(t, repo.Dir(), "app.ts", "export const ok = 2\n")
	repo.StageAll(ctx)

	staged, err = repo.HasStagedChanges(ctx)
	if err != nil {
		t.Fatalf("HasStagedChanges: %v", err)
	}
	if !staged {
		t.Error("an edited and staged file reported no staged changes")
	}
}

// The identity is CrossRev's own unless the operator named one, and it is
// passed with -c rather than written into the repository's config
// (lib/github.sh:439-442).
func TestCommitUsesTheDefaultIdentity(t *testing.T) {
	ctx := context.Background()
	git := testGit(t)
	repo := initRepo(t, git, filepath.Join(realTempDir(t), "clone"))
	commitFile(t, repo, "app.ts", "export const ok = 1\n", "init")

	write(t, repo.Dir(), "app.ts", "export const ok = 2\n")
	repo.StageAll(ctx)
	if err := repo.Commit(ctx, vcs.CommitOptions{Message: "fix: resolve finding 1"}); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	author := mustGit(t, repo, "log", "-1", "--format=%an <%ae>").Text()
	want := vcs.DefaultCommitName + " <" + vcs.DefaultCommitEmail + ">"
	if author != want {
		t.Errorf("author = %q, want %q", author, want)
	}
	if subject := mustGit(t, repo, "log", "-1", "--format=%s").Text(); subject != "fix: resolve finding 1" {
		t.Errorf("subject = %q", subject)
	}
	config, err := repo.ConfigGet(ctx, "user.name")
	if err != nil {
		t.Fatalf("ConfigGet: %v", err)
	}
	if config != "" {
		t.Errorf("the repository's own config was written: %q", config)
	}
}

// A repository hook may refuse the commit, and the message has to say whether
// the operator's own hooks were in play at all (lib/github.sh:455-461,
// :481-486).
func TestCommitRefusedByAHook(t *testing.T) {
	ctx := context.Background()
	git := testGit(t)
	repo := initRepo(t, git, filepath.Join(realTempDir(t), "clone"))
	commitFile(t, repo, "app.ts", "export const ok = 1\n", "init")

	hook := write(t, repo.Dir(), ".git/hooks/pre-commit", "#!/bin/sh\necho 'the hook says no' >&2\nexit 1\n")
	chmodExecutable(t, hook)

	write(t, repo.Dir(), "app.ts", "export const ok = 2\n")
	repo.StageAll(ctx)

	// With hooks skipped the commit lands, which is what a GitHub-hosted runner
	// does — it has no hooks installed.
	if err := repo.Commit(ctx, vcs.CommitOptions{Message: "fix: one"}); err != nil {
		t.Fatalf("a skipped hook still refused the commit: %v", err)
	}

	write(t, repo.Dir(), "app.ts", "export const ok = 3\n")
	repo.StageAll(ctx)
	err := repo.Commit(ctx, vcs.CommitOptions{Message: "fix: two", RunHooks: true})
	refusal, ok := err.(*vcs.Refusal)
	if !ok {
		t.Fatalf("Commit = %v, want a refusal", err)
	}
	if !strings.HasPrefix(refusal.Message, "could not commit the resolver's changes — ") {
		t.Errorf("message = %q", refusal.Message)
	}
	if !strings.Contains(refusal.Message, "the hook says no") {
		t.Errorf("the message does not carry what git said: %q", refusal.Message)
	}
	if !strings.Contains(refusal.Hint, "git.hooks: run") {
		t.Errorf("the hint does not name the setting that put the hooks in play: %q", refusal.Hint)
	}
	if !strings.Contains(refusal.Hint, "The working tree still holds them") {
		t.Errorf("the hint does not say the changes are safe: %q", refusal.Hint)
	}
}

func TestCommitHintWhenHooksWereSkipped(t *testing.T) {
	ctx := context.Background()
	git := testGit(t)
	repo := initRepo(t, git, filepath.Join(realTempDir(t), "clone"))
	commitFile(t, repo, "app.ts", "export const ok = 1\n", "init")

	// Nothing staged at all, which git itself refuses.
	err := repo.Commit(ctx, vcs.CommitOptions{Message: "fix: nothing"})
	refusal, ok := err.(*vcs.Refusal)
	if !ok {
		t.Fatalf("Commit = %v, want a refusal", err)
	}
	if !strings.Contains(refusal.Hint, "git itself refusing rather than a hook") {
		t.Errorf("hint = %q", refusal.Hint)
	}
}

func TestPushAndRemoteHead(t *testing.T) {
	ctx := context.Background()
	git := testGit(t)
	root := realTempDir(t)

	origin := filepath.Join(root, "origin.git")
	bare := git.At(root)
	mustGit(t, bare, "init", "-q", "--bare", "-b", "main", origin)

	repo := initRepo(t, git, filepath.Join(root, "clone"))
	commitFile(t, repo, "app.ts", "export const ok = 1\n", "init")
	mustGit(t, repo, "remote", "add", "origin", origin)

	if err := repo.Push(ctx, "origin", "main", false); err != nil {
		t.Fatalf("Push: %v", err)
	}

	url, err := repo.PushURL(ctx, "origin")
	if err != nil {
		t.Fatalf("PushURL: %v", err)
	}
	if url != origin {
		t.Errorf("push url = %q, want %q", url, origin)
	}

	head, err := repo.RemoteHead(ctx, url, "main")
	if err != nil {
		t.Fatalf("RemoteHead: %v", err)
	}
	local, err := repo.Head(ctx)
	if err != nil {
		t.Fatalf("Head: %v", err)
	}
	if head != local.SHA() {
		t.Errorf("remote head = %q, want %q", head, local.SHA())
	}

	// An unreachable remote and a branch nobody has pushed are one empty
	// answer, which is why the caller warns on empty rather than trusting it.
	missing, err := repo.RemoteHead(ctx, filepath.Join(root, "no-such-repo.git"), "main")
	if err != nil {
		t.Fatalf("RemoteHead against an unreachable remote: %v", err)
	}
	if missing != "" {
		t.Errorf("remote head = %q, want the empty string", missing)
	}
}

// A remote with no URL at all falls back to its own name, which is what git
// itself accepts as a push target (lib/github.sh:496).
func TestPushURLFallsBackToTheRemoteName(t *testing.T) {
	ctx := context.Background()
	git := testGit(t)
	repo := initRepo(t, git, filepath.Join(realTempDir(t), "clone"))
	commitFile(t, repo, "app.ts", "x\n", "init")

	url, err := repo.PushURL(ctx, "upstream")
	if err != nil {
		t.Fatalf("PushURL: %v", err)
	}
	if url != "upstream" {
		t.Errorf("push url = %q, want %q", url, "upstream")
	}
}

func TestPushRefused(t *testing.T) {
	ctx := context.Background()
	git := testGit(t)
	root := realTempDir(t)
	repo := initRepo(t, git, filepath.Join(root, "clone"))
	commitFile(t, repo, "app.ts", "x\n", "init")
	mustGit(t, repo, "remote", "add", "origin", filepath.Join(root, "no-such-repo.git"))

	err := repo.Push(ctx, "origin", "main", false)
	refusal, ok := err.(*vcs.Refusal)
	if !ok {
		t.Fatalf("Push = %v, want a refusal", err)
	}
	if !strings.HasPrefix(refusal.Message, "could not push to main — ") {
		t.Errorf("message = %q", refusal.Message)
	}
	if !strings.Contains(refusal.Hint, "branch protection refused it") {
		t.Errorf("hint = %q", refusal.Hint)
	}
}

// The tail is the last five non-blank lines, capped, with the cut marked. It is
// _gh_git_tail at lib/github.sh:412-418.
func TestGitTail(t *testing.T) {
	tests := []struct {
		name  string
		text  string
		want  string
		found bool
	}{
		{name: "nothing at all", text: "", want: "", found: false},
		{name: "blank lines only", text: "\n   \n\t\n", want: "", found: false},
		{
			name:  "the last five non-blank lines",
			text:  "one\n\ntwo\nthree\nfour\nfive\nsix\n",
			want:  "two\nthree\nfour\nfive\nsix",
			found: true,
		},
		{
			name:  "capped with the cut marked",
			text:  strings.Repeat("x", 500),
			want:  "…" + strings.Repeat("x", 400),
			found: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, found := vcs.GitTail(tt.text)
			if found != tt.found {
				t.Fatalf("found = %t, want %t", found, tt.found)
			}
			if got != tt.want {
				t.Errorf("tail = %q, want %q", got, tt.want)
			}
		})
	}
}
