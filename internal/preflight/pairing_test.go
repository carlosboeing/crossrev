package preflight_test

import (
	"context"
	"testing"

	"github.com/carlosboeing/crossrev/internal/config"
	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/preflight"
)

// Which harnesses are reachable in CI is a property of the runner, not of the
// config (lib/preflight.sh:188-233). Every row was measured by calling
// `preflight_pairing_supported` in the shell.
func TestPairingSupported(t *testing.T) {
	doc := document(t)
	for _, tt := range []struct {
		runner, harness, leg string
		wantOK               bool
		wantReason           string
	}{
		// Archetype A authenticates from a secret that does not rotate.
		{runner: "github-hosted", harness: "claude", wantOK: true},
		// Archetype B with a refresher is kept warm on a hosted runner.
		{runner: "github-hosted", harness: "codex", wantOK: true},
		// Archetype C with a measured provenance and a secret to carry it.
		{runner: "github-hosted", harness: "grok", wantOK: true},
		{runner: "github-hosted", harness: "opencode", wantOK: true},
		// Archetype C with no secret: nothing can seed it into a hosted runner.
		{
			runner: "github-hosted", harness: "agy", wantOK: false,
			wantReason: "Antigravity's subscription token lives about 56 minutes, and CrossRev has no way to seed it into a hosted runner yet",
		},
		// A self-hosted machine already holds the login, so the credential
		// question never arises (lib/preflight.sh:202).
		{runner: "self-hosted", harness: "agy", wantOK: true},
		{runner: "self-hosted", harness: "agy", leg: "review", wantOK: true},
		// A runner that is neither takes the credential path, because the
		// early return names self-hosted alone.
		{
			runner: "some-other-runner", harness: "agy", wantOK: false,
			wantReason: "Antigravity's subscription token lives about 56 minutes, and CrossRev has no way to seed it into a hosted runner yet",
		},
		// opencode names both legs, so both pairings pass.
		{runner: "github-hosted", harness: "opencode", leg: "review", wantOK: true},
		{runner: "github-hosted", harness: "opencode", leg: "resolve", wantOK: true},
		{runner: "self-hosted", harness: "opencode", leg: "review", wantOK: true},
		{runner: "self-hosted", harness: "opencode", leg: "resolve", wantOK: true},
		// A name with no adapter is refused on the credential path, and the
		// leg guard above it does not fire: jq supplies the default legs over
		// an empty selection, so an unknown name serves both.
		{runner: "github-hosted", harness: "bogus", wantOK: false, wantReason: "CrossRev has no adapter for 'bogus'"},
		{runner: "github-hosted", harness: "bogus", leg: "review", wantOK: false, wantReason: "CrossRev has no adapter for 'bogus'"},
		{runner: "self-hosted", harness: "bogus", wantOK: true},
		{runner: "github-hosted", harness: "gemini", wantOK: false, wantReason: "CrossRev has no adapter for 'gemini'"},
	} {
		reason, ok := preflight.PairingSupported(doc, tt.runner, tt.harness, tt.leg)
		if ok != tt.wantOK {
			t.Errorf("PairingSupported(%q, %q, %q) = %v, want %v", tt.runner, tt.harness, tt.leg, ok, tt.wantOK)
		}
		if reason != tt.wantReason {
			t.Errorf("PairingSupported(%q, %q, %q) reason = %q, want %q", tt.runner, tt.harness, tt.leg, reason, tt.wantReason)
		}
	}
}

// TestPairingSupportedNamesWhyANotDrivenHarnessHasNoAdapter: the descriptor
// carries names it deliberately does not drive, each with the reason, and the
// refusal says it rather than leaving the operator to guess why a harness they
// can run locally is missing from CI (lib/preflight.sh:204-212).
//
// kimi is the descriptor's only not_driven entry. Measured on the shell:
//
//	$ preflight_pairing_supported github-hosted kimi ""
//	CrossRev has no adapter for 'kimi' (reached through the claude adapter as a
//	named endpoint, so there is no adapter_kimi behind the name)
//	rc=1
//
// The bare form is already pinned above by `bogus`, a name the descriptor does
// not carry at all. Without this case the parenthesised half is dead: the
// branch can be replaced with the bare form and the package stays green.
func TestPairingSupportedNamesWhyANotDrivenHarnessHasNoAdapter(t *testing.T) {
	doc := document(t)
	want := "CrossRev has no adapter for 'kimi' (reached through the claude adapter " +
		"as a named endpoint, so there is no adapter_kimi behind the name)"

	for _, leg := range []string{"", "review", "resolve"} {
		reason, ok := preflight.PairingSupported(doc, "github-hosted", "kimi", leg)
		if ok {
			t.Errorf("PairingSupported(github-hosted, kimi, %q) = true, want false", leg)
		}
		if reason != want {
			t.Errorf("PairingSupported(github-hosted, kimi, %q) reason =\n%q\nwant\n%q", leg, reason, want)
		}
	}

	// The branch sits below the self-hosted return, so a machine that
	// already holds the login never reaches it (lib/preflight.sh:202).
	if reason, ok := preflight.PairingSupported(doc, "self-hosted", "kimi", ""); !ok || reason != "" {
		t.Errorf("PairingSupported(self-hosted, kimi) = (%q, %v), want (\"\", true)", reason, ok)
	}
}

