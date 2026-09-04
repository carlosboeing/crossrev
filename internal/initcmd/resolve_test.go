package initcmd_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/initcmd"
	"github.com/carlosboeing/crossrev/internal/ui"
)

// baseline is the configuration the rest of these tests vary: automated mode on
// a hosted runner, one harness on both legs, and no deferred work.
const baseline = `version: 1
mode: automated
policy:
  max_passes_per_cycle: 3
reviewer:
  harness: claude
resolver:
  harness: claude
backlog:
  destination: none
`

func TestResolveSettlesEveryValueBeforeAnythingIsPrinted(t *testing.T) {
	request := request(t, baseline)
	plan, err := initcmd.Resolve(context.Background(), request)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}

	if got := plan.Repo.String(); got != "acme/widget" {
		t.Errorf("repo = %q, want acme/widget", got)
	}
	if plan.Owner != "acme" {
		t.Errorf("owner = %q, want acme", plan.Owner)
	}
	if plan.OwnerType != "user" {
		t.Errorf("owner type = %q, want user", plan.OwnerType)
	}
	if plan.Runner != "github-hosted" {
		t.Errorf("runner = %q, want github-hosted", plan.Runner)
	}
	if plan.SourceSHA != strings.Repeat("a", 40) || plan.SourceRef != "v9.9.9" {
		t.Errorf("source = %q %q", plan.SourceSHA, plan.SourceRef)
	}
	wantPass := []string{"crossrev/pass-1", "crossrev/pass-2", "crossrev/pass-3"}
	if !reflect.DeepEqual(plan.PassLabels, wantPass) {
		t.Errorf("pass labels = %v, want %v", plan.PassLabels, wantPass)
	}
	// The five fixed labels, in the order lib/init.sh:109 writes them.
	wantFixed := []string{
		"crossrev/awaiting-resolution",
		"crossrev/awaiting-review",
		"crossrev/converged",
		"crossrev/halted",
		"crossrev/stop",
	}
	if !reflect.DeepEqual(plan.FixedLabels, wantFixed) {
		t.Errorf("fixed labels = %v, want %v", plan.FixedLabels, wantFixed)
	}
	if plan.BacklogLabels != "" {
		t.Errorf("backlog labels = %q, want none for a destination that is not github_issues", plan.BacklogLabels)
	}
	if plan.BacklogResolved != "none" || plan.BacklogOrigin != "named in the repository config as 'none'" {
		t.Errorf("backlog = %q / %q", plan.BacklogResolved, plan.BacklogOrigin)
	}
	wantWorkflows := []string{"review", "resolve", "watchdog"}
	if !reflect.DeepEqual(plan.Workflows, wantWorkflows) {
		t.Errorf("workflows = %v, want %v", plan.Workflows, wantWorkflows)
	}
	wantWrites := []string{
		".github/workflows/crossrev-review.yml",
		".github/workflows/crossrev-resolve.yml",
		".github/workflows/crossrev-watchdog.yml",
		".github/crossrev.yml",
	}
	if !reflect.DeepEqual(plan.Writes, wantWrites) {
		t.Errorf("writes = %v, want %v", plan.Writes, wantWrites)
	}
	if len(plan.Overwrites) != 0 {
		t.Errorf("overwrites = %v, want none against an empty working tree", plan.Overwrites)
	}
	if plan.NeedsRefresher {
		t.Error("a claude/claude pairing needs no refresher")
	}
	if plan.Config == nil {
		t.Error("the plan carries the configuration it resolved from")
	}
}

func TestResolveTakesTheRepositoryFromTheForgeWhenNoneIsNamed(t *testing.T) {
	req := request(t, baseline)
	req.Repo = core.Slug{}
	forge := req.GitHub.(*fakeGitHub)

	plan, err := initcmd.Resolve(context.Background(), req)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got := plan.Repo.String(); got != "acme/widget" {
		t.Errorf("repo = %q, want the forge's answer", got)
	}
	if forge.calls[0] != "RepoSlug" {
		t.Errorf("first forge call = %q, want RepoSlug", forge.calls[0])
	}
}

