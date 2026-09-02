package ghexec

import (
	"context"
	"slices"
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
// runs in the shell that called it. Go passes an exact environment
// (exec.Spec.Env), so the names `gh` actually needs are written down. Every one
// of them is either documented by `gh help environment` or read by the Go
// runtime `gh` is built on:
//
//   - PATH, HOME: finding the binary and its own config.
//   - XDG_CONFIG_HOME, GH_CONFIG_DIR: where `gh auth login` left the host
//     entry, and where the offline suite points it away from a real one.
//   - GH_HOST: the enterprise host, when there is one.
//   - GH_TOKEN, GITHUB_TOKEN, GH_ENTERPRISE_TOKEN, GITHUB_ENTERPRISE_TOKEN:
//     the credential itself. `gh help environment` documents the enterprise
//     pair "in order of precedence", so a GHES operator whose token is in the
//     second name authenticates with it and no other.
//   - SSL_CERT_FILE, SSL_CERT_DIR: the trust store. crypto/x509 reads both on
//     Linux (crypto/x509/root_unix.go, which is build-tagged away on darwin),
//     `gh` is a Go program, and CI and every container are Linux. Behind a
//     TLS-inspecting proxy their absence is a total failure reported as an
//     unverifiable certificate.
//   - the proxy variables, in both cases, because Go's own
//     http.ProxyFromEnvironment reads both.
//
// # Two names left out, and both are parity differences
//
// GH_REPO is excluded, and the reason is not that every call names its
// repository — two do not. RepoSlug asks `gh` which repository the working
// directory belongs to, and ViewerLogin asks who the operator is. GH_REPO
// overrides exactly the first: with it inherited, RepoSlug answers the named
// repository rather than the checkout, and every write the run then makes lands
// there. The shell inherits it and behaves that way. Excluded, an explicit
// instruction is ignored without a word — which is the lesser fault, because it
// fails towards the repository the operator is standing in.
//
// GH_FORCE_TTY is excluded, and here Go is simply better than the shell. `gh`
// honours it by rendering for a terminal, so a run carrying it hands back JSON
// with ANSI escapes in it — into reads that unmarshal the bytes. The shell
// inherits it and would.
var ghEnvironment = []string{
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

// EnvironmentNames is the list above, for the other caller in the tree that
// builds `gh` invocations of its own.
//
// Exported rather than copied into internal/app, for the reason
// exec.ForgeCredentialNames is exported rather than copied into internal/cred:
// two lists of the same names drift apart in silence. A name added to one side
// widens or narrows only that side's environment, and the failure that follows
// arrives as an unauthenticated call, an unverifiable certificate or a
// retargeted write rather than as a missing name. internal/archtest compares
// what the two constructors pass, which is the test neither package could hold.
//
// It answers a fresh slice each time, for the reason internal/validate's asset
// accessors do: an exported slice variable is writable from any package in the
// binary, and shortening this one would narrow what `gh` receives everywhere at
// once.
func EnvironmentNames() []string { return slices.Clone(ghEnvironment) }

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
// A nil runner panics. Substituting NewOrchestratorRunner would start a
// child that may hold a forge credential, which is a wiring bug and not a
// default. Tests that want a real child pass NewOrchestratorRunner;
// tests that do not inject a fake.
//
// A nil filter is not a shortcut for "publish as written": every write refuses.
// The filter is the last inspection a body gets before it is public, and a
// missing one is exactly the case where a body might carry a credential
// (lib/log.sh:143-147).
func New(runner exec.Runner, filter forge.Publisher, opts ...Option) *Client {
	if runner == nil {
		panic("ghexec.New: runner is nil")
	}
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
// # Why the runner is orchestrator-facing
//
// NewOSRunner refuses a child whose environment names GH_TOKEN, GITHUB_TOKEN,
// GH_ENTERPRISE_TOKEN or GITHUB_ENTERPRISE_TOKEN
// (internal/exec/osrunner.go). That default is correct almost everywhere in
// the tree and wrong here: this package hands a GitHub credential to a child,
// because the child is `gh` and not a model. `gh` cannot authenticate without
// it, and lib/github.sh never sets it at any of its call sites — lines 39, 74,
// 99 and 120 among them — because a shell function inherits it ambiently from
// the orchestrator that called it. A real child is started through
// NewOrchestratorRunner; a nil runner panics rather than inventing one.
//
// The promise ADR 0001 makes is about the process that reads
// attacker-controlled text. `gh` does not read any: every argument it receives
// is built here, and no model output reaches it except as a comment body, which
// is data on its way out rather than an instruction. The model-facing side of
// the boundary is internal/harness, whose children are started through
// NewOSRunner.
//
// A test in this package fails the build if any other file constructs an
// exec.Spec.
func (c *Client) run(ctx context.Context, args ...string) exec.Result {
	return c.runner.Run(ctx, exec.Spec{
		Path: program,
		Args: args,
		Env:  c.env,
	})
}

// publishNotice is what stands in for a body the filter could not process. It
// is the text log_redact_publish prints in the same case (lib/log.sh:155).
const publishNotice = "CrossRev could not filter this text for credential shapes, so it withheld it rather than publishing it."

// publish runs a body through the injected filter.
//
// It returns the text to send: the filtered body, or — when the filter reports
// it could not process one — this package's own notice.
//
// The notice is this package's and not the filter's return value, and that is
// the whole of the difference between a contract and a guarantee. A Publisher
// is asked what is unsafe; it is not trusted to say what to send. One that
// answers `(body, err)` on failure would otherwise publish the original text
// verbatim, and the case that makes that worst is a body carrying no marker:
// `lost` is false, so nothing warns and nothing refuses, and the unfiltered
// text is simply on the pull request. The shell cannot reach that state at all
// — log_redact_publish prints the notice itself and returns non-zero, so the
// provider never holds the original (lib/log.sh:143-157) — and this is how that
// structure is kept.
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
		return publishNotice, markerBody(body), nil
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
