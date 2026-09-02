package initcmd

import (
	"regexp"
	"strings"

	"github.com/carlosboeing/crossrev/internal/config"
	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/harness"
)

// The placeholder the install block replaces (lib/init.sh:741), and the two
// fence markers (lib/init.sh:754-755, documented at :662-664).
const installPlaceholder = "__HARNESS_INSTALL__"

var (
	fenceOpen  = regexp.MustCompile(`^[[:space:]]*# crossrev:only `)
	fenceClose = regexp.MustCompile(`^[[:space:]]*# crossrev:end`)
)

// RenderWorkflow renders one workflow template for this plan's runner and
// pairing (_init_render_workflow, lib/init.sh:711-757).
//
// Two mechanisms, and the split is deliberate. Scalars are substituted; whole
// steps and env entries are fenced, because a hosted runner installs the
// harness per run and passes credentials in as secrets, while a self-hosted one
// has both already and passing them would be the mistake — a secret restored
// over a working on-disk login is how you get a job authenticating as something
// nobody intended.
//
//	# crossrev:only <runner>
//	...lines kept only for that runner...
//	# crossrev:end
//
// The three stages run in the shell's order, and the order is observable: the
// substitutions are applied to the install block the first stage inserted, and
// the fences are evaluated after both.
//
// The six substitutions are literal on both sides. `strings.NewReplacer`
// writes a replacement as given, and the Bash — which substitutes with
// `sed s#…#…#g`, where `&` stands for the whole match and `#` closes the command
// — escapes `\`, `&` and the delimiter through `_init_sed_replacement`
// (lib/init.sh:703-709) before sed reads them. Of the six values only the
// described ref can carry any of the three, because it is whatever
// `git describe --tags` reported. Measured: `_init_sed_replacement 'v1&2'`
// gives `v1\&2`, and sed writes `v1&2`, which is what this writes.
func (p Plan) RenderWorkflow(req Request, template []byte) []byte {
	runsOn := "ubuntu-latest"
	if p.Runner == "self-hosted" {
		// Two labels, not one: `self-hosted` alone matches every
		// self-hosted runner the owner has, including ones set up for
		// something else entirely.
		runsOn = "[self-hosted, crossrev]"
	}

	// Always repository-scoped. An organisation-level rotating credential
	// would be refreshed by every repository reading it, and concurrency
	// groups do not span repositories — so "one writer" would quietly become
	// several, and the first to refresh would invalidate the rest.
	refreshHarness := refresherHarness(req.Harness)
	refreshSecret := ""
	if entry, found := req.Harness.For(refreshHarness); found {
		// The environment key and the secret lookup are the same name,
		// and both come from the descriptor rather than from the
		// template. A refresher harness whose secret is not the one the
		// template names would otherwise render a workflow that passes
		// one variable and looks up another.
		refreshSecret = entry.Credential.Secret
	}

	rendered := expandInstall(string(template), harnessInstallLine(req.Harness, p.Config))
	rendered = strings.NewReplacer(
		"__SOURCE_SHA__", p.SourceSHA,
		"__SOURCE_REF__", p.SourceRef,
		"__RUNS_ON__", runsOn,
		"__REFRESH_SCOPE__", "--repo "+p.Repo.String(),
		"__REFRESH_HARNESS__", refreshHarness,
		"__REFRESH_SECRET__", refreshSecret,
	).Replace(rendered)
	return []byte(filterFences(rendered, p.Runner))
}

// expandInstall replaces the whole placeholder line with the install block
// (lib/init.sh:740-746).
//
// The line is replaced, not the placeholder within it: awk matches the record
// and calls `next`, so anything else on that line goes with it. An empty
// install block leaves nothing at all rather than a blank line, because awk's
// split of an empty string yields no fields.
func expandInstall(document, install string) string {
	lines := records(document)
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if !strings.Contains(line, installPlaceholder) {
			out = append(out, line)
			continue
		}
		if install == "" {
			continue
		}
		out = append(out, strings.Split(install, "\n")...)
	}
	return join(out)
}

// filterFences keeps a fenced block only for the runner it names, and drops
// both markers (lib/init.sh:753-756).
func filterFences(document, runner string) string {
	lines := records(document)
	out := make([]string, 0, len(lines))
	skip := false
	for _, line := range lines {
		if fenceOpen.MatchString(line) {
			// awk's `$3` with the default field separator: the
			// third whitespace-separated word, which is the runner
			// the block is fenced for.
			skip = field(line, 3) != runner
			continue
		}
		if fenceClose.MatchString(line) {
			skip = false
			continue
		}
		if skip {
			continue
		}
		out = append(out, line)
	}
	return join(out)
}

// harnessInstallLine is the install commands for the harnesses this
// configuration actually names (_init_harness_install_line, lib/init.sh:673-689).
//
// Installing one harness when the pairing names two is worse than failing: the
// resolve leg falls back to whatever harness is present, warns in one line
// nobody reads in a CI log, and the loop runs both legs on one model. It
// completes normally, and the cross-model property the whole design exists for
// is gone with no error anywhere.
//
// The lines are indented to sit inside the template's `run: |` block, and the
// trailing newline is trimmed so the substitution leaves no blank line behind.
func harnessInstallLine(doc harness.Document, cfg *config.Config) string {
	host := doc.EndpointHost()
	seen := map[string]bool{}
	var out strings.Builder
	for _, role := range core.Roles() {
		name := cfg.Get("." + string(role) + ".harness")
		// A leg on an endpoint still runs through the endpoint host
		// binary.
		//
		// Nothing observable turns on this after Resolve, which refuses
		// a leg whose harness is not the endpoint host
		// (lib/init.sh:184-186): by the time rendering happens, a leg
		// with an endpoint already names the host. It is kept because
		// the shell keeps it and because it is the line that would
		// matter if that refusal ever moved.
		if named(cfg.Get("." + string(role) + ".endpoint")) {
			name = host
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		entry, found := doc.For(name)
		if !found || entry.Install.Command == "" {
			continue
		}
		out.WriteString("          " + entry.Install.Command + "\n")
	}
	return strings.TrimSuffix(out.String(), "\n")
}

// records splits a document the way awk reads it: one record per line, with a
// final newline ending the last record rather than starting an empty one. An
// empty document holds no records at all.
func records(document string) []string {
	if document == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(document, "\n"), "\n")
}

// join writes records back out, and every record ends with a newline because
// awk's `print` ends every record with ORS. A template that carried no final
// newline therefore comes out of the pipeline with one — measured, a template of
// `foo\nbar` renders as `foo\nbar\n`. A document with no records prints nothing.
func join(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n") + "\n"
}

// field is awk's `$n` under the default field separator: fields are runs of
// non-blank characters, and leading blanks start no field. It answers the empty
// string for a field the record does not hold.
func field(record string, n int) string {
	fields := strings.Fields(record)
	if n < 1 || n > len(fields) {
		return ""
	}
	return fields[n-1]
}
