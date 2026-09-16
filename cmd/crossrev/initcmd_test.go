package main

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/exec"
	"github.com/carlosboeing/crossrev/internal/initcmd"
	"github.com/carlosboeing/crossrev/internal/vcs"
)

// The peeled form (`refs/tags/v0.6.2^{}`) is the commit an annotated tag
// points at; the direct form is a lightweight tag. action.yml reads both and
// so does this, because which kind a release carries is not this code's
// business.
func TestTagForSHAReadsBothTagForms(t *testing.T) {
	const sha = "3cbdb5489de49c1503b82e8a6ce48567a5e9e8f4"
	out := "" +
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\trefs/tags/v0.6.1\n" +
		"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb\trefs/tags/v0.6.2\n" +
		sha + "\trefs/tags/v0.6.2^{}\n"
	if got := tagForSHA(out, sha); got != "v0.6.2" {
		t.Errorf("tagForSHA = %q, want v0.6.2", got)
	}
}

func TestTagForSHAAnswersUntaggedForACommitNoTagPointsAt(t *testing.T) {
	out := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\trefs/tags/v0.6.1\n"
	if got := tagForSHA(out, "3cbdb5489de49c1503b82e8a6ce48567a5e9e8f4"); got != initcmd.Untagged {
		t.Errorf("tagForSHA = %q, want %q", got, initcmd.Untagged)
	}
}

// A branch or a non-release tag is not a pin comment. action.yml matches
// ^v[0-9]+\.[0-9]+\.[0-9]+$ and so does this.
func TestTagForSHAIgnoresRefsThatAreNotReleaseTags(t *testing.T) {
	const sha = "3cbdb5489de49c1503b82e8a6ce48567a5e9e8f4"
	out := sha + "\trefs/tags/nightly\n" + sha + "\trefs/heads/main\n"
	if got := tagForSHA(out, sha); got != initcmd.Untagged {
		t.Errorf("tagForSHA = %q, want %q", got, initcmd.Untagged)
	}
}

// scriptedLsRemote answers `git ls-remote --tags` and records the invocation.
//
// It is the seam the prompt-suppression test needs: initSource.Ref reads the
// pin from the build stamp, which a test binary does not carry, so the test
// drives refForSHA — Ref's remote half — directly over an answer table.
type scriptedLsRemote struct {
	// out is the ls-remote stdout.
	out string
	// exitCode is the status. Non-zero exercises the unreachable fallback.
	exitCode int
	// calls records every invocation's environment and argument vector.
	calls []lsRemoteCall
}

// lsRemoteCall is one git invocation as the child would have seen it.
type lsRemoteCall struct {
	env  []string
	args []string
}

func (g *scriptedLsRemote) Run(_ context.Context, spec exec.Spec) exec.Result {
	g.calls = append(g.calls, lsRemoteCall{env: slices.Clone(spec.Env), args: slices.Clone(spec.Args)})
	return exec.Result{Stdout: []byte(g.out), ExitCode: g.exitCode}
}

// effectiveEnv folds NAME=VALUE entries the way os/exec reads them: the last
// value for a duplicate key wins, so an override appended after an inherited
// entry is the value the child sees.
func effectiveEnv(env []string) map[string]string {
	m := map[string]string{}
	for _, e := range env {
		if k, v, ok := strings.Cut(e, "="); ok {
			m[k] = v
		}
	}
	return m
}

// The tag lookup must never ask the operator anything. The answer is already
// non-fatal — unknown means `untagged` with ErrSourceUnreachable — so any
// prompt pays interaction for a value init is willing to give up on.
//
// GIT_TERMINAL_PROMPT=0 alone does not do this: git consults an inherited
// GIT_ASKPASS before the terminal setting, and an `insteadOf` rewrite to SSH
// leaves OpenSSH free to ask for a passphrase or a host-key confirmation. The
// lookup therefore carries its own askpass override and BatchMode ssh, for
// this one call only (P2, PR #255).
func TestRefLookupDisablesEveryPrompt(t *testing.T) {
	const sha = "3cbdb5489de49c1503b82e8a6ce48567a5e9e8f4"
	git := &scriptedLsRemote{out: sha + "\trefs/tags/v0.6.2\n"}
	// The base environment carries a hostile askpass helper, the way the
	// orchestrator's allowlist inherits the operator's own.
	base := []string{"PATH=/usr/bin:/bin", "GIT_ASKPASS=/usr/bin/ssh-askpass"}
	s := initSource{repo: vcs.New(git, base).At("")}

	ref, err := s.refForSHA(context.Background(), sha)
	if err != nil {
		t.Fatalf("refForSHA: %v", err)
	}
	if ref != "v0.6.2" {
		t.Fatalf("refForSHA = %q, want v0.6.2", ref)
	}
	if len(git.calls) != 1 {
		t.Fatalf("refForSHA made %d git calls, want 1", len(git.calls))
	}
	call := git.calls[0]
	if want := []string{"ls-remote", "--tags", crossrevRemote}; !slices.Equal(call.args, want) {
		t.Errorf("args = %v, want %v", call.args, want)
	}
	env := effectiveEnv(call.env)
	if env["GIT_TERMINAL_PROMPT"] != "0" {
		t.Errorf("GIT_TERMINAL_PROMPT = %q, want 0", env["GIT_TERMINAL_PROMPT"])
	}
	if env["GIT_ASKPASS"] != "false" {
		t.Errorf("GIT_ASKPASS = %q, want the failing override, not the inherited helper", env["GIT_ASKPASS"])
	}
	if ssh := env["GIT_SSH_COMMAND"]; ssh != "ssh -o BatchMode=yes" {
		t.Errorf("GIT_SSH_COMMAND = %q, want BatchMode ssh", ssh)
	}
}

// A lookup that fails — BatchMode refusing to ask is one way — answers
// `untagged` with ErrSourceUnreachable, so the plan says the question went
// unanswered rather than claiming no release exists.
func TestRefLookupFailureIsUnreachable(t *testing.T) {
	git := &scriptedLsRemote{exitCode: 128}
	s := initSource{repo: vcs.New(git, nil).At("")}
	ref, err := s.refForSHA(context.Background(), strings.Repeat("ab", 20))
	if ref != initcmd.Untagged {
		t.Errorf("ref = %q, want %q", ref, initcmd.Untagged)
	}
	if !errors.Is(err, initcmd.ErrSourceUnreachable) {
		t.Errorf("err = %v, want ErrSourceUnreachable", err)
	}
}