// A harness that lists its legs is refused for the others on every runner,
// which is a descriptor fact rather than a runner fact (lib/preflight.sh:194-200).
//
// No harness in the shipped descriptor declares one leg and not the other, so
// the refusal is reached here with a leg name no descriptor carries. That is
// the same branch: `index($l) != null` is false for it too.
func TestPairingSupportedRefusesALegTheDescriptorDoesNotName(t *testing.T) {
	doc := document(t)
	reason, ok := preflight.PairingSupported(doc, "self-hosted", "opencode", "verify")
	if ok {
		t.Errorf("PairingSupported for an unlisted leg = true, want false")
	}
	want := "opencode is limited to the review, resolve leg, and cannot serve the verify leg"
	if reason != want {
		t.Errorf("reason = %q, want %q", reason, want)
	}

	// A harness with no `legs` key has nothing to join, so the sentence names
	// an empty set. It reads badly and it is what the shell prints.
	reason, ok = preflight.PairingSupported(doc, "github-hosted", "claude", "verify")
	if ok || reason != "Claude Code is limited to the  leg, and cannot serve the verify leg" {
		t.Errorf("PairingSupported(claude, verify) = (%q, %v)", reason, ok)
	}

	// An unknown name reaches the same branch, with neither a product name nor
	// a leg list to put in it.
	reason, ok = preflight.PairingSupported(doc, "github-hosted", "bogus", "verify")
	if ok || reason != " is limited to the  leg, and cannot serve the verify leg" {
		t.Errorf("PairingSupported(bogus, verify) = (%q, %v)", reason, ok)
	}
}

// Which secret carries a harness's subscription credential in automated mode
// (lib/preflight.sh:236-241). A harness with no secret answers false.
func TestHarnessSecret(t *testing.T) {
	doc := document(t)
	for _, tt := range []struct {
		harness, want string
		wantOK        bool
	}{
		{harness: "claude", want: "CLAUDE_CODE_OAUTH_TOKEN", wantOK: true},
		{harness: "codex", want: "CROSSREV_CODEX_AUTH", wantOK: true},
		{harness: "grok", want: "CROSSREV_GROK_AUTH", wantOK: true},
		{harness: "opencode", want: "CROSSREV_OPENCODE_AUTH", wantOK: true},
		{harness: "agy", want: "", wantOK: false},
		{harness: "bogus", want: "", wantOK: false},
	} {
		got, ok := preflight.HarnessSecret(doc, tt.harness)
		if got != tt.want || ok != tt.wantOK {
			t.Errorf("HarnessSecret(%q) = (%q, %v), want (%q, %v)", tt.harness, got, ok, tt.want, tt.wantOK)
		}
	}
}

// RUNNER_ENVIRONMENT is the only signal that separates the three environments
// CrossRev runs in; GITHUB_ACTIONS does not (lib/preflight.sh:245-251).
func TestHostedRunner(t *testing.T) {
	t.Setenv("GITHUB_ACTIONS", "true")
	t.Setenv("RUNNER_ENVIRONMENT", "")
	if preflight.HostedRunner() {
		t.Errorf("HostedRunner on a runner with no RUNNER_ENVIRONMENT = true, want false")
	}
	t.Setenv("RUNNER_ENVIRONMENT", "self-hosted")
	if preflight.HostedRunner() {
		t.Errorf("HostedRunner on a self-hosted runner = true, want false")
	}
	t.Setenv("RUNNER_ENVIRONMENT", "github-hosted")
	if !preflight.HostedRunner() {
		t.Errorf("HostedRunner on a hosted runner = false, want true")
	}
}

