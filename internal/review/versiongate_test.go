package review_test

import (
	"strings"
	"testing"
)

// The review leg refuses an opencode past its supported majors before any run
// child starts (issue #272). The version probe is the only spec the runner
// sees.
func TestReviewRefusesAnOpencodePastTheSupportedMajor(t *testing.T) {
	e := newEnv(t)
	e.runner.version = "opencode v3.1.4"
	req := e.request(t)
	req.HarnessOverride = "opencode"

	got := runLeg(t, e, req)
	if got.Err == nil {
		t.Fatal("the leg accepted an opencode 3.x install")
	}
	if !strings.Contains(got.Err.Error(), "opencode 1.x and 2.x") {
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

// A supported 2.x install passes the gate, and the confirmed major reaches
// the spec: the run child carries the 2.x vector rather than the 1.x one.
func TestReviewBuildsThe2xVectorOnA2xInstall(t *testing.T) {
	e := newEnv(t)
	e.runner.version = "opencode v2.0.15"
	req := e.request(t)
	req.HarnessOverride = "opencode"

	runLeg(t, e, req)
	if len(e.runner.probes) == 0 {
		t.Fatal("the leg started no version probe")
	}
	if len(e.runner.specs) == 0 {
		t.Fatal("the leg started no run child on a supported install")
	}
	got := e.runner.specs[0].Args
	if len(got) < 2 || got[0] != "run" || got[1] != "--standalone" {
		t.Errorf("the run child does not open with run --standalone: %v", got)
	}
}
