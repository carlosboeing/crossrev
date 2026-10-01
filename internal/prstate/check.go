package prstate

import (
	"encoding/json"
	"fmt"
	"strings"
)

// The cross-model check's outcomes, as the marker's `check` key carries
// them. Absent when the pass never reached the check phase.
const (
	// CheckRan means the checker judged every candidate and the
	// check_record carries every decision.
	CheckRan = "ran"
	// CheckOff means the check was configured off and no second model
	// looked at all.
	CheckOff = "off"
	// CheckNoCandidates means the pass merged zero candidates, so no
	// check call was made.
	CheckNoCandidates = "no_candidates"
	// CheckDegraded means the check was attempted and failed past its
	// retries, so every candidate posted unchecked.
	CheckDegraded = "degraded"
	// CheckUnavailable means the checker could not run at all, so every
	// candidate posted unchecked.
	CheckUnavailable = "unavailable"
)

// CheckReasonSameModel records a check that ran on the reviewer's own
// model: the decisions stand, but the pass had no second lineage and is
// never described as cross-model.
const CheckReasonSameModel = "same_model"

// CheckDecision is one recorded check decision: what the checker judged
// one candidate to be, and the corrections it carried.
type CheckDecision struct {
	// Position is the candidate's number, 1-based in prompt order.
	Position int `json:"position"`
	// ID is the candidate's finding id, which is what a resume
	// reconciles against.
	ID string `json:"id"`
	// Decision is confirmed, rejected or duplicate.
	Decision string `json:"decision"`
	// DuplicateOf is the duplicated position, set only for duplicates.
	DuplicateOf int `json:"duplicate_of,omitempty"`
	// Reason is the checker's one line, truncated to 300 characters at
	// recording; the transcript keeps the full text.
	Reason string `json:"reason"`
	// Severity and PreExisting are the corrections the decision
	// carried, when it carried any.
	Severity    string `json:"severity,omitempty"`
	PreExisting *bool  `json:"pre_existing,omitempty"`
	// OriginalConcerns is what the survivor's concerns were before the
	// group union, so a discarded record reverts them: the JSON array,
	// or [] when the survivor carried none. Set only when the union
	// added something; raw bytes rather than a list, because omitempty
	// cannot tell an empty original from an absent one.
	OriginalConcerns json.RawMessage `json:"original_concerns,omitempty"`
}

// CheckRecord is the durable checked state: what the candidates were,
// what judged them, and what was decided. A resume whose digest,
// revision or check setting no longer matches discards it and checks
// again.
type CheckRecord struct {
	// Digest is the candidate-set digest the decisions were recorded
	// against.
	Digest string `json:"digest"`
	// Base and Head are the revisions the candidates were built
	// between.
	Base string `json:"base"`
	Head string `json:"head"`
	// Check is the check mode in force: resolver.
	Check string `json:"check"`
	// Harness is the configured checker; ModelReported is the model
	// that answered, empty when the harness names none.
	Harness       string `json:"harness"`
	ModelReported string `json:"model_reported,omitempty"`
	// Usage is the checker's priced usage, or null when the harness
	// reported none.
	Usage json.RawMessage `json:"usage,omitempty"`
	// Decisions holds every decision, in candidate order.
	Decisions []CheckDecision `json:"decisions"`
}

// CheckedOutEntry is one candidate that stayed off the pull request: a
// rejection, or a duplicate folded into its confirmed group.
type CheckedOutEntry struct {
	// Position is the candidate's number, 1-based in prompt order.
	Position int `json:"position"`
	// ID is the candidate's finding id.
	ID string `json:"id"`
	// Path and Line anchor the candidate the finding faulted.
	Path string `json:"path"`
	Line int    `json:"line"`
	// Title is the finding's title, truncated to 120 characters.
	Title string `json:"title"`
	// Reason is the checker's one line.
	Reason string `json:"reason"`
	// Decision is rejected or duplicate.
	Decision string `json:"decision"`
}

