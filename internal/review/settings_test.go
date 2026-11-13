package review_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/exec"
	"github.com/carlosboeing/crossrev/internal/review"
)

// The run log records each effective review-contract setting and its
// source, so a run's concerns, check mode, required checks and wait read
// back off run.log.
func TestTheRunLogRecordsEachEffectiveSettingAndItsSource(t *testing.T) {
	run := func(t *testing.T, cfg string, req func(*review.Request)) string {
		t.Helper()
		e := newEnv(t)
		if cfg != "" {
			e.cfg = mustConfig(t, cfg)
		}
		writeAppGo(t, e.dir)
		e.runner.script = []exec.Result{{ExitCode: 0, Stdout: claudeStdout(issuesPayload(twoFindings))}}

		r := e.request(t)
		if req != nil {
			req(&r)
		}
		leg := e.leg(t)
		if got := leg.Run(context.Background(), r); got.Err != nil {
			t.Fatalf("Run: %v", got.Err)
		}
		return readRunLog(t, e)
	}

	// The helper scopes cases to check: off; the default case names the
	// bare config explicitly.
	if log := run(t, "version: 2\n", nil); !strings.Contains(log,
		"concerns=correctness,consistency concerns_source=default"+
			" check=resolver check_source=default"+
			" required_checks= required_checks_source=default"+
			" check_wait=10 check_wait_source=default") {
		t.Errorf("run.log does not record the default settings:\n%s", log)
	}

	const cfg = "version: 2\nreview:\n  concerns: [correctness]\n  check: off\nverification:\n  required_checks: [build]\n  wait_minutes: 5\n"
	if log := run(t, cfg, nil); !strings.Contains(log,
		"concerns=correctness concerns_source=config"+
			" check=off check_source=config"+
			" required_checks=build required_checks_source=config"+
			" check_wait=5 check_wait_source=config") {
		t.Errorf("run.log does not record the configured settings:\n%s", log)
	}

	if log := run(t, cfg, func(r *review.Request) {
		r.ConcernsOverride = "consistency"
		r.CheckOverride = "resolver"
		r.RequiredChecks = []string{"test@my-app"}
		r.CheckWait = "0"
	}); !strings.Contains(log,
		"concerns=consistency concerns_source=flag"+
			" check=resolver check_source=flag"+
			" required_checks=test@my-app required_checks_source=flag"+
			" check_wait=0 check_wait_source=flag") {
		t.Errorf("run.log does not record the flag settings:\n%s", log)
	}

	if log := run(t, cfg, func(r *review.Request) {
		r.NoRequiredChecks = true
	}); !strings.Contains(log, "required_checks= required_checks_source=flag") {
		t.Errorf("run.log does not record the cleared required checks:\n%s", log)
	}
}

// A setting-override flag is refused where the base policy says
// automated, even when its value equals the base value: the mode is read
// from the pull request's base revision, never the head, so no value can
// evade the restriction.
func TestReviewSettingOverrideFlagsRefusedInAutomatedMode(t *testing.T) {
	for _, tc := range []struct {
		name string
		flag func(*review.Request)
		want string
	}{
		{"concerns", func(r *review.Request) { r.ConcernsOverride = "correctness" }, "--concerns"},
		{"check", func(r *review.Request) { r.CheckOverride = "off" }, "--check"},
		{"required check", func(r *review.Request) { r.RequiredChecks = []string{"build"} }, "--required-check"},
		{"no required checks", func(r *review.Request) { r.NoRequiredChecks = true }, "--no-required-checks"},
		{"check wait", func(r *review.Request) { r.CheckWait = "5" }, "--check-wait"},
		{"equal to the base value", func(r *review.Request) { r.CheckOverride = "resolver" }, "--check"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			e.cfg = mustConfig(t, "version: 2\nmode: automated\n")
			req := e.request(t)
			tc.flag(&req)
			got := runLeg(t, e, req)
			if got.Outcome != review.OutcomeError {
				t.Fatalf("outcome = %q, want %q", got.Outcome, review.OutcomeError)
			}
			if got.Err == nil || !strings.Contains(got.Err.Error(), "the "+tc.want+" flag cannot override policy in automated mode (ADR 0003)") {
				t.Fatalf("err = %v, want the automated-mode refusal naming %s", got.Err, tc.want)
			}
			if len(e.forge.created) != 0 || len(e.forge.edits) != 0 {
				t.Errorf("created=%d edited=%d, want 0: the refusal precedes the claim", len(e.forge.created), len(e.forge.edits))
			}
		})
	}
}

// The same flags are accepted where the base policy says local.
func TestReviewSettingOverrideFlagsAcceptedInLocalMode(t *testing.T) {
	e := newEnv(t)
	writeAppGo(t, e.dir)
	e.runner.script = []exec.Result{{ExitCode: 0, Stdout: claudeStdout(issuesPayload(twoFindings))}}
	req := e.request(t)
	req.ConcernsOverride = "correctness"
	req.CheckOverride = "off"
	req.RequiredChecks = []string{"build"}
	req.CheckWait = "5"
	if got := runLeg(t, e, req); got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if log := readRunLog(t, e); !strings.Contains(log, "concerns=correctness concerns_source=flag") {
		t.Errorf("run.log does not record the accepted flag:\n%s", log)
	}
}

// The published generation carries the review-contract engine identity
// computed from the base policy, not the bare engine version.
func TestReviewPublishesTheBasePolicyEngineID(t *testing.T) {
	e := newEnv(t)
	e.cfg = mustConfig(t, "version: 2\n")
	writeRequiredHead(e, "a.go", "package a\n")
	e.runner.script = []exec.Result{
		{ExitCode: 0, Stdout: claudeStdout(batchAnswer(t, 1))},
	}
	if got := runLeg(t, e, e.request(t)); got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	gens := ledgerGenerations(t, e)
	if len(gens) == 0 {
		t.Fatal("no generation published")
	}
	if gens[0].Engine != testEngineID() {
		t.Errorf("engine = %q, want the base-policy identity %q", gens[0].Engine, testEngineID())
	}
}