func TestResolveNeverAsksTheForgeForARepositoryItWasGiven(t *testing.T) {
	req := request(t, baseline)
	forge := req.GitHub.(*fakeGitHub)

	if _, err := initcmd.Resolve(context.Background(), req); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	for _, call := range forge.calls {
		if call == "RepoSlug" {
			t.Fatal("--repo was given, so the forge is not asked which repository this is")
		}
	}
}

func TestResolveRefusesWhenTheForgeNamesNoRepository(t *testing.T) {
	req := request(t, baseline)
	req.Repo = core.Slug{}
	req.GitHub = &fakeGitHub{}
	out, printed := capture()
	req.Out = out

	_, err := initcmd.Resolve(context.Background(), req)
	var fatal *ui.FatalError
	if !errors.As(err, &fatal) {
		t.Fatalf("err = %v, want a fatal refusal", err)
	}
	if fatal.Reason != "could not work out which repository to set up" {
		t.Errorf("reason = %q", fatal.Reason)
	}
	if fatal.Action != "Run crossrev init from a checkout with a GitHub remote, or pass --repo owner/name." {
		t.Errorf("action = %q", fatal.Action)
	}
	if !strings.Contains(printed.String(), "could not work out which repository to set up") {
		t.Errorf("the refusal was not printed:\n%s", printed.String())
	}
}

func TestResolveCarriesTheForgesOwnRefusalUp(t *testing.T) {
	req := request(t, baseline)
	req.Repo = core.Slug{}
	sentinel := errors.New("gh repo view failed")
	req.GitHub = &fakeGitHub{slugErr: sentinel}

	if _, err := initcmd.Resolve(context.Background(), req); !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want the forge's own error", err)
	}
}

func TestResolveOwnerTypeIsOrganisationOnlyWhenGitHubSaysSo(t *testing.T) {
	for _, row := range []struct {
		answer string
		want   string
	}{
		{"Organization", "organization"},
		{"ORGANIZATION", "organization"},
		{"organization", "organization"},
		{"User", "user"},
		{"Bot", "user"},
		{"", "user"},
	} {
		t.Run(row.answer, func(t *testing.T) {
			req := request(t, baseline)
			req.GitHub = &fakeGitHub{slug: slug(t), ownerType: row.answer}
			plan, err := initcmd.Resolve(context.Background(), req)
			if err != nil {
				t.Fatalf("resolve: %v", err)
			}
			if plan.OwnerType != row.want {
				t.Errorf("owner type = %q, want %q", plan.OwnerType, row.want)
			}
		})
	}
}

func TestResolveAsksAboutTheOwnerTheFlagNamed(t *testing.T) {
	req := request(t, baseline)
	req.Owner = "other-org"
	forge := req.GitHub.(*fakeGitHub)

	plan, err := initcmd.Resolve(context.Background(), req)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if plan.Owner != "other-org" {
		t.Errorf("owner = %q, want the flag's value", plan.Owner)
	}
	if !holds(forge.calls, "OwnerType other-org") {
		t.Errorf("calls = %v, want the owner type read for other-org", forge.calls)
	}
}

func TestResolveRefusesARunnerItDoesNotRecognise(t *testing.T) {
	req := request(t, strings.Replace(baseline, "version: 1\n", "version: 1\nrunner: github_hosted\n", 1))

	_, err := initcmd.Resolve(context.Background(), req)
	var fatal *ui.FatalError
	if !errors.As(err, &fatal) {
		t.Fatalf("err = %v, want a fatal refusal", err)
	}
	if fatal.Reason != "the config sets runner: github_hosted, which CrossRev does not recognise" {
		t.Errorf("reason = %q", fatal.Reason)
	}
	if !strings.HasPrefix(fatal.Action, "It must be exactly github-hosted or self-hosted.") {
		t.Errorf("action = %q", fatal.Action)
	}
}

func TestResolveAcceptsSelfHosted(t *testing.T) {
	req := request(t, strings.Replace(baseline, "version: 1\n", "version: 1\nrunner: self-hosted\n", 1))
	plan, err := initcmd.Resolve(context.Background(), req)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if plan.Runner != "self-hosted" {
		t.Errorf("runner = %q", plan.Runner)
	}
}

