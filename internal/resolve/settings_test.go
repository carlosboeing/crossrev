package resolve

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/config"
	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/policy"
	"github.com/carlosboeing/crossrev/internal/prstate"
	"github.com/carlosboeing/crossrev/internal/prstate/storetest"
)

// The run log records the effective required checks and their source, so
// a run's wait reads back off run.log.
func TestTheRunLogRecordsTheRequiredChecksAndTheirSource(t *testing.T) {
	run := func(t *testing.T, cfg string, req func(*Request)) string {
		t.Helper()
		e := setup(t)
		e.git.staged = true
		if cfg != "" {
			e.git.show = map[string][]byte{e.base.SHA() + ":.github/crossrev.yml": []byte(cfg)}
		}
		e.addReview(t, defaultFindings(), "issues-remain")

		r := Request{PR: 42, Repo: e.slug, Trigger: TriggerHuman}
		if req != nil {
			req(&r)
		}
		got := e.runReq(t, r)
		if got.Err != nil {
			t.Fatalf("Run: %v", got.Err)
		}
		if got.Outcome != OutcomeComplete {
			t.Fatalf("Outcome = %q, want complete", got.Outcome)
		}
		body, err := os.ReadFile(filepath.Join(e.log.Dir(), "run.log"))
		if err != nil {
			t.Fatalf("read run.log: %v", err)
		}
		return string(body)
	}

	if log := run(t, "", nil); !strings.Contains(log, "required_checks= required_checks_source=default") {
		t.Errorf("run.log does not record the default required checks:\n%s", log)
	}

	const cfg = "version: 2\nverification:\n  required_checks: [build]\n"
	if log := run(t, cfg, nil); !strings.Contains(log, "required_checks=build required_checks_source=config") {
		t.Errorf("run.log does not record the configured required checks:\n%s", log)
	}
	if log := run(t, cfg, func(r *Request) {
		r.RequiredChecks = []string{"test@my-app"}
	}); !strings.Contains(log, "required_checks=test@my-app required_checks_source=flag") {
		t.Errorf("run.log does not record the flag required checks:\n%s", log)
	}
	if log := run(t, cfg, func(r *Request) {
		r.NoRequiredChecks = true
	}); !strings.Contains(log, "required_checks= required_checks_source=flag") {
		t.Errorf("run.log does not record the cleared required checks:\n%s", log)
	}
}

// The run log records the review-contract settings on the resolve leg
// too, so a run's concerns, check mode and wait read back off run.log
// for both legs of a cycle. The resolve leg takes no override flags
// for them, so their sources are config or default.
func TestTheRunLogRecordsTheReviewSettingsOnTheResolveLeg(t *testing.T) {
	run := func(t *testing.T, cfg string) string {
		t.Helper()
		e := setup(t)
		e.git.staged = true
		if cfg != "" {
			e.git.show = map[string][]byte{e.base.SHA() + ":.github/crossrev.yml": []byte(cfg)}
		}
		e.addReview(t, defaultFindings(), "issues-remain")

		got := e.runReq(t, Request{PR: 42, Repo: e.slug, Trigger: TriggerHuman})
		if got.Err != nil {
			t.Fatalf("Run: %v", got.Err)
		}
		if got.Outcome != OutcomeComplete {
			t.Fatalf("Outcome = %q, want complete", got.Outcome)
		}
		body, err := os.ReadFile(filepath.Join(e.log.Dir(), "run.log"))
		if err != nil {
			t.Fatalf("read run.log: %v", err)
		}
		return string(body)
	}

	if log := run(t, ""); !strings.Contains(log,
		"concerns=correctness,consistency concerns_source=default"+
			" check=resolver check_source=default"+
			" required_checks= required_checks_source=default"+
			" check_wait=10 check_wait_source=default") {
		t.Errorf("run.log does not record the default settings on the resolve leg:\n%s", log)
	}

	const cfg = "version: 2\nreview:\n  concerns: [correctness]\n  check: off\nverification:\n  required_checks: [build]\n  wait_minutes: 5\n"
	if log := run(t, cfg); !strings.Contains(log,
		"concerns=correctness concerns_source=config"+
			" check=off check_source=config"+
			" required_checks=build required_checks_source=config"+
			" check_wait=5 check_wait_source=config") {
		t.Errorf("run.log does not record the configured settings on the resolve leg:\n%s", log)
	}
}

