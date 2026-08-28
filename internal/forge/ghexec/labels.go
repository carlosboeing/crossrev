package ghexec

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/forge"
)

// defaultLabelColour is the grey lib/github.sh:283 falls back to.
const defaultLabelColour = "ededed"

// LabelColour is the hex a label currently carries, lowercased, or nothing if
// it does not exist.
//
// Absence is the answer rather than an error, and the shell says why: without
// its `|| true` a missing label fails the pipeline under `pipefail` and takes
// `set -e` down with it — which is every fresh repository, on the most
// consequential command CrossRev has (lib/github.sh:261-266).
//
// The read is a bare `gh api` with no --jq, so the call is byte-identical to
// the existence check it replaces. That is not a style choice: the offline
// suite matches routes on the whole argument string, and an extra flag would
// silently miss every label fixture in it.
func (c *Client) LabelColour(ctx context.Context, repo core.Slug, name string) string {
	res := c.run(ctx, "api", "repos/"+repo.String()+"/labels/"+name)
	if !answered(res) {
		return ""
	}
	var label struct {
		Color string `json:"color"`
	}
	if err := json.Unmarshal(res.Stdout, &label); err != nil {
		return ""
	}
	return strings.ToLower(label.Color)
}

// LabelEnsure declares a label at a colour and a description.
//
// The state it reports is what lets a caller reporting an inventory tell the
// truth about which it did. A plan claiming to create a label it did not create
// is the same class of lie as a count that disagrees with its own list
// (lib/github.sh:276-279).
//
// Recolouring an existing label is what makes the six loop colours need no
// migration: `init --upgrade` on a repository minted under the old single
// purple brings all six into line. A failed recolour is cosmetic rather than
// fatal, because a label with the wrong colour still drives the chain — so it
// warns and answers LabelExists.
func (c *Client) LabelEnsure(ctx context.Context, repo core.Slug, label forge.Label) (forge.LabelState, error) {
	colour := label.Colour
	if colour == "" {
		colour = defaultLabelColour
	}

	current := c.LabelColour(ctx, repo, label.Name)
	if current == "" {
		res := c.run(ctx, "api", "--method", "POST", "repos/"+repo.String()+"/labels",
			"-f", "name="+label.Name, "-f", "color="+colour, "-f", "description="+label.Description)
		if !answered(res) {
			return "", failure(fmt.Sprintf("could not create the label '%s' on %s", label.Name, repo), res)
		}
		return forge.LabelCreated, nil
	}

	if current == strings.ToLower(colour) {
		return forge.LabelExists, nil
	}

	res := c.run(ctx, "api", "--method", "PATCH", "repos/"+repo.String()+"/labels/"+label.Name,
		"-f", "color="+colour)
	if !answered(res) {
		c.warn(fmt.Sprintf("could not update the colour of '%s' on %s", label.Name, repo),
			"The label still exists and the loop still runs on it, so this is cosmetic — the pull request's label row just carries less signal than it should. Recolour it by hand, or grant the token issues write.")
		return forge.LabelExists, nil
	}
	return forge.LabelRecoloured, nil
}

// PullRequestLabelAdd applies a label to the pull request.
//
// Fatal on failure rather than cosmetic: the chain is label-driven, so any API
// failure applying a label leaves the next workflow with no event to hear.
// Absence itself is not the failure — GitHub's add-labels endpoint creates a
// missing label with default metadata (lib/state.sh:424-435).
func (c *Client) PullRequestLabelAdd(ctx context.Context, repo core.Slug, number int, label string) error {
	res := c.run(ctx, "api", "--method", "POST", issuePath(repo, number)+"/labels",
		"-f", "labels[]="+label)
	if !answered(res) {
		return failure(fmt.Sprintf("could not apply the label '%s' to %s#%d", label, repo, number), res)
	}
	return nil
}

// PullRequestLabelRemove removes a label. A label that was not there is not a
// failure, and lib/state.sh:437-440 discards the outcome.
func (c *Client) PullRequestLabelRemove(ctx context.Context, repo core.Slug, number int, label string) {
	c.run(ctx, "api", "--method", "DELETE", issuePath(repo, number)+"/labels/"+label)
}
