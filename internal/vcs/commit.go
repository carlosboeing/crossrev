package vcs

import (
	"context"
	"fmt"
	"strings"
)

// The identity a commit carries when the operator named none. The shell reads
// CROSSREV_GIT_NAME and CROSSREV_GIT_EMAIL and falls back to these
// (lib/github.sh:440-441); the fallback lives here and the environment read
// belongs to the caller that builds CommitOptions.
const (
	DefaultCommitName  = "crossrev"
	DefaultCommitEmail = "crossrev@users.noreply.github.com"
)

// gitTailCap is how much of git's own output a message carries, in characters.
// It is the default second argument of _gh_git_tail (lib/github.sh:405).
const gitTailCap = 400

// CommitOptions is one commit the resolve leg makes.
type CommitOptions struct {
	// Message is the commit message.
	Message string
	// Name and Email are the identity. Empty means the CrossRev default.
	Name  string
	Email string
	// RunHooks is the repository's `git.hooks: run` setting. False skips the
	// repository's own hooks, which is what a GitHub-hosted runner does because
	// it has none installed.
	RunHooks bool
}

// StageAll is `git add -A`.
//
// Its failure is swallowed, exactly as at lib/github.sh:461. The question that
// decides whether there is anything to commit is asked next, by
// HasStagedChanges, and it answers correctly whether or not the add worked.
func (r *Repository) StageAll(ctx context.Context) {
	_, _ = r.Run(ctx, "add", "-A")
}

// Commit writes the staged changes, or refuses with what git said.
//
// The identity is passed with `-c` rather than written into the repository's
// configuration: CrossRev runs in a checkout the operator owns, and a run that
// left `user.name` behind would sign their next commit as well.
//
// The hooks advice depends on whether the operator's own hooks were in play at
// all, because the two failures need different answers: one is a hook to look
// at, and the other is git itself (lib/github.sh:454-461).
func (r *Repository) Commit(ctx context.Context, options CommitOptions) error {
	name := options.Name
	if name == "" {
		name = DefaultCommitName
	}
	email := options.Email
	if email == "" {
		email = DefaultCommitEmail
	}

	args := []string{"-c", "user.name=" + name, "-c", "user.email=" + email, "commit", "-q"}
	if !options.RunHooks {
		args = append(args, "--no-verify")
	}
	args = append(args, "-m", options.Message)

	// Captured as one stream, which is the `2>&1` at lib/github.sh:481. The
	// selection below is by position, so a merge after the fact would keep a
	// different set of lines rather than the same lines reordered.
	output, err := r.RunCombined(ctx, args...)
	if err != nil {
		return err
	}
	if output.OK() {
		return nil
	}

	why, found := GitTail(output.Stdout)
	if !found {
		why = "git printed nothing on either stream."
	}
	return &Refusal{
		Message: "could not commit the resolver's changes — " + why,
		Hint:    "The working tree still holds them, so nothing is lost. " + hooksAdvice(options.RunHooks) + " Check `git status` in the checkout.",
	}
}

// hooksAdvice is what to tell someone whose commit was refused, which depends
// on whether their own hooks ran (lib/github.sh:456-460).
func hooksAdvice(runHooks bool) string {
	if runHooks {
		return "This repository sets git.hooks: run, so its own commit hooks ran on this commit and one of them may have refused it. Setting git.hooks: skip makes the resolver commit the way it already does on a GitHub-hosted runner, which has no hooks installed."
	}
	return "The repository's own git hooks were skipped, so this is git itself refusing rather than a hook."
}

// Push sends HEAD to one branch of one remote.
//
// A pre-push hook is a host repository hook firing on an automated action for
// the same reason a pre-commit hook is, and git spells the two flags apart —
// which is why the same `git.hooks` setting reaches this as `--no-verify` on
// push and not only on commit (lib/github.sh:443-448).
//
// The refspec is explicit rather than left to git's default. `HEAD:refs/heads/
// <branch>` names the destination whatever the local checkout's own branch is,
// and the resolve leg runs on a detached worktree where there is no local
// branch to infer one from.
func (r *Repository) Push(ctx context.Context, remote, branch string, runHooks bool) error {
	args := []string{"push"}
	if !runHooks {
		args = append(args, "--no-verify")
	}
	args = append(args, remote, "HEAD:refs/heads/"+branch)

	// Captured as one stream, and here it is load-bearing rather than tidy.
	// A pre-push hook writes to both: git sends the hook's stdout to git's
	// stdout, and the hook's stderr plus its own `error: failed to push some
	// refs` to stderr. GitTail keeps the last five non-blank lines by position,
	// so stdout concatenated ahead of stderr pushes every stdout line out of
	// the window — a different SET of lines from the `2>&1` at
	// lib/github.sh:510, published into a pull request comment.
	output, err := r.RunCombined(ctx, args...)
	if err != nil {
		return err
	}
	if output.OK() {
		return nil
	}

	why, found := GitTail(output.Stdout)
	if !found {
		why = "git printed nothing on either stream."
	}
	return &Refusal{
		Message: fmt.Sprintf("could not push to %s — %s", branch, why),
		Hint:    "The commit exists locally. If branch protection refused it, that is the backstop working — check the rule, or push by hand.",
	}
}

