package initcmd_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/config"
	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/initcmd"
)

// configFrom loads one repository configuration the way `init` does: the
// working tree, no base revision.
func configFrom(t *testing.T, body string) *config.Config {
	t.Helper()
	loaded, err := config.Load(context.Background(), core.Revision{},
		showing(map[string]string{".github/crossrev.yml": body}))
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	return loaded
}

// golden reads one file the shell wrote.
func golden(t *testing.T, name string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", "config", name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(body)
}

// TestWriteConfigMatchesTheShell is the whole generated policy file, byte for
// byte, against what `_init_write_config` wrote for the same inputs.
//
// The fixtures were produced by sourcing lib/init.sh and calling the function,
// so they are yq's own output rather than a reading of it. They pin two things
// at once: the edits, and the emitter — yq drops every blank line and normalises
// a line comment to one space before the `#`, and the file a repository ends up
// committing carries that.
func TestWriteConfigMatchesTheShell(t *testing.T) {
	for _, row := range []struct {
		name     string
		resolved string
		body     string
	}{
		{
			name:     "github-issues.yml",
			resolved: "github_issues",
			body: `version: 1
reviewer:
  harness: claude
  model: reviewer-model
resolver:
  harness: claude
  model: resolver-model`,
		},
		{
			name:     "repository-folder.yml",
			resolved: "repository folder backlog/findings",
			body: `version: 1
reviewer:
  harness: codex
  effort: high
resolver:
  harness: claude
  model: claude-opus-5
  effort: low`,
		},
		{
			name:     "none-destination.yml",
			resolved: "none",
			body: `version: 1
reviewer:
  harness: opencode
  model: anthropic/claude-fable-5
resolver:
  harness: agy`,
		},
		{
			name:     "endpoint-leg.yml",
			resolved: "repository file .crossrev/backlog.md",
			body: `version: 1
reviewer:
  harness: claude
  endpoint: kimi
  model: kimi-k2
resolver:
  harness: claude
  model: claude-opus-5
  effort: high
endpoints:
  kimi:
    base_url: https://api.kimi.com/coding/
    token_env: KIMI_API_KEY`,
		},
		{
			// `repository` with nothing after it is not the
			// `repository <layout> <path>` the case arm matches, so
			// it falls through to none (lib/init.sh:877-883).
			name:     "unresolvable.yml",
			resolved: "repository",
			body: `version: 1
reviewer:
  harness: claude
resolver:
  harness: claude`,
		},
	} {
		t.Run(row.name, func(t *testing.T) {
			plan := initcmd.Plan{BacklogResolved: row.resolved, Config: configFrom(t, row.body)}
			got := string(plan.WriteConfig(initcmd.PolicyTemplate()))
			if want := golden(t, row.name); got != want {
				t.Errorf("the written policy differs from the shell's:\n%s", firstDifference(got, want))
			}
		})
	}
}

// TestWriteConfigStatesThePairingItProvisionedFor: init derives the secret list
// and whether a refresher App is needed from the resolved pairing, so a policy
// file naming a different one leaves the repository provisioned for a leg that
// never runs (lib/init.sh:897-927, tests/test-init.sh:298-325).
func TestWriteConfigStatesThePairingItProvisionedFor(t *testing.T) {
	plan := initcmd.Plan{BacklogResolved: "github_issues", Config: configFrom(t, `version: 1
reviewer:
  harness: codex
resolver:
  harness: claude
  model: resolver-model`)}
	written := string(plan.WriteConfig(initcmd.PolicyTemplate()))

	for _, want := range []string{
		"reviewer:\n  harness: codex\nresolver:\n  harness: claude\n  model: resolver-model\n",
	} {
		if !strings.Contains(written, want) {
			t.Errorf("the written policy is\n%s\nand does not carry\n%q", written, want)
		}
	}
	// A field resolving to nothing is deleted rather than left at the
	// template's value, so a leg cannot inherit a model under a harness that
	// never had it (lib/init.sh:911-913). Asserted on the two blocks rather
	// than the document, whose comments name the template's own model.
	for _, leg := range []string{"reviewer:", "resolver:"} {
		block := blockOf(written, leg)
		if strings.Contains(block, "claude-fable-5") || strings.Contains(block, "claude-opus-5") {
			t.Errorf("the template's own model survived in %s\n%s", leg, block)
		}
		if strings.Contains(block, "effort:") {
			t.Errorf("an effort nothing resolved to was left at the template's value in %s\n%s", leg, block)
		}
	}
}

