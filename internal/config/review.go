package config

import (
	"errors"
	"fmt"

	"github.com/carlosboeing/crossrev/internal/core"
)

// The review input policies: how a required file reaches the reviewer.
//
// hunks_first, the default, sends every file in the Task 1 hunk form —
// whole files in full when new or small, function-context hunks past the
// size bound, header-only diffs where there is nothing to number.
// whole_when_fits sends a file whole when its rendered form fits the
// per-call budget and uses the hunk form otherwise. Splitting applies to
// anything over the budget under either policy.
const (
	ReviewInputHunksFirst    = "hunks_first"
	ReviewInputWholeWhenFits = "whole_when_fits"
)

// ReviewInputPolicy answers review.input_policy from the merged
// configuration, read from the pull request's base revision like every
// other policy key. Absent or unrecognised reads as hunks_first here;
// Load refuses the unrecognised value before any leg runs, so this
// default is reached only for the absent key.
func (c *Config) ReviewInputPolicy() string {
	if c.Get(".review.input_policy") == ReviewInputWholeWhenFits {
		return ReviewInputWholeWhenFits
	}
	return ReviewInputHunksFirst
}

// The review concerns: what the reviewer looks for. Correctness is bugs,
// consistency is the change against its surroundings.
const (
	ReviewConcernCorrectness = "correctness"
	ReviewConcernConsistency = "consistency"
)

// reviewConcernOrder is the fixed order concerns read back in, whatever
// order the config or the flag lists them in.
var reviewConcernOrder = []string{ReviewConcernCorrectness, ReviewConcernConsistency}

// The cross-model check modes: the resolver verifies each finding, or no
// second model looks at all.
const (
	ReviewCheckResolver = "resolver"
	ReviewCheckOff      = "off"
)

// NormalizeConcerns validates raw concern names — config items or a split
// flag value — and answers them in fixed order. It is the one validator
// both surfaces pass through: the config assert and the flag parser decide
// here and render the fault in their own words.
func NormalizeConcerns(items []string) ([]string, error) {
	if len(items) == 0 {
		return nil, errors.New("no concerns listed")
	}
	seen := map[string]bool{}
	for _, item := range items {
		if item == "" {
			return nil, errors.New("a concern is empty")
		}
		if item != ReviewConcernCorrectness && item != ReviewConcernConsistency {
			return nil, fmt.Errorf("unknown concern %q", item)
		}
		if seen[item] {
			return nil, fmt.Errorf("duplicate concern %q", item)
		}
		seen[item] = true
	}
	out := make([]string, 0, len(reviewConcernOrder))
	for _, concern := range reviewConcernOrder {
		if seen[concern] {
			out = append(out, concern)
		}
	}
	return out, nil
}

// NormalizeCheckMode validates a cross-model check mode from the config
// or the flag. It is the one validator both surfaces pass through.
func NormalizeCheckMode(value string) (string, error) {
	switch value {
	case ReviewCheckResolver, ReviewCheckOff:
		return value, nil
	case "":
		return "", errors.New("no check mode named")
	default:
		return "", fmt.Errorf("unknown check mode %q", value)
	}
}

// ReviewConcerns answers review.concerns from the merged configuration,
// in fixed order. Absent reads as both; Load refuses the unrecognised
// value before any leg runs, so the default below is reached only for the
// absent key and for callers that never passed through it.
func (c *Config) ReviewConcerns() []string {
	both := []string{ReviewConcernCorrectness, ReviewConcernConsistency}
	raw, ok := lookup(c.Merged, ".review.concerns").([]any)
	if !ok || len(raw) == 0 {
		return both
	}
	items := make([]string, 0, len(raw))
	for _, item := range raw {
		text, ok := item.(string)
		if !ok {
			return both
		}
		items = append(items, text)
	}
	if normalized, err := NormalizeConcerns(items); err == nil {
		return normalized
	}
	return both
}

// ReviewCheck answers review.check from the merged configuration.
// Absent or unrecognised reads as resolver here; Load refuses the
// unrecognised value before any leg runs, so this default is reached only
// for the absent key.
func (c *Config) ReviewCheck() string {
	if c.Get(".review.check") == ReviewCheckOff {
		return ReviewCheckOff
	}
	return ReviewCheckResolver
}

