package initcmd_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/forge"
	"github.com/carlosboeing/crossrev/internal/initcmd"
)

// refusingFiles fails the test if anything asks it to write.
type refusingFiles struct{ t *testing.T }

func (f refusingFiles) MkdirAll(path string) error {
	f.t.Errorf("a directory was created: %s", path)
	return nil
}

func (f refusingFiles) WriteFile(path string, _ []byte) error {
	f.t.Errorf("a file was written: %s", path)
	return nil
}

// refusingLabels fails the test if anything asks it to declare a label.
type refusingLabels struct{ t *testing.T }

func (l refusingLabels) LabelEnsure(context.Context, core.Slug, forge.Label) (forge.LabelState, error) {
	l.t.Error("a label was declared")
	return forge.LabelExists, nil
}

// failingFiles refuses every write with an error the caller has to carry up.
type failingFiles struct{ err error }

func (f failingFiles) MkdirAll(string) error          { return f.err }
func (f failingFiles) WriteFile(string, []byte) error { return f.err }

// wired is an Execution with every port answering and nothing failing.
func wired(t *testing.T, root string) initcmd.Execution {
	t.Helper()
	return initcmd.Execution{
		Labels:   &fakeLabels{},
		Secrets:  &initcmd.SecretStore{Runner: &runRecorder{}},
		Keys:     fakeKeys{path: "/apps"},
		Register: &fakeRegistrar{apps: fakeApps{}},
		Tokens:   &fakeTokens{},
		Files:    initcmd.Dir(root),
	}
}

// TestWriteFilesWritesEveryWorkflowAndThePolicy is the Files section, byte for
// byte, and the files it left behind (lib/init.sh:564-589).
func TestWriteFilesWritesEveryWorkflowAndThePolicy(t *testing.T) {
	root := t.TempDir()
	plan, req, _, buffer := planned(t, issueSinkConfig, func(r *initcmd.Request) {
		r.Files = initcmd.Dir(root)
	})
	if err := plan.WriteFiles(context.Background(), req, wired(t, root)); err != nil {
		t.Fatalf("WriteFiles: %v", err)
	}

	want := "\n◇  Files\n" +
		"│  ✓ wrote .github/workflows/crossrev-review.yml\n" +
		"│  ✓ wrote .github/workflows/crossrev-resolve.yml\n" +
		"│  ✓ wrote .github/workflows/crossrev-watchdog.yml\n" +
		"│  ✓ wrote .github/crossrev.yml, with deferred work resolved to github_issues\n"
	if got := buffer.String(); got != want {
		t.Errorf("the Files block is\n%q\nwant\n%q", got, want)
	}

	review := readUnder(t, root, ".github/workflows/crossrev-review.yml")
	if want := string(plan.RenderWorkflow(req, initcmd.ReviewWorkflowTemplate())); review != want {
		t.Error("the review workflow on disk is not what the renderer produced")
	}
	// Against the shell's own bytes rather than against WriteConfig, which
	// would agree with itself whatever it appended. issueSinkConfig is the
	// pairing testdata/config/github-issues.yml was rendered for.
	policy := readUnder(t, root, ".github/crossrev.yml")
	if want := golden(t, "github-issues.yml"); policy != want {
		t.Errorf("the policy file on disk differs from the shell's:\n%s", firstDifference(policy, want))
	}
	if strings.Contains(review, "__RUNS_ON__") {
		t.Error("a placeholder survived into the file that was written")
	}
}

