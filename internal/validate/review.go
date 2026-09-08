// review.go — the review leg's payload, checked the way the reviewer
// contract requires: Findings holds the shape half, and Review adds the
// semantic half that compares the answer against what the orchestrator
// supplied.
//
// Findings stays the compatibility entry point for tests that have no input
// batch. Review first applies the shape check, then returns a SemanticError
// when unit numbers do not equal the expected set exactly, finding numbers
// name no returned finding, evidence names an unprovided path or revision,
// spans are inverted or outside supplied readable content, finding has no
// finding number, not_affected has no evidence and reason, or
// could_not_review has no failed-fallback reason. The error orders and names
// missing, duplicate and unknown unit numbers so the retry prompt can quote
// them.

package validate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/carlosboeing/crossrev/internal/core"
)

// UnitExpectation is what the orchestrator showed one numbered batch file at:
// the path the evidence names, the content revision shown beside it, and how
// many readable lines that content holds. A unit with no readable bytes —
// binary, missing or unreadable — carries no line count, so no span can sit
// inside it: evidence there is file-level only.
type UnitExpectation struct {
	// Path is the current path as the batch printed it.
	Path string
	// Revision is the full content revision shown beside the file: the base
	// for a deletion, the head for every other kind.
	Revision core.Revision
	// Lines is how many readable lines the supplied content holds. Zero
	// means no readable bytes were supplied, so only null spans pass.
	Lines int
	// Readable reports whether any bytes were supplied at all.
	Readable bool
}

// ReviewExpectations is what the orchestrator handed the review leg: the
// numbered batch units in prompt order, and the base and head the batch was
// built between. Numbers run 1 to len(Units) by position, the same mapping
// the prompt prints beside each file.
type ReviewExpectations struct {
	Units []UnitExpectation
	Base  core.Revision
	Head  core.Revision
}

// reviewCoverageDoc is the decoded shape-half output: the coverage entries
// and the returned findings the finding numbers point into.
type reviewCoverageDoc struct {
	Coverage []json.RawMessage `json:"coverage"`
	Findings []json.RawMessage `json:"findings"`
}

// Review checks one review payload and returns nil, a ShapeError, or a
// SemanticError.
//
// An empty or whitespace-only document is a shape error: an empty finding
// list cannot tell examined code from omitted code, so empty output never
// means clean. A malformed shape is exit 1. A well-shaped payload whose
// coverage contradicts the supplied batch is exit 2.
func Review(payload []byte, expected ReviewExpectations) error {
	if len(bytes.TrimSpace(payload)) == 0 {
		return shapef("the payload is empty, and empty output is never clean coverage")
	}
	// The shape half: the same verdict and findings checks Findings runs,
	// plus the reviewer contract's three top-level fields and the coverage
	// member shape.
	if err := reviewShape(payload); err != nil {
		return err
	}
	return reviewSemantic(payload, expected)
}

