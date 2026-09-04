package initcmd_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/initcmd"
)

// read is one testdata file, verbatim.
func read(t *testing.T, path string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", path))
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return body
}

// rendering resolves a plan for one runner and pairing and renders one
// template through it.
func rendering(t *testing.T, configuration, runner string, template []byte) string {
	t.Helper()
	if runner == "self-hosted" {
		configuration = strings.Replace(configuration, "version: 1\n", "version: 1\nrunner: self-hosted\n", 1)
	}
	req := request(t, configuration)
	req.Source = fakeSource{sha: "0123456789abcdef0123456789abcdef01234567", ref: "v9.9.9"}
	if runner == "self-hosted" {
		req.Source = fakeSource{sha: "0123456789abcdef0123456789abcdef01234567", ref: "untagged"}
	}
	return string(resolved(t, req).RenderWorkflow(req, template))
}

// TestRenderWorkflowMatchesTheShell pins the rendered bytes for the four
// shipped workflows on two pairings.
//
// The fixtures were rendered by `_init_render_workflow` itself. They pin the
// renderer and the templates together, so a deliberate template edit is a
// deliberate fixture regeneration.
func TestRenderWorkflowMatchesTheShell(t *testing.T) {
	hosted := `version: 1
reviewer:
  harness: codex
resolver:
  harness: claude
`
	selfHosted := `version: 1
reviewer:
  harness: claude
resolver:
  harness: agy
`
	for _, row := range []struct {
		directory     string
		configuration string
		runner        string
		workflows     []string
	}{
		{"hosted-codex-claude", hosted, "github-hosted", []string{"review", "resolve", "watchdog", "token-refresh"}},
		{"selfhosted-claude-agy", selfHosted, "self-hosted", []string{"review", "resolve"}},
	} {
		for _, workflow := range row.workflows {
			t.Run(row.directory+"/"+workflow, func(t *testing.T) {
				template := templateFor(t, workflow)
				got := rendering(t, row.configuration, row.runner, template)
				want := string(read(t, filepath.Join(row.directory, "crossrev-"+workflow+".yml")))
				if got != want {
					t.Errorf("rendered:\n%s\nwant:\n%s", got, want)
				}
			})
		}
	}
}

// templateFor is the embedded copy of one shipped workflow template.
func templateFor(t *testing.T, workflow string) []byte {
	t.Helper()
	switch workflow {
	case "review":
		return initcmd.ReviewWorkflowTemplate()
	case "resolve":
		return initcmd.ResolveWorkflowTemplate()
	case "watchdog":
		return initcmd.WatchdogWorkflowTemplate()
	case "token-refresh":
		return initcmd.TokenRefreshWorkflowTemplate()
	}
	t.Fatalf("no template for %q", workflow)
	return nil
}

func TestRenderSubstitutesEveryPlaceholderEverywhereOnALine(t *testing.T) {
	configuration := `version: 1
reviewer:
  harness: codex
resolver:
  harness: claude
`
	got := rendering(t, configuration, "github-hosted", read(t, "synthetic.yml"))
	want := "head ubuntu-latest ubuntu-latest\n" +
		"sha 0123456789abcdef0123456789abcdef01234567 ref v9.9.9\n" +
		"scope --repo acme/widget harness codex secret CROSSREV_CODEX_AUTH\n" +
		"          curl -fsSL https://chatgpt.com/codex/install.sh | sh -s -- --release 0.148.0\n" +
		"          curl -fsSL https://claude.ai/install.sh | bash -s 2.1.237\n" +
		"  hosted only\n" +
		"tail\n"
	if got != want {
		t.Errorf("rendered:\n%s\nwant:\n%s", got, want)
	}
}

func TestRenderKeepsOnlyTheBlockFencedForThisRunner(t *testing.T) {
	configuration := `version: 1
reviewer:
  harness: claude
resolver:
  harness: claude
`
	got := rendering(t, configuration, "self-hosted", read(t, "synthetic.yml"))
	want := "head [self-hosted, crossrev] [self-hosted, crossrev]\n" +
		"sha 0123456789abcdef0123456789abcdef01234567 ref untagged\n" +
		"scope --repo acme/widget harness codex secret CROSSREV_CODEX_AUTH\n" +
		"          curl -fsSL https://claude.ai/install.sh | bash -s 2.1.237\n" +
		"  self only\n" +
		"tail\n"
	if got != want {
		t.Errorf("rendered:\n%s\nwant:\n%s", got, want)
	}
	if strings.Contains(got, "crossrev:only") || strings.Contains(got, "crossrev:end") {
		t.Error("the fence markers are dropped along with the blocks they close")
	}
	if strings.Contains(got, "neither") {
		t.Error("a block fenced for a third runner is kept by neither")
	}
}

func TestRenderNamesTheRefreshScopeRepositoryEvenForAnOrganisation(t *testing.T) {
	// An organisation-level rotating credential would be refreshed by every
	// repository reading it, and concurrency groups do not span
	// repositories, so "one writer" would quietly become several.
	configuration := `version: 1
reviewer:
  harness: codex
resolver:
  harness: claude
`
	req := request(t, configuration)
	req.GitHub = &fakeGitHub{slug: slug(t), ownerType: "Organization", orgOK: true}

	got := string(resolved(t, req).RenderWorkflow(req, read(t, "synthetic.yml")))
	if !strings.Contains(got, "scope --repo acme/widget ") {
		t.Errorf("rendered:\n%s", got)
	}
}