// TestWriteFilesGivesTheFilesTheModesTheShellGivesThem: `mkdir -p` creates 0777
// and `>` creates 0666, each against the process umask, so a repository ends up
// with the same permissions either implementation put there.
func TestWriteFilesGivesTheFilesTheModesTheShellGivesThem(t *testing.T) {
	root := t.TempDir()
	plan, req, _, _ := planned(t, issueSinkConfig, func(r *initcmd.Request) {
		r.Files = initcmd.Dir(root)
	})
	if err := plan.WriteFiles(context.Background(), req, wired(t, root)); err != nil {
		t.Fatalf("WriteFiles: %v", err)
	}

	// The umask is the process's and a test may not change it, so the
	// comparison is against a control the test made with the modes the shell
	// asks for. A port that asked for 0600 or 0755 would differ from it.
	control := filepath.Join(root, "control")
	if err := os.Mkdir(control, 0o777); err != nil {
		t.Fatalf("make the control directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "control.txt"), nil, 0o666); err != nil {
		t.Fatalf("write the control file: %v", err)
	}
	if got, want := perm(t, filepath.Join(root, ".github", "workflows")), perm(t, control); got != want {
		t.Errorf("the workflows directory is %v, want %v", got, want)
	}
	if got, want := perm(t, filepath.Join(root, ".github", "crossrev.yml")), perm(t, filepath.Join(root, "control.txt")); got != want {
		t.Errorf("the policy file is %v, want %v", got, want)
	}
}

// TestWriteFilesWritesTheRefresherWorkflowOnlyForAPairingThatNeedsOne
// (lib/init.sh:149-150 and :568-572).
func TestWriteFilesWritesTheRefresherWorkflowOnlyForAPairingThatNeedsOne(t *testing.T) {
	root := t.TempDir()
	plan, req, _, buffer := planned(t, hostedPairing("codex", "claude"), func(r *initcmd.Request) {
		r.Pairing = livePairing{doc: descriptor(t)}
		r.Files = initcmd.Dir(root)
	})
	if err := plan.WriteFiles(context.Background(), req, wired(t, root)); err != nil {
		t.Fatalf("WriteFiles: %v", err)
	}
	if !strings.Contains(buffer.String(), "│  ✓ wrote .github/workflows/crossrev-token-refresh.yml\n") {
		t.Errorf("the refresher workflow was not written:\n%s", buffer.String())
	}
	entries, err := os.ReadDir(filepath.Join(root, ".github", "workflows"))
	if err != nil {
		t.Fatalf("read the workflows directory: %v", err)
	}
	if len(entries) != 4 {
		t.Errorf("%d workflows on disk, want 4", len(entries))
	}
}

// TestWriteFilesWarnsAboutARefresherWorkflowNothingNeedsAnyMore: it stays on its
// schedule and fails every run once the credential it reads is gone
// (lib/init.sh:576-579).
func TestWriteFilesWarnsAboutARefresherWorkflowNothingNeedsAnyMore(t *testing.T) {
	root := t.TempDir()
	stale := filepath.Join(root, ".github", "workflows", "crossrev-token-refresh.yml")
	if err := os.MkdirAll(filepath.Dir(stale), 0o777); err != nil {
		t.Fatalf("make the workflows directory: %v", err)
	}
	if err := os.WriteFile(stale, []byte("stale\n"), 0o666); err != nil {
		t.Fatalf("write the stale workflow: %v", err)
	}
	plan, req, _, buffer := planned(t, issueSinkConfig, func(r *initcmd.Request) {
		r.Files = initcmd.Dir(root)
	})
	if err := plan.WriteFiles(context.Background(), req, wired(t, root)); err != nil {
		t.Fatalf("WriteFiles: %v", err)
	}
	want := "\n⚠  this configuration needs no refresher, but .github/workflows/crossrev-token-refresh.yml is still there\n" +
		"   It stays on its schedule and fails every run once the credential it reads is gone. Delete it, and remove the refresher App's secrets, if the pairing is not going back.\n\n"
	if !strings.Contains(buffer.String(), want) {
		t.Errorf("the block is\n%q\nand does not carry\n%q", buffer.String(), want)
	}
	if got := readUnder(t, root, ".github/workflows/crossrev-token-refresh.yml"); got != "stale\n" {
		t.Error("the leftover workflow was rewritten rather than reported")
	}
}

// TestWriteFilesLeavesThePolicyAloneOnUpgrade: --upgrade regenerates workflows
// from the installed version, so drift across repositories is handled by
// regeneration rather than by hand-editing every copy. It deliberately leaves
// the policy file alone (lib/init.sh:581-589).
func TestWriteFilesLeavesThePolicyAloneOnUpgrade(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".github"), 0o777); err != nil {
		t.Fatalf("make .github: %v", err)
	}
	handEdited := "version: 1\npolicy:\n  max_passes_per_cycle: 7\n"
	if err := os.WriteFile(filepath.Join(root, ".github", "crossrev.yml"), []byte(handEdited), 0o666); err != nil {
		t.Fatalf("write the policy: %v", err)
	}
	plan, req, _, buffer := planned(t, issueSinkConfig, func(r *initcmd.Request) {
		r.Upgrade = true
		r.Files = initcmd.Dir(root)
	})
	if err := plan.WriteFiles(context.Background(), req, wired(t, root)); err != nil {
		t.Fatalf("WriteFiles: %v", err)
	}
	if !strings.Contains(buffer.String(), "  left .github/crossrev.yml alone — --upgrade regenerates workflows, not policy\n") {
		t.Errorf("the block is\n%s", buffer.String())
	}
	if got := readUnder(t, root, ".github/crossrev.yml"); got != handEdited {
		t.Errorf("the hand-edited policy was rewritten:\n%s", got)
	}
	if got := readUnder(t, root, ".github/workflows/crossrev-review.yml"); !strings.Contains(got, "name: crossrev review") {
		t.Error("--upgrade did not regenerate the workflows")
	}
}

