package initcmd_test

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/initcmd"
)

// shippedColours is what GitHub answers for a repository where two labels
// already exist in the colour CrossRev mints them in.
func shippedColours() map[string]string {
	return map[string]string{
		"crossrev/stop": "cf222e",
		"bug":           "d4c5f9",
	}
}

// resolved runs Resolve and fails the test rather than returning an error, so
// the print assertions read as one block.
func resolved(t *testing.T, req initcmd.Request) initcmd.Plan {
	t.Helper()
	plan, err := initcmd.Resolve(context.Background(), req)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	return plan
}

func TestPrintPlanForAUserOwnedRepository(t *testing.T) {
	configuration := `version: 1
mode: automated
policy:
  max_passes_per_cycle: 3
reviewer:
  harness: claude
resolver:
  harness: claude
backlog:
  destination: github_issues
  github_issues:
    tracking_label: crossrev-review
    labels: [bug]
`
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".github"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".github/crossrev.yml"), []byte("stale\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	req := request(t, configuration)
	req.Files = diskFS{root: root}
	req.Pairing = livePairing{doc: descriptor(t)}
	req.GitHub = &fakeGitHub{slug: slug(t), ownerType: "User", colours: shippedColours()}
	req.Source = fakeSource{sha: "f86dbe6bb2acbb0c2e229111c3129b59365eb019", ref: "v0.5.0-32-gf86dbe6"}
	out, printed := capture()
	req.Out = out

	resolved(t, req).Print(context.Background(), req)

	want := "\n" +
		"◇  Plan for acme/widget\n" +
		"│  \n" +
		"│  GitHub App        none registered for acme — run `crossrev auth login` first\n" +
		"│  source pin        f86dbe6bb2acbb0c2e229111c3129b59365eb019\n" +
		"│                    (v0.5.0-32-gf86dbe6 — the SHA is the pin, the tag is a comment)\n" +
		"│  \n" +
		"│  runner            github-hosted\n" +
		"│                    reviewer: claude by subscription\n" +
		"│                    resolver: claude by subscription\n" +
		"│  \n" +
		"│  secrets           checked at repository level, and set only where CrossRev has the value\n" +
		"│                    APP_ID — MISSING — run `crossrev auth login` first\n" +
		"│                    APP_PRIVATE_KEY — MISSING — run `crossrev auth login` first\n" +
		"│                    CLAUDE_CODE_OAUTH_TOKEN — MISSING — crossrev runs `claude setup-token` and captures the output; the token is never printed\n" +
		"│  \n" +
		"│  labels            8 for the loop:\n" +
		"│                      create    crossrev/pass-1\n" +
		"│                      create    crossrev/pass-2\n" +
		"│                      create    crossrev/pass-3\n" +
		"│                      create    crossrev/awaiting-resolution\n" +
		"│                      create    crossrev/awaiting-review\n" +
		"│                      create    crossrev/converged\n" +
		"│                      create    crossrev/halted\n" +
		"│                      exists    crossrev/stop\n" +
		"│                    2 for filed issues:\n" +
		"│                      create    crossrev-review\n" +
		"│                      exists    bug\n" +
		"│                    init creates these up front so their colours and descriptions\n" +
		"│                    carry the loop's state; GitHub would otherwise create a missing\n" +
		"│                    label later with default metadata\n" +
		"│  \n" +
		"│  deferred work     github_issues\n" +
		"│                    named in the repository config as 'github_issues'\n" +
		"│  \n" +
		"│  write             .github/workflows/crossrev-review.yml\n" +
		"│                    .github/workflows/crossrev-resolve.yml\n" +
		"│                    .github/workflows/crossrev-watchdog.yml\n" +
		"│                    .github/crossrev.yml\n" +
		"│  \n" +
		"│  overwrites        .github/crossrev.yml\n" +
		"\n" +
		"⚠  branch protection is off on main in acme/widget\n" +
		"   The orchestrator's own push guard would be the only thing stopping a bad push. It asserts the target equals the pull request's head branch and is not the default branch, but branch protection is the backstop behind it.\n" +
		"\n"

	if printed.String() != want {
		t.Errorf("plan:\n%s\nwant:\n%s", printed.String(), want)
	}
}

