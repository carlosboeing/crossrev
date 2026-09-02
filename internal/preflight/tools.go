package preflight

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/carlosboeing/crossrev/internal/config"
	"github.com/carlosboeing/crossrev/internal/exec"
	"github.com/carlosboeing/crossrev/internal/harness"
	"github.com/carlosboeing/crossrev/internal/ui"
)

// The requirement sets preflight_check takes (lib/preflight.sh:69-71).
//
// Anything that is not NeedHarness is the core set, which is what the Bash
// `if [[ "$need" == "harness" ]]` does with an argument it does not recognise.
const (
	// NeedCore is git, gh (authenticated), jq, yq and openssl.
	NeedCore = "core"
	// NeedHarness is the core set plus at least one harness CLI.
	NeedHarness = "harness"
)

// coreTools are the five probed in this order (lib/preflight.sh:86).
//
// Go reads no YAML through yq, no JSON through jq and signs nothing with
// openssl, so three of the five are not this binary's own dependencies. They
// stay because the report is observable: `crossrev doctor` names all five, the
// composite action reads the same report, and dropping one is a change to what
// an operator sees rather than an internal tidy-up.
var coreTools = []string{"git", "gh", "jq", "yq", "openssl"}

// Checker holds what every probe needs: somewhere to report, a way to start a
// child, an environment for it, a PATH lookup and the harness descriptor.
type Checker struct {
	// IO is where the report goes. A nil IO discards it, which is the zero
	// ui.IO's own answer.
	IO *ui.IO

	// Runner starts each probe.
	//
	// Nil is exec.NewOSRunner, the model-facing runner, which refuses a Spec
	// whose environment names a forge credential. That is the safe default and
	// not the right one for `crossrev doctor`: the three gh identity probes
	// below are the orchestrator asking GitHub who it is, and they carry the
	// credential, so the command wires in the orchestrator runner the way
	// internal/forge/ghexec is wired.
	Runner exec.Runner

	// Env is the environment a probe receives, as NAME=VALUE entries. Nil is
	// the allowlist below, read from this process.
	//
	// A version probe receives it with the four forge credentials removed,
	// because a version probe starts a harness CLI and that is a model-facing
	// process (ADR 0001). The gh identity probes receive it whole.
	Env []string

	// LookPath reports whether a program is on PATH, which is `command -v` at
	// lib/preflight.sh:56. Nil searches PATH the way the shell does.
	LookPath func(string) (string, error)

	// Harness is the descriptor the harness probes and the install hints are
	// read from.
	Harness harness.Document

	// Config is the merged configuration the pairing report reads. `doctor`
	// loads it from the working tree with no base revision, which is what
	// cfg_load does for a command that is not a leg (lib/config.sh:183-242;
	// the base-SHA contract is stated at :180-182).
	// Its refusal belongs to the caller that loaded it.
	Config *config.Config

	// OS is what `uname -s` answers (lib/preflight.sh:10). Empty asks the
	// platform this binary was built for, where "darwin" is Darwin.
	OS string

	// Dir is the checkout the quarantine probe looks in. Empty is the working
	// directory, which is where the Bash relative path resolves
	// (lib/preflight.sh:309).
	Dir string
}

// probeEnvironment is what a probe may inherit.
//
// The Bash boundary hands every probe the whole environment, because a shell
// function runs in the shell that called it. Go passes an exact environment, so
// the names are written down. They are the ones `gh` needs — `gh help
// environment` documents the four credentials and the host, and `gh` is a Go
// program whose runtime reads the trust store and the proxy variables — plus
// PATH and HOME, which every other probe needs to find its own configuration.
var probeEnvironment = []string{
	"PATH",
	"HOME",
	"XDG_CONFIG_HOME",
	"GH_CONFIG_DIR",
	"GH_HOST",
	"GH_TOKEN",
	"GITHUB_TOKEN",
	"GH_ENTERPRISE_TOKEN",
	"GITHUB_ENTERPRISE_TOKEN",
	"SSL_CERT_FILE",
	"SSL_CERT_DIR",
	"HTTP_PROXY",
	"HTTPS_PROXY",
	"NO_PROXY",
	"http_proxy",
	"https_proxy",
	"no_proxy",
}

func (c *Checker) io() *ui.IO {
	if c == nil {
		return nil
	}
	return c.IO
}