// DecodeCheckRecord unmarshals the marker's `check_record` payload. False
// when the marker carries none.
func (m Marker) DecodeCheckRecord() (CheckRecord, bool) {
	if len(m.CheckRecord) == 0 || string(m.CheckRecord) == "null" {
		return CheckRecord{}, false
	}
	var record CheckRecord
	if err := json.Unmarshal(m.CheckRecord, &record); err != nil {
		return CheckRecord{}, false
	}
	return record, true
}

// DecodeCheckedOut unmarshals the marker's `checked_out` payload. A
// marker with none reads as no entry.
func (m Marker) DecodeCheckedOut() []CheckedOutEntry {
	return DecodeCheckedOutRaw(m.CheckedOut)
}

// DecodeCheckedOutRaw unmarshals a `checked_out` payload. An absent or
// unreadable one reads as no entry.
func DecodeCheckedOutRaw(raw json.RawMessage) []CheckedOutEntry {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var out []CheckedOutEntry
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	return out
}

// CheckedOutIDs answers the finding ids the check kept off the pull
// request: every rejected and duplicate candidate. Both review-summary
// renderers filter the table and the counts on this set, and the
// posting loop reconciles against it, so a checked-out candidate never
// posts however its entry reads.
func CheckedOutIDs(raw json.RawMessage) map[string]bool {
	entries := DecodeCheckedOutRaw(raw)
	if len(entries) == 0 {
		return nil
	}
	out := make(map[string]bool, len(entries))
	for _, entry := range entries {
		if entry.ID != "" {
			out[entry.ID] = true
		}
	}
	return out
}

// checkSummaryListed caps the rejected candidates the summary names;
// past it the line ends with a count of the rest.
const checkSummaryListed = 10

// CheckSummaryBlock renders the cross-model check's lines for a review
// summary: the rejected candidates, at most ten with a count of the
// rest, and the check status when it degraded, was unavailable, or ran
// on the reviewer's own model. Empty when the check posted everything
// it saw without remark.
//
// Both review-summary renderers call this with the marker's own values,
// so the resolve leg's rewrite renders the same lines the review leg
// wrote rather than stripping them.
func CheckSummaryBlock(check, reason string, checkedOut json.RawMessage) string {
	var rejected []CheckedOutEntry
	for _, entry := range DecodeCheckedOutRaw(checkedOut) {
		if entry.Decision == "rejected" {
			rejected = append(rejected, entry)
		}
	}
	var b strings.Builder
	// A check that ran on the reviewer's own model is recorded as
	// same_model, never as cross-model: the mechanism word follows
	// the record.
	mechanism := "the cross-model check"
	if reason == CheckReasonSameModel {
		mechanism = "the check"
	}
	if len(rejected) > 0 {
		noun := fmt.Sprintf("%d candidates", len(rejected))
		if len(rejected) == 1 {
			noun = "1 candidate"
		}
		fmt.Fprintf(&b, "%s rejected by %s:\n\n", noun, mechanism)
		for i, entry := range rejected {
			if i >= checkSummaryListed {
				break
			}
			fmt.Fprintf(&b, "- `%s:%d` — %s — %s\n", entry.Path, entry.Line, oneLine(entry.Title), oneLine(entry.Reason))
		}
		if rest := len(rejected) - checkSummaryListed; rest > 0 {
			fmt.Fprintf(&b, "- …and %d more\n", rest)
		}
		b.WriteString("\n")
	}
	switch check {
	case CheckDegraded, CheckUnavailable:
		if reason == "" {
			fmt.Fprintf(&b, "check: %s: every candidate posted unchecked.\n\n", check)
		} else {
			fmt.Fprintf(&b, "check: %s (%s): every candidate posted unchecked.\n\n", check, reason)
		}
	case CheckRan:
		if reason == CheckReasonSameModel {
			b.WriteString("check: ran (same_model): the checker answered as the model that reviewed, so this pass had no second lineage.\n\n")
		}
	}
	return b.String()
}

// oneLine folds a title or reason onto one line for the summary list.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