// PushURL is the address a push to this remote would use, falling back to the
// fetch URL and then to the remote's own name (lib/github.sh:496).
//
// The last fallback is not a guess. git accepts a bare name as a push target,
// so handing the name back keeps the ls-remote that follows asking about the
// same thing the push will reach.
func (r *Repository) PushURL(ctx context.Context, remote string) (string, error) {
	for _, args := range [][]string{
		{"remote", "get-url", "--push", remote},
		{"remote", "get-url", remote},
	} {
		output, err := r.Run(ctx, args...)
		if err != nil {
			return "", err
		}
		if output.OK() {
			if url := output.Text(); url != "" {
				return url, nil
			}
		}
	}
	return remote, nil
}

// RemoteHead is the object name a branch points at on the other end, read
// immediately before a push (lib/github.sh:497-500).
//
// A human pushing to the same branch mid-leg is a normal event, and overwriting
// them is not — so the head is re-read rather than trusted from the context
// loaded at the start of the leg.
//
// An unreachable remote and a branch nobody has pushed give the same empty
// answer. The shell's `if ! remote_head=…` guard at lib/github.sh:497 does
// fire — bin/crossrev:12 sets `-o pipefail`, so the status is the pipeline's
// and not the `cut` at the end of it — but both arms end at the empty string
// anyway, which is why the two implementations agree without this one having a
// guard of its own. The caller warns on empty rather than reading it as
// "nobody else pushed" (lib/github.sh:500-502).
func (r *Repository) RemoteHead(ctx context.Context, url, branch string) (string, error) {
	output, err := r.Run(ctx, "ls-remote", url, "refs/heads/"+branch)
	if err != nil {
		return "", err
	}
	if !output.OK() {
		return "", nil
	}
	// `cut -f1`, over every line rather than the first: one exact ref answers
	// with one line, and quietly dropping a second would hide the case where
	// it did not.
	var names []string
	for _, line := range output.Lines() {
		name, _, _ := strings.Cut(line, "\t")
		names = append(names, name)
	}
	return strings.Join(names, "\n"), nil
}

// GitTail is the part of git's own output a message carries: the last five
// non-blank lines, capped, with the cut marked. It is _gh_git_tail at
// lib/github.sh:404-410.
//
// It reports false when there was nothing to say, so a caller can print "git
// printed nothing on either stream" rather than an empty dash.
func GitTail(text string) (string, bool) {
	var kept []string
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		kept = append(kept, line)
	}
	if len(kept) == 0 {
		return "", false
	}
	if len(kept) > 5 {
		kept = kept[len(kept)-5:]
	}
	picked := strings.Join(kept, "\n")

	return capTail(picked), true
}

// capTail keeps the last gitTailCap characters of s, marking the cut.
//
// # Characters, because the shell counts characters
//
// `${#picked}` and `${picked: -cap}` both count characters under bash, so a
// byte cap here would cut a multi-byte character in half and put the halves in
// a pull request comment. A differential over 500 × é, 500 × 中, 401 astral
// emoji, no-break and ideographic spaces agrees byte for byte.
//
// # Counted without decoding, because the bytes are not always text
//
// `[]rune(s)` and `string(runes)` are not a round trip: every byte that is not
// valid UTF-8 becomes U+FFFD going in and three bytes coming out, so 500 bytes
// of 0xFF came back as 1203 bytes of EF BF BD where the shell answered 403. A
// hook printing Latin-1, or a filename in a non-UTF-8 encoding, produces that —
// and this text is published rather than merely printed (lib/github.sh:513 to
// lib/ui.sh:115 to lib/run.sh:144-146, which writes the pull request comment).
//
// So the count is taken by walking and the result is a slice of the original.
// `for range` over a string yields U+FFFD for an invalid byte and advances by
// one, which is the property that makes the two counts agree; TestGitTailCounts
// AnInvalidByteAsOneCharacter asserts it rather than trusting it.
//
// One locale caveat, stated rather than hidden: bash counts characters only
// where the locale says the bytes are characters. Under LC_ALL=C `${#picked}`
// counts bytes, and nothing in lib/github.sh pins a locale, so a run under a C
// locale would have the shell counting bytes while this counts characters. That
// is a question about the shell's locale rather than about this port, and the
// frozen behaviour is the UTF-8 one.
func capTail(s string) string {
	characters := 0
	for range s {
		characters++
	}
	if characters <= gitTailCap {
		return s
	}

	skip := characters - gitTailCap
	seen := 0
	for offset := range s {
		if seen == skip {
			return "…" + s[offset:]
		}
		seen++
	}
	return "…" + s
}
