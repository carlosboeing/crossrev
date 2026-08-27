package prstate

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// anchorWidth is the digest cut lib/state.sh:253 takes: the first 8 characters
// of a lowercase hexadecimal sha256.
const anchorWidth = 8

// anchorContext is how far the window reaches either side of the line.
const anchorContext = 2

// Anchor is the fingerprint of a commented line and its neighbours.
//
// It is what lets a finding still be matched after the line moves, and it is
// the third field of the finding's identity. The empty Anchor is a real value:
// lib/state.sh:249 returns it for a file that is not there, and the finding id
// derived from it is still a valid id.
type Anchor string

// String renders the anchor as a marker holds it.
func (a Anchor) String() string { return string(a) }

// AnchorAt fingerprints the five-line window around line
// (lib/state.sh:248-254).
//
// The window is line-2 through line+2, with the start clamped to the first
// line, so lines 1, 2 and 3 all start at line 1. Every ASCII whitespace byte is
// removed before hashing, and only those six: LC_ALL=C is pinned for the same
// reason the identity fold is, and a non-breaking space stays in the
// fingerprint.
//
// A window past the end of the file hashes the empty read rather than
// answering nothing. Only a file that could not be read at all yields the empty
// Anchor, and that refusal belongs to the caller: this function takes bytes.
func AnchorAt(content []byte, line int) Anchor {
	start := line - anchorContext
	if start < 1 {
		start = 1
	}
	end := line + anchorContext

	var window strings.Builder
	for number, text := range lines(content) {
		if number < start {
			continue
		}
		if number > end {
			break
		}
		for i := 0; i < len(text); i++ {
			if !isASCIISpace(text[i]) {
				window.WriteByte(text[i])
			}
		}
	}
	sum := sha256.Sum256([]byte(window.String()))
	return Anchor(hex.EncodeToString(sum[:])[:anchorWidth])
}

// lines yields the file's lines the way sed counts them, numbered from one.
//
// A trailing newline ends the last line rather than starting an empty one, and
// a file with no trailing newline still has a last line.
func lines(content []byte) func(func(int, string) bool) {
	return func(yield func(int, string) bool) {
		text := string(content)
		number := 0
		for len(text) > 0 {
			number++
			i := strings.IndexByte(text, '\n')
			if i < 0 {
				yield(number, text)
				return
			}
			if !yield(number, text[:i]) {
				return
			}
			text = text[i+1:]
		}
	}
}