// TestWriteFilesWritesThePolicyOnUpgradeWhenThereIsNoneToLeaveAlone: the guard
// is `--upgrade AND the file is there`, so a repository with no policy still
// gets one (lib/init.sh:581).
func TestWriteFilesWritesThePolicyOnUpgradeWhenThereIsNoneToLeaveAlone(t *testing.T) {
	root := t.TempDir()
	plan, req, _, buffer := planned(t, issueSinkConfig, func(r *initcmd.Request) {
		r.Upgrade = true
		r.Files = initcmd.Dir(root)
	})
	if err := plan.WriteFiles(context.Background(), req, wired(t, root)); err != nil {
		t.Fatalf("WriteFiles: %v", err)
	}
	if !strings.Contains(buffer.String(), "│  ✓ wrote .github/crossrev.yml, with deferred work resolved to github_issues\n") {
		t.Errorf("the block is\n%s", buffer.String())
	}
}

// TestWriteFilesCarriesAFailedWriteUp.
func TestWriteFilesCarriesAFailedWriteUp(t *testing.T) {
	root := t.TempDir()
	plan, req, _, _ := planned(t, issueSinkConfig, func(r *initcmd.Request) {
		r.Files = initcmd.Dir(root)
	})
	ex := wired(t, root)
	ex.Files = failingFiles{err: errors.New("read-only file system")}
	err := plan.WriteFiles(context.Background(), req, ex)
	if err == nil || !strings.Contains(err.Error(), "read-only file system") {
		t.Fatalf("err = %v, want the file system's own failure", err)
	}
}

