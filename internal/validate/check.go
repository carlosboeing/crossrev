// check.go — the cross-model check's payload, shape then semantic.
//
// The shape half is the flat check this package does everywhere: required
// keys, right types, in-range enums. The semantic half contradicts the
// answer against the candidates the orchestrator sent: exactly one decision
// per position, no self-reference, and every duplicate chain ending at a
// confirmed candidate with no cycle.

package validate

import (
	"bytes"
	"encoding/json"
	"sort"
)

// CheckExpectations is what one check call's answer must cover: the
// candidate positions the call numbered, in prompt order. A packed call
// carries a subset of the pass's 1..n; the merge validates the union.
type CheckExpectations struct {
	// Positions are the candidate numbers this call numbered. Empty
	// means the frozen shape with no call input, which callers that
	// hold no candidates use: shape only.
	Positions []int
	// Total is the pass's candidate count: duplicate targets must
	// number within it. Zero skips the range check, for callers
	// holding no pass numbering to contradict.
	Total int
}

// CheckDecision is one validated decision: the position it judges, what it
// decided, and the corrections it carries. Positions index the decisions in
// prompt order once Check has accepted the payload.
type CheckDecision struct {
	Position    int
	Decision    string
	DuplicateOf int
	Reason      string
	Severity    string
	HasSeverity bool
	PreExisting bool
	HasPre      bool
}

// Check runs the shape half and then the semantic half, and answers the
// decisions the payload carries. A nil expectations set checks shape only,
// for callers with no candidate numbering to contradict.
func Check(payload []byte, expected CheckExpectations) ([]CheckDecision, error) {
	decisions, err := CheckShape(payload)
	if err != nil {
		return nil, err
	}
	if len(expected.Positions) == 0 {
		return decisions, nil
	}
	if err := checkPositions(decisions, expected); err != nil {
		return nil, err
	}
	return decisions, nil
}

