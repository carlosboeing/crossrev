package core

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
)

// FileEngineVersion is the file-coverage engine. Its manifest identity
// is FileEngineID. A later change to enumeration, evidence or verdict
// semantics must change this literal and invalidate prior generations.
//
// hunk-v1 counts coverage over supplied ranges: every required file reaches
// the reviewer as its own gutter-numbered hunks, and the ledger records
// the ranges shown on each side rather than the whole file, so the ranges
// a file-v2 generation recorded no longer mean the same thing.
const FileEngineVersion = "hunk-v1"

// FileEngineID is the first 16 lowercase hex characters of SHA-256 over
// "crossrev-review-intelligence\nhunk-v1\n". It binds a coverage generation
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

// FileVerdict is a reviewer's judgement on one required unit.
type FileVerdict string

// The four verdicts a reviewer may report. Outstanding is a record type,
// not a verdict: there is no pending judgement.
const (
	FileVerdictNoIssue        FileVerdict = "no_issue"
	FileVerdictFinding        FileVerdict = "finding"
	FileVerdictNotAffected    FileVerdict = "not_affected"
	FileVerdictCouldNotReview FileVerdict = "could_not_review"
)

// ErrFileVerdict is returned for a verdict no reviewer reports.
var ErrFileVerdict = errors.New("a verdict is no_issue, finding, not_affected or could_not_review")

// ParseFileVerdict accepts only the four reported values.
func ParseFileVerdict(s string) (FileVerdict, error) {
	switch FileVerdict(s) {
	case FileVerdictNoIssue, FileVerdictFinding, FileVerdictNotAffected, FileVerdictCouldNotReview:
		return FileVerdict(s), nil
	}
	return "", fmt.Errorf("%w: %q", ErrFileVerdict, s)
}

// String renders the verdict as the manifest holds it.
func (d FileVerdict) String() string { return string(d) }

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

// LineSpan is one contiguous run of supplied line numbers, inclusive at
// both ends, numbered as the gutter shows them: old-side numbers on the
// base side, new-side numbers on the head side.
type LineSpan struct {
	Start int
	End   int
}

// SuppliedRanges are the line spans the reviewer was actually shown, per
// side. A whole-file hunk covers the full span on its content side; a
// function-context hunk covers one span per change neighbourhood; a
// header-only diff covers nothing on either side.
type SuppliedRanges struct {
	Base []LineSpan
	Head []LineSpan
}

// CoversBase reports whether the closed span [start, end] sits inside one
// of the base-side spans.
func (r SuppliedRanges) CoversBase(start, end int) bool {
	return coversSpan(r.Base, start, end)
}

// CoversHead reports whether the closed span [start, end] sits inside one
// of the head-side spans.
func (r SuppliedRanges) CoversHead(start, end int) bool {
	return coversSpan(r.Head, start, end)
}

// Union folds another range set into this one, merging overlapping and
// contiguous spans. Split-file parts each cover their own slice; the union
// is what the merged record covers.
func (r SuppliedRanges) Union(o SuppliedRanges) SuppliedRanges {
	return SuppliedRanges{
		Base: MergeLineSpans(append(append([]LineSpan(nil), r.Base...), o.Base...)),
		Head: MergeLineSpans(append(append([]LineSpan(nil), r.Head...), o.Head...)),
	}
}

// MergeLineSpans sorts spans by start and folds overlapping and contiguous
// ones, so a set of gutter runs reads back as the spans the gutter shows.
// A degenerate span — start past end — is dropped rather than widening its
// neighbour, because no gutter run numbers backwards.
func MergeLineSpans(in []LineSpan) []LineSpan {
	kept := make([]LineSpan, 0, len(in))
	for _, s := range in {
		if s.Start >= 1 && s.End >= s.Start {
			kept = append(kept, s)
		}
	}
	if len(kept) == 0 {
		return nil
	}
	sort.Slice(kept, func(i, j int) bool { return kept[i].Start < kept[j].Start })
	out := kept[:1]
	for _, s := range kept[1:] {
		last := &out[len(out)-1]
		if s.Start <= last.End+1 {
			if s.End > last.End {
				last.End = s.End
			}
			continue
		}
		out = append(out, s)
	}
	return out
}

func coversSpan(spans []LineSpan, start, end int) bool {
	if start < 1 || end < start {
		return false
	}
	for _, s := range spans {
		if s.Start <= start && end <= s.End {
			return true
		}
	}
	return false
}

// BodyDigestHex is the full 64-character SHA-256 hex over a unit's evidence
// bytes. Digests are stored at full strength: a truncated digest fails by
// reporting a changed body as unchanged, which is the one error the ledger
// cannot absorb.
func BodyDigestHex(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}
