package policy

import (
	"fmt"
	"strings"

	"github.com/carlosboeing/crossrev/internal/core"
)

// Refusal is a guard's decision to stop, carrying the two strings `ui_die`
// prints: what happened, and what to do about it. A guard that returned only a
// message would drop half of what an operator reads.
type Refusal struct {
	Message string
	Hint    string
}

// Error renders the message.
func (r *Refusal) Error() string { return r.Message }

// Flag is a boolean read off pull request metadata, in the three states the push
// guard distinguishes: explicitly true, explicitly false, and unreadable.
//
// lib/legs.sh:449-451 uses `${n-…}` rather than `${n:-…}` for exactly this: an
// empty cross-repository flag has to stay empty, because "false" means "this
// repository's own branch" and would skip the maintainer-edit check.
type Flag string

// The three states a Flag distinguishes.
const (
	FlagTrue    Flag = "true"
	FlagFalse   Flag = "false"
	FlagUnknown Flag = ""
)

// PushTarget is the pull request state AssertPushTarget decides over.
type PushTarget struct {
	Current             core.Revision
	Head                core.Revision
	HeadBranch          string
	DefaultBranch       string
	HeadRepo            core.Slug
	OriginRepo          core.Slug
	MaintainerCanModify Flag
	CrossRepo           Flag
}

// AssertPushTarget refuses a push anywhere except the pull request's own head
// revision and repository (lib/legs.sh:447-479).
//
// Branch protection is a backstop, not a control: it fires after a bad push is
// attempted and says nothing about which branch was targeted. This asserts the
// target before anything leaves the machine.
func AssertPushTarget(t PushTarget) error {
	if !t.Current.Equal(t.Head) {
		return &Refusal{
			Message: fmt.Sprintf("the tree is at revision '%s' but the pull request is at '%s'",
				t.Current.SHA(), t.Head.SHA()),
			Hint: "crossrev pushes only to the revision under review.",
		}
	}
	if t.HeadBranch == t.DefaultBranch {
		return &Refusal{
			Message: fmt.Sprintf("the pull request's head branch is the repository default branch ('%s')",
				t.DefaultBranch),
			Hint: "crossrev refuses to push to a default branch. Re-open the pull request from a feature branch.",
		}
	}
	if t.HeadRepo == (core.Slug{}) {
		return &Refusal{
			Message: "could not determine the head repository for this pull request",
			Hint:    "The pull request metadata is missing head repository information.",
		}
	}
	if t.HeadRepo != t.OriginRepo {
		return &Refusal{
			Message: fmt.Sprintf("the pull request's head is in '%s' but this checkout pushes to '%s'",
				t.HeadRepo, t.OriginRepo),
			Hint: "crossrev pushes only to the head repository of the pull request under review.",
		}
	}
	// Anything other than an explicit false — a fork, or provenance that could
	// not be read — needs the contributor's permission before a push.
	if t.CrossRepo != FlagFalse && t.MaintainerCanModify != FlagTrue {
		return &Refusal{
			Message: fmt.Sprintf("the contributor has not allowed maintainer edits on %s", t.HeadRepo),
			Hint:    "The contributor has not allowed maintainer edits on this pull request, so the fix cannot be pushed.",
		}
	}
	return nil
}

// EndpointVariables are the variables that redirect a harness process-wide, in
// the order lib/legs.sh:491-492 checks them.
var EndpointVariables = []string{"ANTHROPIC_BASE_URL", "ANTHROPIC_AUTH_TOKEN"}

// AssertEnvClean refuses an inherited environment carrying either endpoint
// variable with a value (lib/legs.sh:490-497).
//
// This is layer one of the divergence guard and the specific failure it exists
// for: the variables are process-scoped, so a leg that leaks one silently
// redirects the other leg too, and the loop completes normally with the
// cross-model property gone and no error anywhere.
//
// The environment is passed in rather than read here. policy is pure, and
// internal/exec/env.go is the only file allowed to call os.Environ.
func AssertEnvClean(env map[string]string) error {
	var leaked []string
	for _, name := range EndpointVariables {
		if env[name] != "" {
			leaked = append(leaked, name)
		}
	}
	if len(leaked) == 0 {
		return nil
	}
	return &Refusal{
		Message: "these endpoint variables are set in the environment CrossRev inherited: " +
			strings.Join(leaked, " "),
		Hint: "They redirect the harness process-wide, so a leg would silently run on the wrong model and the loop would complete normally with no error anywhere. Unset them; crossrev sets them per invocation.",
	}
}

// LegSettings is the triple the orchestrator controls for one leg: what it
// invoked, where it pointed it, and which model it asked for.
type LegSettings struct {
	Harness  core.HarnessName
	Endpoint string
	Model    string
}

// Difference is what ConfiguredDifference answers.
type Difference string

// The two words lib/legs.sh:531-539 prints.
const (
	DifferenceSame      Difference = "same"
	DifferenceDifferent Difference = "different"
)

// String renders the word.
func (d Difference) String() string { return string(d) }

// ConfiguredDifference reports whether the two legs differ in anything the
// orchestrator controls (lib/legs.sh:531-539).
//
// The comparison reads what each side actually ran, not what the config says,
// because --harness rewrites legs after the config is read.
func ConfiguredDifference(reviewer, resolver LegSettings) Difference {
	if reviewer != resolver {
		return DifferenceDifferent
	}
	return DifferenceSame
}

// AssertModelsDiverged refuses when two legs configured to differ were answered
// by the same model (lib/legs.sh:548-555).
//
// Layer two, where the harness reports it. Do not halt merely because a harness
// reports none — that would disqualify the codex adapter for a field Codex does
// not emit. What this adds is detection of server-side substitution; where it is
// unavailable the marker records its absence rather than implying a check that
// never ran.
//
// The literal string "null" counts as absent because the caller reads the field
// with jq's `// "null"` default (lib/run.sh:2142).
func AssertModelsDiverged(configured Difference, reviewerModel, resolverModel string) error {
	if configured != DifferenceDifferent {
		return nil // one model was asked for
	}
	if !modelReported(reviewerModel) || !modelReported(resolverModel) {
		return nil
	}
	if reviewerModel != resolverModel {
		return nil
	}
	return &Refusal{
		Message: "both legs were configured to differ but the same model answered each: " + reviewerModel,
		Hint:    "This is the failure the cross-model design exists to prevent, and it completes normally when unchecked. Check the endpoint block and that no endpoint variable is exported.",
	}
}

// modelReported reports whether a harness named the model that answered.
func modelReported(model string) bool { return model != "" && model != "null" }
