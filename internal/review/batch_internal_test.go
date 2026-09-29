package review

import (
	"encoding/json"
	"testing"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/intel"
)

// finding_ids on an accepted unit are the batch-local 1-based finding
// positions the reviewer reported, not stable finding ids: the prompt
// numbers findings per batch, and the stable id is minted at enrich time
// from path, title and anchor.
func TestVerdictsFromPayloadKeepsFindingPositions(t *testing.T) {
	payload := json.RawMessage(`{"coverage":[{"unit_number":1,"verdict":"finding","finding_numbers":[2],"evidence":[],"reason":null}],"examined_scope":"read it","known_limits":[],"findings":[{"number":1},{"number":2}]}`)
	files := []intel.FileUnit{{ID: core.UnitID("u1"), Path: "a.go"}}
	verdicts, _, _, err := verdictsFromPayload(payload, files)
	if err != nil {
		t.Fatalf("verdictsFromPayload: %v", err)
	}
	got, ok := verdicts[core.UnitID("u1")]
	if !ok {
		t.Fatalf("no verdict for the batch's unit: %v", verdicts)
	}
	if len(got.FindingIDs) != 1 || got.FindingIDs[0] != "2" {
		t.Errorf("FindingIDs = %v, want [2] (the reported 1-based position)", got.FindingIDs)
	}
}

// An evidence revision the model invents is corrected, never refused:
// verdictsFromPayload records the content revision the batch showed the
// reviewer, so a cited revision that exists nowhere cannot halt the pass.
func TestVerdictsFromPayloadRecordsTheReviewedRevision(t *testing.T) {
	const reviewedSHA = "2c4a46cb321db01826d116b5ef2add6b0284d68c"
	head, err := core.NewRevision(reviewedSHA)
	if err != nil {
		t.Fatalf("revision %s: %v", reviewedSHA, err)
	}
	payload := json.RawMessage(`{"coverage":[{"unit_number":1,"verdict":"no_issue","finding_numbers":[],` +
		`"evidence":[{"path":"a.go","revision":"3333333333333333333333333333333333333333",` +
		`"start_line":1,"end_line":10,"source":"git","note":null}],"reason":null}],` +
		`"examined_scope":"read it","known_limits":[]}`)
	files := []intel.FileUnit{{ID: core.UnitID("u1"), Path: "a.go", ContentRevision: head}}
	verdicts, _, _, err := verdictsFromPayload(payload, files)
	if err != nil {
		t.Fatalf("verdictsFromPayload: %v", err)
	}
	got, ok := verdicts[core.UnitID("u1")]
	if !ok {
		t.Fatalf("no verdict for the batch's unit: %v", verdicts)
	}
	if len(got.Evidence) != 1 {
		t.Fatalf("Evidence = %v, want the one reported item", got.Evidence)
	}
	if rev := got.Evidence[0].Revision.Value(); rev != reviewedSHA {
		t.Errorf("Evidence revision = %q, want the reviewed %q", rev, reviewedSHA)
	}
}
