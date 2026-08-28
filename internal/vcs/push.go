package vcs

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/carlosboeing/crossrev/internal/core"
)

// gitHubHost is the only host a push target may resolve to.
const gitHubHost = "github.com"

// ErrNotGitHubURL is returned for a remote URL that is not a github.com
// repository URL. The shell spells it as a bare `return 1` from
// legs_github_slug (lib/legs.sh:344), with the caller owning the message.
var ErrNotGitHubURL = errors.New("not a github.com repository URL")

// GitHubSlug is the owner/repo a github.com remote URL names, or an error for
// anything else. It is legs_github_slug at lib/legs.sh:342-364.
//
// # Substring matching is not enough, and the rejects are why
//
// `https://github.com.example.net/a/b` and a local path holding
// `github.com/a/b` both contain the host and are not it. So the host is
// isolated and compared whole, in every shape git accepts a remote in: https,
// ssh://, git:// and the scp-style `git@github.com:owner/repo.git`.
//
// # core.ParseSlug is not a substitute
//
// It splits on `/` and never looks at a host, so a remote pointing anywhere
// would produce a slug from it. What makes this function a guard is the host
// comparison, and the character class after it: core.NewSlug accepts an owner
// containing `~` or `$`, and this refuses one, because the value goes on to
// name a repository a commit is pushed to.
func GitHubSlug(url string) (core.Slug, error) {
	rest, ok := authorityAndPath(url)
	if !ok {
		return core.Slug{}, fmt.Errorf("%w: %q", ErrNotGitHubURL, url)
	}

	// Drop userinfo only when the `@` is in the authority, never when it is a
	// character in the path. The test reads the authority; the cut is made on
	// the whole string, which is the same cut because the `@` it finds first is
	// the one the test saw.
	if authority, _, _ := strings.Cut(rest, "/"); strings.Contains(authority, "@") {
		_, rest, _ = strings.Cut(rest, "@")
	}

	host, _, _ := strings.Cut(rest, "/")
	host, _, _ = strings.Cut(host, ":")
	if asciiLower(host) != gitHubHost {
		return core.Slug{}, fmt.Errorf("%w: %q", ErrNotGitHubURL, url)
	}

	// `${rest#*/}` leaves the string untouched when there is no slash in it, so
	// a URL with no path at all carries the host through to the shape test and
	// is refused there rather than here.
	path := rest
	if _, after, found := strings.Cut(rest, "/"); found {
		path = after
	}
	path = strings.TrimSuffix(path, "/")
	path = strings.TrimSuffix(path, ".git")

	owner, name, found := strings.Cut(path, "/")
	if !found || !isSlugSegment(owner) || !isSlugSegment(name) {
		return core.Slug{}, fmt.Errorf("%w: %q", ErrNotGitHubURL, url)
	}
	return core.NewSlug(owner, name)
}

// authorityAndPath reduces a remote URL to `host[:port]/path`, and reports
// whether the URL had a shape this understands at all.
func authorityAndPath(url string) (string, bool) {
	if _, after, found := strings.Cut(url, "://"); found {
		return after, true
	}
	// scp-style. The first colon separates host from path; a colon after a
	// slash is part of a local path, which is why the guard tests for one.
	if before, after, found := strings.Cut(url, ":"); found && !strings.Contains(before, "/") {
		return before + "/" + after, true
	}
	return "", false
}

// asciiLower folds A-Z and nothing else.
//
// The shell folds with `tr '[:upper:]' '[:lower:]'`, and the identity rules
// elsewhere in CrossRev are byte-oriented for the reason core.isHex gives. Only
// the seven-bit spelling of github.com can pass the comparison that follows, so
// a fold that also touched non-ASCII could only ever admit a host this must
// refuse.
func asciiLower(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		b.WriteByte(c)
	}
	return b.String()
}

// isSlugSegment is one half of `^[A-Za-z0-9._-]+/[A-Za-z0-9._-]+$`.
func isSlugSegment(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z':
		case c >= 'a' && c <= 'z':
		case c >= '0' && c <= '9':
		case c == '.' || c == '_' || c == '-':
		default:
			return false
		}
	}
	return true
}

// PushTarget is where `git push <remote>` would write.
type PushTarget struct {
	// Repo is the repository every push URL of the remote resolves to. The
	// zero value means the remote carries no URL at all, which
	// legs_resolve_push_repo reports by leaving LEGS_PUSH_REPO empty
	// (lib/legs.sh:383-385) and leaves the caller to describe, because what to
	// say about it depends on how the remote was resolved.
	Repo core.Slug

	// Warnings are the pushInsteadOf rewrites found after the target was
	// approved. They do not stop the push.
	Warnings []Warning
}