// CheckShape checks one check payload's shape and answers its decisions:
// an object carrying a decisions array of well-typed entries. Only shape
// is checked, because this is the entry point for callers with no
// candidate numbering to contradict; callers that hold it call Check,
// which runs this first and then the semantic half.
func CheckShape(payload []byte) ([]CheckDecision, error) {
	top, ok := jqParse(payload)
	if !ok {
		return nil, shapef("the payload is not parseable JSON")
	}
	if jqType(top) != "object" {
		return nil, shapef("the payload is not a JSON object")
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(top, &doc); err != nil {
		return nil, shapef("the payload is not parseable JSON")
	}
	raw, has := doc["decisions"]
	if !has || jqType(raw) != "array" {
		return nil, shapef("decisions is missing or not an array")
	}
	var elements []json.RawMessage
	if err := json.Unmarshal(raw, &elements); err != nil {
		return nil, shapef("decisions is missing or not an array")
	}
	out := make([]CheckDecision, 0, len(elements))
	for _, element := range elements {
		decision, err := checkDecisionShape(element)
		if err != nil {
			return nil, err
		}
		out = append(out, decision)
	}
	return out, nil
}

// checkDecisionShape is one decision entry's shape: a well-typed position,
// decision, duplicate_of, reason, severity and pre_existing. A null or a
// missing optional reads as unset; a present one must hold its type.
func checkDecisionShape(element json.RawMessage) (CheckDecision, error) {
	var out CheckDecision
	if jqType(element) != "object" {
		return out, shapef("a decision is not an object: %s", jqCompact(element))
	}
	var entry map[string]json.RawMessage
	if err := json.Unmarshal(element, &entry); err != nil {
		return out, shapef("a decision is not an object: %s", jqCompact(element))
	}
	position, ok := jqInt(entry["position"])
	if !ok || position < 1 {
		return out, shapef("a decision carries no usable position: %s", jqCompact(element))
	}
	out.Position = position
	decision, ok := jqString(entry["decision"])
	if !ok || (decision != "confirmed" && decision != "rejected" && decision != "duplicate") {
		return out, shapef("decision %d carries no usable decision: %s", position, jqCompact(element))
	}
	out.Decision = decision
	dup, present := entry["duplicate_of"]
	if present && jqType(dup) != "null" {
		target, ok := jqInt(dup)
		if !ok || target < 1 {
			return out, shapef("decision %d carries no usable duplicate_of: %s", position, jqCompact(element))
		}
		out.DuplicateOf = target
	}
	if decision == "duplicate" && out.DuplicateOf == 0 {
		return out, shapef("decision %d is a duplicate naming no other candidate", position)
	}
	if decision != "duplicate" && out.DuplicateOf != 0 {
		return out, shapef("decision %d is %s and still names candidate %d", position, decision, out.DuplicateOf)
	}
	reason, ok := jqString(entry["reason"])
	if !ok || len(bytes.TrimSpace([]byte(reason))) == 0 {
		return out, shapef("decision %d carries no reason", position)
	}
	out.Reason = reason
	if sev, present := entry["severity"]; present && jqType(sev) != "null" {
		name, ok := jqString(sev)
		if !ok || (name != "high" && name != "medium" && name != "low") {
			return out, shapef("decision %d carries no usable severity correction: %s", position, jqCompact(element))
		}
		out.Severity = name
		out.HasSeverity = true
	}
	if pre, present := entry["pre_existing"]; present && jqType(pre) != "null" {
		if jqType(pre) != "boolean" {
			return out, shapef("decision %d carries no usable pre_existing correction: %s", position, jqCompact(element))
		}
		out.PreExisting = string(bytes.TrimSpace(pre)) == "true"
		out.HasPre = true
	}
	return out, nil
}

// checkPositions contradicts the decisions against the positions the call
// numbered: exactly one decision per position, duplicate targets within
// the pass's range, no self-reference, and every duplicate chain ending
// at a confirmed candidate with no cycle.
func checkPositions(decisions []CheckDecision, expected CheckExpectations) error {
	positions := expected.Positions
	want := make(map[int]bool, len(positions))
	for _, position := range positions {
		want[position] = true
	}
	seen := make(map[int]bool, len(decisions))
	for _, decision := range decisions {
		if !want[decision.Position] {
			return semanticf("decision %d judges no candidate this call numbered", decision.Position)
		}
		if _, repeated := seen[decision.Position]; repeated {
			return semanticf("candidate %d carries two decisions", decision.Position)
		}
		seen[decision.Position] = true
	}
	for _, position := range positions {
		if _, ok := seen[position]; !ok {
			return semanticf("candidate %d carries no decision", position)
		}
	}
	byPosition := make(map[int]CheckDecision, len(decisions))
	for _, decision := range decisions {
		byPosition[decision.Position] = decision
	}
	for _, decision := range decisions {
		if decision.Decision != "duplicate" {
			continue
		}
		if decision.DuplicateOf == decision.Position {
			return semanticf("candidate %d duplicates itself", decision.Position)
		}
		if expected.Total > 0 && decision.DuplicateOf > expected.Total {
			return semanticf("candidate %d duplicates candidate %d, which is not a candidate in this pass", decision.Position, decision.DuplicateOf)
		}
		// The chain leaves this call's numbering behind: a packed call
		// judges a subset, and its duplicates may name a candidate
		// another call numbered. The merge validates the union; here
		// only the range and self-reference above are in reach.
		if _, ok := byPosition[decision.DuplicateOf]; !ok {
			continue
		}
		if err := checkDuplicateChain(byPosition, decision.Position); err != nil {
			return err
		}
	}
	return nil
}

// checkDuplicateChain walks one duplicate chain to its end: a confirmed
// candidate, reached without revisiting a position and without landing on
// a rejection. Callers hold every position the chain may name.
func checkDuplicateChain(byPosition map[int]CheckDecision, start int) error {
	_, err := walkDuplicateChain(byPosition, start)
	return err
}

// walkDuplicateChain follows one duplicate chain from its start,
// answering the positions it visited and where the chain fails: a
// cycle, a rejection, or a position with no decision. Callers hold
// every position the chain may name.
func walkDuplicateChain(byPosition map[int]CheckDecision, start int) ([]int, error) {
	visited := map[int]bool{start: true}
	chain := []int{start}
	at := byPosition[start].DuplicateOf
	for {
		next, ok := byPosition[at]
		if !ok {
			return chain, semanticf("candidate %d duplicates candidate %d, which carries no decision", start, at)
		}
		if visited[at] {
			return chain, semanticf("candidate %d sits on a duplicate cycle", start)
		}
		visited[at] = true
		chain = append(chain, at)
		switch next.Decision {
		case "confirmed":
			return chain, nil
		case "rejected":
			return chain, semanticf("candidate %d duplicates rejected candidate %d", start, at)
		}
		at = next.DuplicateOf
	}
}

// CheckChains validates the merged decisions of every check call the pass
// made: every duplicate chain in the union ends at a confirmed candidate
// with no cycle. Per-call validation already proved each call's own
// numbering; what is left is what spans calls.
func CheckChains(decisions []CheckDecision) error {
	byPosition := make(map[int]CheckDecision, len(decisions))
	for _, decision := range decisions {
		byPosition[decision.Position] = decision
	}
	for _, decision := range decisions {
		if decision.Decision != "duplicate" {
			continue
		}
		if err := checkDuplicateChain(byPosition, decision.Position); err != nil {
			return err
		}
	}
	return nil
}

// FailingChainPositions answers the sorted positions sitting on
// duplicate chains that fail: cycles, chains ending at a rejection,
// and chains naming a position with no decision. Empty when every
// chain ends confirmed.
func FailingChainPositions(decisions []CheckDecision) []int {
	byPosition := make(map[int]CheckDecision, len(decisions))
	for _, decision := range decisions {
		byPosition[decision.Position] = decision
	}
	seen := map[int]bool{}
	var out []int
	for _, decision := range decisions {
		if decision.Decision != "duplicate" {
			continue
		}
		chain, err := walkDuplicateChain(byPosition, decision.Position)
		if err == nil {
			continue
		}
		for _, position := range chain {
			if !seen[position] {
				seen[position] = true
				out = append(out, position)
			}
		}
	}
	sort.Ints(out)
	return out
}

// jqInt reports a raw value's integer content, and whether it was a whole
// number at all. A fractional literal is not one: positions are counted,
// never measured.
func jqInt(raw json.RawMessage) (int, bool) {
	if jqType(raw) != "number" {
		return 0, false
	}
	var number json.Number
	if err := json.Unmarshal(raw, &number); err != nil {
		return 0, false
	}
	n, err := number.Int64()
	if err != nil {
		return 0, false
	}
	return int(n), true
}