// reviewShape is the shape half behind both Findings and Review: the verdict,
// the findings array, and every element's range — then the reviewer
// contract's required top-level fields and the coverage member shape.
//
// It stays in findings.go's words where findings.go already speaks, so one
// payload cannot pass one entry point and fail the other on the same bytes.
func reviewShape(payload []byte) error {
	top, ok := jqParse(payload)
	if !ok {
		return shapef("the payload is not parseable JSON")
	}

	if jqType(top) != "object" {
		return shapef("the payload is not a JSON object")
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(top, &doc); err != nil {
		return shapef("the payload is not parseable JSON")
	}

	verdict, hasVerdict := doc["verdict"]
	if !hasVerdict {
		return shapef("no verdict key")
	}
	if !jqIn(verdict, "converged", "issues-remain", "blocked") {
		return shapef(`verdict is "%s", which is not one of converged, issues-remain, blocked`,
			jqInterp(verdict))
	}

	findings, hasFindings := doc["findings"]
	if !hasFindings || jqType(findings) != "array" {
		return shapef("findings is missing or not an array")
	}
	var elements []json.RawMessage
	if err := json.Unmarshal(findings, &elements); err != nil {
		return shapef("findings is missing or not an array")
	}

	bad := 0
	var first json.RawMessage
	for _, element := range elements {
		if !findingIsBad(element) {
			continue
		}
		if bad == 0 {
			first = element
		}
		bad++
	}
	if bad > 0 {
		return shapef("%d finding(s) have a missing or out-of-range path, line, side, "+
			"severity, category, pre_existing or title — first: %s", bad, jqCompact(first))
	}

	coverage, hasCoverage := doc["coverage"]
	if !hasCoverage || jqType(coverage) != "array" {
		return shapef("coverage is missing or not an array")
	}
	var entries []json.RawMessage
	if err := json.Unmarshal(coverage, &entries); err != nil {
		return shapef("coverage is missing or not an array")
	}

	scope, hasScope := doc["examined_scope"]
	if !hasScope {
		return shapef("examined_scope is missing")
	}
	if text, ok := jqString(scope); !ok || text == "" {
		return shapef("examined_scope is missing or empty")
	}

	limits, hasLimits := doc["known_limits"]
	if !hasLimits || jqType(limits) != "array" {
		return shapef("known_limits is missing or not an array")
	}
	var limitItems []json.RawMessage
	if err := json.Unmarshal(limits, &limitItems); err != nil {
		return shapef("known_limits is missing or not an array")
	}
	for _, item := range limitItems {
		if _, ok := jqString(item); !ok {
			return shapef("known_limits holds a value that is not a string")
		}
	}

	badCoverage := 0
	var firstCoverage json.RawMessage
	for _, entry := range entries {
		if !coverageEntryIsBad(entry) {
			continue
		}
		if badCoverage == 0 {
			firstCoverage = entry
		}
		badCoverage++
	}
	if badCoverage > 0 {
		return shapef("%d coverage %s have a missing or out-of-range unit_number, disposition, "+
			"finding_numbers, evidence or reason — first: %s",
			badCoverage, coveragePlural(badCoverage), jqCompact(firstCoverage))
	}
	return nil
}

func coveragePlural(n int) string {
	if n == 1 {
		return "entry"
	}
	return "entries"
}

// coverageEntryIsBad reports whether one coverage entry fails the member
// shape: a whole unit number at or above 1, a disposition inside the four, a
// finding_numbers array of whole numbers at or above 1, an evidence array of
// well-shaped items, and a reason that is a string or null.
func coverageEntryIsBad(entry json.RawMessage) bool {
	if jqType(entry) != "object" {
		return true
	}
	var c map[string]json.RawMessage
	if err := json.Unmarshal(entry, &c); err != nil {
		return true
	}
	if !isWholeAtLeast(c["unit_number"], 1) {
		return true
	}
	if !jqIn(c["disposition"], "no_issue", "finding", "not_affected", "could_not_review") {
		return true
	}
	if jqType(c["finding_numbers"]) != "array" {
		return true
	}
	var numbers []json.RawMessage
	if err := json.Unmarshal(c["finding_numbers"], &numbers); err != nil {
		return true
	}
	for _, n := range numbers {
		if !isWholeAtLeast(n, 1) {
			return true
		}
	}
	if jqType(c["evidence"]) != "array" {
		return true
	}
	var evidence []json.RawMessage
	if err := json.Unmarshal(c["evidence"], &evidence); err != nil {
		return true
	}
	for _, item := range evidence {
		if coverageEvidenceIsBad(item) {
			return true
		}
	}
	switch jqType(c["reason"]) {
	case "string", "null":
	default:
		return true
	}
	return false
}

// coverageEvidenceIsBad reports whether one evidence item fails the member
// shape: a non-empty path and revision, a source inside the four, and whole
// or null line spans. Whether the path and revision were supplied, and
// whether the span sits inside readable content, is the semantic half's
// question.
func coverageEvidenceIsBad(item json.RawMessage) bool {
	if jqType(item) != "object" {
		return true
	}
	var e map[string]json.RawMessage
	if err := json.Unmarshal(item, &e); err != nil {
		return true
	}
	if path, ok := jqString(e["path"]); !ok || path == "" {
		return true
	}
	if revision, ok := jqString(e["revision"]); !ok || revision == "" {
		return true
	}
	if !jqIn(e["source"], "git", "search", "convention", "reviewer") {
		return true
	}
	for _, end := range []json.RawMessage{e["start_line"], e["end_line"]} {
		if jqType(end) == "null" {
			continue
		}
		if !isWholeAtLeast(end, 1) {
			return true
		}
	}
	return false
}

// isWholeAtLeast reports whether a raw value is a JSON number holding a whole
// value at or above low. jq has one number type, so whole-ness is checked
// rather than assumed: a harness that constrains output cannot return 1.5
// here, and the fenced-JSON path has no such guarantee.
func isWholeAtLeast(raw json.RawMessage, low int) bool {
	n, ok := jqFloat(raw)
	if !ok || math.Floor(n) != n {
		return false
	}
	return n >= float64(low)
}

// reviewSemantic compares a well-shaped payload against the batch the
// orchestrator supplied. The expected numbers are the positions 1 to
// len(Units), whatever order discovery ran in: the mapping is the prompt's
// own numbering, so set equality is against positions rather than paths.
func reviewSemantic(payload []byte, expected ReviewExpectations) error {
	var doc reviewCoverageDoc
	// Unreachable as a failure: the shape half has already parsed the same
	// bytes and refused every non-object. Kept rather than assumed, so a
	// caller comparing against an invented expectation is refused rather
	// than trusted.
	if err := json.Unmarshal(payload, &doc); err != nil {
		return semanticf("the payload stopped being parseable between the two checks")
	}

	got := make([]reviewCoverageEntry, 0, len(doc.Coverage))
	for _, entry := range doc.Coverage {
		item, ok := decodeReviewCoverageEntry(entry)
		if !ok {
			return semanticf("the payload stopped being parseable between the two checks")
		}
		got = append(got, item)
	}

	want := len(expected.Units)
	counts := map[int]int{}
	for _, item := range got {
		counts[item.number]++
	}
	var missing []int
	for n := 1; n <= want; n++ {
		if counts[n] == 0 {
			missing = append(missing, n)
		}
	}
	var duplicates []int
	for n, count := range counts {
		if count > 1 {
			duplicates = append(duplicates, n)
		}
	}
	var unknown []string
	for _, item := range got {
		if item.number < 1 || item.number > want {
			unknown = append(unknown, item.literal)
		}
	}
	if len(missing) > 0 || len(duplicates) > 0 || len(unknown) > 0 {
		return semanticf("coverage %s", coverageSetWords(missing, duplicates, unknown, want))
	}

	byNumber := map[int]reviewCoverageEntry{}
	for _, item := range got {
		byNumber[item.number] = item
	}
	ordered := make([]reviewCoverageEntry, 0, len(got))
	for n := 1; n <= want; n++ {
		ordered = append(ordered, byNumber[n])
	}

	for _, item := range ordered {
		for _, f := range item.findings {
			if f < 1 || f > len(doc.Findings) {
				return semanticf("coverage for unit %d names finding number %d, but %s",
					item.number, f, reviewFindingsRangeWords(len(doc.Findings)))
			}
		}
		if item.dispo == string(core.DispositionFinding) {
			if len(item.findings) == 0 {
				return semanticf("coverage for unit %d says finding but names no finding number",
					item.number)
			}
		} else if len(item.findings) > 0 {
			return semanticf("coverage for unit %d says %s but names finding number(s) %s",
				item.number, item.dispo, reviewIntsWords(item.findings))
		}
	}

	for _, item := range ordered {
		switch item.dispo {
		case string(core.DispositionNotAffected):
			if len(item.evidence) == 0 || !item.hasReason {
				return semanticf("coverage for unit %d says not_affected without evidence and a reason, and a changed file is not cleared by assertion",
					item.number)
			}
		case string(core.DispositionCouldNotReview):
			if !item.hasReason {
				return semanticf("coverage for unit %d says could_not_review without the failed fallbacks in reason",
					item.number)
			}
		}
	}

	for _, item := range ordered {
		unit := expected.Units[item.number-1]
		if len(item.evidence) == 0 && item.dispo != string(core.DispositionCouldNotReview) {
			return semanticf("coverage for unit %d names no evidence, and a judgement with nothing behind it is not a judgement",
				item.number)
		}
		for _, ev := range item.evidence {
			if err := checkReviewEvidence(ev, unit, expected, item.number); err != nil {
				return err
			}
		}
	}

	claimed := map[int]int{}
	for _, item := range ordered {
		for _, f := range item.findings {
			claimed[f]++
		}
	}
	for f, count := range claimed {
		if count > 1 {
			return semanticf("finding number %d is named by more than one unit, and one finding answers for one unit",
				f)
		}
	}

	return nil
}

// reviewCoverageEntry is one decoded coverage entry, past the shape half's
// range checks.
type reviewCoverageEntry struct {
	number    int
	literal   string
	dispo     string
	findings  []int
	evidence  []reviewEvidenceRef
	reason    string
	hasReason bool
}

// reviewEvidenceRef is one coverage entry's evidence item, decoded past the
// shape half's range checks.
type reviewEvidenceRef struct {
	path     string
	revision string
	source   string
	hasStart bool
	start    int
	hasEnd   bool
	end      int
}

// decodeReviewCoverageEntry reads one coverage entry the shape half has
// already range-checked. False means the bytes changed shape between the two
// reads rather than that the model wrote something wrong.
func decodeReviewCoverageEntry(entry json.RawMessage) (reviewCoverageEntry, bool) {
	var c struct {
		UnitNumber     json.RawMessage   `json:"unit_number"`
		Disposition    string            `json:"disposition"`
		FindingNumbers []json.RawMessage `json:"finding_numbers"`
		Evidence       []json.RawMessage `json:"evidence"`
		Reason         json.RawMessage   `json:"reason"`
	}
	var item reviewCoverageEntry
	if err := json.Unmarshal(entry, &c); err != nil {
		return item, false
	}
	n, ok := jqFloat(c.UnitNumber)
	if !ok {
		return item, false
	}
	item.number = int(n)
	item.literal = strings.TrimSpace(string(c.UnitNumber))
	item.dispo = c.Disposition
	for _, raw := range c.FindingNumbers {
		f, ok := jqFloat(raw)
		if !ok {
			return item, false
		}
		item.findings = append(item.findings, int(f))
	}
	for _, raw := range c.Evidence {
		ev, ok := decodeReviewEvidenceRef(raw)
		if !ok {
			return item, false
		}
		item.evidence = append(item.evidence, ev)
	}
	if jqType(c.Reason) == "string" {
		var s string
		if err := json.Unmarshal(c.Reason, &s); err != nil {
			return item, false
		}
		item.reason = s
		item.hasReason = s != ""
	}
	return item, true
}

// decodeReviewEvidenceRef reads one evidence item the shape half has already
// range-checked.
func decodeReviewEvidenceRef(raw json.RawMessage) (reviewEvidenceRef, bool) {
	var ev struct {
		Path      string          `json:"path"`
		Revision  string          `json:"revision"`
		StartLine json.RawMessage `json:"start_line"`
		EndLine   json.RawMessage `json:"end_line"`
		Source    string          `json:"source"`
	}
	var ref reviewEvidenceRef
	if err := json.Unmarshal(raw, &ev); err != nil {
		return ref, false
	}
	ref.path = ev.Path
	ref.revision = ev.Revision
	ref.source = ev.Source
	if jqType(ev.StartLine) != "null" {
		s, ok := jqFloat(ev.StartLine)
		if !ok {
			return ref, false
		}
		ref.hasStart = true
		ref.start = int(s)
	}
	if jqType(ev.EndLine) != "null" {
		e, ok := jqFloat(ev.EndLine)
		if !ok {
			return ref, false
		}
		ref.hasEnd = true
		ref.end = int(e)
	}
	return ref, true
}

// checkReviewEvidence compares one evidence item against the unit it answers
// for: the path must be one the batch supplied, the revision must be the base
// or head the batch was built between, and the span must sit inside readable
// content the batch supplied — or be file-level nulls.
func checkReviewEvidence(ev reviewEvidenceRef, unit UnitExpectation, expected ReviewExpectations, number int) error {
	supplied := false
	for _, u := range expected.Units {
		if u.Path == ev.path {
			supplied = true
			break
		}
	}
	if !supplied {
		return semanticf("coverage for unit %d cites evidence path %q, which the batch did not supply",
			number, ev.path)
	}
	if ev.revision != expected.Base.SHA() && ev.revision != expected.Head.SHA() {
		return semanticf("coverage for unit %d cites evidence revision %q, which is neither the base nor the head",
			number, ev.revision)
	}
	if !ev.hasStart && !ev.hasEnd {
		return nil
	}
	if !ev.hasStart || !ev.hasEnd {
		return semanticf("coverage for unit %d cites a half-open span, and a judgement rests on lines or on the file, not half of each",
			number)
	}
	if ev.start > ev.end {
		return semanticf("coverage for unit %d cites lines %d-%d, and a span cannot end before it starts",
			number, ev.start, ev.end)
	}
	if !unit.Readable || unit.Lines == 0 {
		return semanticf("coverage for unit %d cites lines %d-%d for content supplied as an access limit, and a limit carries file-level evidence only",
			number, ev.start, ev.end)
	}
	if ev.start < 1 || ev.end > unit.Lines {
		return semanticf("coverage for unit %d cites lines %d-%d outside the %d readable line(s) supplied",
			number, ev.start, ev.end, unit.Lines)
	}
	return nil
}

// coverageSetWords names the missing, duplicate and unknown unit numbers one
// failed set-equality check found, in the order the retry prompt quotes them.
func coverageSetWords(missing, duplicates []int, unknown []string, want int) string {
	sort.Ints(missing)
	sort.Ints(duplicates)
	sort.Strings(unknown)
	var parts []string
	if len(missing) > 0 {
		parts = append(parts, fmt.Sprintf("is missing unit number(s) %s", reviewIntsWords(missing)))
	}
	if len(duplicates) > 0 {
		parts = append(parts, fmt.Sprintf("names unit number(s) %s more than once", reviewIntsWords(duplicates)))
	}
	if len(unknown) > 0 {
		parts = append(parts, fmt.Sprintf("names unknown unit number(s) %s", strings.Join(unknown, ", ")))
	}
	rng := "no units were supplied"
	if want > 0 {
		rng = fmt.Sprintf("%d unit(s) were supplied, numbered 1 to %d", want, want)
	}
	return strings.Join(parts, "; ") + " — " + rng
}

// reviewFindingsRangeWords names how many findings the payload returned, the
// way the resolve semantic half names its own range.
func reviewFindingsRangeWords(n int) string {
	return fmt.Sprintf("%d finding(s) were returned, numbered 1 to %d", n, n)
}

// reviewIntsWords joins whole numbers the way the resolve semantic half joins
// them.
func reviewIntsWords(in []int) string {
	parts := make([]string, len(in))
	for i, n := range in {
		parts[i] = fmt.Sprintf("%d", n)
	}
	return strings.Join(parts, ", ")
}
