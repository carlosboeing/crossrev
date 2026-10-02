package review

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/carlosboeing/crossrev/internal/config"
	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/harness"
	"github.com/carlosboeing/crossrev/internal/policy"
	"github.com/carlosboeing/crossrev/internal/prstate"
	"github.com/carlosboeing/crossrev/internal/validate"
)

// checkReasonCap is the longest reason a check record keeps: the checker's
// one line, in at most 300 characters. The transcript keeps the full text.
const checkReasonCap = 300

// checkTitleCap is the longest title a checked-out entry keeps.
const checkTitleCap = 120

// applyCheckDecisions applies validated decisions to the candidate
// findings: corrections with their originals recorded, duplicate groups
// folded onto their confirmed survivor with the highest severity and the
// union of concerns, and rejected and duplicate entries stamped
// posted:false so they never post and never reach the resolver input.
//
// ordered carries the findings in candidate order: decisions judge
// positions 1..n of it, and each candidate's index addresses its entry
// in the stored record. It answers the updated findings, the record
// decisions with the survivors' original concerns filled in, and the
// checked-out entries for the marker.
//
// The edit goes through the order-preserving node rather than a struct
// round-trip, the way stampNotPosted does, so an untouched finding
// encodes exactly as the enricher wrote it.
//
// A raw record that no longer parses beside its findings fails loudly:
// applying decisions positionally onto entries they were not judged
// for would post the wrong set.
func applyCheckDecisions(raw json.RawMessage, ordered []checkCandidate, decisions []validate.CheckDecision) (json.RawMessage, []prstate.CheckDecision, []prstate.CheckedOutEntry, error) {
	byPosition := make(map[int]validate.CheckDecision, len(decisions))
	for _, decision := range decisions {
		byPosition[decision.Position] = decision
	}
	groups := checkGroups(decisions)
	var nodes []harness.Node
	if err := json.Unmarshal(raw, &nodes); err != nil || len(nodes) != len(ordered) {
		return nil, nil, nil, fmt.Errorf("the check decisions do not align with the %d stored findings", len(ordered))
	}
	records := make([]prstate.CheckDecision, 0, len(decisions))
	var checkedOut []prstate.CheckedOutEntry
	for _, candidate := range ordered {
		decision, ok := byPosition[candidate.position]
		if !ok {
			continue
		}
		if candidate.index < 0 || candidate.index >= len(nodes) {
			return nil, nil, nil, fmt.Errorf("candidate %d addresses no stored finding", candidate.position)
		}
		finding := candidate.finding
		record := prstate.CheckDecision{
			Position: candidate.position,
			ID:       finding.ID,
			Decision: decision.Decision,
			Reason:   truncateRunes(decision.Reason, checkReasonCap),
		}
		if decision.Decision == "duplicate" {
			record.DuplicateOf = decision.DuplicateOf
		}
		if decision.HasSeverity {
			record.Severity = decision.Severity
		}
		if decision.HasPre {
			pre := decision.PreExisting
			record.PreExisting = &pre
		}
		node := &nodes[candidate.index]
		switch decision.Decision {
		case "duplicate", "rejected":
			node.Set("posted", harness.FromBool(false))
			checkedOut = append(checkedOut, prstate.CheckedOutEntry{
				Position: candidate.position,
				ID:       finding.ID,
				Path:     finding.Path,
				Line:     finding.Line,
				Title:    truncateRunes(finding.Title, checkTitleCap),
				Reason:   record.Reason,
				Decision: decision.Decision,
			})
		default:
			members := groups[candidate.position]
			applySeverity(node, finding, groupSeverity(ordered, byPosition, candidate.position, members))
			applyPreExisting(node, finding, decision)
			if unioned := unionGroupConcerns(ordered, candidate.position, members); unioned != nil {
				original := finding.Concerns
				if original == nil {
					original = []string{}
				}
				if rawConcerns, err := json.Marshal(original); err == nil {
					record.OriginalConcerns = rawConcerns
				}
				setConcerns(node, unioned)
			}
		}
		records = append(records, record)
	}
	sort.Slice(records, func(i, j int) bool { return records[i].Position < records[j].Position })
	sort.Slice(checkedOut, func(i, j int) bool { return checkedOut[i].Position < checkedOut[j].Position })
	out, err := json.Marshal(nodes)
	if err != nil {
		return nil, nil, nil, err
	}
	return out, records, checkedOut, nil
}

