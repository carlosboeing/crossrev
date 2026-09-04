package vcs_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/vcs"
)

type slugVectors struct {
	Function string `json:"function"`
	Cases    []struct {
		Name string  `json:"name"`
		URL  string  `json:"url"`
		Slug *string `json:"slug"`
		RC   int     `json:"rc"`
	} `json:"cases"`
}

// Every case in the frozen file, accepts and refusals alike. The refusals are
// the half that matters: a host merely containing github.com, a local path
// holding it, and an `@` that is in the path rather than the authority.
func TestGitHubSlugParity(t *testing.T) {
	var vectors slugVectors
	readFixture(t, slugFixture, &vectors)
	if want := "legs_github_slug"; vectors.Function != want {
		t.Fatalf("the fixture records %q, not %q", vectors.Function, want)
	}
	if len(vectors.Cases) == 0 {
		t.Fatal("the fixture records no cases")
	}

	for _, vector := range vectors.Cases {
		t.Run(vector.Name, func(t *testing.T) {
			slug, err := vcs.GitHubSlug(vector.URL)
			if vector.RC != 0 {
				if err == nil {
					t.Fatalf("GitHubSlug(%q) = %q, want a refusal", vector.URL, slug)
				}
				if vector.Slug != nil {
					t.Fatalf("the fixture records rc %d with a slug: %q", vector.RC, *vector.Slug)
				}
				return
			}
			if err != nil {
				t.Fatalf("GitHubSlug(%q): %v", vector.URL, err)
			}
			if vector.Slug == nil {
				t.Fatalf("the fixture records rc 0 with no slug")
			}
			if slug.String() != *vector.Slug {
				t.Errorf("slug = %q, want %q", slug.String(), *vector.Slug)
			}
		})
	}
}

// A refusal must be reported as one rather than as some other failure, so a
// caller can tell "this remote points elsewhere" from "git would not run".
func TestGitHubSlugRefusalIsTyped(t *testing.T) {
	if _, err := vcs.GitHubSlug("https://gitlab.com/o/r.git"); err == nil {
		t.Fatal("a gitlab URL was accepted")
	} else if !strings.Contains(err.Error(), vcs.ErrNotGitHubURL.Error()) {
		t.Errorf("error = %v, want it to name %v", err, vcs.ErrNotGitHubURL)
	}
}

type pushVectors struct {
	Function string `json:"function"`
	Cases    []struct {
		Name     string   `json:"name"`
		Config   []string `json:"config"`
		PushRepo string   `json:"push_repo"`
		Stderr   string   `json:"stderr"`
		RC       int      `json:"rc"`
	} `json:"cases"`
}

// Each case rebuilds the repository its config lines describe, resolves the
// push target against it, and compares all three recorded facts: the target,
// the exact stderr, and the return code.
func TestResolvePushRepoParity(t *testing.T) {
	var vectors pushVectors
	readFixture(t, pushFixture, &vectors)
	if want := "legs_resolve_push_repo"; vectors.Function != want {
		t.Fatalf("the fixture records %q, not %q", vectors.Function, want)
	}
	if len(vectors.Cases) == 0 {
		t.Fatal("the fixture records no cases")
	}

	git := testGit(t)
	root := realTempDir(t)

	for _, vector := range vectors.Cases {
		t.Run(vector.Name, func(t *testing.T) {
			repo := initRepo(t, git, filepath.Join(root, vector.Name))
			for _, line := range vector.Config {
				key, value, found := strings.Cut(line, "\t")
				if !found {
					t.Fatalf("the fixture config line %q has no tab", line)
				}
				// --add, never --replace-all: a repeated key keeps both
				// entries, which is the whole point of the two-pushurl cases.
				mustGit(t, repo, "config", "--add", key, value)
			}

			target, err := repo.ResolvePushRepo(context.Background(), "origin")

			gotRC := 0
			gotStderr := ""
			if err != nil {
				refusal, ok := err.(*vcs.Refusal)
				if !ok {
					t.Fatalf("ResolvePushRepo: %v", err)
				}
				gotRC = 1
				gotStderr = refusal.Rendered()
			} else {
				gotStderr = renderedWarnings(target.Warnings)
			}

			if gotRC != vector.RC {
				t.Errorf("rc = %d, want %d", gotRC, vector.RC)
			}
			if got := target.Slug(); got != vector.PushRepo {
				t.Errorf("push_repo = %q, want %q", got, vector.PushRepo)
			}
			if gotStderr != vector.Stderr {
				t.Errorf("stderr differs\n want: %q\n  got: %q", vector.Stderr, gotStderr)
			}
		})
	}
}

// renderedWarnings is what the shell's stderr holds after a run that warned:
// each ui_warn writes its two lines and a blank one, and the command
// substitution that captured the file dropped the trailing newlines.
func renderedWarnings(warnings []vcs.Warning) string {
	var b strings.Builder
	for _, warning := range warnings {
		b.WriteString(warning.Rendered())
		b.WriteString("\n\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// A remote whose URL is fine but whose name is not configured at all answers
// with no target and no warning, which is how the caller learns to print its
// own message (lib/legs.sh:386-389, lib/run.sh:1869-1871).
func TestResolvePushRepoUnknownRemote(t *testing.T) {
	git := testGit(t)
	repo := initRepo(t, git, filepath.Join(realTempDir(t), "r"))
	mustGit(t, repo, "config", "--add", "remote.origin.url", "https://github.com/o/r.git")

	target, err := repo.ResolvePushRepo(context.Background(), "upstream")
	if err != nil {
		t.Fatalf("ResolvePushRepo: %v", err)
	}
	if got := target.Slug(); got != "" {
		t.Errorf("push_repo = %q, want the empty string", got)
	}
	if len(target.Warnings) != 0 {
		t.Errorf("warnings = %v, want none", target.Warnings)
	}
}