// TestWriteConfigQuotesAValueTheWayYqDoes.
//
// yq sets a string, and its emitter quotes only what has to be quoted: double
// quotes where the plain text would resolve to something other than a string,
// single quotes where the plain form is not allowed at all, and nothing
// otherwise. Every row was measured by running yq against the same template.
func TestWriteConfigQuotesAValueTheWayYqDoes(t *testing.T) {
	for _, row := range []struct{ value, want string }{
		{"claude-opus-5", "claude-opus-5"},
		{"anthropic/claude-fable-5", "anthropic/claude-fable-5"},
		{"ver-1.2", "ver-1.2"},
		{"a b", "a b"},
		{"a  b", "a  b"},
		{"x#y", "x#y"},
		{"it's", "it's"},
		{"-dash", "-dash"},
		{"?x", "?x"},
		{":x", ":x"},
		{"x,y", "x,y"},
		{"x[y", "x[y"},
		{"_3", "_3"},
		{"on", "on"},
		{"yes", "yes"},
		{"YES", "YES"},
		{"Off", "Off"},
		{"y", "y"},
		{"n", "n"},
		{"12:30", "12:30"},
		// A value of `null` is not here: _init_policy_pairing treats it
		// as a field that resolved to nothing and deletes the key
		// (lib/init.sh:919), so it never reaches the emitter.
		{"true", `"true"`},
		{"False", `"False"`},
		{"~", `"~"`},
		{"NULL", `"NULL"`},
		{"123", `"123"`},
		{"+3", `"+3"`},
		{"1_000", `"1_000"`},
		{"0x1f", `"0x1f"`},
		{"0o17", `"0o17"`},
		{"0b101", `"0b101"`},
		{"1.5", `"1.5"`},
		{".5", `".5"`},
		{"5.", `"5."`},
		{"1e3", `"1e3"`},
		{".inf", `".inf"`},
		{".nan", `".nan"`},
		{"-.inf", `"-.inf"`},
		{"2026-09-02", `"2026-09-02"`},
		{"a: b", `'a: b'`},
		{"x:", `'x:'`},
		{"#x", `'#x'`},
		{"*star", `'*star'`},
		{"&amp", `'&amp'`},
		{"!x", `'!x'`},
		{"|x", `'|x'`},
		{">x", `'>x'`},
		{"%x", `'%x'`},
		{"@x", `'@x'`},
		{"`x", "'`x'"},
		{`"x`, `'"x'`},
		{"'x", `'''x'`},
		{",x", `',x'`},
		{"[x", `'[x'`},
		{"{x", `'{x'`},
		{"- x", `'- x'`},
		{" lead", `' lead'`},
		{"trail ", `'trail '`},
		{"-", `'-'`},
		{"--", "--"},
		{"x---", "x---"},
		{"---", `'---'`},
		{"...", `'...'`},
		{"--- x", `'--- x'`},
		{"... x", `'... x'`},
		{"!!str x", `'!!str x'`},
	} {
		t.Run(row.value, func(t *testing.T) {
			plan := initcmd.Plan{BacklogResolved: "none", Config: configFrom(t, "version: 1\nreviewer:\n  harness: claude\n  model: \""+yamlEscape(row.value)+"\"\n")}
			written := plan.WriteConfig(initcmd.PolicyTemplate())
			want := "\n  model: " + row.want + "\n"
			if !strings.Contains(string(written), want) {
				t.Errorf("the reviewer block is\n%s\nand does not carry\n%q",
					blockOf(string(written), "reviewer:"), want)
			}
		})
	}
}

