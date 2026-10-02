package preflight

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/carlosboeing/crossrev/internal/exec"
)

// What `crossrev doctor` reports about the required-check gate: which checks
// a review waits for, how long it waits, and whether the token can read
// check runs at all.
//
// Silent without a configured gate: no required checks means no gate, and
// doctor's output stays exactly as it was for every repository that has
// none.
func (c *Checker) ReportVerification(ctx context.Context) bool {
	checks := c.Config.Verification().RequiredChecks
	if len(checks) == 0 {
		return true
	}
	c.io().Section("Required checks")
	c.reportVerificationGate()
	return c.reportVerificationPermission(ctx)
}

// reportVerificationGate names the required checks and the wait, each with
// whether it came from the config or the default.
func (c *Checker) reportVerificationGate() {
	names := make([]string, 0, len(c.Config.Verification().RequiredChecks))
	for _, check := range c.Config.Verification().RequiredChecks {
		names = append(names, check.String())
	}
	c.io().Line("required " + strings.Join(names, ", ") + " (from verification.required_checks): " +
		"a review waits for these before judging, and converges only when they pass")
	wait := c.Config.Verification().WaitMinutes
	source := "the default"
	if c.Config.Get(".verification.wait_minutes") != "" {
		source = "from verification.wait_minutes"
	}
	c.io().Line(fmt.Sprintf("wait %d minutes (%s): then a pass with checks still outstanding halts for a re-drive", wait, source))
}

// reportVerificationPermission probes whether the token gh holds can read
// check runs, which is the `checks: read` the gate judges with. It reads
// one run off the default branch's head: any answer, empty or not, proves
// the permission.
//
// It answers false only when the probe positively shows the token cannot
// read. An unanswered probe is unprobed rather than refused — gh may be
// missing, or this may be no checkout — and a local token is not the token
// automated mode runs with, so an absence proves nothing either way.
func (c *Checker) reportVerificationPermission(ctx context.Context) bool {
	if !c.installed("gh") {
		c.io().Opt("checks permission unprobed — gh is not installed")
		return true
	}
	slug, branch := c.verificationRepo(ctx)
	if slug == "" || branch == "" {
		c.io().Opt("checks permission unprobed — gh could not answer for this checkout")
		return true
	}
	sha := c.verificationHead(ctx, slug, branch)
	if sha == "" {
		c.io().Opt("checks permission unprobed — gh could not read the default branch")
		return true
	}
	// gh sends POST whenever a field is given, so the read names its method.
	result := c.runner().Run(ctx, exec.Spec{
		Path: "gh",
		Args: []string{"api", "--method", "GET", "repos/" + slug + "/commits/" + sha + "/check-runs",
			"-F", "per_page=1", "--jq", ".total_count"},
		Env: c.env(),
		Dir: c.Dir,
	})
	if result.OK() {
		c.io().OK("token can read check runs (checks: read)")
		return true
	}
	combined := string(result.Stdout) + "\n" + string(result.Stderr)
	if strings.Contains(combined, "(HTTP 403)") || strings.Contains(combined, "(HTTP 404)") {
		c.io().No("token cannot read check runs (checks: read is missing)")
		c.io().Line("   The loop App needs Checks: Read so the gate can judge required checks — " +
			"approve the new permission on the installation, then run this again.")
		return false
	}
	c.io().Opt("checks permission unprobed — gh could not read check runs")
	return true
}

// verificationRepo names the checkout's repository and default branch,
// which the permission probe reads one commit through.
func (c *Checker) verificationRepo(ctx context.Context) (slug, branch string) {
	result := c.runner().Run(ctx, exec.Spec{
		Path: "gh",
		Args: []string{"repo", "view", "--json", "nameWithOwner,defaultBranchRef"},
		Env:  c.env(),
		Dir:  c.Dir,
	})
	if !result.OK() {
		return "", ""
	}
	var view struct {
		NameWithOwner    string `json:"nameWithOwner"`
		DefaultBranchRef struct {
			Name string `json:"name"`
		} `json:"defaultBranchRef"`
	}
	if err := json.Unmarshal(result.Stdout, &view); err != nil {
		return "", ""
	}
	return view.NameWithOwner, view.DefaultBranchRef.Name
}

// verificationHead resolves the default branch to the commit the probe
// reads. Any commit answers the permission question; the default branch's
// head is the one that always exists.
func (c *Checker) verificationHead(ctx context.Context, slug, branch string) string {
	result := c.runner().Run(ctx, exec.Spec{
		Path: "gh",
		Args: []string{"api", "repos/" + slug + "/commits/" + branch, "--jq", ".sha"},
		Env:  c.env(),
		Dir:  c.Dir,
	})
	if !result.OK() {
		return ""
	}
	return strings.TrimSpace(string(result.Stdout))
}
