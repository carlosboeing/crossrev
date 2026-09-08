package review

import (
	"errors"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/validate"
)

// TestCheckPayloadWithoutExpectationsKeepsTheShapeOnlyEntryPoint pins that
// a leg with no batch expectations still runs validate.Findings: the frozen
// prompt carries no numbered files, so there is no set to contradict. A leg
// with expectations runs validate.Review, which refuses an omitted unit the
// shape half accepts.
func TestCheckPayloadWithoutExpectationsKeepsTheShapeOnlyEntryPoint(t *testing.T) {
	leg := &Leg{}
	empty := `{"verdict":"converged","findings":[]}`
	if err := leg.checkPayload([]byte(empty)); err != nil {
		t.Fatalf("no expectations: wanted the empty payload accepted the way Findings accepts it, got %q", err)
	}
	if err := validate.Findings([]byte(empty)); err != nil {
		t.Fatalf("Findings: wanted acceptance, got %q", err)
	}
}

// TestCheckPayloadWithExpectationsRefusesAnOmittedUnit pins the seam change:
// the same coverage that passes the shape half fails the semantic half once
// the leg holds the batch it was built from.
func TestCheckPayloadWithExpectationsRefusesAnOmittedUnit(t *testing.T) {
	base, err := core.NewRevision("1111111111111111111111111111111111111111")
	if err != nil {
		t.Fatalf("base: %v", err)
	}
	head, err := core.NewRevision("2222222222222222222222222222222222222222")
	if err != nil {
		t.Fatalf("head: %v", err)
	}
	leg := &Leg{Expect: validate.ReviewExpectations{
		Base: base,
		Head: head,
		Units: []validate.UnitExpectation{
			{Path: "a.go", Revision: head, Lines: 10, Readable: true},
			{Path: "b.go", Revision: head, Lines: 4, Readable: true},
		},
	}}
	payload := `{"verdict":"converged","findings":[],` +
		`"coverage":[{"unit_number":1,"disposition":"no_issue","finding_numbers":[],` +
		`"evidence":[{"path":"a.go","revision":"2222222222222222222222222222222222222222",` +
		`"start_line":1,"end_line":10,"source":"git","note":null}],"reason":null}],` +
		`"examined_scope":"read the first file","known_limits":[]}`
	err = leg.checkPayload([]byte(payload))
	var semantic *validate.SemanticError
	if !errors.As(err, &semantic) {
		t.Fatalf("with expectations: wanted a *SemanticError, got %#v", err)
	}
	if !strings.Contains(semantic.Problem, "missing unit number(s) 2") {
		t.Errorf("problem = %q, want it to name the omitted unit", semantic.Problem)
	}
}

// TestCapitaliseName pins the Bash `$(printf '%s' "${h:0:1}" | tr '[:lower:]' '[:upper:]')${h:1}` at
// lib/run.sh:509, including the two edges the not-driven refusal never reaches
// expansion is the empty string, and a one-character name, where `${h:1}` is
// empty rather than out of range.
//
// It lives in an internal test file because capitaliseName is unexported and
// internal/review's other tests are package review_test, which cannot call it.
// The resolve leg carries the same function and pins it the same way.
func TestCapitaliseName(t *testing.T) {
	for _, tt := range []struct{ in, want string }{
		{"", ""},
		{"k", "K"},
		{"kimi", "Kimi"},
		{"Kimi", "Kimi"},
		{"opencode", "Opencode"},
	} {
		if got := capitaliseName(tt.in); got != tt.want {
			t.Errorf("capitaliseName(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
