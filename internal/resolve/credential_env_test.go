package resolve

import (
	"strings"
	"testing"
)

// stagedGrokCredential is a placeholder grok auth.json. Grok's descriptor
// asserts no freshness, so the leg stages these bytes opaquely; the property
// under test is that the staging directory reaches the child, not what the
// credential says.
func stagedGrokCredential() string {
	return `{"auth":"stub"}`
}

// The resolve leg stages a credential the same way the review leg does, and it
// reached the same fault from the same cause: Leg.Env is read at the
// composition root (cmd/crossrev/legs.go:168) before cred.Prepare exports
// GROK_HOME, so the child was handed a list that never named the scratch home.
//
// The review leg has the matching case. Both are kept: the fix is one line in
// each file, and one line is exactly what a later edit drops from one of them.
//
// This ran on codex until codex was refused as a resolver: with its shell
// disabled codex 0.158.0 has no file-reading tool, answers `blocked` instead
// of editing, and never starts a child to stage for. Grok stages the same way
// through GROK_HOME, so the pin moved to it.
func TestTheStagedCredentialReachesTheResolveHarness(t *testing.T) {
	e := setup(t)
	t.Setenv("CROSSREV_GROK_AUTH", stagedGrokCredential())
	e.addReview(t, defaultFindings(), "issues-remain")

	got := e.runReq(t, Request{
		PR:      42,
		Repo:    e.slug,
		Trigger: TriggerHuman,
		Harness: "grok",
	})
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}

	var home string
	found := 0
	for _, entry := range got.Invocation.Env {
		if rest, ok := strings.CutPrefix(entry, "GROK_HOME="); ok {
			home = rest
			found++
		}
	}

	if found == 0 {
		t.Fatalf("the child environment names no GROK_HOME, so grok reads its own store instead of the staged copy: %v", got.Invocation.Env)
	}
	// One entry, not two. Go's exec takes the last of a repeated name, so a
	// second one would work by accident rather than by design.
	if found > 1 {
		t.Errorf("GROK_HOME appears %d times: %v", found, got.Invocation.Env)
	}
	if home == "" {
		t.Error("GROK_HOME is empty, which points grok at nothing")
	}
}
