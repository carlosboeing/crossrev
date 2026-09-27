package runlog

import (
	"math"
	"strings"
	"testing"
)

// TestShannonEntropy checks the entropy gate against values computed by hand
// from the definition: mean bits per character over the runes, with a
// byte-length denominator, matching gitleaks' shannonEntropy at the pinned
// commit.
func TestShannonEntropy(t *testing.T) {
	cases := []struct {
		name  string
		in    string
		want  float64
		exact bool
	}{
		{"empty", "", 0, true},
		{"single character", "a", 0, true},
		{"one repeated character", "aaaa", 0, true},
		{"two characters", "ab", 1, true},
		{"two pairs", "aabb", 1, true},
		{"four distinct", "abcd", 2, true},
		{"three to one", "aaab", 0.8112781245, false},
		{"prefixed lookalike", fragments("npm_", strings.Repeat("a", 36)), 0.6689955936, false},
		// Two runes over four bytes: the frequency is one half, so the
		// entropy is one half a bit. This pins the byte-length denominator
		// the port inherits from gitleaks rather than correcting it.
		{"multibyte counts runes over byte length", "éé", 0.5, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := shannonEntropy(c.in)
			if c.exact {
				if got != c.want {
					t.Errorf("shannonEntropy(%q) = %v, want %v", c.in, got, c.want)
				}
				return
			}
			if math.Abs(got-c.want) > 1e-9 {
				t.Errorf("shannonEntropy(%q) = %.16f, want %.10f", c.in, got, c.want)
			}
		})
	}
}
