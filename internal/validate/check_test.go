package validate_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/validate"
)

// A well-formed answer over two candidates is accepted, with its decisions
// and corrections read back.
func TestCheckAcceptsOneDecisionPerPosition(t *testing.T) {
	payload := `{"decisions":[
		{"position":1,"decision":"confirmed","duplicate_of":null,"reason":"the nil dereference is real","severity":null,"pre_existing":null},
		{"position":2,"decision":"duplicate","duplicate_of":1,"reason":"same nil dereference","severity":"high","pre_existing":false}
	]}`
	got, err := validate.Check([]byte(payload), validate.CheckExpectations{Positions: []int{1, 2}})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("decisions = %d, want 2", len(got))
	}
	if got[0].Decision != "confirmed" || got[0].Reason != "the nil dereference is real" {
		t.Errorf("decision 1 = %+v", got[0])
	}
	if got[0].HasSeverity || got[0].HasPre {
		t.Errorf("decision 1 carries corrections it did not name: %+v", got[0])
	}
	if got[1].Decision != "duplicate" || got[1].DuplicateOf != 1 {
		t.Errorf("decision 2 = %+v", got[1])
	}
	if !got[1].HasSeverity || got[1].Severity != "high" {
		t.Errorf("decision 2 severity = %q, want the high correction", got[1].Severity)
	}
	if !got[1].HasPre || got[1].PreExisting {
		t.Errorf("decision 2 pre_existing = %v, want the false correction", got[1].PreExisting)
	}
}

// Shape failures are ShapeErrors: unparseable JSON, a non-object, a missing
// decisions array, and mistyped entries.
func TestCheckShapeRefusesMalformedPayloads(t *testing.T) {
	for _, tc := range []struct {
		name    string
		payload string
		want    string
	}{
		{"unparseable", `{"decisions":`, "not parseable"},
		{"non-object", `[]`, "not a JSON object"},
		{"missing decisions", `{}`, "decisions is missing"},
		{"decisions not an array", `{"decisions":{}}`, "decisions is missing"},
		{"non-object entry", `{"decisions":[1]}`, "not an object"},
		{"missing position", `{"decisions":[{"decision":"confirmed","duplicate_of":null,"reason":"x","severity":null,"pre_existing":null}]}`, "no usable position"},
		{"fractional position", `{"decisions":[{"position":1.5,"decision":"confirmed","duplicate_of":null,"reason":"x","severity":null,"pre_existing":null}]}`, "no usable position"},
		{"unknown decision", `{"decisions":[{"position":1,"decision":"maybe","duplicate_of":null,"reason":"x","severity":null,"pre_existing":null}]}`, "no usable decision"},
		{"duplicate without target", `{"decisions":[{"position":1,"decision":"duplicate","duplicate_of":null,"reason":"x","severity":null,"pre_existing":null}]}`, "naming no other candidate"},
		{"confirmed with target", `{"decisions":[{"position":1,"decision":"confirmed","duplicate_of":2,"reason":"x","severity":null,"pre_existing":null}]}`, "still names candidate 2"},
		{"missing reason", `{"decisions":[{"position":1,"decision":"confirmed","duplicate_of":null,"reason":"","severity":null,"pre_existing":null}]}`, "carries no reason"},
		{"bad severity", `{"decisions":[{"position":1,"decision":"confirmed","duplicate_of":null,"reason":"x","severity":"critical","pre_existing":null}]}`, "no usable severity"},
		{"bad pre_existing", `{"decisions":[{"position":1,"decision":"confirmed","duplicate_of":null,"reason":"x","severity":null,"pre_existing":"no"}]}`, "no usable pre_existing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := validate.Check([]byte(tc.payload), validate.CheckExpectations{Positions: []int{1}})
			if err == nil {
				t.Fatal("the payload was accepted")
			}
			var shape *validate.ShapeError
			if !errors.As(err, &shape) {
				t.Fatalf("err = %T %v, want a *ShapeError", err, err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %q, want it to say %q", err, tc.want)
			}
		})
	}
}

