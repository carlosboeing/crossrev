package prstate_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/carlosboeing/crossrev/internal/prstate"
)

// TestParityAnchor drives lib/state.sh:250's state_anchor from the frozen
// oracle. The Bash function reads a file, so the fixture's `exists` arm is run
// through the same os.ReadFile refusal a caller performs: a file that is not
// there yields the empty anchor rather than a hash of nothing.
func TestParityAnchor(t *testing.T) {
	var f struct {
		Cases []struct {
			Name    string  `json:"name"`
			Line    int     `json:"line"`
			Exists  string  `json:"exists"`
			Content *string `json:"content"`
			Anchor  string  `json:"anchor"`
		} `json:"cases"`
	}
	loadFixture(t, "state_anchor.json", &f)

	for _, c := range f.Cases {
		t.Run(c.Name, func(t *testing.T) {
			recordCase("state_anchor.json", c.Name)
			path := filepath.Join(t.TempDir(), "subject.txt")
			if c.Exists == "true" {
				if c.Content == nil {
					t.Fatal("the case says the file exists and carries no content")
				}
				if err := os.WriteFile(path, []byte(*c.Content), 0o600); err != nil {
					t.Fatalf("writing the subject: %v", err)
				}
			}
			var got prstate.Anchor
			if content, err := os.ReadFile(path); err == nil {
				got = prstate.AnchorAt(content, c.Line)
			}
			if got.String() != c.Anchor {
				t.Errorf("anchor at line %d is %q, want %q", c.Line, got.String(), c.Anchor)
			}
		})
	}
}

// The window is line-2 through line+2, clamped to the first line
// (lib/state.sh:251). Lines 1, 2 and 3 all start at line 1.
func TestAnchorWindowClampsToTheFirstLine(t *testing.T) {
	content := []byte("a\nb\nc\nd\ne\nf\n")
	if prstate.AnchorAt(content, 3) != prstate.AnchorAt(content, 3) {
		t.Fatal("the anchor is not deterministic")
	}
	// Line 3's window is 1..5 and line 4's is 2..6, so they differ.
	if prstate.AnchorAt(content, 3) == prstate.AnchorAt(content, 4) {
		t.Error("the window did not move between lines 3 and 4")
	}
	// Lines 1, 2 and 3 clamp their start to 1, so only the end moves.
	if prstate.AnchorAt(content, 1) == prstate.AnchorAt(content, 2) {
		t.Error("the window end did not move between lines 1 and 2")
	}
}

// tr -d '[:space:]' under LC_ALL=C deletes the six ASCII whitespace bytes and
// nothing else, so a non-breaking space stays in the fingerprint.
func TestAnchorKeepsANonBreakingSpace(t *testing.T) {
	if prstate.AnchorAt([]byte("x\u00a0y\n"), 1) == prstate.AnchorAt([]byte("xy\n"), 1) {
		t.Error("a non-breaking space was stripped")
	}
}

func TestAnchorStripsEveryASCIIWhitespaceByte(t *testing.T) {
	if prstate.AnchorAt([]byte("a b\tc\v\fd\r\n"), 1) != prstate.AnchorAt([]byte("abcd\n"), 1) {
		t.Error("an ASCII whitespace byte survived the strip")
	}
}

// A window past the end of the file hashes the empty read rather than
// returning nothing: sed prints no lines, and shasum still answers.
func TestAnchorPastTheEndHashesTheEmptyRead(t *testing.T) {
	if got := prstate.AnchorAt([]byte("only\n"), 9); got.String() != "e3b0c442" {
		t.Errorf("got %q", got.String())
	}
}

// A file with no trailing newline still has a last line, the way sed sees one.
func TestAnchorReadsAFinalLineWithNoNewline(t *testing.T) {
	if prstate.AnchorAt([]byte("a\nb"), 1) != prstate.AnchorAt([]byte("a\nb\n"), 1) {
		t.Error("the final line was dropped when the file had no trailing newline")
	}
}