// checkGroups folds duplicate decisions onto their confirmed survivor:
// each root position beside its member positions, transitively. Chains
// the validator refused cannot reach here; a defensive break stops a
// malformed one from looping.
func checkGroups(decisions []validate.CheckDecision) map[int][]int {
	byPosition := make(map[int]validate.CheckDecision, len(decisions))
	for _, decision := range decisions {
		byPosition[decision.Position] = decision
	}
	groups := map[int][]int{}
	for _, decision := range decisions {
		if decision.Decision != "duplicate" {
			continue
		}
		root := decision.Position
		visited := map[int]bool{root: true}
		for {
			next, ok := byPosition[root]
			if !ok || next.Decision != "duplicate" {
				break
			}
			root = next.DuplicateOf
			if visited[root] {
				root = -1
				break
			}
			visited[root] = true
		}
		if root <= 0 {
			continue
		}
		if survivor, ok := byPosition[root]; !ok || survivor.Decision != "confirmed" {
			continue
		}
		groups[root] = append(groups[root], decision.Position)
	}
	return groups
}

// applySeverity writes a confirmed survivor's group severity: the highest
// among its members, each read at its corrected value. A change records
// what the reviewer raised.
func applySeverity(node *harness.Node, finding Finding, severity string) {
	if severity == "" || severity == finding.Severity {
		return
	}
	node.Set("original_severity", harness.FromString(finding.Severity))
	node.Set("severity", harness.FromString(severity))
}

// applyPreExisting writes a confirmed candidate's pre_existing correction.
// A change records what the reviewer raised.
func applyPreExisting(node *harness.Node, finding Finding, decision validate.CheckDecision) {
	if !decision.HasPre || decision.PreExisting == finding.PreExisting {
		return
	}
	node.Set("original_pre_existing", harness.FromBool(finding.PreExisting))
	node.Set("pre_existing", harness.FromBool(decision.PreExisting))
}

// candidateAt answers the candidate judging a position, or false when
// the position numbers nothing.
func candidateAt(ordered []checkCandidate, position int) (checkCandidate, bool) {
	if position-1 < 0 || position-1 >= len(ordered) || ordered[position-1].position != position {
		return checkCandidate{}, false
	}
	return ordered[position-1], true
}

// unionGroupConcerns answers the union of a confirmed group's concerns —
// the survivor's beside every member's — in fixed order, or nil when the
// group adds nothing to what the survivor already carries.
func unionGroupConcerns(ordered []checkCandidate, root int, members []int) []string {
	if len(members) == 0 {
		return nil
	}
	survivor, ok := candidateAt(ordered, root)
	if !ok {
		return nil
	}
	seen := map[string]bool{}
	var union []string
	add := func(list []string) {
		for _, concern := range list {
			if !seen[concern] {
				seen[concern] = true
				union = append(union, concern)
			}
		}
	}
	add(survivor.finding.Concerns)
	for _, position := range members {
		if member, ok := candidateAt(ordered, position); ok {
			add(member.finding.Concerns)
		}
	}
	union = concernOrder(union)
	if sameConcerns(union, survivor.finding.Concerns) {
		return nil
	}
	return union
}

// sameConcerns reports whether two concern lists name the same set,
// whatever order either lists it in.
func sameConcerns(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := make(map[string]bool, len(a))
	for _, concern := range a {
		seen[concern] = true
	}
	for _, concern := range b {
		if !seen[concern] {
			return false
		}
	}
	return true
}