// Semantic failures are SemanticErrors: a missing or repeated position, a
// decision for a candidate the call never numbered, a self-reference, a
// chain that does not end confirmed, and a cycle.
func TestCheckSemanticRefusesContradictoryAnswers(t *testing.T) {
	dec := func(position int, decision string, dup int) string {
		target := "null"
		if dup > 0 {
			target = itoa(dup)
		}
		return `{"position":` + itoa(position) +
			`,"decision":"` + decision + `","duplicate_of":` + target +
			`,"reason":"because","severity":null,"pre_existing":null}`
	}
	for _, tc := range []struct {
		name      string
		positions []int
		payload   string
		want      string
	}{
		{"missing position", []int{1, 2},
			`{"decisions":[` + dec(1, "confirmed", 0) + `]}`, "candidate 2 carries no decision"},
		{"repeated position", []int{1},
			`{"decisions":[` + dec(1, "confirmed", 0) + `,` + dec(1, "rejected", 0) + `]}`, "candidate 1 carries two decisions"},
		{"unnumbered position", []int{1},
			`{"decisions":[` + dec(2, "confirmed", 0) + `]}`, "decision 2 judges no candidate"},
		{"self reference", []int{1, 2},
			`{"decisions":[` + dec(1, "duplicate", 1) + `,` + dec(2, "confirmed", 0) + `]}`, "candidate 1 duplicates itself"},
		{"duplicate of rejected", []int{1, 2},
			`{"decisions":[` + dec(1, "rejected", 0) + `,` + dec(2, "duplicate", 1) + `]}`, "duplicates rejected candidate 1"},
		{"two-cycle", []int{1, 2},
			`{"decisions":[` + dec(1, "duplicate", 2) + `,` + dec(2, "duplicate", 1) + `]}`, "duplicate cycle"},
		{"three-cycle", []int{1, 2, 3},
			`{"decisions":[` + dec(1, "duplicate", 2) + `,` + dec(2, "duplicate", 3) + `,` + dec(3, "duplicate", 1) + `]}`, "duplicate cycle"},
		{"chain through rejected", []int{1, 2, 3},
			`{"decisions":[` + dec(1, "rejected", 0) + `,` + dec(2, "duplicate", 1) + `,` + dec(3, "duplicate", 2) + `]}`, "duplicates rejected candidate 1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := validate.Check([]byte(tc.payload), validate.CheckExpectations{Positions: tc.positions})
			if err == nil {
				t.Fatal("the payload was accepted")
			}
			var semantic *validate.SemanticError
			if !errors.As(err, &semantic) {
				t.Fatalf("err = %T %v, want a *SemanticError", err, err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %q, want it to say %q", err, tc.want)
			}
		})
	}
}

// A duplicate chain through another duplicate still ends confirmed, and is
// accepted.
func TestCheckAcceptsADuplicateChainEndingConfirmed(t *testing.T) {
	payload := `{"decisions":[
		{"position":1,"decision":"confirmed","duplicate_of":null,"reason":"real","severity":null,"pre_existing":null},
		{"position":2,"decision":"duplicate","duplicate_of":1,"reason":"same","severity":null,"pre_existing":null},
		{"position":3,"decision":"duplicate","duplicate_of":2,"reason":"same again","severity":null,"pre_existing":null}
	]}`
	if _, err := validate.Check([]byte(payload), validate.CheckExpectations{Positions: []int{1, 2, 3}}); err != nil {
		t.Fatalf("Check: %v", err)
	}
}

// A duplicate naming a candidate no call numbered is refused against
// the pass's full range at the call, so the semantic retry corrects it
// there; a duplicate naming another call's candidate still passes to
// the merge.
func TestCheckRefusesADuplicateOutsideThePassRange(t *testing.T) {
	dec := func(position int, decision string, dup int) string {
		target := "null"
		if dup > 0 {
			target = itoa(dup)
		}
		return `{"position":` + itoa(position) +
			`,"decision":"` + decision + `","duplicate_of":` + target +
			`,"reason":"because","severity":null,"pre_existing":null}`
	}
	ranged := `{"decisions":[` + dec(1, "duplicate", 3) + `]}`
	if _, err := validate.Check([]byte(ranged), validate.CheckExpectations{Positions: []int{1}, Total: 2}); err == nil {
		t.Fatal("a duplicate of candidate 3 in a 2-candidate pass was accepted")
	} else {
		var semantic *validate.SemanticError
		if !errors.As(err, &semantic) {
			t.Fatalf("err = %T %v, want a *SemanticError", err, err)
		}
		if !strings.Contains(err.Error(), "candidate 1 duplicates candidate 3, which is not a candidate in this pass") {
			t.Errorf("err = %q", err)
		}
	}
	crossCall := `{"decisions":[` + dec(1, "duplicate", 2) + `]}`
	if _, err := validate.Check([]byte(crossCall), validate.CheckExpectations{Positions: []int{1}, Total: 2}); err != nil {
		t.Errorf("a duplicate of another call's candidate was refused: %v", err)
	}
	unranged := `{"decisions":[` + dec(1, "duplicate", 3) + `]}`
	if _, err := validate.Check([]byte(unranged), validate.CheckExpectations{Positions: []int{1}}); err != nil {
		t.Errorf("a caller without pass numbering is range-checked: %v", err)
	}
}

