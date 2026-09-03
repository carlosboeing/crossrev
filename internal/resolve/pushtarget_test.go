package resolve

import (
	"strings"
	"testing"
)

// A fork pull request whose payload names no head repository refuses, rather
// than pushing to the repository the checkout happens to point at.
//
// lib/run.sh:285-291 sets CTX_HEAD_REPO to this repository ONLY on an explicit
// `isCrossRepository: false`. Every other case reads it out of the payload and
// leaves it empty when it is not there, which lib/legs.sh:468 then refuses.
// Defaulting to the origin repository skipped that refusal, and the fork's
// maintainer-edit check with it.
func TestAForkWithNoHeadRepositoryRefusesToPush(t *testing.T) {
	e := setup(t)
	e.forge.pr.IsCrossRepository = true
	e.forge.pr.HeadRepositoryOwner = ""
	e.forge.pr.HeadRepository = ""
	e.forge.pr.MaintainerCanModify = true
	e.addReview(t, defaultFindings(), "issues-remain")

	got := e.run(t)
	if got.Err == nil {
		t.Fatal("a fork with no head repository pushed anyway")
	}
	if !strings.Contains(got.Err.Error(), "could not determine the head repository for this pull request") {
		t.Fatalf("refusal = %q, want the head-repository one (lib/legs.sh:468)", got.Err)
	}
}
