package policy_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/policy"
)

const (
	shaA = "1111111111111111111111111111111111111111"
	shaB = "2222222222222222222222222222222222222222"
)

func rev(t *testing.T, sha string) core.Revision {
	t.Helper()
	r, err := core.NewRevision(sha)
	if err != nil {
		t.Fatalf("NewRevision(%q): %v", sha, err)
	}
	return r
}

func slug(t *testing.T, s string) core.Slug {
	t.Helper()
	v, err := core.ParseSlug(s)
	if err != nil {
		t.Fatalf("ParseSlug(%q): %v", s, err)
	}
	return v
}

// TestAssertPushTarget transcribes lib/legs.sh:446-478 and the `guard` block at
// tests/test-legs.sh:91-94, plus the two cross-repository arms the Bash block
// leaves to its six-argument default.
func TestAssertPushTarget(t *testing.T) {
	base := func() policy.PushTarget {
		return policy.PushTarget{
			Current:             rev(t, shaA),
			Head:                rev(t, shaA),
			HeadBranch:          "feat/x",
			DefaultBranch:       "main",
			HeadRepo:            slug(t, "o/r"),
			OriginRepo:          slug(t, "o/r"),
			MaintainerCanModify: policy.FlagFalse,
			CrossRepo:           policy.FlagFalse,
		}
	}
	cases := []struct {
		desc   string
		mutate func(p *policy.PushTarget)
		refuse string // a substring of the refusal message, or "" to allow
	}{
		{"pushes when the tree is at the PR head revision", func(p *policy.PushTarget) {}, ""},
		{"refuses when the tree is at another revision",
			func(p *policy.PushTarget) { p.Current = rev(t, shaB) }, "but the pull request is at"},
		{"refuses when the head branch is the default",
			func(p *policy.PushTarget) { p.HeadBranch = "main" }, "is the repository default branch"},
		{"refuses when the head repo is unreadable",
			func(p *policy.PushTarget) { p.HeadRepo = core.Slug{} }, "could not determine the head repository"},
		// Both revisions unread compare equal, the way Bash's `[[ "" == "" ]]`
		// does. The first guard is a revision check, not a presence check.
		{"two unread revisions pass the revision guard", func(p *policy.PushTarget) {
			p.Current = core.Revision{}
			p.Head = core.Revision{}
		}, ""},
		{"refuses when the head repo is not the origin",
			func(p *policy.PushTarget) { p.HeadRepo = slug(t, "fork/r") }, "but this checkout pushes to"},
		{"refuses a fork without maintainer edits",
			func(p *policy.PushTarget) { p.CrossRepo = policy.FlagTrue }, "has not allowed maintainer edits"},
		{"allows a fork with maintainer edits", func(p *policy.PushTarget) {
			p.CrossRepo = policy.FlagTrue
			p.MaintainerCanModify = policy.FlagTrue
		}, ""},
		{"unreadable provenance needs permission too",
			func(p *policy.PushTarget) { p.CrossRepo = policy.FlagUnknown }, "has not allowed maintainer edits"},
		{"unreadable provenance with maintainer edits allows", func(p *policy.PushTarget) {
			p.CrossRepo = policy.FlagUnknown
			p.MaintainerCanModify = policy.FlagTrue
		}, ""},
	}
	for _, tc := range cases {
		p := base()
		tc.mutate(&p)
		err := policy.AssertPushTarget(p)
		if tc.refuse == "" {
			if err != nil {
				t.Errorf("%s: got refusal %q, want allow", tc.desc, err)
			}
			continue
		}
		if err == nil {
			t.Errorf("%s: allowed, want refusal", tc.desc)
			continue
		}
		var r *policy.Refusal
		if !errors.As(err, &r) {
			t.Errorf("%s: error is not a *policy.Refusal: %T", tc.desc, err)
			continue
		}
		if !strings.Contains(r.Message, tc.refuse) {
			t.Errorf("%s: message %q does not contain %q", tc.desc, r.Message, tc.refuse)
		}
		if r.Hint == "" {
			t.Errorf("%s: refusal carries no hint", tc.desc)
		}
	}
}

