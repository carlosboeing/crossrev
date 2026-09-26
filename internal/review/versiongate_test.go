package review_test

import (
	"strings"
	"testing"
)

// The review leg refuses an opencode past its supported major before any run
// child starts (issue #272). The version probe is the only spec the runner
// sees.
func TestReviewRefusesAnOpencodePastTheSupportedMajor(t *testing.T) {
	e := newEnv(t)
	e.runner.version = "opencode v2.0.15"
	req := e.request(t)
	req.HarnessOverride = "opencode"

	got := runLeg(t, e, req)
	if got.Err == nil {
		t.Fatal("the leg accepted an opencode 2.x install")
	}
	if !strings.Contains(got.Err.Error(), "opencode 1.x") {
		t.Errorf("err = %q, want the supported range named", got.Err)
	}
	if !strings.Contains(got.Err.Error(), "#272") {
		t.Errorf("err = %q, want issue #272 named", got.Err)
	}
	if len(e.runner.specs) != 0 {
		t.Fatalf("the runner started %d session children, want none: %v", len(e.runner.specs), e.runner.specs)
	}
	if len(e.runner.probes) != 1 {
		t.Fatalf("the runner saw %d version probes, want 1", len(e.runner.probes))
	}
	if got := e.runner.probes[0].Args; len(got) != 1 || got[0] != "--version" {
		t.Errorf("the only child was the version probe; got %v", got)
	}
}
