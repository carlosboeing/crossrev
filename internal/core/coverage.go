package core

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
)

// FileEngineVersion is the first file-coverage engine. Its manifest identity
// is FileEngineID. A later change to enumeration, evidence or disposition
// semantics must change this literal and invalidate prior generations.
const FileEngineVersion = "file-v1"

// FileEngineID is the first 16 lowercase hex characters of SHA-256 over
// "crossrev-review-intelligence\nfile-v1\n". It binds a coverage generation
// to the engine semantics that produced it.
func FileEngineID() string {
	sum := sha256.Sum256([]byte("crossrev-review-intelligence\n" + FileEngineVersion + "\n"))
	return hex.EncodeToString(sum[:])[:16]
}

// UnitID is a review unit's identity. For this release every unit is a file
// unit minted by FileUnitID; symbol units arrive with a later engine.
type UnitID string

// ErrUnitID is returned for a string that is not a unit identity.
var ErrUnitID = errors.New("a unit id is 16 lowercase hexadecimal characters")

// ParseUnitID validates a unit identity.
func ParseUnitID(s string) (UnitID, error) {
	if len(s) != 16 {
		return "", fmt.Errorf("%w: %q", ErrUnitID, s)
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return "", fmt.Errorf("%w: %q", ErrUnitID, s)
		}
	}
	return UnitID(s), nil
}

// FileUnitID mints a file unit's identity: the first 16 lowercase hex
// characters of SHA-256 over "u1\n" + path + "\nfile\n\n0\n". The kind is
// file, the qualified name is empty and the ordinal is zero, so the ordinary
// case carries the design's full preimage with nothing to vary.
func FileUnitID(path string) UnitID {
	sum := sha256.Sum256([]byte("u1\n" + path + "\nfile\n\n0\n"))
	return UnitID(hex.EncodeToString(sum[:])[:16])
}

// ChangeKind is how git describes one changed path.
type ChangeKind string

// The five change kinds one complete enumeration may report.
const (
	ChangeAdded       ChangeKind = "added"
	ChangeModified    ChangeKind = "modified"
	ChangeDeleted     ChangeKind = "deleted"
	ChangeRenamed     ChangeKind = "renamed"
	ChangeTypeChanged ChangeKind = "type_changed"
)

// ErrChangeKind is returned for a change kind no enumeration reports.
var ErrChangeKind = errors.New("a change kind is added, modified, deleted, renamed or type_changed")

// ParseChangeKind accepts only the five reported values.
func ParseChangeKind(s string) (ChangeKind, error) {
	switch ChangeKind(s) {
	case ChangeAdded, ChangeModified, ChangeDeleted, ChangeRenamed, ChangeTypeChanged:
		return ChangeKind(s), nil
	}
	return "", fmt.Errorf("%w: %q", ErrChangeKind, s)
}

// String renders the change kind as the manifest holds it.
func (k ChangeKind) String() string { return string(k) }

// Disposition is a reviewer's judgement on one required unit.
type Disposition string

// The four dispositions a reviewer may report. Outstanding is a record type,
// not a disposition: there is no pending judgement.
const (
	DispositionNoIssue        Disposition = "no_issue"
	DispositionFinding        Disposition = "finding"
	DispositionNotAffected    Disposition = "not_affected"
	DispositionCouldNotReview Disposition = "could_not_review"
)

// ErrDisposition is returned for a disposition no reviewer reports.
var ErrDisposition = errors.New("a disposition is no_issue, finding, not_affected or could_not_review")

// ParseDisposition accepts only the four reported values.
func ParseDisposition(s string) (Disposition, error) {
	switch Disposition(s) {
	case DispositionNoIssue, DispositionFinding, DispositionNotAffected, DispositionCouldNotReview:
		return Disposition(s), nil
	}
	return "", fmt.Errorf("%w: %q", ErrDisposition, s)
}

// String renders the disposition as the manifest holds it.
func (d Disposition) String() string { return string(d) }

// FileChange is one path from a complete git enumeration: the current path,
// the previous path where one exists, and how the two differ.
//
// The type lives in core rather than vcs because intel consumes it and tier 1
// may not import tier 2. vcs produces these values; intel reads them.
type FileChange struct {
	// OldPath is the previous path for a rename or a deletion, and empty
	// otherwise. A deletion carries its path in both fields so the base
	// evidence read has a path to name.
	OldPath string
	// Path is the current path the required set sorts by.
	Path string
	// Kind is how the path changed between the two revisions.
	Kind ChangeKind
}

// BodyDigestHex is the full 64-character SHA-256 hex over a unit's evidence
// bytes. Digests are stored at full strength: a truncated digest fails by
// reporting a changed body as unchanged, which is the one error the ledger
// cannot absorb.
func BodyDigestHex(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}
