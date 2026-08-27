package prstate

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/carlosboeing/crossrev/internal/core"
)

// findingIDWidth is the digest cut lib/state.sh:242 takes: the first 16
// characters of a lowercase hexadecimal sha256.
const findingIDWidth = 16

// ErrFindingID is returned for a string the hash could not have produced.
var ErrFindingID = errors.New("a finding id is 16 lowercase hexadecimal characters")

// NewFindingID derives a finding's identity from its path, its normalised
// title and its anchor (lib/state.sh:238-243).
//
// The identity has to be stable across passes, because "already posted" is a
// set-membership test rather than a guess. The anchor is what still matches a
// finding after its line moves.
//
// The ordering rule is snap the line, then derive the id. Deriving first and
// correcting the line after makes it a different finding on the next pass.
//
// This is the one place in the codebase that mints a core.FindingID, and
// internal/archtest/findingid_test.go enforces that. A second implementation of
// this hash would produce an id that looks like a new finding and gets posted
// again.
func NewFindingID(path string, title string, anchor Anchor) core.FindingID {
	preimage := path + "\n" + normaliseTitle(title) + "\n" + string(anchor)
	sum := sha256.Sum256([]byte(preimage))
	return core.FindingID(hex.EncodeToString(sum[:])[:findingIDWidth])
}

// ParseFindingID reads an id back off a marker. Decoding one is not minting
// one, which is what makes an id readable outside this package at all.
func ParseFindingID(s string) (core.FindingID, error) {
	id := core.FindingID(s)
	if !id.Valid() {
		return "", fmt.Errorf("%w: %q", ErrFindingID, s)
	}
	return id, nil
}

// normaliseTitle folds and squeezes a title exactly as the two `tr` calls and
// the `sed` at lib/state.sh:241 do, under LC_ALL=C.
//
// Byte-oriented on purpose, in all three steps:
//
//   - The fold covers ASCII A-Z and nothing else. strings.ToLower folds the
//     Greek capital sigma, and BSD tr under a UTF-8 locale used to as well.
//     Commit f1e93c5 pinned LC_ALL=C so macOS gives the byte answer Linux
//     already produced, and every id automated mode has minted depends on it.
//   - The squeeze covers the six ASCII whitespace bytes and nothing else.
//     unicode.IsSpace counts U+00A0, and strings.Fields squeezes by the same
//     Unicode rule; either would rewrite a title a real pull request carries.
//   - The trim removes ASCII spaces, which is all the squeeze can have left.
func normaliseTitle(title string) string {
	var folded strings.Builder
	folded.Grow(len(title))
	previousSpace := false
	for i := 0; i < len(title); i++ {
		c := title[i]
		if isASCIISpace(c) {
			if !previousSpace {
				folded.WriteByte(' ')
			}
			previousSpace = true
			continue
		}
		previousSpace = false
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		folded.WriteByte(c)
	}
	return strings.Trim(folded.String(), " ")
}

// isASCIISpace reports whether c is one of the six bytes [:space:] names in
// the C locale: space, tab, newline, vertical tab, form feed, carriage return.
func isASCIISpace(c byte) bool {
	switch c {
	case ' ', '\t', '\n', '\v', '\f', '\r':
		return true
	}
	return false
}