func (c *Checker) runner() exec.Runner {
	if c != nil && c.Runner != nil {
		return c.Runner
	}
	return exec.NewOSRunner()
}

func (c *Checker) env() []string {
	if c != nil && c.Env != nil {
		return c.Env
	}
	return exec.Inherit(probeEnvironment)
}

// modelFacingEnv is env with the four forge credentials removed, which is the
// `env -u GH_TOKEN …` the adapters build (lib/adapters/claude.sh:72).
func (c *Checker) modelFacingEnv() []string {
	withheld := map[string]bool{}
	for _, name := range exec.ForgeCredentialNames() {
		withheld[name] = true
	}
	entries := c.env()
	kept := make([]string, 0, len(entries))
	for _, entry := range entries {
		name, _, found := strings.Cut(entry, "=")
		if found && withheld[name] {
			continue
		}
		kept = append(kept, entry)
	}
	return kept
}

func (c *Checker) installed(name string) bool {
	look := lookPath
	if c != nil && c.LookPath != nil {
		look = c.LookPath
	}
	_, err := look(name)
	return err == nil
}

// lookPath is `command -v` over PATH, the same walk internal/review and
// internal/resolve do (lib/run.sh:524). os/exec is confined to internal/exec,
// so the search is written out here.
//
// bash's search is executable-preferred rather than first-match, and the whole
// contract was measured rather than reasoned about. With `zzt` planted as a
// directory (nd), a mode-0644 file (f1) and a mode-0755 file (x1):
//
//	PATH=nd          → exit 1        PATH=f1          → f1/zzt
//	PATH=nd:f1       → exit 1        PATH=f1:nd       → f1/zzt
//	PATH=nd:x1       → x1/zzt        PATH=f1:x1       → x1/zzt
//	PATH=nd:f1:x1    → x1/zzt        PATH=x1:f1       → x1/zzt
//
// Which is: the first PATH entry holding an executable regular file wins, from
// anywhere in the list. With none, the answer is the first entry holding
// anything at all — and it is an answer only if that first thing is a regular
// file. A directory is not skipped; it takes the fallback slot and then loses
// it, which is why `nd:f1` finds nothing while `f1:nd` finds f1.
//
// A name carrying a separator is not searched and takes no fallback: measured,
// `command -v ./f` on a mode-0644 file exits 1 where `./x` answers `./x`.
//
// os.Stat rather than os.Lstat, because a symlink to a program is a program:
// measured, `command -v gh` over a symlink pointing at an executable answers
// with the symlink's own path.
func lookPath(name string) (string, error) {
	if name == "" {
		return "", os.ErrNotExist
	}
	if strings.ContainsRune(name, os.PathSeparator) {
		if !isProgram(name) {
			return "", os.ErrNotExist
		}
		return name, nil
	}
	fallback, fallbackIsFile := "", false
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if dir == "" {
			continue
		}
		candidate := filepath.Join(dir, name)
		info, err := os.Stat(candidate)
		if err != nil {
			continue
		}
		if isExecutableFile(info) {
			return candidate, nil
		}
		if fallback == "" {
			fallback, fallbackIsFile = candidate, info.Mode().IsRegular()
		}
	}
	if fallbackIsFile {
		return fallback, nil
	}
	return "", os.ErrNotExist
}

// isProgram reports whether a path names a regular file somebody may execute.
//
// Any of the three execute bits, not the caller's own: the shell's search asks
// the same question of the mode and lets the exec fail if the bit that is set
// belongs to a group the caller is not in.
func isProgram(path string) bool {
	info, err := os.Stat(path)
	return err == nil && isExecutableFile(info)
}

// isExecutableFile is the same question asked of a mode already read.
func isExecutableFile(info os.FileInfo) bool {
	return info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0
}

// InstallHint is how to install a given tool, phrased for the platform we are
// actually on (_install_hint, lib/preflight.sh:8-34).
//
// A harness takes its hint from the descriptor. A name the descriptor does not
// drive has none, and falls to the generic sentence — which is what jq's `//
// empty` leaves behind for a name it cannot select.
func (c *Checker) InstallHint(tool string) string {
	if c.darwin() {
		switch tool {
		case "gh":
			return "brew install gh"
		case "jq":
			return "brew install jq"
		case "yq":
			return "brew install yq"
		case "git":
			return "xcode-select --install"
		case "openssl":
			return "already present on macOS; otherwise brew install openssl"
		}
	} else {
		switch tool {
		case "gh":
			return "https://github.com/cli/cli#installation"
		case "jq":
			return "https://jqlang.github.io/jq/download/"
		case "yq":
			return "https://github.com/mikefarah/yq#install"
		case "git":
			return "your package manager, e.g. apt install git"
		case "openssl":
			return "your package manager, e.g. apt install openssl"
		}
	}
	if entry, found := c.Harness.For(tool); found && entry.Install.Hint != "" {
		return entry.Install.Hint
	}
	return "install " + tool
}

