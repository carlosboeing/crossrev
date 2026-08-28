// findings.go — the review leg's payload, checked the way lib/validate.sh's
// validate_findings checks it (lib/validate.sh:33-66).

package validate

import (
	"bytes"
	"encoding/json"
)

// Findings checks one review payload and returns nil, or a ShapeError naming the
// first problem in the words the shell prints.
//
// Only shape is checked, because the review leg has nothing to contradict: the
// orchestrator hands it a diff and prior findings, not a numbering the answer has
// to cover. validate_resolve is the one with a semantic half.
//
// Three of the shell's answers look wrong and are reproduced anyway, because
// this is a parity port and each one is jq behaving as jq does.
//
//   - An empty or whitespace-only payload is accepted. jq runs the filter once
//     per input value, and there is no input value, so it prints nothing and
//     exits 0 — which the shell reads as no problem.
//   - A findings element that is a number, a string, a boolean or an array is
//     skipped. `.path?` raises on all four and the `?` swallows it, so the
//     `select` yields nothing at all. A null element is not skipped, because
//     indexing null yields null rather than raising.
//   - `side: false` passes as `RIGHT`, and `severity: false` as the empty
//     string, because jq's `//` reads false as absent.
func Findings(payload []byte) error {
	// jq's parse failure, which the shell turns into one line
	// (lib/validate.sh:62). An empty document is not a parse failure: jq exits 0
	// having printed nothing, so there is no problem to report.
	if len(bytes.TrimSpace(payload)) == 0 {
		return nil
	}
	var top json.RawMessage
	if err := json.Unmarshal(payload, &top); err != nil {
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
	return nil
}

// findingIsBad is the `select` at lib/validate.sh:44-58, clause for clause and
// in the same order. jq's `or` short-circuits, so the first true clause decides;
// every clause here answers the same way about the same element, so the order is
// kept for reading rather than for the result.
func findingIsBad(element json.RawMessage) bool {
	kind := jqType(element)
	if kind != "object" && kind != "null" {
		// `.path?` raises on a scalar or an array, the `?` swallows it, and the
		// select produces nothing.
		return false
	}
	var f map[string]json.RawMessage
	if kind == "object" {
		if err := json.Unmarshal(element, &f); err != nil {
			return false
		}
	}
	// A null element indexes to null for every key, which is what an empty map
	// already answers.

	if path, ok := jqString(f["path"]); !ok || path == "" {
		return true
	}
	if jqType(f["line"]) != "number" {
		return true
	}
	if !jqIn(jqAlt(f["side"], `"RIGHT"`), "LEFT", "RIGHT") {
		return true
	}
	if !jqIn(jqAlt(f["severity"], `""`), "high", "medium", "low") {
		return true
	}
	if !jqIn(jqAlt(f["category"], `""`),
		"correctness", "security", "performance", "maintainability", "testing", "docs") {
		return true
	}
	// Presence and type are checked separately, and neither through a default.
	// The only default available is false, which is the value that says "this
	// pull request introduced it" — so an absent or null field would reach the
	// resolve leg as something it may change code for, defeating the one
	// guardrail that is not configurable (lib/validate.sh:50-56).
	if _, present := f["pre_existing"]; !present {
		return true
	}
	if jqType(f["pre_existing"]) != "boolean" {
		return true
	}
	if title, ok := jqString(f["title"]); !ok || title == "" {
		return true
	}
	return false
}
