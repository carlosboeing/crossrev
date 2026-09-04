package review_test

import (
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/exec"
)

// An endpoint named in the config and defined nowhere stops the leg
// (cfg_endpoint, lib/config.sh:400-412, reached from
// lib/adapters/claude.sh:78-92).
//
// The review leg's Invocation carried no endpoint at all, so `endpoint: ghost`
// was ignored: the leg ran against the vendor's own API while the config named
// something else, which is the silent substitution the divergence guard exists
// to catch arriving through a different door.
func TestAnUnresolvedEndpointHaltsTheReviewLeg(t *testing.T) {
	e := newEnv(t)
	writeAppGo(t, e.dir)
	e.cfg = mustConfig(t, "version: 1\nreviewer:\n  harness: claude\n  endpoint: ghost\n")
	e.runner.script = []exec.Result{{ExitCode: 0, Stdout: claudeStdout(issuesPayload(twoFindings))}}

	req := e.request(t)
	req.HarnessOverride = ""
	leg := e.leg(t)
	got := leg.Run(t.Context(), req)
	if got.Err == nil {
		t.Fatal("a review ran against the vendor while the config named an endpoint")
	}
	if !strings.Contains(got.Err.Error(), "defined nowhere") {
		t.Fatalf("refusal = %q, want the unresolved-endpoint one", got.Err)
	}
}

// A defined endpoint whose token variable is unset is a refusal too, never a
// fallback (lib/adapters/claude.sh:83-85).
func TestAnEndpointWithNoTokenHaltsTheReviewLeg(t *testing.T) {
	e := newEnv(t)
	writeAppGo(t, e.dir)
	e.cfg = mustConfig(t, "version: 1\nreviewer:\n  harness: claude\n  endpoint: ollama\nendpoints:\n  ollama:\n    base_url: http://localhost:11434\n    token_env: OLLAMA_TOKEN\n")
	e.runner.script = []exec.Result{{ExitCode: 0, Stdout: claudeStdout(issuesPayload(twoFindings))}}

	req := e.request(t)
	req.HarnessOverride = ""
	leg := e.leg(t)
	got := leg.Run(t.Context(), req)
	if got.Err == nil {
		t.Fatal("a review ran against an endpoint with no token")
	}
	if !strings.Contains(got.Err.Error(), "needs $OLLAMA_TOKEN, which is unset") {
		t.Fatalf("refusal = %q, want the missing-token one", got.Err)
	}
}

// A resolved endpoint reaches the child: the base URL and the token variable
// the adapter sets (lib/adapters/claude.sh:86-92).
func TestAResolvedEndpointReachesTheChild(t *testing.T) {
	e := newEnv(t)
	writeAppGo(t, e.dir)
	e.cfg = mustConfig(t, "version: 1\nreviewer:\n  harness: claude\n  endpoint: ollama\nendpoints:\n  ollama:\n    base_url: http://localhost:11434\n    token_env: OLLAMA_TOKEN\n")
	e.legEnv = []string{"PATH=/usr/bin", "HOME=/tmp", "OLLAMA_TOKEN=t"}
	e.runner.script = []exec.Result{{ExitCode: 0, Stdout: claudeStdout(issuesPayload(twoFindings))}}

	req := e.request(t)
	req.HarnessOverride = ""
	leg := e.leg(t)
	got := leg.Run(t.Context(), req)
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	env := strings.Join(e.runner.specs[0].Env, " ")
	if !strings.Contains(env, "ANTHROPIC_BASE_URL=http://localhost:11434") {
		t.Errorf("the child did not get the base URL: %q", env)
	}
	if marker, _ := got.Marker.MarshalJSON(); !strings.Contains(string(marker), `"endpoint":"ollama"`) {
		t.Errorf("the marker does not name the endpoint: %s", marker)
	}
}

// A pass that ends anywhere but awaiting-resolution asks for the upgrade tip
// (lib/run.sh:1325-1330). The leg holds no terminal, so it answers the request
// and the composition root decides whether to print.
func TestAConvergedReviewAsksForTheUpgradeTip(t *testing.T) {
	e := newEnv(t)
	writeAppGo(t, e.dir)
	e.runner.script = []exec.Result{{ExitCode: 0, Stdout: claudeStdout(`{"verdict":"converged","blocked_reason":null,"findings":[]}`)}}

	got := runLeg(t, e, e.request(t))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if !got.Nudge {
		t.Fatal("a converged pass did not ask for the upgrade tip")
	}
}

// A pass that hands over to the resolve leg prints the handover instead, and
// the shell's `if/else` gives it one or the other, never both.
func TestAPassHandingOverDoesNotAskForTheTip(t *testing.T) {
	e := newEnv(t)
	writeAppGo(t, e.dir)
	e.runner.script = []exec.Result{{ExitCode: 0, Stdout: claudeStdout(issuesPayload(twoFindings))}}

	got := runLeg(t, e, e.request(t))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if got.Nudge {
		t.Fatal("a pass handing over to the resolve leg asked for the tip too")
	}
}
