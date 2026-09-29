package config

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
