package intel

import (
	"github.com/carlosboeing/crossrev/internal/core"
)

// FullTextMaxBytes is the largest evidence a modified or renamed file may
// hold at the head and still read in full: 8 KiB. Added and deleted files
// always read in full whatever their size, because their whole content is
// the change; anything larger reads as function-context hunks, and binary,
// unreadable or changeless files read as header-only diffs.
const FullTextMaxBytes = 8 * 1024

// InputForm is which of the three supplied forms a required file takes in
// the review prompt. Every form is a gutter-numbered diff: full_text shows
// the whole file as one hunk, hunks_context shows each change with its
// enclosing function clipped to context near a changed line, and diff_only
// shows the diff header with the access reason and no hunks.
type InputForm string

const (
	// FormFullText covers added files, deleted files, and modified or
	// renamed files of at most FullTextMaxBytes at the head.
	FormFullText InputForm = "full_text"
	// FormHunksContext covers modified or renamed-and-edited files over
	// FullTextMaxBytes.
	FormHunksContext InputForm = "hunks_context"
	// FormDiffOnly covers binary, unreadable, pure-rename and mode-only
	// changes.
	FormDiffOnly InputForm = "diff_only"
)

// SelectForm answers which supplied form a file takes. It is pure over the
// discovery facts — no git, no parsing — so the VCS layer computes
// hasChanges from the file's own diff and this decides from there.
//
// An unavailable or binary file reads header-only whatever else holds:
// there are no readable bytes to show. A file whose diff carries no added
// or removed line reads header-only too: a pure rename or a mode-only
// change has no content to number. Added and deleted files read in full,
// and so does any other file within the byte budget; past it, an edited
// file reads as function-context hunks.
func SelectForm(kind core.ChangeKind, sizeBytes int, binary, unavailable, hasChanges bool) InputForm {
	if unavailable || binary {
		return FormDiffOnly
	}
	if !hasChanges {
		return FormDiffOnly
	}
	if kind == core.ChangeAdded || kind == core.ChangeDeleted {
		return FormFullText
	}
	if sizeBytes <= FullTextMaxBytes {
		return FormFullText
	}
	return FormHunksContext
}

// DiffOnlyReason answers the header-only reason for a file taking
// FormDiffOnly: the unit's own access limit when it is unavailable,
// the binary note for binary content, and the changeless kind otherwise.
// Shaping and prompt mapping both read it, so the sentence never drifts
// between the ledger and the prompt.
func DiffOnlyReason(kind core.ChangeKind, sizeBytes int, binary bool, unavailableReason string) string {
	if unavailableReason != "" {
		return unavailableReason
	}
	if binary {
		return "binary content"
	}
	return changelessReason(kind, sizeBytes)
}

// changelessReason names why a readable, non-binary file carries no hunks:
// an empty file, a rename without edits, or a type change with no content
// change.
func changelessReason(kind core.ChangeKind, sizeBytes int) string {
	if sizeBytes == 0 {
		return "empty file"
	}
	switch kind {
	case core.ChangeRenamed:
		return "renamed without edits"
	case core.ChangeTypeChanged:
		return "mode change with no content change"
	default:
		return "no changed lines"
	}
}
