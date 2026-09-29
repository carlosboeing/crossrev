package config_test

import (
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/config"
)

// The input policy defaults to hunks_first and reads whole_when_fits
// where the repository sets it.
func TestReviewInputPolicyDefaultsAndReads(t *testing.T) {
	for _, tc := range []struct {
		name   string
		yaml   string
		policy string
	}{
		{name: "absent", yaml: "version: 2\n", policy: config.ReviewInputHunksFirst},
		{name: "hunks first", yaml: "version: 2\nreview:\n  input_policy: hunks_first\n", policy: config.ReviewInputHunksFirst},
		{name: "whole when fits", yaml: "version: 2\nreview:\n  input_policy: whole_when_fits\n", policy: config.ReviewInputWholeWhenFits},
	} {
		t.Run(tc.name, func(t *testing.T) {
			loaded, err := load(t, tc.yaml)
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			if got := loaded.ReviewInputPolicy(); got != tc.policy {
				t.Errorf("ReviewInputPolicy() = %q, want %q", got, tc.policy)
			}
		})
	}
}

// A third value is refused at load rather than read leniently as hunks.
func TestReviewInputPolicyRefusesAThirdValue(t *testing.T) {
	_, err := load(t, "version: 2\nreview:\n  input_policy: whole\n")
	if err == nil {
		t.Fatal("an input_policy of whole loaded; nothing would send it")
	}
	if !strings.Contains(err.Error(), "review.input_policy") {
		t.Errorf("err = %q, want it to name review.input_policy", err)
	}
}

// A review key that holds no keys is refused the way every other
// container key is.
func TestReviewInputPolicyRefusesANonMapping(t *testing.T) {
	_, err := load(t, "version: 2\nreview: whole_when_fits\n")
	if err == nil {
		t.Fatal("a review key holding a string loaded")
	}
	if !strings.Contains(err.Error(), "review") {
		t.Errorf("err = %q, want it to name review", err)
	}
}