// TestReportUnfinishedNamesWhatEachMissingSecretCosts is the closing section,
// byte for byte, for the three shapes a missing secret takes
// (lib/init.sh:591-650).
func TestReportUnfinishedNamesWhatEachMissingSecretCosts(t *testing.T) {
	t.Run("nothing outstanding", func(t *testing.T) {
		plan, req, _, buffer := planned(t, issueSinkConfig, nil)
		plan.ReportUnfinished(req, nil)
		want := "\n◇  Still needed\n" +
			"│  ✓ nothing — open a pull request and the loop runs\n" +
			"└  Watch it with: crossrev status --pr <number>\n\n"
		if got := buffer.String(); got != want {
			t.Errorf("got\n%q\nwant\n%q", got, want)
		}
	})

	t.Run("an archetype A token", func(t *testing.T) {
		plan, req, _, buffer := planned(t, hostedPairing("claude", "claude"), func(r *initcmd.Request) {
			r.Pairing = livePairing{doc: descriptor(t)}
		})
		plan.ReportUnfinished(req, []string{"APP_ID", "APP_PRIVATE_KEY", "CLAUDE_CODE_OAUTH_TOKEN"})
		want := "\n◇  Still needed\n" +
			"│  ✗ CLAUDE_CODE_OAUTH_TOKEN — a leg runs on Claude Code, so it cannot authenticate\n" +
			"│     claude setup-token\n" +
			"│     gh secret set CLAUDE_CODE_OAUTH_TOKEN --repo acme/widget\n" +
			"│  \n" +
			"│     That token is valid for a year and the command will not show it\n" +
			"│     again, so put it in the secret in the same sitting. Re-run\n" +
			"│     `crossrev init` from a terminal and it does both, and records the\n" +
			"│     date so `crossrev auth status` can warn as the year closes.\n" +
			"└  The workflows are installed but will fail at the first missing secret, before any review runs.\n\n"
		if got := buffer.String(); got != want {
			t.Errorf("got\n%q\nwant\n%q", got, want)
		}
	})

	t.Run("an archetype B token and the refresher App", func(t *testing.T) {
		plan, req, _, buffer := planned(t, hostedPairing("codex", "claude"), func(r *initcmd.Request) {
			r.Pairing = livePairing{doc: descriptor(t)}
		})
		plan.ReportUnfinished(req, []string{
			"CROSSREV_REFRESH_APP_ID", "CROSSREV_REFRESH_APP_PRIVATE_KEY", "CROSSREV_CODEX_AUTH",
		})
		want := "\n◇  Still needed\n" +
			"│  ✗ CROSSREV_REFRESH_APP_ID — without the refresher App, codex's credential expires and stays expired\n" +
			"│     crossrev auth login --owner acme --role refresher\n" +
			"│     crossrev init --upgrade\n" +
			"│  ✗ CROSSREV_REFRESH_APP_PRIVATE_KEY — without the refresher App, codex's credential expires and stays expired\n" +
			"│     crossrev auth login --owner acme --role refresher\n" +
			"│     crossrev init --upgrade\n" +
			"│  ✗ CROSSREV_CODEX_AUTH — a leg runs on Codex, so it cannot authenticate\n" +
			"│     codex login          # on a machine with a browser\n" +
			"│     gh secret set CROSSREV_CODEX_AUTH --repo acme/widget < ~/.codex/auth.json\n" +
			"│  \n" +
			"│     Repository-scoped, not organisation-scoped, even on an org.\n" +
			"│     Concurrency groups do not span repositories, so an org-level\n" +
			"│     copy is refreshed by every repository reading it and the first\n" +
			"│     one to refresh invalidates it for all the rest.\n" +
			"│  \n" +
			"│     Seeded once. From then on the refresher workflow is the only\n" +
			"│     thing that writes it, because using a refresh token consumes it\n" +
			"│     and a second writer kills the chain for everyone.\n" +
			"└  The workflows are installed but will fail at the first missing secret, before any review runs.\n\n"
		if got := buffer.String(); got != want {
			t.Errorf("got\n%q\nwant\n%q", got, want)
		}
	})

	t.Run("a secret only an endpoint names", func(t *testing.T) {
		plan, req, _, buffer := planned(t, issueSinkConfig, nil)
		plan.ReportUnfinished(req, []string{"KIMI_API_KEY"})
		want := "│  ✗ KIMI_API_KEY — an endpoint in the config names it, and nothing sets it\n" +
			"│     gh secret set KIMI_API_KEY --repo acme/widget\n"
		if !strings.Contains(buffer.String(), want) {
			t.Errorf("got\n%q\nand it does not carry\n%q", buffer.String(), want)
		}
	})
}

// TestReportUnfinishedNamesTheOrganisationScopeForAnOrganisation, except for
// the one secret that must never be organisation-scoped: on an org-owned
// repository the scope helper prints --org, and an instruction someone copies
// verbatim is not the place to be inconsistent with your own warning
// (lib/init.sh:619 against :632, tests/test-runner.sh:259-269).
func TestReportUnfinishedNamesTheOrganisationScopeForAnOrganisation(t *testing.T) {
	plan, req, _, buffer := planned(t, hostedPairing("codex", "claude"), func(r *initcmd.Request) {
		r.Pairing = livePairing{doc: descriptor(t)}
		r.GitHub.(*fakeGitHub).ownerType = "Organization"
	})
	plan.ReportUnfinished(req, []string{"CROSSREV_CODEX_AUTH", "KIMI_API_KEY"})
	if !strings.Contains(buffer.String(), "gh secret set CROSSREV_CODEX_AUTH --repo acme/widget < ~/.codex/auth.json") {
		t.Errorf("the rotating credential was not kept repository-scoped:\n%s", buffer.String())
	}
	if strings.Contains(buffer.String(), "gh secret set CROSSREV_CODEX_AUTH --org") {
		t.Errorf("the rotating credential was named with --org:\n%s", buffer.String())
	}
	if !strings.Contains(buffer.String(), "gh secret set KIMI_API_KEY --org acme") {
		t.Errorf("an endpoint's secret was not named at the organisation's scope:\n%s", buffer.String())
	}
}

