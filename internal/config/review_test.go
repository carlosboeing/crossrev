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

// Concerns default to both, and read back in fixed order whatever order
// the config lists them in.
func TestReviewConcernsDefaultAndFixedOrder(t *testing.T) {
	for _, tc := range []struct {
		name string
		yaml string
		want []string
	}{
		{name: "absent", yaml: "version: 2\n", want: []string{"correctness", "consistency"}},
		{name: "both", yaml: "version: 2\nreview:\n  concerns: [correctness, consistency]\n", want: []string{"correctness", "consistency"}},
		{name: "reversed", yaml: "version: 2\nreview:\n  concerns: [consistency, correctness]\n", want: []string{"correctness", "consistency"}},
		{name: "one", yaml: "version: 2\nreview:\n  concerns: [consistency]\n", want: []string{"consistency"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := loadYAML(t, tc.yaml).ReviewConcerns()
			if len(got) != len(tc.want) {
				t.Fatalf("ReviewConcerns() = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("ReviewConcerns() = %v, want %v", got, tc.want)
				}
			}
		})
	}
}

// Unknown, duplicate, empty and non-list concerns are refused at load.
func TestReviewConcernsRefusals(t *testing.T) {
	for _, tc := range []struct {
		name string
		yaml string
	}{
		{name: "unknown", yaml: "version: 2\nreview:\n  concerns: [correctness, speed]\n"},
		{name: "duplicate", yaml: "version: 2\nreview:\n  concerns: [correctness, correctness]\n"},
		{name: "empty list", yaml: "version: 2\nreview:\n  concerns: []\n"},
		{name: "empty item", yaml: "version: 2\nreview:\n  concerns: [correctness, '']\n"},
		{name: "non-list", yaml: "version: 2\nreview:\n  concerns: correctness\n"},
		{name: "non-string item", yaml: "version: 2\nreview:\n  concerns: [correctness, 7]\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := load(t, tc.yaml); err == nil {
				t.Fatalf("loaded %s, want a refusal", tc.name)
			} else if !strings.Contains(err.Error(), "review.concerns") {
				t.Errorf("err = %q, want it to name review.concerns", err)
			}
		})
	}
}

// The cross-model check defaults to the resolver and reads off.
func TestReviewCheckDefaultsAndReads(t *testing.T) {
	for _, tc := range []struct {
		name  string
		yaml  string
		check string
	}{
		{name: "absent", yaml: "version: 2\n", check: config.ReviewCheckResolver},
		{name: "resolver", yaml: "version: 2\nreview:\n  check: resolver\n", check: config.ReviewCheckResolver},
		{name: "off", yaml: "version: 2\nreview:\n  check: off\n", check: config.ReviewCheckOff},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := loadYAML(t, tc.yaml).ReviewCheck(); got != tc.check {
				t.Errorf("ReviewCheck() = %q, want %q", got, tc.check)
			}
		})
	}
}

// A third check mode is refused at load rather than read leniently.
func TestReviewCheckRefusesAThirdValue(t *testing.T) {
	_, err := load(t, "version: 2\nreview:\n  check: reviewer\n")
	if err == nil {
		t.Fatal("a check of reviewer loaded; nothing would run it")
	}
	if !strings.Contains(err.Error(), "review.check") {
		t.Errorf("err = %q, want it to name review.check", err)
	}
}