// stripCheckApplication removes a check's application from stored
// findings: the posted:false stamps, and the corrections reverted to
// their recorded originals. At the seam every posted:false is the
// check's — the publish hold stamps later — so each one goes.
//
// The concerns union stays: only the record knows what the survivor
// carried before it, and revertCheckConcerns restores that where a
// record survived. Without one the union stands, a superset of what
// raised the finding.
func stripCheckApplication(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 || string(raw) == "null" {
		return raw
	}
	var nodes []harness.Node
	if err := json.Unmarshal(raw, &nodes); err != nil {
		return raw
	}
	for i := range nodes {
		node := &nodes[i]
		if posted := node.Member("posted"); posted.Present() && !posted.Truthy() {
			node.Delete("posted")
		}
		if original, ok := node.Member("original_severity").AsString(); ok {
			node.Set("severity", harness.FromString(original))
			node.Delete("original_severity")
		}
		if pre, present := node.Get("original_pre_existing"); present && !pre.IsNull() {
			node.Set("pre_existing", harness.FromBool(pre.Truthy()))
			node.Delete("original_pre_existing")
		}
	}
	out, err := json.Marshal(nodes)
	if err != nil {
		return raw
	}
	return out
}

// revertCheckConcerns restores the survivors' concerns from a discarded
// record: what each carried before the group union. Positions number
// the ordered candidates, and each candidate's index addresses its
// entry in the stored record; an empty original removes the key.
func revertCheckConcerns(raw json.RawMessage, records []prstate.CheckDecision, ordered []checkCandidate) json.RawMessage {
	if len(raw) == 0 || string(raw) == "null" {
		return raw
	}
	var nodes []harness.Node
	if err := json.Unmarshal(raw, &nodes); err != nil {
		return raw
	}
	for _, record := range records {
		if len(record.OriginalConcerns) == 0 {
			continue
		}
		candidate, ok := candidateAt(ordered, record.Position)
		if !ok || candidate.index < 0 || candidate.index >= len(nodes) {
			continue
		}
		at := candidate.index
		// A stale record may index findings that moved: the id
		// reconciles, so a revert never lands on the wrong entry.
		if id, ok := nodes[at].Member("id").AsString(); !ok || id != record.ID {
			continue
		}
		if string(record.OriginalConcerns) == "[]" {
			nodes[at].Delete("concerns")
			continue
		}
		var original []string
		if err := json.Unmarshal(record.OriginalConcerns, &original); err != nil {
			continue
		}
		setConcerns(&nodes[at], original)
	}
	out, err := json.Marshal(nodes)
	if err != nil {
		return raw
	}
	return out
}

// truncateRunes cuts s to at most cap characters, on a rune boundary.
func truncateRunes(s string, cap int) string {
	if utf8.RuneCountInString(s) <= cap {
		return s
	}
	runes := []rune(s)
	return string(runes[:cap])
}

// setConcerns writes the unioned concerns onto a finding node.
func setConcerns(node *harness.Node, concerns []string) {
	raw, err := json.Marshal(concerns)
	if err != nil {
		return
	}
	var value harness.Node
	if err := json.Unmarshal(raw, &value); err != nil {
		return
	}
	node.Set("concerns", value)
}

// concernOrder ranks the known concerns in config order; anything else
// sorts after them, alphabetically.
func concernOrder(concerns []string) []string {
	rank := map[string]int{
		config.ReviewConcernCorrectness:  0,
		config.ReviewConcernConsistency:  1,
	}
	out := append([]string(nil), concerns...)
	sort.SliceStable(out, func(i, j int) bool {
		ri, oki := rank[out[i]]
		rj, okj := rank[out[j]]
		if oki && okj {
			return ri < rj
		}
		if oki != okj {
			return oki
		}
		return strings.Compare(out[i], out[j]) < 0
	})
	return out
}

// groupSeverity answers the highest severity among a confirmed group's
// members, each read at its corrected value.
func groupSeverity(ordered []checkCandidate, decisions map[int]validate.CheckDecision, root int, members []int) string {
	best := ""
	for _, position := range append([]int{root}, members...) {
		severity := ""
		if candidate, ok := candidateAt(ordered, position); ok {
			severity = candidate.finding.Severity
		}
		if decision, ok := decisions[position]; ok && decision.HasSeverity {
			severity = decision.Severity
		}
		if policy.SeverityRank(core.Severity(severity)) > policy.SeverityRank(core.Severity(best)) {
			best = severity
		}
	}
	return best
}