// Slug is the target as a string, empty when the remote has no URL.
func (t PushTarget) Slug() string {
	if t.Repo.Incomplete() {
		return ""
	}
	return t.Repo.String()
}

// ResolvePushRepo is the repository `git push <remote>` would write to. It is
// legs_resolve_push_repo at lib/legs.sh:392-439.
//
// # Why the config keys rather than `git remote get-url --push`
//
// A remote may carry several `remote.<name>.pushurl` entries and git pushes to
// every one of them, but that command returns only the first — so a second
// entry pointing somewhere else is invisible to anything reading it. When no
// pushurl is set at all, git pushes to the `remote.<name>.url` entries
// instead, so those are the fallback.
//
// Two entries resolving to different repositories is therefore a refusal and
// not a choice. There is no correct one to pick: git pushes to both.
//
// A URL that does not resolve to a github.com slug is refused rather than
// swapped for one that does. Checking the fetch URL because the push URL is
// unreadable validates an address the commit will never reach, which is exactly
// the configuration the guard exists to catch.
//
// # The rewrite warning, and why it is not an improvement
//
// `git config` returns the configured URL, and git resolves
// `url.<base>.pushInsteadOf` after reading it, so a rewrite can still redirect
// a URL this approved. The effective URLs are read only to decide whether to
// warn, never to validate: reading them instead would break every offline test
// that pushes, since a fixture's effective push URL is a local path and no
// local path resolves to a github.com slug.
//
// The comparison is between URL strings, so a rewrite that changes only the
// transport for the same repository warns as well. That is the shipped
// behaviour, and reproducing it is the point.
//
// The two URL lists pair positionally because a rewrite reaches this code only
// when no explicit pushurl is set, so both come from the same key.
func (r *Repository) ResolvePushRepo(ctx context.Context, remote string) (PushTarget, error) {
	urls, err := r.ConfigGetAll(ctx, "remote."+remote+".pushurl")
	if err != nil {
		return PushTarget{}, err
	}
	if len(urls) == 0 {
		urls, err = r.ConfigGetAll(ctx, "remote."+remote+".url")
		if err != nil {
			return PushTarget{}, err
		}
	}

	var target PushTarget
	for _, url := range urls {
		slug, slugErr := GitHubSlug(url)
		if slugErr != nil {
			return PushTarget{}, &Refusal{
				Message: fmt.Sprintf("remote '%s' pushes to '%s', which is not a github.com repository URL", remote, url),
				Hint:    fmt.Sprintf("CrossRev checks where a fix will land before it leaves the machine, and it can only do that for a github.com URL. Check `git config --get-all remote.%s.pushurl`.", remote),
			}
		}
		if target.Repo.Incomplete() {
			target.Repo = slug
			continue
		}
		if target.Repo.String() != slug.String() {
			return PushTarget{}, &Refusal{
				Message: fmt.Sprintf("remote '%s' pushes to two different repositories, '%s' and '%s'", remote, target.Repo, slug),
				Hint:    fmt.Sprintf("git pushes to every `remote.%s.pushurl` entry, and CrossRev pushes only to the head repository of the pull request under review. Remove the entry that does not belong.", remote),
			}
		}
	}

	effective, err := r.effectivePushURLs(ctx, remote)
	if err != nil {
		return PushTarget{}, err
	}
	for i, configured := range urls {
		// `${eff_arr[i]:-$cfg}`: a list shorter than the configured one leaves
		// the entry comparing against itself, which never warns.
		rewritten := configured
		if i < len(effective) {
			rewritten = effective[i]
		}
		if configured == "" || rewritten == "" || configured == rewritten {
			continue
		}
		target.Warnings = append(target.Warnings, Warning{
			Message: fmt.Sprintf("remote '%s' push URL '%s' is rewritten to '%s'", remote, configured, rewritten),
			Hint:    fmt.Sprintf("The guard approved '%s', but git push will send commits to '%s'. Check `git config --get-regexp '^url\\..*\\.pushInsteadOf'`.", configured, rewritten),
		})
	}

	return target, nil
}

// effectivePushURLs is `git remote get-url --push --all <remote>`, empty when
// the remote does not exist. The shell reads it as `|| true` (lib/legs.sh:423).
func (r *Repository) effectivePushURLs(ctx context.Context, remote string) ([]string, error) {
	output, err := r.Run(ctx, "remote", "get-url", "--push", "--all", remote)
	if err != nil {
		return nil, err
	}
	if !output.OK() {
		return nil, nil
	}
	return output.Lines(), nil
}
