package review

import "testing"

// TestCapitaliseName pins the Bash
// `$(printf '%s' "${h:0:1}" | tr '[:lower:]' '[:upper:]')${h:1}` at
// lib/run.sh:503, including the two edges the not-driven refusal never reaches
// on the shipped descriptor: an empty name, where `${h:0:1}` is empty and the
// expansion is the empty string, and a one-character name, where `${h:1}` is
// empty rather than out of range.
//
// It lives in an internal test file because capitaliseName is unexported and
// internal/review's other tests are package review_test, which cannot call it.
// The resolve leg carries the same function and pins it the same way.
func TestCapitaliseName(t *testing.T) {
	for _, tt := range []struct{ in, want string }{
		{"", ""},
		{"k", "K"},
		{"kimi", "Kimi"},
		{"Kimi", "Kimi"},
		{"opencode", "Opencode"},
	} {
		if got := capitaliseName(tt.in); got != tt.want {
			t.Errorf("capitaliseName(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