// A setting-override flag is refused where the base policy says
// automated, even when its value equals the base value: the mode is read
// from the pull request's base revision, never the head, so no value can
// evade the restriction.
func TestResolveSettingOverrideFlagsRefusedInAutomatedMode(t *testing.T) {
	for _, tc := range []struct {
		name string
		flag func(*Request)
		want string
	}{
		{"required check", func(r *Request) { r.RequiredChecks = []string{"build"} }, "--required-check"},
		{"no required checks", func(r *Request) { r.NoRequiredChecks = true }, "--no-required-checks"},
		{"equal to the base value", func(r *Request) { r.NoRequiredChecks = true }, "--no-required-checks"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := setup(t)
			e.git.show = map[string][]byte{
				e.base.SHA() + ":.github/crossrev.yml": []byte("version: 2\nmode: automated\n"),
			}
			e.addReview(t, defaultFindings(), "issues-remain")
			req := Request{PR: 42, Repo: e.slug, Trigger: TriggerHuman}
			tc.flag(&req)
			got := e.runReq(t, req)
			if got.Outcome != OutcomeRefused {
				t.Fatalf("outcome = %q, want %q", got.Outcome, OutcomeRefused)
			}
			if got.Err == nil || !strings.Contains(got.Err.Error(), "the "+tc.want+" flag cannot override policy in automated mode (ADR 0003)") {
				t.Fatalf("err = %v, want the automated-mode refusal naming %s", got.Err, tc.want)
			}
		})
	}
}

// TestReviewEngineIDMatchesTheBasePolicy pins that the resolve leg
// computes the review-contract engine identity from the same base policy
// the review leg publishes under: the configured concerns, check mode
// and input policy beside the configured reviewer's effective read mode.
// Status computes the same identity from the same base policy; the
// no-findings settle below and the converged report retire a generation
// judged under another contract alike.
func TestReviewEngineIDMatchesTheBasePolicy(t *testing.T) {
	e := setup(t)
	cfg, err := config.Load(context.Background(), e.base,
		func(context.Context, core.Revision, string) ([]byte, config.FileStatus, error) {
			return []byte("version: 2\nreviewer:\n  harness: opencode\nreview:\n  concerns: [correctness]\n  check: off\n  input_policy: whole_when_fits\n"), config.IsFile, nil
		})
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	leg := &Leg{Harness: mustHarness(t)}
	want := core.ReviewEngineID(core.ReviewContract{
		Concerns:    []string{"correctness"},
		Check:       "off",
		InputPolicy: "whole_when_fits",
		ReadMode:    "supplied",
	})
	if got := leg.reviewEngineID(cfg); got != want {
		t.Errorf("reviewEngineID = %q, want %q", got, want)
	}
}

// TestSettleWithAnotherContractCoverageHalts pins the fingerprint on the
// resolve side: a complete generation judged under narrowed concerns is
// not current under the base policy's both, so the no-findings settle
// cannot take its converged verdict on trust and halts for a human.
func TestSettleWithAnotherContractCoverageHalts(t *testing.T) {
	e := setup(t)
	e.addReview(t, json.RawMessage(`[]`), "issues-remain")
	narrower := core.ReviewEngineID(core.ReviewContract{
		Concerns:    []string{"correctness"},
		Check:       "resolver",
		InputPolicy: "hunks_first",
		ReadMode:    "served",
	})
	gen := prstate.Generation{
		Gen:      1,
		Revision: core.RevisionPair{Base: e.base, Head: e.head},
		Engine:   narrower,
		Slot:     prstate.DefaultSlot,
		Producer: prstate.Producer{Harness: "codex"},
		Form:     prstate.GenerationFull,
		Paths:    []string{"a.go"},
		Records: []prstate.Record{{
			Type:       prstate.CoverageRecordUnit,
			UnitID:     string(core.FileUnitID("a.go")),
			PathIndex:  0,
			Kind:       prstate.CoverageGranularityFile,
			Change:     string(core.ChangeModified),
			BodyDigest: core.BodyDigestHex([]byte("body of a.go")),
			Verdict:    prstate.Some("no_issue"),
		}},
		ScopeReport: prstate.ScopeReport{ExaminedScope: "read the batch"},
	}
	store := storetest.NewFakeStore()
	e.forge.ledger = store
	handle, err := store.PublishGeneration(context.Background(),
		prstate.SlotRef{Repo: e.slug, Number: 42, Slot: prstate.DefaultSlot}, prstate.Handle{}, gen)
	if err != nil {
		t.Fatalf("PublishGeneration: %v", err)
	}
	setReviewCoverage(t, e, handle)

	got := e.run(t)
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	for _, label := range e.forge.addedLabels {
		if label == policy.LabelConverged {
			t.Fatalf("converged label applied on coverage judged under another contract: %v", e.forge.addedLabels)
		}
	}
}
