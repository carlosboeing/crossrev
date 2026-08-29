package ghexec

import (
	"context"
	"strings"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/forge"
)

// WorkflowRunStatus is whether an automated leg is still going.
//
// This is how a leg's liveness is knowable from any machine: the marker carries
// GITHUB_RUN_ID and this turns it into an answer. It answers unknown rather
// than guessing — a run in another repository, a token without `actions: read`,
// or no network — and every caller has to treat unknown as unknown rather than
// as finished (lib/github.sh:50-64).
func (c *Client) WorkflowRunStatus(ctx context.Context, repo core.Slug, runID string) forge.RunStatus {
	if !allDigits(runID) {
		return ""
	}
	res := c.run(ctx, "run", "view", runID, "--repo", repo.String(), "--json", "status", "--jq", ".status // empty")
	if !answered(res) {
		return ""
	}
	return forge.RunStatus(strings.TrimSpace(string(res.Stdout)))
}

// allDigits is the `[[ "$run_id" =~ ^[0-9]+$ ]]` guard at lib/github.sh:62. A
// run id is what a marker carried, so it is checked rather than trusted.
func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