// TestRunPrintsThePlanAndStopsOnDryRun, with nothing written and nothing
// declared (lib/init.sh:52-55).
func TestRunPrintsThePlanAndStopsOnDryRun(t *testing.T) {
	root := t.TempDir()
	req := request(t, issueSinkConfig)
	req.DryRun = true
	req.Files = initcmd.Dir(root)
	out, buffer := capture()
	req.Out = out
	before := tree(t, root)

	if err := initcmd.Run(context.Background(), req, initcmd.Execution{
		Labels:  refusingLabels{t},
		Secrets: &initcmd.SecretStore{Runner: refusingRunner{t}},
		Keys:    fakeKeys{path: "/apps"},
		Files:   refusingFiles{t},
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !strings.HasPrefix(buffer.String(), "\n◇  Plan for acme/widget\n") {
		t.Errorf("the plan was not printed:\n%s", buffer.String())
	}
	if !strings.HasSuffix(buffer.String(), "└  Nothing was changed — --dry-run prints the plan and stops.\n\n") {
		t.Errorf("the run did not close with the dry-run line:\n%s", buffer.String())
	}
	if strings.Contains(buffer.String(), "◇  Labels") {
		t.Errorf("a dry run reached execution:\n%s", buffer.String())
	}
	if got := tree(t, root); len(got) != len(before) {
		t.Errorf("a dry run changed the working tree: %v", got)
	}
}

// TestRunStopsOnANoAtTheGate: the gate is the difference between a tool people
// trust with a second repository and one they run once (lib/init.sh:61).
func TestRunStopsOnANoAtTheGate(t *testing.T) {
	root := t.TempDir()
	req := request(t, issueSinkConfig)
	req.Files = initcmd.Dir(root)
	out, buffer := capture()
	out.Input = answers{"n"}
	req.Out = out

	if err := initcmd.Run(context.Background(), req, initcmd.Execution{
		Labels:  refusingLabels{t},
		Secrets: &initcmd.SecretStore{Runner: refusingRunner{t}},
		Keys:    fakeKeys{path: "/apps"},
		Files:   refusingFiles{t},
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.HasSuffix(buffer.String(), "└  Nothing was changed.\n\n") {
		t.Errorf("a no did not close with the unchanged line:\n%s", buffer.String())
	}
}

// TestRunRefusesWithNobodyToAnswerTheGate: `--yes` is the only way past it
// without a terminal, and its absence is a refusal rather than a silent yes
// (lib/ui.sh:150).
func TestRunRefusesWithNobodyToAnswerTheGate(t *testing.T) {
	req := request(t, issueSinkConfig)
	req.Files = initcmd.Dir(t.TempDir())
	out, _ := capture()
	req.Out = out

	err := initcmd.Run(context.Background(), req, initcmd.Execution{
		Labels:  refusingLabels{t},
		Secrets: &initcmd.SecretStore{Runner: refusingRunner{t}},
		Keys:    fakeKeys{path: "/apps"},
		Files:   refusingFiles{t},
	})
	fatal := fatalError(t, err)
	if fatal.Reason != "CrossRev needs to ask you something, but no terminal is attached" {
		t.Errorf("reason = %q", fatal.Reason)
	}
}

// TestRunAnswersTheGateWithYes, and the same yes reaches the refresher question
// the way CROSSREV_ASSUME_YES does (lib/init.sh:57-60 against :524).
func TestRunAnswersTheGateWithYes(t *testing.T) {
	root := t.TempDir()
	req := request(t, issueSinkConfig)
	req.Yes = true
	req.Files = initcmd.Dir(root)
	out, buffer := capture()
	req.Out = out

	if err := initcmd.Run(context.Background(), req, wired(t, root)); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(buffer.String(), "◆  Proceed?  yes (--yes)\n") {
		t.Errorf("the gate did not answer itself:\n%s", buffer.String())
	}
	for _, section := range []string{"◇  Labels", "◇  Secrets", "◇  Files", "◇  Still needed"} {
		if !strings.Contains(buffer.String(), section) {
			t.Errorf("%s never ran:\n%s", section, buffer.String())
		}
	}
	if !out.AssumeYes {
		t.Error("--yes did not reach the questions execution asks")
	}
}

// TestRunAnswersTheGateWithYesWithNoOutputInjected: --yes is the Bash exporting
// CROSSREV_ASSUME_YES, and an environment variable is not something an absent
// output writer can withhold. Measured:
//
//	$ source lib/ui.sh
//	$ CROSSREV_ASSUME_YES=1 ui_confirm "Proceed?" </dev/null
//	◆  Proceed?  yes (--yes)
//	exit=0
//
// Run used to set the flag only when the caller had injected an IO, so
// `crossrev init --yes` writing to the real terminal — a nil Out, which is what
// a caller that has not overridden anything leaves — asked the question and then
// died with nowhere to read the answer from. stdin is closed here so the case
// fails rather than blocks if the guard ever comes back.
func TestRunAnswersTheGateWithYesWithNoOutputInjected(t *testing.T) {
	root := t.TempDir()
	req := request(t, issueSinkConfig)
	req.Yes = true
	req.Files = initcmd.Dir(root)
	req.Out = nil

	withClosedStdin(t)
	if err := initcmd.Run(context.Background(), req, wired(t, root)); err != nil {
		t.Fatalf("Run with --yes and no injected output: %v", err)
	}
	// The gate was answered rather than skipped: Run only reaches the
	// sections past a yes, and the workflows are what they leave behind.
	if _, err := os.Stat(filepath.Join(root, ".github", "crossrev.yml")); err != nil {
		t.Errorf("the run stopped at the gate: %v", err)
	}
}

// withClosedStdin points os.Stdin at a closed pipe for one test, so a question
// asked without --yes has nowhere to read from rather than waiting on whatever
// the suite was started with.
func withClosedStdin(t *testing.T) {
	t.Helper()
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	write.Close()
	read.Close()
	saved := os.Stdin
	os.Stdin = read
	t.Cleanup(func() { os.Stdin = saved })
}

// TestWriteFilesRefusesAnUnwiredFileWriter: a nil port is a wiring fault named
// where it is needed, not a default that quietly writes nowhere.
func TestWriteFilesRefusesAnUnwiredFileWriter(t *testing.T) {
	plan, req, _, _ := planned(t, issueSinkConfig, nil)
	err := plan.WriteFiles(context.Background(), req, initcmd.Execution{})
	if err == nil || !strings.Contains(err.Error(), "Files") {
		t.Fatalf("err = %v, want a refusal naming the missing port", err)
	}
}

// TestExecuteRunsTheSectionsInTheShellsOrder. The order is what an operator
// sees when something goes wrong: a label that will not create stops the run
// before a secret is written, and a secret nobody can set is reported after the
// workflows are already installed.
func TestExecuteRunsTheSectionsInTheShellsOrder(t *testing.T) {
	root := t.TempDir()
	plan, req, _, buffer := planned(t, issueSinkConfig, func(r *initcmd.Request) {
		r.Files = initcmd.Dir(root)
	})
	if err := plan.Execute(context.Background(), req, wired(t, root)); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	order := []string{"◇  Labels", "◇  Secrets", "◇  Files", "◇  Still needed"}
	at := 0
	for _, section := range order {
		found := strings.Index(buffer.String()[at:], section)
		if found < 0 {
			t.Fatalf("%s is missing or out of order:\n%s", section, buffer.String())
		}
		at += found
	}
}

// TestDirReadsAndWritesUnderItsRoot. Dir is the one implementation of both
// halves of the working tree, so the tests above that write through it and read
// back through Request.Files are reading what was actually put there.
func TestDirReadsAndWritesUnderItsRoot(t *testing.T) {
	root := t.TempDir()
	dir := initcmd.Dir(root)
	if dir.Exists(".github/crossrev.yml") {
		t.Error("an empty directory reported a file")
	}
	if err := dir.MkdirAll(".github/workflows"); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if !dir.Exists(".github/workflows") {
		t.Error("a directory that was created reads as absent")
	}
	if err := dir.WriteFile(".github/crossrev.yml", []byte("version: 1\n")); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if got := readUnder(t, root, ".github/crossrev.yml"); got != "version: 1\n" {
		t.Errorf("the file holds %q", got)
	}
}

// readUnder is one file under a working tree root.
func readUnder(t *testing.T, root, path string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(body)
}

// perm is one path's permission bits.
func perm(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return info.Mode().Perm()
}