func TestResolveRefusesALegWhoseHarnessCannotUseItsEndpoint(t *testing.T) {
	configuration := `version: 1
reviewer:
  harness: codex
  endpoint: kimi
resolver:
  harness: claude
endpoints:
  kimi:
    base_url: https://api.moonshot.ai/anthropic
    token_env: KIMI_API_KEY
`
	req := request(t, configuration)

	_, err := initcmd.Resolve(context.Background(), req)
	var fatal *ui.FatalError
	if !errors.As(err, &fatal) {
		t.Fatalf("err = %v, want a fatal refusal", err)
	}
	if fatal.Reason != "the reviewer names the endpoint 'kimi' but runs on 'codex', which cannot use one" {
		t.Errorf("reason = %q", fatal.Reason)
	}
	want := "Named endpoints are Anthropic-compatible and reached through the claude adapter. Use harness: claude with endpoint: kimi, or drop the endpoint for this leg."
	if fatal.Action != want {
		t.Errorf("action = %q, want %q", fatal.Action, want)
	}
}

func TestResolveCarriesAnUnresolvableEndpointUp(t *testing.T) {
	configuration := `version: 1
reviewer:
  harness: claude
  endpoint: nowhere
resolver:
  harness: claude
`
	req := request(t, configuration)

	_, err := initcmd.Resolve(context.Background(), req)
	if err == nil {
		t.Fatal("an endpoint named nowhere is a hard failure")
	}
	if !strings.Contains(err.Error(), "the endpoint 'nowhere' is named in the config but defined nowhere") {
		t.Errorf("err = %v", err)
	}
}

func TestResolveRefusesAPairingTheRunnerCannotServe(t *testing.T) {
	req := request(t, baseline)
	req.Pairing = fakePairing{refuse: map[string]string{
		"claude": "Claude Code's subscription token lives about 480 minutes",
	}}

	_, err := initcmd.Resolve(context.Background(), req)
	var fatal *ui.FatalError
	if !errors.As(err, &fatal) {
		t.Fatalf("err = %v, want a fatal refusal", err)
	}
	if fatal.Reason != "the reviewer is configured to run claude by subscription, and a github-hosted runner cannot serve that" {
		t.Errorf("reason = %q", fatal.Reason)
	}
	want := "Claude Code's subscription token lives about 480 minutes. Two fixes: set runner: self-hosted in the config, where every harness refreshes its own credential on disk; or name a different harness for this leg."
	if fatal.Action != want {
		t.Errorf("action = %q, want %q", fatal.Action, want)
	}
}

func TestResolveAsksThePairingInTheDescriptorsVocabulary(t *testing.T) {
	// The config says reviewer and resolver; the descriptor says review and
	// resolve. Without the translation a review-only harness passes on its
	// credential archetype and init installs workflows that die on every
	// resolve.
	req := request(t, baseline)
	req.Pairing = fakePairing{legs: map[string]string{
		"claude/resolve": "claude is limited to the review leg, and cannot serve the resolve leg",
	}}

	_, err := initcmd.Resolve(context.Background(), req)
	var fatal *ui.FatalError
	if !errors.As(err, &fatal) {
		t.Fatalf("err = %v, want a fatal refusal", err)
	}
	if !strings.HasPrefix(fatal.Reason, "the resolver is configured to run claude") {
		t.Errorf("reason = %q, want the resolver refused rather than the reviewer", fatal.Reason)
	}
}

func TestResolveNeverAsksThePairingAboutALegOnAnEndpoint(t *testing.T) {
	// An endpoint means a static token in a secret, which never rotates and
	// so never cares what kind of runner it is on.
	configuration := `version: 1
reviewer:
  harness: claude
  endpoint: kimi
resolver:
  harness: claude
  endpoint: kimi
endpoints:
  kimi:
    base_url: https://api.moonshot.ai/anthropic
    token_env: KIMI_API_KEY
`
	req := request(t, configuration)
	req.Pairing = fakePairing{refuse: map[string]string{"claude": "would refuse every leg"}}

	if _, err := initcmd.Resolve(context.Background(), req); err != nil {
		t.Fatalf("resolve: %v", err)
	}
}

func TestResolvePassLabelsFollowMaxPassesPerCycle(t *testing.T) {
	req := request(t, strings.Replace(baseline, "max_passes_per_cycle: 3", "max_passes_per_cycle: 1", 1))
	plan, err := initcmd.Resolve(context.Background(), req)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !reflect.DeepEqual(plan.PassLabels, []string{"crossrev/pass-1"}) {
		t.Errorf("pass labels = %v", plan.PassLabels)
	}
}