// TestAssertEnvClean transcribes lib/legs.sh:487-494 and the leakage block at
// tests/test-legs.sh:176-182. Only a non-empty value leaks.
func TestAssertEnvClean(t *testing.T) {
	cases := []struct {
		desc  string
		env   map[string]string
		names string // the leaked names the message must name, space-joined
	}{
		{"a clean environment passes the leakage check", nil, ""},
		{"an empty value is not a leak",
			map[string]string{"ANTHROPIC_BASE_URL": ""}, ""},
		{"an exported ANTHROPIC_BASE_URL is refused",
			map[string]string{"ANTHROPIC_BASE_URL": "http://leaked.example"}, "ANTHROPIC_BASE_URL"},
		{"an exported ANTHROPIC_AUTH_TOKEN is refused",
			map[string]string{"ANTHROPIC_AUTH_TOKEN": "sk-leaked"}, "ANTHROPIC_AUTH_TOKEN"},
		{"both are named in declaration order", map[string]string{
			"ANTHROPIC_AUTH_TOKEN": "sk-leaked", "ANTHROPIC_BASE_URL": "http://leaked.example",
		}, "ANTHROPIC_BASE_URL ANTHROPIC_AUTH_TOKEN"},
	}
	for _, tc := range cases {
		err := policy.AssertEnvClean(tc.env)
		if tc.names == "" {
			if err != nil {
				t.Errorf("%s: got refusal %q, want allow", tc.desc, err)
			}
			continue
		}
		if err == nil {
			t.Errorf("%s: allowed, want refusal", tc.desc)
			continue
		}
		if !strings.HasSuffix(err.Error(), ": "+tc.names) {
			t.Errorf("%s: message %q does not end with %q", tc.desc, err.Error(), tc.names)
		}
	}
}

// TestConfiguredDifference transcribes the `is_diff` block at
// tests/test-legs.sh:159-162 over lib/legs.sh:531-539.
func TestConfiguredDifference(t *testing.T) {
	cases := []struct {
		desc               string
		reviewer, resolver policy.LegSettings
		want               policy.Difference
	}{
		{"a different harness counts as configured to differ",
			policy.LegSettings{Harness: core.HarnessCodex, Endpoint: "vendor", Model: "null"},
			policy.LegSettings{Harness: core.HarnessClaude, Endpoint: "vendor", Model: "null"},
			policy.DifferenceDifferent},
		{"a different model counts",
			policy.LegSettings{Harness: core.HarnessClaude, Endpoint: "vendor", Model: "claude-fable-5"},
			policy.LegSettings{Harness: core.HarnessClaude, Endpoint: "vendor", Model: "claude-opus-5"},
			policy.DifferenceDifferent},
		{"a different endpoint counts",
			policy.LegSettings{Harness: core.HarnessClaude, Endpoint: "kimi", Model: "k3"},
			policy.LegSettings{Harness: core.HarnessClaude, Endpoint: "vendor", Model: "claude-opus-5"},
			policy.DifferenceDifferent},
		{"identical configuration is not a difference",
			policy.LegSettings{Harness: core.HarnessClaude, Endpoint: "vendor", Model: "claude-opus-5"},
			policy.LegSettings{Harness: core.HarnessClaude, Endpoint: "vendor", Model: "claude-opus-5"},
			policy.DifferenceSame},
	}
	for _, tc := range cases {
		if got := policy.ConfiguredDifference(tc.reviewer, tc.resolver); got != tc.want {
			t.Errorf("%s: ConfiguredDifference = %q, want %q", tc.desc, got, tc.want)
		}
	}
}

