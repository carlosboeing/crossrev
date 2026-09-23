package review

import (
	"testing"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/prstate"
)

// A file-v1 generation contributes no verdicts under the file-v2 engine: the
// enumeration semantics changed, so a resumed pass starts from zero accepted
// units rather than trusting what an older engine covered.
func TestAcceptedFromGenerationIgnoresFileV1(t *testing.T) {
	base, errBase := core.NewRevision("1111111111111111111111111111111111111111")
	head, errHead := core.NewRevision("2222222222222222222222222222222222222222")
	if errBase != nil || errHead != nil {
		t.Fatalf("revisions: %v %v", errBase, errHead)
	}
	record := prstate.Record{Type: prstate.CoverageRecordUnit, UnitID: "e7d250f4226ea120"}
	record.Verdict = prstate.Some("no_issue")
	gen := prstate.Generation{
		Gen:      1,
		Revision: core.RevisionPair{Base: base, Head: head},
		Engine:   "file-v1",
		Records:  []prstate.Record{record},
	}
	if got := acceptedFromGeneration(gen, base, head, core.FileEngineVersion); len(got) != 0 {
		t.Errorf("accepted from a file-v1 generation under %s = %d, want 0", core.FileEngineVersion, len(got))
	}
	gen.Engine = core.FileEngineVersion
	if got := acceptedFromGeneration(gen, base, head, core.FileEngineVersion); len(got) != 1 {
		t.Errorf("accepted from a same-engine generation = %d, want 1", len(got))
	}
}