// ReviewContract answers the review half of the coverage identity from the
// merged configuration: the configured concerns, check mode and input
// policy beside the effective read mode the caller resolved. Resolve and
// status read this to judge a generation by the same contract the review
// leg published under.
func (c *Config) ReviewContract(readMode string) core.ReviewContract {
	return core.ReviewContract{
		Concerns:    c.ReviewConcerns(),
		Check:       c.ReviewCheck(),
		InputPolicy: c.ReviewInputPolicy(),
		ReadMode:    readMode,
	}
}

// assertReviewConcerns refuses a concerns list CrossRev does not implement.
//
// Read leniently, `concerns: [speed]` would review nothing under that name
// while the config says it reviews something, so a repository that meant
// to narrow the review would get an empty one with nothing ever saying so.
func (c *Config) assertReviewConcerns() error {
	if err := requireMappingAt(c.Merged, ".review"); err != nil {
		return err
	}
	raw := lookup(c.Merged, ".review.concerns")
	if raw == nil {
		return nil
	}
	list, ok := raw.([]any)
	if !ok {
		return &Refusal{
			Message: "review.concerns is " + shapeOf(raw) + ", which is not a list",
			Hint:    "It names what the reviewer looks for: correctness, consistency or both. Set it to a list in the repository config, or remove it to review both.",
		}
	}
	items := make([]string, 0, len(list))
	for _, item := range list {
		text, ok := item.(string)
		if !ok {
			return &Refusal{
				Message: "review.concerns holds " + shapeOf(item) + ", which is not a concern name",
				Hint:    "Each entry names one concern: correctness or consistency. Correct it where it is set, or remove review.concerns to review both.",
			}
		}
		items = append(items, text)
	}
	if _, err := NormalizeConcerns(items); err != nil {
		return &Refusal{
			Message: "review.concerns: " + err.Error(),
			Hint:    "It names what the reviewer looks for: correctness, consistency or both. Correct it where it is set, or remove it to review both.",
		}
	}
	return nil
}

// assertReviewCheck refuses a check mode CrossRev does not implement.
//
// Read leniently, `check: reviewer` would fall through to whichever branch
// is not `off`, so a repository that meant to disable the cross-model
// check would keep running it with nothing ever saying so.
func (c *Config) assertReviewCheck() error {
	if err := requireMappingAt(c.Merged, ".review"); err != nil {
		return err
	}
	raw := lookup(c.Merged, ".review.check")
	if raw == nil {
		return nil
	}
	text, ok := raw.(string)
	if !ok {
		return &Refusal{
			Message: "review.check is " + shapeOf(raw) + ", which is not resolver or off",
			Hint:    "It decides whether the resolver verifies each finding as a second model. Set it to resolver or off in the repository config, or remove it to take the default of resolver.",
		}
	}
	if _, err := NormalizeCheckMode(text); err != nil {
		return &Refusal{
			Message: "review.check: " + err.Error(),
			Hint:    "It decides whether the resolver verifies each finding as a second model. Set it to resolver or off in the repository config, or remove it to take the default of resolver.",
		}
	}
	return nil
}

// assertReviewInputPolicy refuses an input policy CrossRev does not
// implement.
//
// Read leniently, `input_policy: whole` would fall through to the hunk
// form, so a repository that meant to send whole files would review
// hunks with nothing ever saying so.
func (c *Config) assertReviewInputPolicy() error {
	if err := requireMappingAt(c.Merged, ".review"); err != nil {
		return err
	}
	switch value := c.Get(".review.input_policy"); value {
	case "", ReviewInputHunksFirst, ReviewInputWholeWhenFits:
		return nil
	default:
		return &Refusal{
			Message: "review.input_policy is '" + named(value) + "', which is not one of hunks_first or whole_when_fits",
			Hint:    "It decides whether the reviewer reads whole files wherever they fit or hunk-shaped input first. Set it to hunks_first or whole_when_fits in the repository config, or remove it to take the default of hunks_first.",
		}
	}
}