// TestAssertModelsDiverged transcribes tests/test-legs.sh:166-174 over
// lib/legs.sh:547-555. Absence is not a halt: it would disqualify the codex
// adapter for a field Codex does not emit.
func TestAssertModelsDiverged(t *testing.T) {
	cases := []struct {
		desc               string
		configured         policy.Difference
		reviewer, resolver string
		refuse             bool
	}{
		{"an unreported answering model does not halt the loop",
			policy.DifferenceDifferent, "claude-opus-5", "", false},
		{"the jq spelling of an absent model does not halt either",
			policy.DifferenceDifferent, "claude-opus-5", "null", false},
		// Both sides "null" is the real codex shape, not a contrived one:
		// lib/run.sh:2148 defaults both reads with jq's `// "null"` and Codex
		// reports no model_reported at all. The mixed case above never reaches
		// the absence check — the two models differ, so it returns one arm
		// earlier — so only this pairing pins it.
		{"neither leg reporting a model does not halt either",
			policy.DifferenceDifferent, "null", "null", false},
		{"nor does neither leg reporting anything at all",
			policy.DifferenceDifferent, "", "", false},
		{"two legs configured to differ but answered by one model halts",
			policy.DifferenceDifferent, "claude-opus-5", "claude-opus-5", true},
		{"one configured model answering both legs is fine",
			policy.DifferenceSame, "claude-opus-5", "claude-opus-5", false},
		{"two models that did differ pass",
			policy.DifferenceDifferent, "claude-opus-5", "gpt-5", false},
	}
	for _, tc := range cases {
		err := policy.AssertModelsDiverged(tc.configured, tc.reviewer, tc.resolver)
		if tc.refuse != (err != nil) {
			t.Errorf("%s: err = %v, want refusal %t", tc.desc, err, tc.refuse)
		}
		if tc.refuse && err != nil && !strings.Contains(err.Error(), tc.reviewer) {
			t.Errorf("%s: message %q does not name the model", tc.desc, err.Error())
		}
	}
}

// TestEndpointVariablesCannotBeShrunk pins the guard against its own caller. A
// package-level `var` would let any importer write policy.EndpointVariables =
// nil, after which AssertEnvClean approves a leaked ANTHROPIC_AUTH_TOKEN — the
// single failure the cross-model design exists to catch, switched off from
// outside the package that owns the rule.
func TestEndpointVariablesCannotBeShrunk(t *testing.T) {
	got := policy.EndpointVariables()
	want := []string{"ANTHROPIC_BASE_URL", "ANTHROPIC_AUTH_TOKEN"}
	if len(got) != len(want) {
		t.Fatalf("EndpointVariables() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("EndpointVariables() = %v, want %v", got, want)
		}
	}

	// Whatever a caller does to the slice it was handed — overwrite it, shrink
	// it — the next call gets the full list back.
	for i := range got {
		got[i] = "IRRELEVANT"
	}
	got = got[:0]
	again := policy.EndpointVariables()
	if len(again) != len(want) {
		t.Fatalf("a caller shrank the guard's list to %d entries: %v", len(again), again)
	}
	for i := range want {
		if again[i] != want[i] {
			t.Fatalf("a caller rewrote the guard's list to %v", again)
		}
	}

	// And the guard still refuses a leak of either variable.
	for _, name := range want {
		if err := policy.AssertEnvClean(map[string]string{name: "leaked"}); err == nil {
			t.Errorf("AssertEnvClean approved a leaked %s", name)
		}
	}
}

// TestPushTargetZeroCrossRepoNeedsPermission pins the Go zero value of
// CrossRepo to the require-permission branch, by leaving the field out of the
// literal rather than naming FlagUnknown.
//
// This is the opposite of the Bash default for an absent eighth argument
// (`is_cross_repo="${8-false}"` at lib/legs.sh:451, which skips the
// maintainer-edit check). Neither default ships: lib/run.sh:1910-1911 is the
// only caller and always passes all eight arguments, so the Bash default is as
// unreachable in production as this zero value is. Requiring permission is the
// safe reading of a field nobody filled in.
func TestPushTargetZeroCrossRepoNeedsPermission(t *testing.T) {
	target := policy.PushTarget{
		Current:       rev(t, shaA),
		Head:          rev(t, shaA),
		HeadBranch:    "feat/x",
		DefaultBranch: "main",
		HeadRepo:      slug(t, "o/r"),
		OriginRepo:    slug(t, "o/r"),
		// MaintainerCanModify and CrossRepo deliberately unset.
	}
	if policy.FlagUnknown != "" {
		t.Fatalf("FlagUnknown is %q, not the zero value", policy.FlagUnknown)
	}
	err := policy.AssertPushTarget(target)
	if err == nil {
		t.Fatal("an unset CrossRepo allowed the push, want a refusal")
	}
	if !strings.Contains(err.Error(), "has not allowed maintainer edits") {
		t.Errorf("refusal %q is not the maintainer-edit one", err)
	}
}