func TestResolveBacklogOriginSaysWhereTheAnswerCameFrom(t *testing.T) {
	t.Run("named in the config", func(t *testing.T) {
		req := request(t, strings.Replace(baseline, "destination: none", "destination: github_issues", 1))
		plan, err := initcmd.Resolve(context.Background(), req)
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if plan.BacklogResolved != "github_issues" {
			t.Errorf("resolved = %q", plan.BacklogResolved)
		}
		if plan.BacklogOrigin != "named in the repository config as 'github_issues'" {
			t.Errorf("origin = %q", plan.BacklogOrigin)
		}
	})

	t.Run("read out of the Project Map", func(t *testing.T) {
		req := request(t, strings.Replace(baseline, "destination: none", "destination: auto", 1))
		req.Show = showing(map[string]string{
			".github/crossrev.yml": strings.Replace(baseline, "destination: none", "destination: auto", 1),
			"AGENTS.md":            "## Project Map\n\n- **Tracker**: GitHub Issues\n",
		})
		plan, err := initcmd.Resolve(context.Background(), req)
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if plan.BacklogResolved != "github_issues" {
			t.Errorf("resolved = %q", plan.BacklogResolved)
		}
		if plan.BacklogOrigin != "resolved from the ## Project Map section's Tracker field" {
			t.Errorf("origin = %q", plan.BacklogOrigin)
		}
	})

	t.Run("sniffed", func(t *testing.T) {
		req := request(t, strings.Replace(baseline, "destination: none", "destination: auto", 1))
		plan, err := initcmd.Resolve(context.Background(), req)
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if plan.BacklogOrigin != "resolved by sniffing for a convention already in use" {
			t.Errorf("origin = %q", plan.BacklogOrigin)
		}
	})
}

func TestResolveBacklogLabelsAreTheTrackingLabelAndTheExtras(t *testing.T) {
	configuration := `version: 1
reviewer:
  harness: claude
resolver:
  harness: claude
backlog:
  destination: github_issues
  github_issues:
    tracking_label: crossrev-review
    labels: [bug, chore]
`
	req := request(t, configuration)
	plan, err := initcmd.Resolve(context.Background(), req)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if plan.BacklogLabels != "crossrev-review bug chore" {
		t.Errorf("backlog labels = %q", plan.BacklogLabels)
	}
}

func TestResolveBacklogLabelsSqueezeTheGapAnEmptyListLeaves(t *testing.T) {
	// `printf '%s %s' … | tr -s ' ' | sed 's/ *$//'` at lib/init.sh:130. An
	// empty extras list leaves a trailing space, which the trim removes.
	configuration := `version: 1
reviewer:
  harness: claude
resolver:
  harness: claude
backlog:
  destination: github_issues
  github_issues:
    tracking_label: crossrev-review
    labels: []
`
	req := request(t, configuration)
	plan, err := initcmd.Resolve(context.Background(), req)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if plan.BacklogLabels != "crossrev-review" {
		t.Errorf("backlog labels = %q, want no trailing space", plan.BacklogLabels)
	}
}

func TestResolveKeepsTheLeadingGapAnEmptyTrackingLabelLeaves(t *testing.T) {
	// `tr -s` squeezes a repeated space; one leading space is not repeated,
	// and `sed 's/ *$//'` only trims the end. So the value is " bug", which
	// is not empty — and the plan prints a filed-issues block for it.
	configuration := `version: 1
reviewer:
  harness: claude
resolver:
  harness: claude
backlog:
  destination: github_issues
  github_issues:
    tracking_label: ""
    labels: [bug]
`
	req := request(t, configuration)
	plan, err := initcmd.Resolve(context.Background(), req)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if plan.BacklogLabels != " bug" {
		t.Errorf("backlog labels = %q, want the leading gap kept", plan.BacklogLabels)
	}
}

