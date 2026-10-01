package review

import (
	"encoding/json"
	"github.com/carlosboeing/crossrev/internal/prstate"
	"reflect"
	"testing"
)

func concernAnswer(verdict, title, why string) json.RawMessage {
	findings := []map[string]any{}
	numbers := []int{}
	if title != "" {
		findings = append(findings, map[string]any{"path": "a.go", "side": "RIGHT", "line": 1, "category": "correctness", "title": title, "why": why, "severity": "high", "fix": "check"})
		if verdict == "finding" {
			numbers = []int{1}
		}
	}
	raw, _ := json.Marshal(map[string]any{"verdict": "issues-remain", "findings": findings, "coverage": []map[string]any{{"unit_number": 1, "verdict": verdict, "finding_numbers": numbers, "evidence": []any{}, "reason": "read"}}, "examined_scope": "read", "known_limits": []string{}})
	return raw
}

func TestMergeConcernsVerdictTable(t *testing.T) {
	values := []string{"no_issue", "not_affected", "finding", "could_not_review"}
	for _, a := range values {
		for _, b := range values {
			t.Run(a+"/"+b, func(t *testing.T) {
				want := "no_issue"
				if a == "not_affected" && b == "not_affected" {
					want = "not_affected"
				}
				if a == "finding" || b == "finding" {
					want = "finding"
				}
				if a == "could_not_review" || b == "could_not_review" {
					want = "could_not_review"
				}
				raw, err := mergeConcernPayloads([]json.RawMessage{concernAnswer(a, "A", "why"), concernAnswer(b, "B", "why")}, []string{"correctness", "consistency"})
				if err != nil {
					t.Fatal(err)
				}
				var doc struct {
					Coverage []struct {
						Verdict        string
						FindingNumbers []int `json:"finding_numbers"`
					}
				}
				if err := json.Unmarshal(raw, &doc); err != nil {
					t.Fatal(err)
				}
				if got := doc.Coverage[0].Verdict; got != want {
					t.Fatalf("got %s want %s", got, want)
				}
				if want == "could_not_review" && len(doc.Coverage[0].FindingNumbers) != 0 {
					t.Fatal("unjudged unit names findings")
				}
			})
		}
	}
}

func TestMergeConcernsRenumbersAndCollapsesOnlyIdenticalCandidates(t *testing.T) {
	for _, same := range []bool{true, false} {
		why := "why"
		if !same {
			why = "why "
		}
		raw, err := mergeConcernPayloads([]json.RawMessage{concernAnswer("finding", "A", "why"), concernAnswer("finding", "A", why)}, []string{"correctness", "consistency"})
		if err != nil {
			t.Fatal(err)
		}
		var doc struct {
			Findings []struct {
				Concerns []string
			}
			Coverage []struct {
				FindingNumbers []int `json:"finding_numbers"`
			}
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatal(err)
		}
		count := 2
		if same {
			count = 1
		}
		if len(doc.Findings) != count {
			t.Fatalf("same=%v got %d findings want %d", same, len(doc.Findings), count)
		}
		want := []int{1}
		if !same {
			want = []int{1, 2}
		}
		if !reflect.DeepEqual(doc.Coverage[0].FindingNumbers, want) {
			t.Fatalf("numbers=%v want %v", doc.Coverage[0].FindingNumbers, want)
		}
		if same && !reflect.DeepEqual(doc.Findings[0].Concerns, []string{"correctness", "consistency"}) {
			t.Fatalf("provenance=%v", doc.Findings[0].Concerns)
		}
	}
}

func TestSplitConcernUnionRenumbersAcrossParts(t *testing.T) {
	first, err := mergeConcernPayloads([]json.RawMessage{concernAnswer("finding", "A", "why"), concernAnswer("finding", "B", "why")}, []string{"correctness", "consistency"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := mergeConcernPayloads([]json.RawMessage{concernAnswer("finding", "C", "why"), concernAnswer("finding", "A", "why")}, []string{"correctness", "consistency"})
	if err != nil {
		t.Fatal(err)
	}
	ps := &pendingSplit{count: 2, verdicts: []string{"finding", "finding"}, numbers: [][]int{{1, 2}, {1, 2}}, reasons: []string{"read", "read"}, evidence: make([][]prstate.Evidence, 2), payloads: []json.RawMessage{first, second}, diffs: make([][]byte, 2)}
	verdicts, _, payload, err := mergePendingSplit(ps)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(verdicts[ps.unit.ID].FindingIDs, []string{"1", "2", "3"}) {
		t.Fatalf("ids=%v", verdicts[ps.unit.ID].FindingIDs)
	}
	var doc struct {
		Findings []struct {
			Title    string
			Concerns []string
		}
	}
	if err := json.Unmarshal(payload, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Findings) != 3 {
		t.Fatalf("findings=%d", len(doc.Findings))
	}
	if got := []string{doc.Findings[0].Title, doc.Findings[1].Title, doc.Findings[2].Title}; !reflect.DeepEqual(got, []string{"A", "B", "C"}) {
		t.Fatalf("union order=%v", got)
	}
	if !reflect.DeepEqual(doc.Findings[0].Concerns, []string{"correctness", "consistency"}) {
		t.Fatalf("provenance=%v", doc.Findings[0].Concerns)
	}
}

func TestConcernUnionUsesArrayPositionsWithoutAddingNumberFields(t *testing.T) {
	raw, err := mergeConcernPayloads([]json.RawMessage{concernAnswer("finding", "A", "why"), concernAnswer("finding", "B", "why")}, []string{"correctness", "consistency"})
	if err != nil {
		t.Fatal(err)
	}
	var doc struct{ Findings []map[string]json.RawMessage }
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	for _, f := range doc.Findings {
		if _, ok := f["number"]; ok {
			t.Fatal("finding references use array positions, not a new number field")
		}
	}
}
