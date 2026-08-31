package resolve

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/carlosboeing/crossrev/internal/validate"
)

// TestValidate pins the shape/semantic split measured from lib/validate.sh
// against this checkout:
//
//	missing rc=2
//	duplicate rc=2
//	shape (finding_id) rc=1
func TestValidate(t *testing.T) {
	expect := &validate.Expectations{Findings: 1, Candidates: nil}

	t.Run("missing resolutions return semantic code 2", func(t *testing.T) {
		err := validate.Resolve(missingPayload(), expect)
		if err == nil {
			t.Fatal("missing payload was accepted")
		}
		var sem *validate.SemanticError
		if !errors.As(err, &sem) {
			t.Fatalf("err is %T %v, want *validate.SemanticError", err, err)
		}
		if sem.Code() != 2 {
			t.Fatalf("Code = %d, want 2", sem.Code())
		}
	})

	t.Run("duplicate resolutions return semantic code 2", func(t *testing.T) {
		err := validate.Resolve(duplicatePayload(), expect)
		if err == nil {
			t.Fatal("duplicate payload was accepted")
		}
		var sem *validate.SemanticError
		if !errors.As(err, &sem) {
			t.Fatalf("err is %T %v, want *validate.SemanticError", err, err)
		}
		if sem.Code() != 2 {
			t.Fatalf("Code = %d, want 2", sem.Code())
		}
	})

	t.Run("a finding_id field is a shape failure code 1", func(t *testing.T) {
		err := validate.Resolve(shapePayload(), expect)
		if err == nil {
			t.Fatal("finding_id payload was accepted")
		}
		var shape *validate.ShapeError
		if !errors.As(err, &shape) {
			t.Fatalf("err is %T %v, want *validate.ShapeError", err, err)
		}
		if shape.Code() != 1 {
			t.Fatalf("Code = %d, want 1", shape.Code())
		}
	})

	t.Run("the invoke loop maps finding_number back to id", func(t *testing.T) {
		e := setup(t)
		e.addReview(t, defaultFindings(), "issues-remain")
		got := e.run(t)
		if got.Err != nil {
			t.Fatalf("Run: %v", got.Err)
		}
		var resolutions []map[string]json.RawMessage
		if err := json.Unmarshal(got.Resolutions, &resolutions); err != nil {
			t.Fatalf("resolutions: %v", err)
		}
		if len(resolutions) != 1 {
			t.Fatalf("len(resolutions) = %d, want 1", len(resolutions))
		}
		if _, ok := resolutions[0]["finding_number"]; ok {
			t.Fatalf("finding_number survived the mapping: %s", got.Resolutions)
		}
		var id string
		if err := json.Unmarshal(resolutions[0]["finding_id"], &id); err != nil {
			t.Fatalf("finding_id: %v", err)
		}
		if id != testFinding {
			t.Fatalf("finding_id = %q, want %q", id, testFinding)
		}
	})

	t.Run("two semantic failures abort", func(t *testing.T) {
		e := setup(t)
		e.addReview(t, defaultFindings(), "issues-remain")
		e.adapter.payloads = []json.RawMessage{missingPayload(), missingPayload()}
		got := e.run(t)
		if got.Err == nil {
			t.Fatal("two semantic failures were accepted")
		}
		if e.adapter.calls != 2 {
			t.Fatalf("adapter calls = %d, want 2", e.adapter.calls)
		}
	})
}