func TestPrintPlanForAnOrganisationWithARefresher(t *testing.T) {
	configuration := `version: 1
mode: automated
policy:
  max_passes_per_cycle: 2
reviewer:
  harness: codex
resolver:
  harness: claude
  endpoint: kimi
endpoints:
  kimi:
    base_url: https://api.moonshot.ai/anthropic
    token_env: KIMI_API_KEY
backlog:
  destination: none
`
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".github"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".github/crossrev.yml"), []byte("stale\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	req := request(t, configuration)
	req.Files = diskFS{root: root}
	req.Pairing = livePairing{doc: descriptor(t)}
	req.GitHub = &fakeGitHub{
		slug:      slug(t),
		ownerType: "Organization",
		protected: true,
		colours:   shippedColours(),
		orgOK:     true,
		orgList:   "APP_ID\t2026-08-10T11:34:05Z",
		repoList:  "KIMI_API_KEY\t2026-08-10T11:34:06Z",
	}
	req.Apps = fakeApps{
		"acme/loop":      {Name: "crossrev-acme", ID: "123456"},
		"acme/refresher": {Name: "crossrev-acme-refresh", ID: "789"},
	}
	req.Source = fakeSource{sha: "f86dbe6bb2acbb0c2e229111c3129b59365eb019", ref: "v0.5.0-32-gf86dbe6"}
	out, printed := capture()
	req.Out = out

	resolved(t, req).Print(context.Background(), req)

	want := "\n" +
		"◇  Plan for acme/widget\n" +
		"│  \n" +
		"│  GitHub App        reuse \"crossrev-acme\" (id 123456, owner acme)\n" +
		"│  source pin        f86dbe6bb2acbb0c2e229111c3129b59365eb019\n" +
		"│                    (v0.5.0-32-gf86dbe6 — the SHA is the pin, the tag is a comment)\n" +
		"│  \n" +
		"│  runner            github-hosted\n" +
		"│                    reviewer: codex by subscription\n" +
		"│                    resolver: claude via the 'kimi' endpoint, a static token\n" +
		"│  \n" +
		"│  refresher App     needed — codex authenticates by subscription on an\n" +
		"│                    ephemeral runner, and its credential rotates, so one\n" +
		"│                    scheduled job refreshes it and the legs only read\n" +
		"│                    reuse \"crossrev-acme-refresh\" (id 789)\n" +
		"│  \n" +
		"│  secrets           checked at organisation level, and set only where CrossRev has the value\n" +
		"│                    APP_ID — already set\n" +
		"│                    APP_PRIVATE_KEY — MISSING — crossrev will set it\n" +
		"│                    CROSSREV_CODEX_AUTH — MISSING — seed once from a machine with a browser: `codex login`, then `gh secret set CROSSREV_CODEX_AUTH < ~/.codex/auth.json`\n" +
		"│                    KIMI_API_KEY — already set\n" +
		"│                    CROSSREV_REFRESH_APP_ID — MISSING — crossrev will set it\n" +
		"│                    CROSSREV_REFRESH_APP_PRIVATE_KEY — MISSING — crossrev will set it\n" +
		"│  \n" +
		"│  labels            7 for the loop:\n" +
		"│                      create    crossrev/pass-1\n" +
		"│                      create    crossrev/pass-2\n" +
		"│                      create    crossrev/awaiting-resolution\n" +
		"│                      create    crossrev/awaiting-review\n" +
		"│                      create    crossrev/converged\n" +
		"│                      create    crossrev/halted\n" +
		"│                      exists    crossrev/stop\n" +
		"│                    init creates these up front so their colours and descriptions\n" +
		"│                    carry the loop's state; GitHub would otherwise create a missing\n" +
		"│                    label later with default metadata\n" +
		"│  \n" +
		"│  deferred work     none\n" +
		"│                    named in the repository config as 'none'\n" +
		"│  \n" +
		"│  write             .github/workflows/crossrev-review.yml\n" +
		"│                    .github/workflows/crossrev-resolve.yml\n" +
		"│                    .github/workflows/crossrev-watchdog.yml\n" +
		"│                    .github/workflows/crossrev-token-refresh.yml\n" +
		"│                    .github/crossrev.yml\n" +
		"│  \n" +
		"│  overwrites        .github/crossrev.yml\n"

	if printed.String() != want {
		t.Errorf("plan:\n%s\nwant:\n%s", printed.String(), want)
	}
}