// darwin is `[[ "$(uname -s)" == Darwin ]]`. runtime.GOOS is the same fact
// under a different spelling: `uname -s` answers Darwin on the platform Go
// calls darwin.
func (c *Checker) darwin() bool {
	name := c.OS
	if name == "" {
		name = runtime.GOOS
	}
	return strings.EqualFold(name, "darwin")
}

// versionToken is the first version-shaped token of a tool's first output line
// (lib/preflight.sh:62). Every CLI reports itself differently, and pulling the
// token out gives one readable format.
var versionToken = regexp.MustCompile(`v?[0-9]+\.[0-9]+[0-9A-Za-z.+-]*`)

// The three outcomes of a version probe (lib/preflight.sh:44-47). They are
// separate because the fixes differ and the caller has to say which.
const (
	versionOK = iota
	// versionMissing is "not installed" — install it.
	versionMissing
	// versionSilent is "installed, but nothing version-shaped came back" —
	// find out why. It used to be a success, and printing a tool's complaint
	// as its version made every existence check pass whatever the tool did.
	versionSilent
)

// toolVersion is "<tool> <version>", or one of the two failures above
// (_tool_version, lib/preflight.sh:55-65).
func (c *Checker) toolVersion(ctx context.Context, tool string) (string, int) {
	if !c.installed(tool) {
		return "", versionMissing
	}

	// openssl's own subcommand is `openssl version`; --version came later and
	// the build on GitHub's hosted runners rejects it (lib/preflight.sh:53-54).
	args := []string{"--version"}
	if tool == "openssl" {
		args = []string{"version"}
	}

	// stderr is folded into the capture so a tool that complains still gets
	// read (lib/preflight.sh:59-60).
	result := c.runner().Run(ctx, exec.Spec{
		Path:    tool,
		Args:    args,
		Env:     c.modelFacingEnv(),
		Streams: exec.StreamsCombined,
	})

	// `| head -1`: only the first line is looked at, whatever followed it.
	raw := string(result.Stdout)
	if at := strings.IndexByte(raw, '\n'); at >= 0 {
		raw = raw[:at]
	}
	version := versionToken.FindString(raw)
	if version == "" {
		return "", versionSilent
	}
	return tool + " " + version, versionOK
}

// Check probes the tools a given command actually needs and prints a report
// (preflight_check, lib/preflight.sh:79-168).
//
// It answers false when anything required is missing, so the caller decides
// whether that is fatal: install.sh reports, a leg dies.
func (c *Checker) Check(ctx context.Context, need string) bool {
	missing := 0

	c.io().Section("Requirements")

	for _, tool := range coreTools {
		version, rc := c.toolVersion(ctx, tool)
		switch {
		case rc == versionOK && tool == "gh":
			if !c.reportGh(ctx, version) {
				missing++
			}
		case rc == versionOK:
			c.io().OK(version)
		case rc == versionSilent:
			// Present but not answering. Installing it again is the one thing
			// that will not help, so the message says so rather than reaching
			// for the hint (lib/preflight.sh:128-130).
			c.io().No(tool + " — installed, but it did not report a version. Check that it runs.")
			missing++
		default:
			c.io().No(tool + " — not found. Install with: " + c.InstallHint(tool))
			missing++
		}
	}

	if need == NeedHarness && !c.checkHarness(ctx) {
		missing++
	}

	return missing == 0
}

