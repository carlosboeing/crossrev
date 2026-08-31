package review_test

import (
	"context"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/exec"
	"github.com/carlosboeing/crossrev/internal/review"
	"github.com/carlosboeing/crossrev/internal/validate"
)

func TestInvokeWriteCapabilityIsFalse(t *testing.T) {
	e := newEnv(t)
	got := runLeg(t, e, e.request(t))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if len(e.runner.Specs()) != 1 {
		t.Fatalf("harness calls = %d, want 1", len(e.runner.Specs()))
	}
	spec := e.runner.Specs()[0]
	joined := strings.Join(spec.Args, " ")
	if strings.Contains(joined, "acceptEdits") {
		t.Errorf("review invocation granted writes: %v", spec.Args)
	}
	if _, found := forgeCredentialIn(spec.Env); found {
		t.Errorf("spec env carried a forge credential: %v", spec.Env)
	}
}

func TestInvokeStripsAForgeCredentialFromTheHarnessEnvironment(t *testing.T) {
	e := newEnv(t)
	leg := e.leg(t)
	leg.Env = []string{"PATH=/usr/bin:/bin", "GH_TOKEN=should-never-reach-the-model", "HOME=" + e.dir}
	req := e.request(t)
	got := leg.Run(context.Background(), req)
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	specs := e.runner.Specs()
	if len(specs) == 0 {
		t.Fatal("no harness spec")
	}
	if name, found := forgeCredentialIn(specs[0].Env); found {
		t.Fatalf("harness env carried %s", name)
	}
}

func TestInvokeSemanticRetryRunsTheHarnessOnceMore(t *testing.T) {
	e := newEnv(t)
	leg := e.leg(t)
	attempts := 0
	leg.Validate = func(payload []byte) error {
		attempts++
		if attempts == 1 {
			return &validate.SemanticError{Problem: "finding 9 was not in the numbered list"}
		}
		return validate.Findings(payload)
	}
	got := leg.Run(context.Background(), e.request(t))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if len(e.runner.Specs()) != 2 {
		t.Fatalf("harness calls = %d, want 2 (one semantic retry)", len(e.runner.Specs()))
	}
	if got.Outcome != review.OutcomeInvoked {
		t.Errorf("Outcome = %q, want invoked", got.Outcome)
	}
}

func TestInvokeSchemaNativeShapeErrorDoesNotRetry(t *testing.T) {
	e := newEnv(t)
	leg := e.leg(t)
	leg.Validate = func([]byte) error {
		return &validate.ShapeError{Problem: "no verdict key"}
	}
	got := leg.Run(context.Background(), e.request(t))
	if got.Err == nil {
		t.Fatal("wanted a shape failure")
	}
	if len(e.runner.Specs()) != 1 {
		t.Fatalf("harness calls = %d, want 1 (schema-native shape does not retry)", len(e.runner.Specs()))
	}
}

func TestInvokeRecordsClaimThenHarness(t *testing.T) {
	e := newEnv(t)
	if got := runLeg(t, e, e.request(t)); got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	events := e.log.all()
	if len(events) < 2 || events[0] != "claim" || events[1] != "harness" {
		t.Fatalf("event order = %v, want claim then harness", events)
	}
}

func forgeCredentialIn(env []string) (string, bool) {
	for _, name := range exec.ForgeCredentialNames() {
		prefix := name + "="
		for _, entry := range env {
			if strings.HasPrefix(entry, prefix) {
				return name, true
			}
		}
	}
	return "", false
}