func TestRenderInstallLine(t *testing.T) {
	endpoints := `
endpoints:
  kimi:
    base_url: https://api.moonshot.ai/anthropic
    token_env: KIMI_API_KEY
`
	codex := "          curl -fsSL https://chatgpt.com/codex/install.sh | sh -s -- --release 0.148.0"
	claude := "          curl -fsSL https://claude.ai/install.sh | bash -s 2.1.237"

	for _, row := range []struct {
		name          string
		configuration string
		want          []string
	}{
		{
			name:          "two harnesses install in leg order",
			configuration: "version: 1\nreviewer:\n  harness: codex\nresolver:\n  harness: claude\n",
			want:          []string{codex, claude},
		},
		{
			name:          "one harness on both legs installs once",
			configuration: "version: 1\nreviewer:\n  harness: claude\nresolver:\n  harness: claude\n",
			want:          []string{claude},
		},
		{
			name:          "a leg on a named endpoint installs the endpoint host",
			configuration: "version: 1\nreviewer:\n  harness: codex\nresolver:\n  harness: claude\n  endpoint: kimi\n" + endpoints,
			want:          []string{codex, claude},
		},
		{
			name:          "two legs on one endpoint install the host once",
			configuration: "version: 1\nreviewer:\n  harness: claude\n  endpoint: kimi\nresolver:\n  harness: claude\n  endpoint: kimi\n" + endpoints,
			want:          []string{claude},
		},
	} {
		t.Run(row.name, func(t *testing.T) {
			got := rendering(t, row.configuration, "github-hosted", read(t, "synthetic.yml"))
			lines := strings.Split(got, "\n")
			// The install lines sit between the scope line and the
			// fenced block.
			var installed []string
			for _, line := range lines[3:] {
				if !strings.HasPrefix(line, "          ") {
					break
				}
				installed = append(installed, line)
			}
			if strings.Join(installed, "\n") != strings.Join(row.want, "\n") {
				t.Errorf("install lines = %q, want %q", installed, row.want)
			}
			if strings.Contains(got, "prefix") || strings.Contains(got, "suffix") {
				t.Error("the whole placeholder line is replaced, not the placeholder within it")
			}
		})
	}
}

func TestRenderWritesNothing(t *testing.T) {
	root := t.TempDir()
	before := tree(t, root)

	configuration := `version: 1
reviewer:
  harness: codex
resolver:
  harness: claude
`
	req := request(t, configuration)
	req.Files = diskFS{root: root}
	plan := resolved(t, req)
	for _, template := range [][]byte{
		initcmd.ReviewWorkflowTemplate(),
		initcmd.ResolveWorkflowTemplate(),
		initcmd.WatchdogWorkflowTemplate(),
		initcmd.TokenRefreshWorkflowTemplate(),
	} {
		if len(plan.RenderWorkflow(req, template)) == 0 {
			t.Fatal("a rendered workflow is not empty")
		}
	}
	if after := tree(t, root); len(after) != len(before) {
		t.Errorf("rendering wrote into the working tree: %v", after)
	}
}

func TestRenderLeavesTheEmbeddedTemplateAlone(t *testing.T) {
	// RenderWorkflow must not edit the bytes it was handed: the accessors
	// hand out a fresh copy per call, and a renderer writing through one
	// would still corrupt the copy its own caller holds.
	template := initcmd.ReviewWorkflowTemplate()
	original := string(template)

	configuration := `version: 1
reviewer:
  harness: codex
resolver:
  harness: claude
`
	req := request(t, configuration)
	resolved(t, req).RenderWorkflow(req, template)

	if string(template) != original {
		t.Error("the template handed in was modified")
	}
}

// TestRenderLeavesNoBlankLineWhenNothingInstalls pins awk's answer for an empty
// install block: `split("", a, "\n")` yields no fields, so the placeholder line
// leaves nothing at all rather than one blank line (lib/init.sh:740-745).
//
// The whole document is compared, because the blank line the wrong answer
// leaves is invisible to an assertion that only looks at the install lines.
func TestRenderLeavesNoBlankLineWhenNothingInstalls(t *testing.T) {
	configuration := "version: 1\nreviewer:\n  harness: bogus\nresolver:\n  harness: bogus\n"
	got := rendering(t, configuration, "github-hosted", read(t, "synthetic.yml"))
	want := "head ubuntu-latest ubuntu-latest\n" +
		"sha 0123456789abcdef0123456789abcdef01234567 ref v9.9.9\n" +
		"scope --repo acme/widget harness codex secret CROSSREV_CODEX_AUTH\n" +
		"  hosted only\n" +
		"tail\n"
	if got != want {
		t.Errorf("rendered:\n%q\nwant:\n%q", got, want)
	}
}

// TestRenderEndsADocumentTheTemplateLeftUnterminated: awk's `print` ends every
// record with ORS, so the pipeline writes a final newline whether or not the
// template carried one, and the last `!skip` of the fence filter prints through
// the same `print`. Measured on the shell: a file holding `foo\nbar`, seven
// bytes with no final newline, comes out of `awk | sed | awk` as eight bytes
// ending `bar\n`.
//
// An empty template stays empty: awk reads no records from it and prints
// nothing, so there is no newline to add.
func TestRenderEndsADocumentTheTemplateLeftUnterminated(t *testing.T) {
	configuration := "version: 1\nreviewer:\n  harness: codex\nresolver:\n  harness: claude\n"
	for _, row := range []struct{ name, template, want string }{
		{"no final newline", "head __RUNS_ON__\ntail", "head ubuntu-latest\ntail\n"},
		{"a final newline already", "head __RUNS_ON__\ntail\n", "head ubuntu-latest\ntail\n"},
		{"one unterminated line", "tail", "tail\n"},
		{"nothing at all", "", ""},
	} {
		t.Run(row.name, func(t *testing.T) {
			got := rendering(t, configuration, "github-hosted", []byte(row.template))
			if got != row.want {
				t.Errorf("rendered %q as %q, want %q", row.template, got, row.want)
			}
		})
	}
}
