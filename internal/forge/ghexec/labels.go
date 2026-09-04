package ghexec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/forge"
)

// defaultLabelColour is the grey lib/github.sh:292 falls back to.
const defaultLabelColour = "ededed"

// errLabelName is what a name that cannot be put in a URL path is refused with.
var errLabelName = errors.New("a label name may not contain a path segment of . or ..")

// labelPath renders a label name as the last part of a `gh api` path.
//
// A label name is configuration read from the base revision (ADR 0003), and it
// reaches three paths here: the colour read, the recolour, and the removal —
// two of them writes. lib/github.sh interpolates it raw, so `../../../orgs/x`
// leaves the repository the caller named and asks for something else entirely.
//
// # Why this escapes per segment rather than escaping the whole name
//
// url.PathEscape on the whole name would close the traversal, but only as a
// side effect of escaping the separator: CrossRev's own labels are
// `crossrev/pass-1` and `crossrev/awaiting-review`, so every label call would
// start sending `%2F` where the shell sends `/`. That is a changed request on a
// live write path, measured against nothing.
//
// Traversal is a validation question rather than an escaping one — PathEscape
// leaves `.` and `..` exactly as they are — so the two are handled separately.
// Each segment is escaped for what it is, a segment, and a segment that is `.`
// or `..` or empty is refused. A name with no character needing an escape comes
// out byte-identical to what the shell sends, which is every label the tool
// itself declares; `C#` comes out as `C%23`, where the shell sends a `#` that
// starts a URL fragment and asks for the wrong label.
func labelPath(name string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("%w: the name is empty", errLabelName)
	}
	segments := strings.Split(name, "/")
	for i, segment := range segments {
		switch segment {
		case "", ".", "..":
			return "", fmt.Errorf("%w: %q", errLabelName, name)
		}
		segments[i] = url.PathEscape(segment)
	}
	return strings.Join(segments, "/"), nil
}

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
	path, err := labelPath(name)
	if err != nil {
		// A name that cannot be asked for reads as absent, which is the same
		// answer this gives for a label that is not there. LabelEnsure refuses
		// it below rather than creating one from it.
		return ""
	}
	res := c.run(ctx, "api", "repos/"+repo.String()+"/labels/"+path)
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
	path, err := labelPath(label.Name)
	if err != nil {
		return "", fmt.Errorf("could not declare a label on %s: %w", repo, err)
	}

	colour := label.Colour
	if colour == "" {
		colour = defaultLabelColour
	}

	current := c.LabelColour(ctx, repo, label.Name)
	if current == "" {
		res := c.run(ctx, "api", "--method", "POST", "repos/"+repo.String()+"/labels",
			"-f", "name="+label.Name, "-f", "color="+colour, "-f", "description="+label.Description)
		if !answered(res) {
			return "", failure(fmt.Sprintf("could not create the label '%s' on %s\n   Init could not establish the declared colour and description. Create it by hand, or grant the token issues write.", label.Name, repo), res)
		}
		return forge.LabelCreated, nil
	}

	if current == strings.ToLower(colour) {
		return forge.LabelExists, nil
	}

	res := c.run(ctx, "api", "--method", "PATCH", "repos/"+repo.String()+"/labels/"+path,
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
// failure, and lib/state.sh:436-439 discards the outcome.
func (c *Client) PullRequestLabelRemove(ctx context.Context, repo core.Slug, number int, label string) {
	path, err := labelPath(label)
	if err != nil {
		// Nothing to report to, and nothing safe to ask for. A label that
		// cannot be named cannot have been applied either.
		return
	}
	c.run(ctx, "api", "--method", "DELETE", issuePath(repo, number)+"/labels/"+path)
}