// A policy change moves the published identity: a generation judged
// under narrowed concerns is not current under both.
func TestReviewEngineIDMovesWithTheBasePolicy(t *testing.T) {
	e := newEnv(t)
	e.cfg = mustConfig(t, "version: 2\nreview:\n  concerns: [correctness]\n")
	writeRequiredHead(e, "a.go", "package a\n")
	e.runner.script = []exec.Result{
		{ExitCode: 0, Stdout: claudeStdout(batchAnswer(t, 1))},
	}
	if got := runLeg(t, e, e.request(t)); got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	gens := ledgerGenerations(t, e)
	if len(gens) == 0 {
		t.Fatal("no generation published")
	}
	narrower := core.ReviewEngineID(core.ReviewContract{
		Concerns:    []string{"correctness"},
		Check:       "resolver",
		InputPolicy: "hunks_first",
		ReadMode:    "served",
	})
	if gens[0].Engine != narrower {
		t.Errorf("engine = %q, want the narrowed identity %q", gens[0].Engine, narrower)
	}
	if gens[0].Engine == testEngineID() {
		t.Errorf("engine = %q, want it retired against the both-concerns identity", gens[0].Engine)
	}
}

// A local flag override moves the published identity: the pass publishes
// under the contract it actually ran with, not the base policy, so a
// later pass under other settings retires its verdicts instead of
// reusing them. Resolve and status read the recorded identity off the
// marker rather than reproducing it.
func TestReviewFlagOverridesMoveThePublishedEngineID(t *testing.T) {
	e := newEnv(t)
	e.cfg = mustConfig(t, "version: 2\n")
	writeRequiredHead(e, "a.go", "package a\n")
	e.runner.script = []exec.Result{
		{ExitCode: 0, Stdout: claudeStdout(batchAnswer(t, 1))},
	}
	req := e.request(t)
	req.ConcernsOverride = "correctness"
	req.CheckOverride = "off"
	req.InputPolicyOverride = "whole_when_fits"
	if got := runLeg(t, e, req); got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	gens := ledgerGenerations(t, e)
	if len(gens) == 0 {
		t.Fatal("no generation published")
	}
	narrower := core.ReviewEngineID(core.ReviewContract{
		Concerns:    []string{"correctness"},
		Check:       "off",
		InputPolicy: "whole_when_fits",
		ReadMode:    "served",
	})
	if gens[0].Engine != narrower {
		t.Errorf("engine = %q, want the flagged identity %q", gens[0].Engine, narrower)
	}
	if gens[0].Engine == testEngineID() {
		t.Errorf("engine = %q, want it retired against the base-policy identity", gens[0].Engine)
	}
}

// A resume under different flags re-examines rather than reuses: the
// earlier calls' verdicts were judged under another contract, so the
// resumed pass invokes the harness again instead of skipping the
// covered batches.
func TestReviewResumeUnderDifferentFlagsReexaminesCoveredFiles(t *testing.T) {
	e := newEnv(t)
	writeRequiredHead(e, "a.go", "package a\n")
	// Untouched adjacent test: advisory context, not required work.
	writeHead(e, "a_test.go", "package a\n\nfunc TestNothing(t *testing.T) {}\n")
	e.runner.script = []exec.Result{
		{ExitCode: 0, Stdout: claudeStdout(outsideDiffAnswer(t))},
	}
	// The first run accepts the batch and commits the generation, then dies
	// posting the outside-diff finding: the claim stays resumable.
	e.runner.onSpec = func(exec.Spec) { e.forge.createErr = errors.New("comments API down") }
	first := runLeg(t, e, e.request(t))
	if first.Err == nil {
		t.Fatal("first Run: want the posting failure to stop the leg")
	}
	calls := e.runner.calls
	e.forge.createErr = nil
	e.runner.onSpec = nil
	req := e.request(t)
	req.ConcernsOverride = "correctness"
	second := runLeg(t, e, req)
	if second.Err != nil {
		t.Fatalf("second Run: %v", second.Err)
	}
	if second.Outcome != review.OutcomeInvoked {
		t.Fatalf("second Outcome = %q, want invoked", second.Outcome)
	}
	if e.runner.calls == calls {
		t.Fatalf("second run invoked no batch under narrowed concerns, want the covered file re-examined")
	}
}

// The pass marker records the engine identity the pass published under,
// so resolve and status judge the generation by what ran rather than by
// the base policy they can reproduce.
func TestReviewMarkerRecordsThePublishedEngineID(t *testing.T) {
	e := newEnv(t)
	e.cfg = mustConfig(t, "version: 2\n")
	writeRequiredHead(e, "a.go", "package a\n")
	e.runner.script = []exec.Result{
		{ExitCode: 0, Stdout: claudeStdout(batchAnswer(t, 1))},
	}
	req := e.request(t)
	req.ConcernsOverride = "correctness"
	got := runLeg(t, e, req)
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	narrower := core.ReviewEngineID(core.ReviewContract{
		Concerns:    []string{"correctness"},
		Check:       "resolver",
		InputPolicy: "hunks_first",
		ReadMode:    "served",
	})
	engine, ok := got.Marker.CoverageEngine.Get()
	if !ok || engine != narrower {
		t.Errorf("marker coverage_engine = %q,%v, want the flagged identity %q", engine, ok, narrower)
	}
}