func TestPrintSaysOverwritesNoneRatherThanLeavingTheRowBlank(t *testing.T) {
	req := request(t, baseline)
	req.GitHub = &fakeGitHub{slug: slug(t), ownerType: "User", protected: true}
	out, printed := capture()
	req.Out = out

	resolved(t, req).Print(context.Background(), req)

	if !strings.Contains(printed.String(), "│  overwrites        none\n") {
		t.Errorf("plan:\n%s", printed.String())
	}
}

func TestPrintLabelInventoryStatesTheChangeItWouldMake(t *testing.T) {
	configuration := `version: 1
policy:
  max_passes_per_cycle: 1
reviewer:
  harness: claude
resolver:
  harness: claude
backlog:
  destination: github_issues
  github_issues:
    tracking_label: crossrev-review
    labels: [bug]
`
	req := request(t, configuration)
	req.GitHub = &fakeGitHub{
		slug:      slug(t),
		ownerType: "User",
		protected: true,
		colours: map[string]string{
			// The loop's own labels: one in the colour CrossRev mints
			// it in, one minted under the old single purple.
			"crossrev/converged": "1a7f37",
			"crossrev/halted":    "5319e7",
			// The repository's own taxonomy, in the repository's own
			// colour. init created it once and never repaints it, so
			// a plan promising a recolour would promise a change that
			// never comes.
			"bug": "d73a4a",
		},
	}
	out, printed := capture()
	req.Out = out

	resolved(t, req).Print(context.Background(), req)

	for _, want := range []string{
		"│                      create    crossrev/pass-1\n",
		"│                      exists    crossrev/converged\n",
		"│                      recolour  crossrev/halted\n",
		"│                      exists    bug\n",
		"│                      create    crossrev-review\n",
	} {
		if !strings.Contains(printed.String(), want) {
			t.Errorf("plan does not carry %q:\n%s", want, printed.String())
		}
	}
	if strings.Contains(printed.String(), "recolour  bug") {
		t.Error("a label the repository owns is never recoloured, so the plan must not promise one")
	}
}