// FailingChainPositions names the positions on broken duplicate
// chains — cycles and chains ending at a rejection — and nothing on
// chains ending confirmed.
func TestFailingChainPositionsNamesOnlyBrokenChains(t *testing.T) {
	decisions := []validate.CheckDecision{
		{Position: 1, Decision: "confirmed"},
		{Position: 2, Decision: "duplicate", DuplicateOf: 1},
		{Position: 3, Decision: "duplicate", DuplicateOf: 4},
		{Position: 4, Decision: "rejected"},
		{Position: 5, Decision: "duplicate", DuplicateOf: 6},
		{Position: 6, Decision: "duplicate", DuplicateOf: 5},
	}
	got := validate.FailingChainPositions(decisions)
	want := []int{3, 4, 5, 6}
	if len(got) != len(want) {
		t.Fatalf("positions = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("positions = %v, want %v", got, want)
		}
	}
	clean := []validate.CheckDecision{
		{Position: 1, Decision: "confirmed"},
		{Position: 2, Decision: "duplicate", DuplicateOf: 1},
	}
	if got := validate.FailingChainPositions(clean); len(got) != 0 {
		t.Errorf("positions = %v, want none", got)
	}
}

// A duplicate chain may leave the call after an intermediate local
// candidate: positions 1 and 2 judged here, 2 naming 3, which another
// call numbered. The per-call check stops at the foreign position and
// leaves the endpoint for the merge, which still requires confirmed.
func TestCheckAcceptsAMultiHopChainCrossingACallBoundary(t *testing.T) {
	dec := func(position int, decision string, dup int) string {
		target := "null"
		if dup > 0 {
			target = itoa(dup)
		}
		return `{"position":` + itoa(position) +
			`,"decision":"` + decision + `","duplicate_of":` + target +
			`,"reason":"because","severity":null,"pre_existing":null}`
	}
	crossing := `{"decisions":[` + dec(1, "duplicate", 2) + `,` + dec(2, "duplicate", 3) + `]}`
	first, err := validate.Check([]byte(crossing), validate.CheckExpectations{Positions: []int{1, 2}, Total: 3})
	if err != nil {
		t.Fatalf("a chain leaving the call after a local candidate was refused: %v", err)
	}
	confirmed := `{"decisions":[` + dec(3, "confirmed", 0) + `]}`
	second, err := validate.Check([]byte(confirmed), validate.CheckExpectations{Positions: []int{3}, Total: 3})
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if err := validate.CheckChains(append(first, second...)); err != nil {
		t.Errorf("the merged chain ending confirmed was refused: %v", err)
	}
	rejected := `{"decisions":[` + dec(3, "rejected", 0) + `]}`
	third, err := validate.Check([]byte(rejected), validate.CheckExpectations{Positions: []int{3}, Total: 3})
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if err := validate.CheckChains(append(first, third...)); err == nil {
		t.Error("the merged chain ending rejected was accepted")
	}
	// A cycle the walk can close without leaving the call is still
	// refused at the call, foreign positions beside it or not.
	cycled := `{"decisions":[` + dec(1, "duplicate", 2) + `,` + dec(2, "duplicate", 3) + `,` + dec(3, "duplicate", 2) + `]}`
	if _, err := validate.Check([]byte(cycled), validate.CheckExpectations{Positions: []int{1, 2, 3}, Total: 5}); err == nil {
		t.Error("a cycle closed inside the call was accepted")
	} else if !strings.Contains(err.Error(), "duplicate cycle") {
		t.Errorf("err = %q, want it to name the cycle", err)
	}
}

// A packed call judges a subset, and its duplicates may name a candidate
// another call numbered: the per-call check leaves the cross-call chain
// for the merge, which CheckChains then judges.
func TestCheckChainsJudgesTheMergedDecisions(t *testing.T) {
	one := `{"decisions":[{"position":1,"decision":"duplicate","duplicate_of":2,"reason":"same","severity":null,"pre_existing":null}]}`
	two := `{"decisions":[{"position":2,"decision":"rejected","duplicate_of":null,"reason":"wrong","severity":null,"pre_existing":null}]}`
	first, err := validate.Check([]byte(one), validate.CheckExpectations{Positions: []int{1}})
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	second, err := validate.Check([]byte(two), validate.CheckExpectations{Positions: []int{2}})
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if err := validate.CheckChains(append(first, second...)); err == nil {
		t.Fatal("the merged chain ending rejected was accepted")
	} else if !strings.Contains(err.Error(), "duplicates rejected candidate 2") {
		t.Errorf("err = %q", err)
	}
	three := `{"decisions":[{"position":2,"decision":"confirmed","duplicate_of":null,"reason":"real","severity":null,"pre_existing":null}]}`
	confirmed, err := validate.Check([]byte(three), validate.CheckExpectations{Positions: []int{2}})
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if err := validate.CheckChains(append(first, confirmed...)); err != nil {
		t.Errorf("the merged chain ending confirmed was refused: %v", err)
	}
}

// With no numbered positions the check is shape only, for callers holding
// no candidate numbering to contradict.
func TestCheckWithoutPositionsChecksShapeOnly(t *testing.T) {
	if _, err := validate.Check([]byte(`{"decisions":[]}`), validate.CheckExpectations{}); err != nil {
		t.Errorf("an empty decision list is well-shaped: %v", err)
	}
	if _, err := validate.Check([]byte(`{}`), validate.CheckExpectations{}); err == nil {
		t.Error("a missing decisions array was accepted")
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