// yamlEscape puts a value inside a double-quoted YAML scalar in a source
// fixture, so the config layer reads back exactly the string under test.
func yamlEscape(value string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(value)
}

// TestWriteConfigCreatesAMappingThePathNeedsAndDoesNotHave: the shipped
// template carries every block init writes into, so this is the arm no fixture
// reaches. yq creates the missing mappings on the way down.
func TestWriteConfigCreatesAMappingThePathNeedsAndDoesNotHave(t *testing.T) {
	template := []byte("version: 1\nreviewer:\n  harness: claude\n")
	plan := initcmd.Plan{BacklogResolved: "repository folder notes/x", Config: configFrom(t, `version: 1
reviewer:
  harness: claude
resolver:
  harness: codex`)}
	want := "version: 1\n" +
		"reviewer:\n" +
		"  harness: claude\n" +
		"backlog:\n" +
		"  destination: repository\n" +
		"  repository:\n" +
		"    layout: folder\n" +
		"    path: notes/x\n" +
		"resolver:\n" +
		"  harness: codex\n"
	if got := string(plan.WriteConfig(template)); got != want {
		t.Errorf("got\n%q\nwant\n%q", got, want)
	}
}

// TestWriteConfigKeepsABacklogPathWhoseSpacesAreInIt: the shell reads the
// resolved backlog with `read -r _ layout path`, and the last variable of a
// read takes the whole remainder — so a directory name holding a space is a
// path, not a path and a stray word.
//
// Every row was measured by calling _init_write_config with that
// INIT_BACKLOG_RESOLVED and reading the three lines out of the file it wrote:
//
//	repository folder docs/backlog folder/findings
//	  destination: repository / layout: folder / path: docs/backlog folder/findings
//	repository folder   a   b   (three spaces each side of b)
//	  destination: repository / layout: folder / path: a   b
//	repository folder
//	  destination: repository, and layout and path left at the template's own
//	  values — the shell builds no clause for either when the path is empty
//	repository
//	  destination: none, template layout and path
//	(two leading spaces) repository   file   a b
//	  destination: none — the case pattern is anchored, so the read never runs
func TestWriteConfigKeepsABacklogPathWhoseSpacesAreInIt(t *testing.T) {
	// The template's own values, which a case that writes no clause leaves
	// exactly where they were.
	const templateLayout, templatePath = "folder", "backlog/findings"
	for _, row := range []struct{ name, resolved, destination, layout, path string }{
		{
			name: "a path holding a space", resolved: "repository folder docs/backlog folder/findings",
			destination: "repository", layout: "folder", path: "docs/backlog folder/findings",
		},
		{
			name: "runs of spaces around and inside it", resolved: "repository folder   a   b   ",
			destination: "repository", layout: "folder", path: "a   b",
		},
		{
			name: "runs of blanks between the fields", resolved: "repository   file   a b  ",
			destination: "repository", layout: "file", path: "a b",
		},
		{
			// The case pattern is anchored, so a leading blank
			// stops `repository *` matching at all and the read
			// never runs. Measured, not reasoned about: the same
			// line without the two leading spaces resolves to
			// repository/file/`a b`.
			name: "a blank before the whole line", resolved: "  repository   file   a b  ",
			destination: "none", layout: templateLayout, path: templatePath,
		},
		{
			name: "a layout and no path at all", resolved: "repository folder",
			destination: "repository", layout: templateLayout, path: templatePath,
		},
		{
			name: "repository with nothing after it", resolved: "repository",
			destination: "none", layout: templateLayout, path: templatePath,
		},
	} {
		t.Run(row.name, func(t *testing.T) {
			plan := initcmd.Plan{BacklogResolved: row.resolved, Config: configFrom(t, `version: 1
reviewer:
  harness: claude
resolver:
  harness: claude`)}
			written := string(plan.WriteConfig(initcmd.PolicyTemplate()))
			for _, want := range []string{
				"\n  destination: " + row.destination + " ",
				"\n    layout: " + row.layout + " ",
				"\n    path: " + row.path + " ",
			} {
				if !strings.Contains(written, want) {
					t.Errorf("the backlog block is\n%s\nand does not carry\n%q",
						blockOf(written, "backlog:"), want)
				}
			}
		})
	}
}