// reportGh proves gh usable rather than merely installed (lib/preflight.sh:91-123).
//
// Which endpoint proves it depends on what kind of token gh holds, and both
// kinds are ordinary here: a person runs the local path with a user token, and
// automated mode authenticates as a GitHub App installation on every run. `GET
// /user` answers only the first — an installation token is scoped to the
// installation, not to a user, so asking it for a user is a 403 on a credential
// that is working perfectly.
//
// So each is asked for identity at the endpoint that suits it, cheapest first,
// and rate_limit — which every token type can reach — settles the case where
// neither answers. Reaching none of the three is the only thing that means
// unauthenticated.
func (c *Checker) reportGh(ctx context.Context, version string) bool {
	if who := c.ghText(ctx, "user", ".login"); who != "" {
		c.io().OK(version + " — authenticated as " + who)
		return true
	}
	if c.ghOK(ctx, "installation/repositories", ".total_count") {
		c.io().OK(version + " — authenticated as a GitHub App installation")
		return true
	}
	if c.ghOK(ctx, "rate_limit", "") {
		c.io().OK(version + " — authenticated")
		return true
	}
	if os.Getenv("GITHUB_ACTIONS") != "" {
		// Rule 4: name the next action. A runner cannot log in interactively,
		// and nothing it could do to gh would help — the credential it was
		// handed is the thing to look at.
		c.io().No("gh — installed, but the token it was given was refused. Check the app-token the workflow passes, and that the App is still installed on this repository.")
		return false
	}
	c.io().No("gh — installed but not authenticated. Run: gh auth login")
	return false
}

// ghAPI runs one `gh api` probe. It carries the environment whole, credential
// included: this is the orchestrator asking GitHub who it is.
func (c *Checker) ghAPI(ctx context.Context, path, jq string) exec.Result {
	args := []string{"api", path}
	if jq != "" {
		args = append(args, "--jq", jq)
	}
	return c.runner().Run(ctx, exec.Spec{Path: "gh", Args: args, Env: c.env()})
}

// ghText is the answer's stdout with the trailing newlines a command
// substitution strips, or empty when the call failed.
func (c *Checker) ghText(ctx context.Context, path, jq string) string {
	result := c.ghAPI(ctx, path, jq)
	if !result.OK() {
		return ""
	}
	return strings.TrimRight(string(result.Stdout), "\n")
}

// ghOK is whether the call exited zero, which is all `>/dev/null 2>&1` leaves.
func (c *Checker) ghOK(ctx context.Context, path, jq string) bool {
	return c.ghAPI(ctx, path, jq).OK()
}

// checkHarness reports every harness the descriptor drives and answers whether
// at least one is installed (lib/preflight.sh:138-165).
func (c *Checker) checkHarness(ctx context.Context) bool {
	// jq is what reads the descriptor in the shell, so without it the probe is
	// skipped rather than reporting every harness as missing. Go reads the
	// descriptor itself and could answer anyway; skipping keeps the report the
	// same on the machine that has no jq, and that machine has already been
	// told jq is missing two lines above.
	if !c.installed("jq") {
		c.io().Opt("harness check skipped — install jq to probe installed harnesses")
		return true
	}

	found := false
	for _, name := range c.Harness.Names() {
		binary := name
		if entry, ok := c.Harness.For(name); ok && entry.Binary != "" {
			binary = entry.Binary
		}
		version, rc := c.toolVersion(ctx, binary)
		switch rc {
		case versionOK:
			c.io().OK(version)
			found = true
		case versionSilent:
			// Deliberately not counted as a harness. A CLI that will not say
			// what it is has not been shown to work, and reporting "found"
			// here means the loop discovers it at the first model invocation
			// instead (lib/preflight.sh:152-154).
			c.io().Opt(name + " — installed, but it did not report a version")
		default:
			c.io().Opt(name + " — not found, optional")
		}
	}
	if !found {
		c.io().No("no harness CLI found — CrossRev needs at least one of " + c.Harness.NamesHuman())
		return false
	}
	return true
}

// RequireYq refuses to carry on without yq (preflight_require_yq,
// lib/preflight.sh:296-300).
//
// yq reads YAML and jq reads JSON. Both config layers are YAML, so yq is not
// optional and the check says why rather than just naming the binary.
//
// The refusal is returned rather than exiting, which is what ui.IO.Die does for
// every ported ui_die. The lowercase `crossrev's` in the reason is the shell's
// own string, copied as it stands.
func (c *Checker) RequireYq() error {
	if c.installed("yq") {
		return nil
	}
	return c.io().Die(
		"yq is not installed, and crossrev's config files are YAML",
		"jq cannot read YAML. Install it with: "+c.InstallHint("yq"),
	)
}
