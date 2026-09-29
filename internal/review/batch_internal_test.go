package review

import (
	"encoding/json"
	"strconv"
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

// A coverage entry may cite any supplied path, and a deletion is read at the
// base while every other change is read at the head, so the recorded
// revision follows the cited path's content revision, not the covering
// unit's: evidence for the deleted file records the base even when the
// verdict covers the modified file, and vice versa.
func TestVerdictsFromPayloadFollowsTheCitedPath(t *testing.T) {
	const baseSHA = "1111111111111111111111111111111111111111"
	const headSHA = "2c4a46cb321db01826d116b5ef2add6b0284d68c"
	base, err := core.NewRevision(baseSHA)
	if err != nil {
		t.Fatalf("revision %s: %v", baseSHA, err)
	}
	head, err := core.NewRevision(headSHA)
	if err != nil {
		t.Fatalf("revision %s: %v", headSHA, err)
	}
	evidence := func(path string) string {
		return `{"path":` + strconv.Quote(path) + `,"revision":"3333333333333333333333333333333333333333",` +
			`"start_line":1,"end_line":10,"source":"git","note":null}`
	}
	payload := json.RawMessage(`{"coverage":[` +
		`{"unit_number":1,"verdict":"no_issue","finding_numbers":[],"evidence":[` + evidence("a.go") + `],"reason":null},` +
		`{"unit_number":2,"verdict":"no_issue","finding_numbers":[],"evidence":[` + evidence("old.go") + `],"reason":null}],` +
		`"examined_scope":"read it","known_limits":[]}`)
	files := []intel.FileUnit{
		{ID: core.UnitID("u1"), Path: "old.go", Change: core.ChangeDeleted, ContentRevision: base},
		{ID: core.UnitID("u2"), Path: "a.go", Change: core.ChangeModified, ContentRevision: head},
	}
	verdicts, _, _, err := verdictsFromPayload(payload, files)
	if err != nil {
		t.Fatalf("verdictsFromPayload: %v", err)
	}
	for id, want := range map[core.UnitID]string{"u1": headSHA, "u2": baseSHA} {
		got, ok := verdicts[id]
		if !ok {
			t.Fatalf("no verdict for unit %s: %v", id, verdicts)
		}
		if len(got.Evidence) != 1 {
			t.Fatalf("unit %s: Evidence = %v, want the one reported item", id, got.Evidence)
		}
		if rev := got.Evidence[0].Revision.Value(); rev != want {
			t.Errorf("unit %s: Evidence revision = %q, want the cited path's %q", id, rev, want)
		}
	}
}
