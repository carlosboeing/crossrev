package preflight

import (
	"context"
	"strings"

	"github.com/carlosboeing/crossrev/internal/config"
	"github.com/carlosboeing/crossrev/internal/exec"
)

// What `crossrev doctor` reports about the coverage ledger: the store in
// force and why, the namespace it would write, the overflow behaviour, what
// the token can do, and the resolved reviewer.
//
// The report states what it cannot prove alongside what it can. A token's
// stated permissions and its actual ones diverge often enough that the leg
// determines availability by attempting the write; doctor, which must not
// write, probes the permission and says plainly that the namespace is beyond
// what a probe reaches.
func (c *Checker) ReportCoverage(ctx context.Context) bool {
	c.io().Section("Coverage ledger")
	cov := c.Config.Coverage()
	c.reportCoverageStore(cov)
	c.reportCoverageHost()
	ok := c.reportCoveragePermission(ctx)
	c.reportCoverageReviewer()
	return ok
}

// reportCoverageStore names the configured store, the namespace it would
// write, and the overflow behaviour — each with whether it came from the
// config or the default, because an operator who expected refs and got the
// fallback should learn it here.
func (c *Checker) reportCoverageStore(cov config.Coverage) {
	source := "the default"
	if c.Config.Get(".coverage.store") != "" {
		source = "from coverage.store"
	}
	var meaning string
	switch cov.Store {
	case "refs":
		meaning = "requires git refs, and a refused write fails the pass loudly rather than degrading silently"
	case "marker":
		meaning = "never touches refs at all — every generation rides inside the marker comment"
	default:
		meaning = "tries git refs first, and falls back to the marker comment when a ref write is refused"
	}
	c.io().Line("store " + cov.Store + " (" + source + "): " + meaning)
	if cov.Store != "marker" {
		c.io().Line("namespace " + cov.RefNamespace + " — one ref per pull request per reviewer, " +
			cov.RefNamespace + "/pr/<number>/<slot>/coverage")
	}
	overflowSource := "the default"
	if c.Config.Get(".coverage.on_overflow") != "" {
		overflowSource = "from coverage.on_overflow"
	}
	var overflowMeaning string
	if cov.OnOverflow == "halt" {
		overflowMeaning = "a marker comment past 64 KiB sheds its predecessor, then halts the pass with ledger_exhausted"
	} else {
		overflowMeaning = "a marker comment past 64 KiB sheds its predecessor, then compacts the current generation to counts, then halts"
	}
	c.io().Line("on_overflow " + cov.OnOverflow + " (" + overflowSource + "): " + overflowMeaning)
}

// reportCoverageHost detects GitHub Enterprise Server and names it as
// unproven: API-compatible in principle, version floor unestablished, this
// path unproven there. Unproven is reported, never failed.
func (c *Checker) reportCoverageHost() {
	host := ""
	for _, entry := range c.env() {
		if name, value, found := strings.Cut(entry, "="); found && name == "GH_HOST" {
			host = value
		}
	}
	if host == "" || isGitHubCloud(host) {
		return
	}
	c.io().Line("this checkout talks to " + host + " (GitHub Enterprise Server): the ref path is " +
		"unproven there — store: marker is the safe setting until someone proves otherwise")
}

// isGitHubCloud reports the one host the ref path has been watched run
// against. GH_HOST names the enterprise host when there is one, and is
// empty on github.com; an explicit github.com is the same cloud.
func isGitHubCloud(host string) bool {
	return strings.ToLower(strings.TrimSuffix(host, ".")) == "github.com"
}

// reportCoveragePermission probes whether the token gh holds can push to
// this checkout's repository, which is the contents: write the loop needs:
// the resolve leg pushes fixes, and the review leg publishes coverage.
//
// It answers false only when the probe positively shows the token cannot
// push. An unanswered probe is unprobed rather than refused — gh may be
// missing, or this may be no checkout — and a local token is not the token
// automated mode runs with, so an absence proves nothing either way.
func (c *Checker) reportCoveragePermission(ctx context.Context) bool {
	if !c.installed("gh") {
		c.io().Opt("permission unprobed — gh is not installed")
		return true
	}
	// viewerPermission is what gh itself resolves the checkout's remote
	// against, so no slug has to be parsed out of it here. ADMIN, MAINTAIN
	// and WRITE can push; TRIAGE and READ cannot.
	result := c.runner().Run(ctx, exec.Spec{
		Path: "gh",
		Args: []string{"repo", "view", "--json", "viewerPermission", "--jq", ".viewerPermission"},
		Env:  c.env(),
		Dir:  c.Dir,
	})
	if !result.OK() {
		c.io().Opt("permission unprobed — gh could not answer for this checkout")
		return true
	}
	permission := strings.TrimSpace(string(result.Stdout))
	if permission == "" {
		c.io().Opt("permission unprobed — gh answered with no permission")
		return true
	}
	switch strings.ToUpper(permission) {
	case "ADMIN", "MAINTAIN", "WRITE":
		c.io().OK("token can push to this repository (contents: write)")
		c.io().Line("   This proves the permission, not the namespace: an organisation ruleset " +
			"restricts ref creation, and only an attempted ref write discovers one.")
		return true
	default:
		c.io().No("token cannot push to this repository (viewerPermission " + permission + ")")
		c.io().Line("   The loop needs contents: write — the resolve leg pushes fixes, and the review leg publishes coverage.")
		return false
	}
}

// reportCoverageReviewer names the resolved reviewer by id and harness, so
// a plural configuration's effect is visible before a leg runs.
func (c *Checker) reportCoverageReviewer() {
	for _, reviewer := range c.Config.Reviewers() {
		c.io().Line("reviewer " + reviewer.ID + " on " + reviewer.Harness)
	}
}
