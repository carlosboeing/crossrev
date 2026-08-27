package core

import (
	"errors"
	"fmt"
)

// shaLength is git's object name in hexadecimal. CrossRev never abbreviates a
// revision it stores: every marker records the full `head_sha` it was written
// against (lib/run.sh:1098), and a comparison against an abbreviation would
// match a different commit as readily as the right one.
const shaLength = 40

// shortLength is what the printed forms abbreviate to, as
// `"${claim_sha:0:7}"` at lib/state.sh:330 and `"${commit_sha:0:7}"` at
// lib/run.sh:2271.
const shortLength = 7

// ErrRevisionSHA is returned when a string is not a git object name.
var ErrRevisionSHA = errors.New("a revision must be 40 lowercase hexadecimal characters")

// Revision is one git commit, and optionally the ref it was read through.
//
// The fields are unexported so the only route to a value is a constructor that
// validates. A revision that reached a marker unvalidated would be compared
// against a real one on every later pass and never match, which reads as a new
// revision landing rather than as a malformed value.
type Revision struct {
	sha string
	ref string
}

// NewRevision validates a git object name and returns the revision it names.
func NewRevision(sha string) (Revision, error) {
	if !isHex(sha, shaLength) {
		return Revision{}, fmt.Errorf("%w: %q", ErrRevisionSHA, sha)
	}
	return Revision{sha: sha}, nil
}

// NewRevisionWithRef validates the object name and records the ref it was read
// through. The ref is provenance for a person reading a log; it never takes
// part in equality.
func NewRevisionWithRef(sha, ref string) (Revision, error) {
	r, err := NewRevision(sha)
	if err != nil {
		return Revision{}, err
	}
	r.ref = ref
	return r, nil
}

// SHA is the full 40-character object name, or the empty string on the zero
// value.
func (r Revision) SHA() string { return r.sha }

// Ref is the ref this revision was read through, or the empty string.
func (r Revision) Ref() string { return r.ref }

// Short is the abbreviated object name the printed messages use.
func (r Revision) Short() string {
	if len(r.sha) < shortLength {
		return r.sha
	}
	return r.sha[:shortLength]
}

// IsZero reports whether this is the unset revision.
func (r Revision) IsZero() bool { return r.sha == "" }

// Equal compares identity alone. Two reads of one commit through different
// refs are the same revision.
func (r Revision) Equal(other Revision) bool { return r.sha == other.sha }

// WithRef records a ref against an existing revision without touching its
// identity.
func (r Revision) WithRef(ref string) Revision {
	r.ref = ref
	return r
}

// String renders the object name, which is what a log line wants.
func (r Revision) String() string { return r.sha }

// RevisionPair is the base and head a diff was produced from.
//
// It travels with the diff rather than being re-read, because GitHub's diff
// endpoint answers for the moment of the call: two reads of a moving pull
// request let a push between them validate lines from one revision and post
// comments against another.
type RevisionPair struct {
	Base Revision
	Head Revision
}

// Equal compares both identities and ignores both refs.
func (p RevisionPair) Equal(other RevisionPair) bool {
	return p.Base.Equal(other.Base) && p.Head.Equal(other.Head)
}

// IsZero reports whether either side is unset.
func (p RevisionPair) IsZero() bool { return p.Base.IsZero() || p.Head.IsZero() }

// isHex reports whether s is exactly n lowercase hexadecimal characters.
//
// Byte-wise on purpose. The identity rules elsewhere in CrossRev fold case
// byte-wise under LC_ALL=C (lib/state.sh:241), and a Unicode-aware test here
// would accept a digit no git object name contains.
func isHex(s string, n int) bool {
	if len(s) != n {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
