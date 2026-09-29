package intel

import (
	"sort"

	"github.com/carlosboeing/crossrev/internal/core"
)

// SplitVerdict is one part's judgement on its slice of a split file: the
// coverage verdict the part's call accepted and the finding numbers that
// verdict named, in the merged finding space the caller numbers into.
type SplitVerdict struct {
	// Verdict is one of no_issue, finding, not_affected or
	// could_not_review.
	Verdict string
	// FindingNumbers names the merged findings this part's verdict
	// points at. Empty unless Verdict is finding.
	FindingNumbers []int
}

// MergeSplitVerdicts merges every part's verdict into the split file's
// single verdict, once every part has one in the same pass. Precedence:
// could_not_review wins outright — a slice nobody judged leaves the file
// unjudged; then finding, joining every part's finding numbers in order;
// then not_affected, only when every part agrees the file needs no
// change; else no_issue. An empty set answers no_issue: nothing was
// judged and nothing was found.
func MergeSplitVerdicts(parts []SplitVerdict) (string, []int) {
	verdict := string(core.FileVerdictNoIssue)
	seen := map[int]bool{}
	var numbers []int
	unanimousNotAffected := len(parts) > 0
	for _, part := range parts {
		switch core.FileVerdict(part.Verdict) {
		case core.FileVerdictCouldNotReview:
			return string(core.FileVerdictCouldNotReview), nil
		case core.FileVerdictFinding:
			verdict = string(core.FileVerdictFinding)
			for _, n := range part.FindingNumbers {
				if !seen[n] {
					seen[n] = true
					numbers = append(numbers, n)
				}
			}
			unanimousNotAffected = false
		case core.FileVerdictNotAffected:
		default:
			unanimousNotAffected = false
		}
	}
	if verdict == string(core.FileVerdictFinding) {
		sort.Ints(numbers)
		return verdict, numbers
	}
	if unanimousNotAffected {
		return string(core.FileVerdictNotAffected), nil
	}
	return string(core.FileVerdictNoIssue), nil
}