func TestResolveAddsTheRefresherWorkflowOnlyWhenThePairingNeedsOne(t *testing.T) {
	req := request(t, baseline)
	req.Pairing = fakePairing{refresher: map[string]bool{"claude": true}}

	plan, err := initcmd.Resolve(context.Background(), req)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !plan.NeedsRefresher {
		t.Fatal("the pairing needs a refresher")
	}
	want := []string{"review", "resolve", "watchdog", "token-refresh"}
	if !reflect.DeepEqual(plan.Workflows, want) {
		t.Errorf("workflows = %v, want %v", plan.Workflows, want)
	}
	if plan.Writes[3] != ".github/workflows/crossrev-token-refresh.yml" {
		t.Errorf("writes = %v, want the refresher workflow before the policy file", plan.Writes)
	}
}

func TestResolveFlagsEveryFileItWouldOverwrite(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".github/workflows"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for _, path := range []string{".github/crossrev.yml", ".github/workflows/crossrev-resolve.yml"} {
		if err := os.WriteFile(filepath.Join(root, path), []byte("stale\n"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	req := request(t, baseline)
	req.Files = diskFS{root: root}

	plan, err := initcmd.Resolve(context.Background(), req)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	want := []string{".github/workflows/crossrev-resolve.yml", ".github/crossrev.yml"}
	if !reflect.DeepEqual(plan.Overwrites, want) {
		t.Errorf("overwrites = %v, want %v in the order the writes are listed", plan.Overwrites, want)
	}
}

func TestResolveRefusesWithoutASourceSHA(t *testing.T) {
	req := request(t, baseline)
	req.Source = fakeSource{shaErr: errors.New("not a git checkout"), ref: "v1"}

	_, err := initcmd.Resolve(context.Background(), req)
	var fatal *ui.FatalError
	if !errors.As(err, &fatal) {
		t.Fatalf("err = %v, want a fatal refusal", err)
	}
	if fatal.Reason != "could not work out which commit of CrossRev to pin the workflows to" {
		t.Errorf("reason = %q", fatal.Reason)
	}
	if fatal.Action != "init generates workflows that pin the action at a 40-character SHA. Run it from a git checkout of CrossRev." {
		t.Errorf("action = %q", fatal.Action)
	}
}

func TestResolveCallsAnUndescribableCheckoutUntagged(t *testing.T) {
	req := request(t, baseline)
	req.Source = fakeSource{sha: strings.Repeat("b", 40), refErr: errors.New("no tags")}

	plan, err := initcmd.Resolve(context.Background(), req)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if plan.SourceRef != "untagged" {
		t.Errorf("ref = %q, want untagged", plan.SourceRef)
	}
}

func TestRequiredSecrets(t *testing.T) {
	endpoints := `
endpoints:
  kimi:
    base_url: https://api.moonshot.ai/anthropic
    token_env: KIMI_API_KEY
`
	for _, row := range []struct {
		name          string
		configuration string
		pairing       fakePairing
		want          []string
	}{
		{
			name:          "a hosted runner needs a credential for each subscription harness",
			configuration: "version: 1\nreviewer:\n  harness: codex\nresolver:\n  harness: claude\n",
			pairing:       fakePairing{secrets: map[string]string{"codex": "CROSSREV_CODEX_AUTH", "claude": "CLAUDE_CODE_OAUTH_TOKEN"}},
			want:          []string{"APP_ID", "APP_PRIVATE_KEY", "CROSSREV_CODEX_AUTH", "CLAUDE_CODE_OAUTH_TOKEN"},
		},
		{
			name:          "one harness on both legs is named once",
			configuration: "version: 1\nreviewer:\n  harness: claude\nresolver:\n  harness: claude\n",
			pairing:       fakePairing{secrets: map[string]string{"claude": "CLAUDE_CODE_OAUTH_TOKEN"}},
			want:          []string{"APP_ID", "APP_PRIVATE_KEY", "CLAUDE_CODE_OAUTH_TOKEN"},
		},
		{
			name:          "a self-hosted runner needs none of them",
			configuration: "version: 1\nrunner: self-hosted\nreviewer:\n  harness: codex\nresolver:\n  harness: claude\n",
			pairing:       fakePairing{secrets: map[string]string{"codex": "CROSSREV_CODEX_AUTH", "claude": "CLAUDE_CODE_OAUTH_TOKEN"}},
			want:          []string{"APP_ID", "APP_PRIVATE_KEY"},
		},
		{
			name:          "a harness with no secret contributes none",
			configuration: "version: 1\nreviewer:\n  harness: claude\nresolver:\n  harness: claude\n",
			pairing:       fakePairing{},
			want:          []string{"APP_ID", "APP_PRIVATE_KEY"},
		},
		{
			name:          "an endpoint names its own token variable",
			configuration: "version: 1\nreviewer:\n  harness: claude\n  endpoint: kimi\nresolver:\n  harness: claude\n" + endpoints,
			pairing:       fakePairing{secrets: map[string]string{"claude": "CLAUDE_CODE_OAUTH_TOKEN"}},
			want:          []string{"APP_ID", "APP_PRIVATE_KEY", "KIMI_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN"},
		},
		{
			name:          "an endpoint on a self-hosted runner still needs its token",
			configuration: "version: 1\nrunner: self-hosted\nreviewer:\n  harness: claude\n  endpoint: kimi\nresolver:\n  harness: claude\n" + endpoints,
			pairing:       fakePairing{secrets: map[string]string{"claude": "CLAUDE_CODE_OAUTH_TOKEN"}},
			want:          []string{"APP_ID", "APP_PRIVATE_KEY", "KIMI_API_KEY"},
		},
		{
			name:          "two legs on one endpoint name its token once",
			configuration: "version: 1\nreviewer:\n  harness: claude\n  endpoint: kimi\nresolver:\n  harness: claude\n  endpoint: kimi\n" + endpoints,
			pairing:       fakePairing{secrets: map[string]string{"claude": "CLAUDE_CODE_OAUTH_TOKEN"}},
			want:          []string{"APP_ID", "APP_PRIVATE_KEY", "KIMI_API_KEY"},
		},
		{
			name:          "a refresher adds its own App",
			configuration: "version: 1\nreviewer:\n  harness: codex\nresolver:\n  harness: claude\n",
			pairing: fakePairing{
				secrets:   map[string]string{"codex": "CROSSREV_CODEX_AUTH", "claude": "CLAUDE_CODE_OAUTH_TOKEN"},
				refresher: map[string]bool{"codex": true},
			},
			want: []string{
				"APP_ID", "APP_PRIVATE_KEY", "CROSSREV_CODEX_AUTH", "CLAUDE_CODE_OAUTH_TOKEN",
				"CROSSREV_REFRESH_APP_ID", "CROSSREV_REFRESH_APP_PRIVATE_KEY",
			},
		},
	} {
		t.Run(row.name, func(t *testing.T) {
			req := request(t, row.configuration)
			req.Pairing = row.pairing
			plan, err := initcmd.Resolve(context.Background(), req)
			if err != nil {
				t.Fatalf("resolve: %v", err)
			}
			if !reflect.DeepEqual(plan.Secrets, row.want) {
				t.Errorf("secrets = %v, want %v", plan.Secrets, row.want)
			}
		})
	}
}

func TestResolveReadsTheRepositorySecretsOnceAndTheOrganisationOnlyForOne(t *testing.T) {
	t.Run("a user-owned repository is read once", func(t *testing.T) {
		req := request(t, baseline)
		forge := req.GitHub.(*fakeGitHub)
		forge.repoList = "APP_ID\t2026-08-10T11:34:05Z"

		plan, err := initcmd.Resolve(context.Background(), req)
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if count(forge.calls, "SecretsAtRepo") != 1 {
			t.Errorf("calls = %v, want one repository read", forge.calls)
		}
		if holds(forge.calls, "SecretsAtOrg acme") {
			t.Errorf("calls = %v, want no organisation read for a user", forge.calls)
		}
		if !strings.Contains(plan.SecretInventory, "APP_ID") {
			t.Errorf("inventory = %q", plan.SecretInventory)
		}
	})

	t.Run("an organisation is read as well, and both are kept", func(t *testing.T) {
		req := request(t, baseline)
		req.GitHub = &fakeGitHub{
			slug:      slug(t),
			ownerType: "Organization",
			orgList:   "ORG_LEVEL\t2026-08-10T11:34:05Z",
			orgOK:     true,
			repoList:  "REPO_LEVEL\t2026-08-10T11:34:06Z",
		}
		plan, err := initcmd.Resolve(context.Background(), req)
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if plan.SecretInventory != "ORG_LEVEL\t2026-08-10T11:34:05Z\nREPO_LEVEL\t2026-08-10T11:34:06Z" {
			t.Errorf("inventory = %q, want the organisation list then the repository list", plan.SecretInventory)
		}
	})

	t.Run("an organisation read that fails is not a fault", func(t *testing.T) {
		req := request(t, baseline)
		req.GitHub = &fakeGitHub{
			slug:      slug(t),
			ownerType: "Organization",
			orgOK:     false,
			repoList:  "REPO_LEVEL\t2026-08-10T11:34:06Z",
		}
		plan, err := initcmd.Resolve(context.Background(), req)
		if err != nil {
			t.Fatalf("a login without admin:org is a permission state, not a fault: %v", err)
		}
		if plan.SecretInventory != "\nREPO_LEVEL\t2026-08-10T11:34:06Z" {
			t.Errorf("inventory = %q", plan.SecretInventory)
		}
	})
}

func TestResolveRefusesWhenTheRepositorySecretsCannotBeRead(t *testing.T) {
	// A failed read would report every secret missing, in the one place an
	// operator decides whether to hand this command a live repository.
	req := request(t, baseline)
	req.GitHub = &fakeGitHub{
		slug:      slug(t),
		ownerType: "User",
		repoList:  "HTTP 404: Not Found",
		repoErr:   errors.New("exit status 1"),
	}

	_, err := initcmd.Resolve(context.Background(), req)
	var fatal *ui.FatalError
	if !errors.As(err, &fatal) {
		t.Fatalf("err = %v, want a fatal refusal", err)
	}
	if fatal.Reason != "could not read the secrets on acme/widget" {
		t.Errorf("reason = %q", fatal.Reason)
	}
	if !strings.HasSuffix(fatal.Action, "GitHub said: HTTP 404: Not Found") {
		t.Errorf("action = %q, want GitHub's own words quoted", fatal.Action)
	}
}

func TestResolveRefusesAWiringFaultRatherThanFillingItIn(t *testing.T) {
	for _, row := range []struct {
		name string
		bend func(*initcmd.Request)
		want string
	}{
		{"Show", func(r *initcmd.Request) { r.Show = nil }, "Show"},
		{"GitHub", func(r *initcmd.Request) { r.GitHub = nil }, "GitHub"},
		{"Apps", func(r *initcmd.Request) { r.Apps = nil }, "Apps"},
		{"Pairing", func(r *initcmd.Request) { r.Pairing = nil }, "Pairing"},
		{"Source", func(r *initcmd.Request) { r.Source = nil }, "Source"},
		{"Files", func(r *initcmd.Request) { r.Files = nil }, "Files"},
	} {
		t.Run(row.name, func(t *testing.T) {
			req := request(t, baseline)
			row.bend(&req)
			_, err := initcmd.Resolve(context.Background(), req)
			if err == nil {
				t.Fatalf("a nil %s is a wiring fault, not a default", row.want)
			}
			if !strings.Contains(err.Error(), row.want) {
				t.Errorf("err = %v, want it to name %s", err, row.want)
			}
		})
	}
}

func TestResolveWritesNothing(t *testing.T) {
	// The plan gate is only a gate if nothing has happened by the time it is
	// printed. The working tree is compared before and after, and every forge
	// call is checked against the read-only set.
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".github/workflows"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".github/crossrev.yml"), []byte("stale\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	before := tree(t, root)

	req := request(t, baseline)
	req.Files = diskFS{root: root}
	forge := req.GitHub.(*fakeGitHub)

	if _, err := initcmd.Resolve(context.Background(), req); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if after := tree(t, root); !reflect.DeepEqual(before, after) {
		t.Errorf("the working tree changed:\nbefore %v\nafter  %v", before, after)
	}
	reads := map[string]bool{
		"RepoSlug": true, "OwnerType": true, "DefaultBranch": true,
		"BranchProtected": true, "LabelColour": true,
		"SecretsAtOrg": true, "SecretsAtRepo": true,
	}
	for _, call := range forge.calls {
		name, _, _ := strings.Cut(call, " ")
		if !reads[name] {
			t.Errorf("forge call %q is not a read", call)
		}
	}
	if len(forge.calls) == 0 {
		t.Error("no forge call was recorded at all, so this proves nothing")
	}
}

// slices reports whether values holds want.
func holds(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// count is how many times values holds want.
func count(values []string, want string) int {
	total := 0
	for _, value := range values {
		if value == want {
			total++
		}
	}
	return total
}