// Only one situation needs the single-writer refresher: a harness whose
// credential rotates, authenticating by subscription, on an ephemeral runner.
// Change any one of the three and it disappears (lib/preflight.sh:258-263).
func TestNeedsRefresher(t *testing.T) {
	doc := document(t)
	for _, tt := range []struct {
		runner, harness, endpoint string
		want                      bool
	}{
		{runner: "github-hosted", harness: "codex", endpoint: "", want: true},
		{runner: "github-hosted", harness: "claude", endpoint: "", want: false},
		{runner: "self-hosted", harness: "codex", endpoint: "", want: false},
		// A static token never rotates, so there is nothing to keep warm.
		{runner: "github-hosted", harness: "codex", endpoint: "openrouter", want: false},
		// The literal "null" is read as no endpoint, which is the `-z ... ||
		// == null` guard. cfg_get cannot produce it — it reads through `//
		// empty` — so the guard is defensive and the answer is unchanged.
		{runner: "github-hosted", harness: "codex", endpoint: "null", want: true},
		{runner: "github-hosted", harness: "bogus", endpoint: "", want: false},
	} {
		if got := preflight.NeedsRefresher(doc, tt.runner, tt.harness, tt.endpoint); got != tt.want {
			t.Errorf("NeedsRefresher(%q, %q, %q) = %v, want %v", tt.runner, tt.harness, tt.endpoint, got, tt.want)
		}
	}
}

// configFor is a Config over one repository layer, loaded the way `doctor`
// loads it: from the working tree, with no base revision.
func configFor(t *testing.T, yaml string) *config.Config {
	t.Helper()
	show := func(_ context.Context, _ core.Revision, path string) ([]byte, config.FileStatus, error) {
		if path == ".github/crossrev.yml" {
			return []byte(yaml), config.IsFile, nil
		}
		return nil, config.NotFound, nil
	}
	cfg, err := config.Load(context.Background(), core.Revision{}, show)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	return cfg
}

// One line per leg, for `crossrev doctor` and the `init` plan
// (lib/preflight.sh:266-292). Every block was measured from the shell.
func TestReportPairings(t *testing.T) {
	for _, tt := range []struct {
		name, runner, yaml string
		wantOK             bool
		want               string
	}{
		{
			name:   "the default pairing on a hosted runner",
			runner: "github-hosted",
			yaml:   "version: \"1\"\nreviewer:\n  harness: codex\nresolver:\n  harness: claude\n",
			wantOK: true,
			want: "\n◇  Pairings on runner: github-hosted\n" +
				"│  ✓ reviewer — codex by subscription, kept warm by the refresher workflow\n" +
				"│  ✓ resolver — claude by subscription\n",
		},
		{
			name:   "a named endpoint is a static token and skips the credential question",
			runner: "github-hosted",
			yaml:   "version: \"1\"\nreviewer:\n  harness: grok\n  endpoint: x\nresolver:\n  harness: claude\n",
			wantOK: true,
			want: "\n◇  Pairings on runner: github-hosted\n" +
				"│  ✓ reviewer — grok via the 'x' endpoint, a static token in a secret\n" +
				"│  ✓ resolver — claude by subscription\n",
		},
		{
			name:   "a refused leg stops the report where it failed",
			runner: "github-hosted",
			yaml:   "version: \"1\"\nreviewer:\n  harness: agy\nresolver:\n  harness: claude\n",
			wantOK: false,
			want: "\n◇  Pairings on runner: github-hosted\n" +
				"│  ✗ reviewer — agy by subscription cannot run on a github-hosted runner\n" +
				"│     Antigravity's subscription token lives about 56 minutes, and CrossRev has no way to seed it into a hosted runner yet\n" +
				"│     Fixes: set runner: self-hosted, or name a different harness for this leg.\n",
		},
		{
			name:   "self-hosted holds the login already",
			runner: "self-hosted",
			yaml:   "version: \"1\"\nreviewer:\n  harness: agy\nresolver:\n  harness: claude\n",
			wantOK: true,
			want: "\n◇  Pairings on runner: self-hosted\n" +
				"│  ✓ reviewer — agy by subscription\n" +
				"│  ✓ resolver — claude by subscription\n",
		},
		{
			name:   "a harness with no adapter is named as such",
			runner: "github-hosted",
			yaml:   "version: \"1\"\nreviewer:\n  harness: bogus\nresolver:\n  harness: claude\n",
			wantOK: false,
			want: "\n◇  Pairings on runner: github-hosted\n" +
				"│  ✗ reviewer — bogus by subscription cannot run on a github-hosted runner\n" +
				"│     CrossRev has no adapter for 'bogus'\n" +
				"│     Fixes: set runner: self-hosted, or name a different harness for this leg.\n",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			io, buf := capture()
			c := &preflight.Checker{IO: io, Harness: document(t), Config: configFor(t, tt.yaml)}
			if got := c.ReportPairings(tt.runner); got != tt.wantOK {
				t.Errorf("ReportPairings = %v, want %v", got, tt.wantOK)
			}
			if got := buf.String(); got != tt.want {
				t.Errorf("report =\n%q\nwant\n%q", got, tt.want)
			}
		})
	}
}
