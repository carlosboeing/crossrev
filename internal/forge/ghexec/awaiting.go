package ghexec

import (
	"context"
	"encoding/json"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/forge"
)

// awaitingJQ is the filter the watchdog's read carries (lib/run.sh:3692-3693).
//
// The newline and the eleven-space continuation are the shell's own bytes.
// They are kept because `gh` hands the string to jq unaltered and the offline
// suite's fake gh runs it with `jq -r "$jq_expr"`, so the two sides can only be
// compared if the program is the same program. jq ignores the layout; the
// comparison does not.
const awaitingJQ = "[.[] | select([.labels[].name] | any(startswith(\"crossrev/awaiting-\")))\n" +
	"           | {number, labels: [.labels[].name], head: .head.sha, draft}]"

// AwaitingPullRequests is every open pull request carrying a
// `crossrev/awaiting-` label (lib/run.sh:3691-3693).
//
// A failed read answers as none. The shell writes `2>/dev/null)" || stuck="[]"`,
// so it cannot tell an unreachable API from a repository with nothing waiting,
// and neither can a caller here. Output that is not the array the filter
// promises is the same case: the shell would hand it to `jq 'length'`, which
// prints nothing, and the loop would run zero times.
func (c *Client) AwaitingPullRequests(ctx context.Context, repo core.Slug) []forge.AwaitingPullRequest {
	res := c.run(ctx, "api", "repos/"+repo.String()+"/pulls?state=open&per_page=100",
		"--jq", awaitingJQ)
	if !answered(res) {
		return nil
	}
	var rows []struct {
		Number int      `json:"number"`
		Labels []string `json:"labels"`
		Head   string   `json:"head"`
		Draft  bool     `json:"draft"`
	}
	if err := json.Unmarshal(res.Stdout, &rows); err != nil {
		return nil
	}
	var out []forge.AwaitingPullRequest
	for _, row := range rows {
		out = append(out, forge.AwaitingPullRequest{
			Number:  row.Number,
			Labels:  row.Labels,
			HeadSHA: row.Head,
			Draft:   row.Draft,
		})
	}
	return out
}
