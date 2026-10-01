package ghexec

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/exec"
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

// checkRunsPerPage is the page size the check-run enumeration asks for,
// the endpoint's maximum.
const checkRunsPerPage = 100

// CheckRuns is every check run GitHub reports for a commit.
//
// One page per call rather than `--paginate`: the endpoint answers an
// object carrying `total_count` beside its runs, and --paginate
// concatenates arrays, so a paginated read would need the same loop to
// compare the total against what arrived. A page that will not parse fails
// the read rather than ending it with a partial list — decodePages keeps
// what came before, which is the right answer for comments and the wrong
// one for a gate.
func (c *Client) CheckRuns(ctx context.Context, repo core.Slug, head core.Revision) (forge.CheckRuns, error) {
	var out forge.CheckRuns
	total := 0
	for page := 1; ; page++ {
		res := c.run(ctx, "api", "repos/"+repo.String()+"/commits/"+head.SHA()+"/check-runs",
			"-F", "per_page="+strconv.Itoa(checkRunsPerPage),
			"-F", "page="+strconv.Itoa(page))
		if !answered(res) {
			return forge.CheckRuns{}, checkRunsFailure(res)
		}
		var decoded struct {
			TotalCount int `json:"total_count"`
			Runs       []struct {
				ID         int64  `json:"id"`
				Name       string `json:"name"`
				Status     string `json:"status"`
				Conclusion string `json:"conclusion"`
				URL        string `json:"html_url"`
				App        struct {
					Slug string `json:"slug"`
				} `json:"app"`
			} `json:"check_runs"`
		}
		if err := json.Unmarshal(res.Stdout, &decoded); err != nil {
			return forge.CheckRuns{}, fmt.Errorf("could not read check runs: %w", err)
		}
		if decoded.TotalCount > total {
			total = decoded.TotalCount
		}
		for _, run := range decoded.Runs {
			out.Runs = append(out.Runs, forge.CheckRun{
				ID:         run.ID,
				Name:       run.Name,
				App:        run.App.Slug,
				Status:     run.Status,
				Conclusion: run.Conclusion,
				URL:        run.URL,
			})
		}
		if len(decoded.Runs) < checkRunsPerPage {
			break
		}
	}
	out.Truncated = total > len(out.Runs)
	return out, nil
}

// checkRunsFailure classifies a refused check-run read: a 403 or 404 names
// the missing `checks: read` permission, and anything else stays the
// ordinary error that fails loudly. It reads the captured streams the way
// permissionRefused does, and withholds them the way failure does.
func checkRunsFailure(res exec.Result) error {
	base := failure("could not read check runs", res)
	if res.Err != nil {
		return base
	}
	combined := string(res.Stdout) + "\n" + string(res.Stderr)
	for _, status := range []int{403, 404} {
		if strings.Contains(combined, "(HTTP "+strconv.Itoa(status)+")") {
			return &forge.CheckRunsDenied{Status: status, Err: base}
		}
	}
	return base
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
