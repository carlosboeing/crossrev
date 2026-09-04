package cred_test

import (
	"slices"
	"testing"

	"github.com/carlosboeing/crossrev/internal/cred"
	"github.com/carlosboeing/crossrev/internal/exec"
)

// The four forge credentials reach every strip set, whatever the harness.
//
// This is the assertion the whole file exists for. Four of the five shipped
// harnesses declare an empty env_keep, so their vendor strip set is already the
// widest one the descriptor can produce — and a StripFor that had dropped the
// forge names entirely would still return five plausible-looking entries for
// each of them. An unknown harness is included for the same reason: nothing
// declares that it may keep anything, so its answer is the widest of all and
// tells the two failures apart least.
//
// ADR 0001 and SECURITY.md: the model-facing process never receives a GitHub
// credential. exec.Run refuses one that still carries it
// (internal/exec/spec.go:86-96); this is what keeps a caller from getting there.
func TestEveryStripSetShedsTheFourForgeCredentials(t *testing.T) {
	doc := descriptors(t)
	forge := exec.ForgeCredentialNames()
	if len(forge) != 4 {
		t.Fatalf("exec names %d forge credentials, want 4: %v", len(forge), forge)
	}

	names := append(doc.Names(), "not-a-harness")
	for _, harness := range names {
		got := cred.StripFor(doc, harness)
		for _, credential := range forge {
			if !slices.Contains(got, credential) {
				t.Errorf("%s: the strip set does not shed %s: %v", harness, credential, got)
			}
		}
	}

	// A harness with an empty keep list is the case an omission would pass, so
	// it is named rather than left to the loop.
	if keep := doc.For("codex").Credential.EnvKeep; len(keep) != 0 {
		t.Fatalf("codex now keeps %v, so it no longer covers the empty-keep-list case", keep)
	}
	if got := cred.StripFor(doc, "codex"); !slices.Contains(got, "GH_TOKEN") {
		t.Errorf("codex, whose keep list is empty, does not shed GH_TOKEN: %v", got)
	}
}

// The forge names come first, then the descriptor's, which is the order the
// adapters build: `env -u GH_TOKEN …` first (lib/adapters/claude.sh:72), then
// whatever cred_env_strip_for prints (:76).
func TestStripForPutsTheForgeCredentialsFirst(t *testing.T) {
	got := cred.StripFor(descriptors(t), "claude")
	want := []string{
		"GH_TOKEN",
		"GITHUB_TOKEN",
		"GH_ENTERPRISE_TOKEN",
		"GITHUB_ENTERPRISE_TOKEN",
		"CROSSREV_CODEX_AUTH",
		"CROSSREV_GROK_AUTH",
		"CROSSREV_OPENCODE_AUTH",
	}
	if !slices.Equal(got, want) {
		t.Errorf("StripFor(claude) = %v, want %v", got, want)
	}
}

// StripFor must not hand back a slice that shares an array with the exec list,
// because appending to one caller's result would then rewrite the other's.
func TestStripForAnswersAnIndependentSlice(t *testing.T) {
	doc := descriptors(t)
	first := cred.StripFor(doc, "claude")
	first[0] = "OVERWRITTEN"

	if second := cred.StripFor(doc, "claude"); second[0] != "GH_TOKEN" {
		t.Errorf("a second strip set was changed by writing through the first: %v", second)
	}
	if names := exec.ForgeCredentialNames(); names[0] != "GH_TOKEN" {
		t.Errorf("writing through a strip set changed exec's own list: %v", names)
	}
}

// claude sheds the other vendors' credentials and keeps the two it
// authenticates with (tests/test-credentials.sh:238-243).
//
// ANTHROPIC_API_KEY is not stripped for claude on purpose: it is the operator's
// own environment, not something a workflow injected, and removing it would
// quietly move a local run from API billing to subscription billing.
func TestClaudeKeepsWhatItAuthenticatesWith(t *testing.T) {
	got := cred.VendorStripFor(descriptors(t), "claude")
	for _, shed := range []string{"CROSSREV_CODEX_AUTH", "CROSSREV_GROK_AUTH", "CROSSREV_OPENCODE_AUTH"} {
		if !slices.Contains(got, shed) {
			t.Errorf("claude does not shed %s: %v", shed, got)
		}
	}
	for _, kept := range []string{"CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_API_KEY"} {
		if slices.Contains(got, kept) {
			t.Errorf("claude sheds %s, which it authenticates with: %v", kept, got)
		}
	}
}

// codex sheds claude's token, the anthropic key, and its own raw credential —
// which by then has been written into CODEX_HOME, so the copy in the
// environment is a second one nothing needs
// (tests/test-credentials.sh:245-250).
func TestCodexShedsEveryCredentialIncludingItsOwnRawCopy(t *testing.T) {
	got := cred.VendorStripFor(descriptors(t), "codex")
	for _, shed := range []string{"CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_API_KEY", "CROSSREV_CODEX_AUTH"} {
		if !slices.Contains(got, shed) {
			t.Errorf("codex does not shed %s: %v", shed, got)
		}
	}
}

// agy holds none of them (tests/test-credentials.sh:252-253).
func TestAgyShedsEveryCredential(t *testing.T) {
	doc := descriptors(t)
	got := cred.VendorStripFor(doc, "agy")
	if !slices.Equal(got, doc.VendorNames()) {
		t.Errorf("agy = %v, want the whole union %v", got, doc.VendorNames())
	}
}

// A harness the descriptor does not carry keeps nothing, because nothing says
// it may. That is jq's own answer: `$keep` is an empty array for a name no
// entry matches (lib/credentials.sh:183).
func TestAnUnknownHarnessKeepsNothing(t *testing.T) {
	doc := descriptors(t)
	got := cred.VendorStripFor(doc, "not-a-harness")
	if !slices.Equal(got, doc.VendorNames()) {
		t.Errorf("an unknown harness = %v, want the whole union %v", got, doc.VendorNames())
	}
}
