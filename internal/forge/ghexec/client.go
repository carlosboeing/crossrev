package ghexec

import (
	"context"
	"strings"

	"github.com/carlosboeing/crossrev/internal/exec"
	"github.com/carlosboeing/crossrev/internal/forge"
	"github.com/carlosboeing/crossrev/internal/prstate"
)

// program is the CLI this package drives. A bare name, resolved on the PATH of
// the calling process, which is what the offline suite relies on: it puts
// tests/stub/gh earlier on the PATH and every call here finds the stub.
const program = "gh"

// ghEnvironment is what `gh` is allowed to inherit.
//
// The Bash boundary hands `gh` the whole environment, because a shell function
// runs in the shell that called it. Go passes an exact environment instead
// (exec.Spec.Env), so the names `gh` actually needs are written down:
//
//   - PATH, HOME: finding the binary and its own config.
//   - XDG_CONFIG_HOME, GH_CONFIG_DIR: where `gh auth login` left the host
//     entry, and where the offline suite points it away from a real one.
//   - GH_HOST: the enterprise host, when there is one.
//   - GH_TOKEN, GITHUB_TOKEN, GH_ENTERPRISE_TOKEN: the credential itself.
//   - the proxy variables, in both cases, because Go's own
//     http.ProxyFromEnvironment reads both and `gh` is a Go program.
//
// GH_REPO is deliberately absent. Every call names its repository, and an
// inherited default would silently retarget the ones that do not.
var ghEnvironment = []string{
	"PATH",
	"HOME",
	"XDG_CONFIG_HOME",
	"GH_CONFIG_DIR",
	"GH_HOST",
	"GH_TOKEN",
	"GITHUB_TOKEN",
	"GH_ENTERPRISE_TOKEN",
	"HTTP_PROXY",
	"HTTPS_PROXY",
	"NO_PROXY",
	"http_proxy",
	"https_proxy",
	"no_proxy",
}

// Client is the `gh`-on-PATH implementation of forge.Forge.
type Client struct {
	runner exec.Runner
	env    []string
	filter forge.Publisher
	warn   func(summary, detail string)
}

var _ forge.Forge = (*Client)(nil)

// Option adjusts a Client at construction.
type Option func(*Client)

// WithEnv replaces the environment `gh` receives. The default is the allowlist
// above, read from this process.
func WithEnv(env []string) Option {
	return func(c *Client) { c.env = env }
}

// WithWarn installs the report for a fact with no other route out.
//
// That is the rule, and it is worth stating as one rather than as the list
// below: a fact already carried in a returned value never comes through here.
// The caller has to read that value to know what happened, so a warning beside
// it reports the same thing twice. The anchor fallback settles it — Placement
// names where the comment landed, and the fallback is the common path rather
// than the edge case, so a provider warning there would double-report on every
// one of them.
//
// lib/github.sh calls ui_warn at six places. Four are reported to the caller as
// a returned error or a Placement, and are left to it. The two with no other
// route go through here: a body whose marker was withheld by the publish
// filter, and a label that exists but could not be recoloured. Both continue
// rather than fail, so without this they would be silent.
//
// The default is a no-op, which makes a Client with no reporter quiet rather
// than broken.
func WithWarn(warn func(summary, detail string)) Option {
	return func(c *Client) { c.warn = warn }
}

// New returns a Client that runs `gh` through runner and filters every
// published body through filter.
//
// A nil filter is not a shortcut for "publish as written": every write refuses.
// The filter is the last inspection a body gets before it is public, and a
// missing one is exactly the case where a body might carry a credential
// (lib/log.sh:143-147).
func New(runner exec.Runner, filter forge.Publisher, opts ...Option) *Client {
	c := &Client{
		runner: runner,
		filter: filter,
		env:    exec.Inherit(ghEnvironment),
		warn:   func(string, string) {},
	}
	for _, opt := range opts {
		opt(c)
	}
	if c.warn == nil {
		c.warn = func(string, string) {}
	}
	return c
}

// run invokes `gh` with exactly these arguments.
//
// # Why the orchestrator audience is set here
//
// exec.Spec.Audience defaults to model-facing, and exec.Run refuses a
// model-facing spec whose environment names GH_TOKEN, GITHUB_TOKEN or
// GH_ENTERPRISE_TOKEN (internal/exec/osrunner.go:53-64). That default is
// correct everywhere else in the tree and wrong exactly here: this is the one
// package that legitimately hands a GitHub credential to a child, because the
// child is `gh` and not a model. `gh` cannot authenticate without it, and
// lib/github.sh never sets it at any of its call sites — lines 39, 74, 99 and
// 120 among them — because a shell function inherits it ambiently from the
// orchestrator that called it. This is that inheritance, written down.
//
// The promise ADR 0001 makes is about the process that reads
// attacker-controlled text. `gh` does not read any: every argument it receives
// is built here, and no model output reaches it except as a comment body, which
// is data on its way out rather than an instruction. The model-facing side of
// the boundary is internal/harness, and its specs keep the strict default.
//
// It is set in this one function so that adding a method cannot forget it. A
// test in this package fails the build if any other file constructs an
// exec.Spec.
func (c *Client) run(ctx context.Context, args ...string) exec.Result {
	return c.runner.Run(ctx, exec.Spec{
		Path:     program,
		Args:     args,
		Env:      c.env,
		Audience: exec.AudienceOrchestrator,
	})
}

// publish runs a body through the injected filter.
//
// It returns the text to send: the filtered body, or — when the filter could
// not process it — the notice that stands in for it, exactly as
// log_redact_publish prints it (lib/log.sh:153-157).
//
// lost says the filter failed on a body carrying a marker, which is the split
// lib/github.sh:166-186 draws. The marker is what CrossRev reads its own state
// back from (ADR 0002), so a notice standing in for one loses the record rather
// than masking it. The writes whose comment holds the pass marker refuse on it;
// the four that degrade warn and publish the notice.
//
// The error is reserved for having no filter at all, where nothing may be
// published.
func (c *Client) publish(body string) (send string, lost bool, err error) {
	if c.filter == nil {
		return "", false, errNoFilter
	}
	filtered, filterErr := c.filter.Filter(body)
	if filterErr != nil {
		return filtered, markerBody(body), nil
	}
	return filtered, false, nil
}

// markerBody reports that a body carries one of CrossRev's own markers
// (lib/github.sh:166-171).
//
// The prefix comes from internal/prstate rather than from a second spelling
// here, because it is the same delimiter the marker readers split on and one
// of the two would eventually be edited alone.
func markerBody(body string) bool {
	return strings.Contains(body, prstate.MarkerPrefix)
}
