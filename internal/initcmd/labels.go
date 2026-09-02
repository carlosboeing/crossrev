// labels.go — the label half of `_init_execute` (lib/init.sh:439-474).
//
// Two sets with two owners. The loop's own labels carry state, so `init` mints
// them at the colours and descriptions the label policy declares and repaints
// one that drifted. The GitHub issues destination's labels are the repository's
// own taxonomy: `init` creates one that is missing so filing does not die after
// a review has already posted, and never touches the colour of one it finds.

package initcmd

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/carlosboeing/crossrev/internal/config"
	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/forge"
	"github.com/carlosboeing/crossrev/internal/policy"
)

// LabelWriter declares a label at a colour and a description, recolouring one
// that exists in another colour (gh_label_ensure, lib/github.sh:289-317).
//
// One method, and it is the only forge write the label half makes. The
// existence question is a read and stays on Request.GitHub, so the read/write
// split the package already documents holds through execution as well.
type LabelWriter interface {
	LabelEnsure(ctx context.Context, repo core.Slug, label forge.Label) (forge.LabelState, error)
}

// The colour and the description every label filed against the GitHub issues
// destination is minted with (lib/init.sh:32 and :464).
//
// They are not loop state and do not follow the loop's colour scheme, which is
// why they are here rather than in the label policy: the policy is the six the
// loop drives, and a seventh hue there would read as a state nothing sets.
const (
	backlogLabelColour      = "d4c5f9"
	backlogLabelDescription = "filed by crossrev"
)

// Labels declares every label this repository needs and says what it did
// (lib/init.sh:439-474).
//
// The declarations all run before the section is opened, which is the shell's
// order and is observable: a label that will not create ends the run with
// nothing printed under a heading promising a count.
func (p Plan) Labels(ctx context.Context, req Request, ex Execution) error {
	if ex.Labels == nil {
		return fmt.Errorf("initcmd: the execution is missing Labels")
	}

	created, existed, recoloured := 0, 0, 0
	for _, label := range p.loopLabels() {
		// Declared one at a time rather than through a batch: the state
		// word is what lets the count agree with the list, and a caller
		// that inferred it from the colour it asked for would report a
		// creation for a label that was already there.
		state, err := ex.Labels.LabelEnsure(ctx, p.Repo, forge.Label{
			Name:        label,
			Colour:      policy.LabelColour(label),
			Description: policy.LabelDescription(label),
		})
		if err != nil {
			return err
		}
		switch state {
		case forge.LabelCreated:
			created++
		case forge.LabelRecoloured:
			recoloured++
		default:
			existed++
		}
	}

	out := req.io()
	out.Section("Labels")
	out.OK(fmt.Sprintf("created %d and found %d already on %s for the loop", created, existed, p.Repo))
	if recoloured > 0 {
		out.OK(fmt.Sprintf("recoloured %d that were already there, so the label row carries state at a glance", recoloured))
	}

	return p.backlogLabelSet(ctx, req, ex)
}

// loopLabels is `$INIT_PASS_LABELS $INIT_FIXED_LABELS` (lib/init.sh:441), in
// that order: the passes the policy allows, then the five fixed states.
func (p Plan) loopLabels() []string {
	return append(append([]string(nil), p.PassLabels...), p.FixedLabels...)
}

// backlogLabelSet mints the labels filed issues carry (lib/init.sh:457-474).
//
// A tool that recoloured somebody's `bug` label because it minted one once
// would be overstepping, so a label that is already there is counted and left
// exactly as the repository set it. The existence question is asked separately
// from the declaration, which is what makes that true: LabelEnsure would
// recolour it.
func (p Plan) backlogLabelSet(ctx context.Context, req Request, ex Execution) error {
	if p.BacklogLabels == "" {
		return nil
	}

	canCreate := jqText(p.Config.Merged, ".backlog.github_issues.create_missing_labels")
	created, existed := 0, 0
	missing := ""
	for _, label := range strings.Fields(p.BacklogLabels) {
		if req.GitHub.LabelColour(ctx, p.Repo, label) != "" {
			existed++
			continue
		}
		if canCreate == "false" {
			missing += " " + label
			continue
		}
		if _, err := ex.Labels.LabelEnsure(ctx, p.Repo, forge.Label{
			Name:        label,
			Colour:      backlogLabelColour,
			Description: backlogLabelDescription,
		}); err != nil {
			return err
		}
		created++
	}

	if missing != "" {
		// A repository that governs its own label set is one where
		// inventing labels is worse than stopping.
		return req.io().Die(
			"the GitHub issues destination needs these labels and create_missing_labels is false:"+missing,
			"The repository asked CrossRev to use existing labels only. Create them by hand, or set backlog.github_issues.create_missing_labels: true.",
		)
	}
	req.io().OK(fmt.Sprintf("created %d and found %d already for filed issues", created, existed))
	return nil
}

// jqText renders one dotted path out of the merge the way `jq -r` writes it,
// which is what lib/init.sh:459 reads create_missing_labels through.
//
// Config.Get is not that reading. It goes through `// empty`, so a legitimate
// `false` comes back as the empty string and would be indistinguishable from an
// absent key — and `false` is the one value this caller acts on. GetJSON folds
// the same two together the other way. So the walk is here.
func jqText(merged *config.Object, path string) string {
	var value any = merged
	for _, segment := range strings.Split(strings.TrimPrefix(path, "."), ".") {
		object, ok := value.(*config.Object)
		if !ok {
			return "null"
		}
		if !object.Has(segment) {
			return "null"
		}
		value = object.Value(segment)
	}
	switch typed := value.(type) {
	case nil:
		return "null"
	case string:
		return typed
	case bool:
		if typed {
			return "true"
		}
		return "false"
	case config.Number:
		return string(typed)
	default:
		encoded, err := json.Marshal(typed)
		if err != nil {
			return "null"
		}
		return string(encoded)
	}
}
