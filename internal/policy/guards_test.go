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

// TestAssertPushTarget transcribes lib/legs.sh:447-479 and the `guard` block at
// tests/test-legs.sh:90-93, plus the two cross-repository arms the Bash block
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

// TestAssertEnvClean transcribes lib/legs.sh:490-497 and the leakage block at
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
// tests/test-legs.sh:154-158 over lib/legs.sh:531-539.
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

// TestAssertModelsDiverged transcribes tests/test-legs.sh:162-172 over
// lib/legs.sh:548-555. Absence is not a halt: it would disqualify the codex
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