func TestPrintSecretNote(t *testing.T) {
	for _, row := range []struct {
		name     string
		secret   string
		apps     fakeApps
		wantNote string
	}{
		{
			name:     "the App's secrets with no App registered",
			secret:   "APP_ID",
			apps:     fakeApps{},
			wantNote: "APP_ID — MISSING — run `crossrev auth login` first",
		},
		{
			name:     "the App's secrets with one registered",
			secret:   "APP_PRIVATE_KEY",
			apps:     fakeApps{"acme/loop": {Name: "a", ID: "1"}},
			wantNote: "APP_PRIVATE_KEY — MISSING — crossrev will set it",
		},
		{
			name:     "the refresher's secrets with no refresher App",
			secret:   "CROSSREV_REFRESH_APP_ID",
			apps:     fakeApps{},
			wantNote: "CROSSREV_REFRESH_APP_ID — MISSING — crossrev will register the refresher App and set it",
		},
		{
			name:     "the refresher's secrets with one registered",
			secret:   "CROSSREV_REFRESH_APP_PRIVATE_KEY",
			apps:     fakeApps{"acme/refresher": {Name: "a", ID: "1"}},
			wantNote: "CROSSREV_REFRESH_APP_PRIVATE_KEY — MISSING — crossrev will set it",
		},
		{
			name:     "a harness credential takes the descriptor's seed hint",
			secret:   "CROSSREV_CODEX_AUTH",
			apps:     fakeApps{},
			wantNote: "CROSSREV_CODEX_AUTH — MISSING — seed once from a machine with a browser: `codex login`, then `gh secret set CROSSREV_CODEX_AUTH < ~/.codex/auth.json`",
		},
		{
			name:     "a secret no harness claims is an endpoint's own token",
			secret:   "KIMI_API_KEY",
			apps:     fakeApps{},
			wantNote: "KIMI_API_KEY — MISSING — the token an endpoint in the config names; set it yourself with `gh secret set KIMI_API_KEY`",
		},
	} {
		t.Run(row.name, func(t *testing.T) {
			// The whole pairing, so every note in the table is
			// reached through the plan rather than through a helper
			// that was handed the secret name directly.
			configuration := `version: 1
reviewer:
  harness: codex
resolver:
  harness: claude
  endpoint: kimi
endpoints:
  kimi:
    base_url: https://api.moonshot.ai/anthropic
    token_env: KIMI_API_KEY
`
			req := request(t, configuration)
			req.Pairing = livePairing{doc: descriptor(t)}
			req.Apps = row.apps
			req.GitHub = &fakeGitHub{slug: slug(t), ownerType: "User", protected: true}
			out, printed := capture()
			req.Out = out

			resolved(t, req).Print(context.Background(), req)

			if !strings.Contains(printed.String(), row.wantNote) {
				t.Errorf("plan does not carry %q:\n%s", row.wantNote, printed.String())
			}
		})
	}
}

func TestPrintReadsTheSecretInventoryRatherThanAskingAgain(t *testing.T) {
	req := request(t, baseline)
	forge := &fakeGitHub{
		slug:      slug(t),
		ownerType: "User",
		protected: true,
		// APP_ID_OLD must not answer for APP_ID, which is why the
		// inventory is matched with an anchor and a boundary.
		repoList: "APP_ID_OLD\t2026-08-10T11:34:05Z\nAPP_PRIVATE_KEY\t2026-08-10T11:34:06Z",
	}
	req.GitHub = forge
	out, printed := capture()
	req.Out = out

	resolved(t, req).Print(context.Background(), req)

	if !strings.Contains(printed.String(), "APP_PRIVATE_KEY — already set") {
		t.Errorf("a secret that is set is reported as set:\n%s", printed.String())
	}
	if !strings.Contains(printed.String(), "APP_ID — MISSING") {
		t.Errorf("APP_ID_OLD is not APP_ID:\n%s", printed.String())
	}
	if count(forge.calls, "SecretsAtRepo") != 1 {
		t.Errorf("calls = %v, want the inventory read once for the whole run", forge.calls)
	}
}

func TestPrintWarnsOnlyWhenTheDefaultBranchIsUnprotected(t *testing.T) {
	for _, protected := range []bool{true, false} {
		req := request(t, baseline)
		forge := &fakeGitHub{slug: slug(t), ownerType: "User", branch: "trunk", protected: protected}
		req.GitHub = forge
		out, printed := capture()
		req.Out = out

		resolved(t, req).Print(context.Background(), req)

		warned := strings.Contains(printed.String(), "branch protection is off on trunk in acme/widget")
		if warned == protected {
			t.Errorf("protected = %v, warned = %v", protected, warned)
		}
		if !holds(forge.calls, "BranchProtected trunk") {
			t.Errorf("calls = %v, want the repository's own default branch asked about", forge.calls)
		}
	}
}

func TestPrintWritesNothing(t *testing.T) {
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
	forge := &fakeGitHub{slug: slug(t), ownerType: "User", colours: shippedColours()}
	req.GitHub = forge

	resolved(t, req).Print(context.Background(), req)

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
	if !holds(forge.calls, "LabelColour crossrev/stop") {
		t.Errorf("calls = %v, want the label colours read while printing", forge.calls)
	}
}