// TestWriteConfigWritesTheLegsInTheShellsOrder: policyEdits walks reviewer then
// resolver, and the order is observable whenever the document does not already
// carry the two mappings — a created key is appended, so the first leg written
// is the first leg in the file.
//
// The shipped template carries both, so no other case here can see it: swapping
// the two names leaves the package green. Measured by handing yq the expression
// _init_write_config builds, over a one-line document:
//
//	$ yq '.backlog.destination = "none" | .reviewer.harness = "codex" |
//	      del(.reviewer.model) | del(.reviewer.effort) | del(.reviewer.endpoint) |
//	      .resolver.harness = "claude" | del(.resolver.model) |
//	      del(.resolver.effort) | del(.resolver.endpoint)' min.yml
//	version: 1
//	backlog:
//	  destination: none
//	reviewer:
//	  harness: codex
//	resolver:
//	  harness: claude
func TestWriteConfigWritesTheLegsInTheShellsOrder(t *testing.T) {
	plan := initcmd.Plan{BacklogResolved: "none", Config: configFrom(t, `version: 1
reviewer:
  harness: codex
resolver:
  harness: claude`)}
	want := "version: 1\n" +
		"backlog:\n" +
		"  destination: none\n" +
		"reviewer:\n" +
		"  harness: codex\n" +
		"resolver:\n" +
		"  harness: claude\n"
	if got := string(plan.WriteConfig([]byte("version: 1\n"))); got != want {
		t.Errorf("got\n%q\nwant\n%q", got, want)
	}
}

// TestWriteConfigLeavesTheEmbeddedTemplateAlone: the bytes handed in are the
// binary's own copy, and a caller that wrote through them would change what
// every later run renders.
func TestWriteConfigLeavesTheEmbeddedTemplateAlone(t *testing.T) {
	plan := initcmd.Plan{BacklogResolved: "github_issues", Config: configFrom(t, "version: 1\nreviewer:\n  harness: codex\n")}
	template := initcmd.PolicyTemplate()
	before := string(template)
	plan.WriteConfig(template)
	if string(template) != before {
		t.Error("WriteConfig wrote through the template it was given")
	}
	if string(initcmd.PolicyTemplate()) != before {
		t.Error("the embedded template changed")
	}
}

// firstDifference names the first line that differs, which is more use than two
// five-kilobyte documents side by side.
func firstDifference(got, want string) string {
	gotLines, wantLines := strings.Split(got, "\n"), strings.Split(want, "\n")
	for i := 0; i < len(gotLines) || i < len(wantLines); i++ {
		g, w := "", ""
		if i < len(gotLines) {
			g = gotLines[i]
		}
		if i < len(wantLines) {
			w = wantLines[i]
		}
		if g != w {
			return "line " + itoa(i+1) + "\n  got  " + g + "\n  want " + w
		}
	}
	return "the documents are equal"
}

// blockOf is the lines from a heading to the next unindented one.
func blockOf(document, heading string) string {
	lines := strings.Split(document, "\n")
	for i, line := range lines {
		if line != heading {
			continue
		}
		out := []string{line}
		for _, next := range lines[i+1:] {
			if next == "" || next[0] != ' ' {
				break
			}
			out = append(out, next)
		}
		return strings.Join(out, "\n")
	}
	return document
}
