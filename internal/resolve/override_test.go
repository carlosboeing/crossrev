package resolve

import (
	"testing"

	"github.com/carlosboeing/crossrev/internal/ui"
)

// --model without --harness overrides the configured leg's model rather than
// the harness's own default: the harness override wipes the configured model
// and endpoint (settings, context.go), but a bare model override lands in the
// same field the config fills, so the run header, the marker and the child
// all name the operator's value.
func TestSettingsModelOverrideWithoutAHarnessOverrideReplacesTheConfiguredModel(t *testing.T) {
	e := setup(t)
	e.git.staged = true
	e.git.show = map[string][]byte{e.base.SHA() + ":.github/crossrev.yml": []byte(
		"version: 2\nresolver:\n  harness: claude\n  model: configured-model\n  effort: configured-effort\n")}
	e.addReview(t, defaultFindings(), "issues-remain")

	got := e.runReq(t, Request{PR: 42, Repo: e.slug, Trigger: TriggerHuman,
		ModelOverride: "cli-model", EffortOverride: "cli-effort"})
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if got.Outcome != OutcomeComplete {
		t.Fatalf("Outcome = %q, want complete", got.Outcome)
	}

	if !containsRun(got.Messages, []ui.Line{ui.Say("Resolver: claude, cli-model, cli-effort effort")}) {
		t.Fatalf("the header does not name the CLI model and effort: %q", ui.Texts(got.Messages))
	}
	if model, _ := got.Marker.Model.Get(); model != "cli-model" {
		t.Errorf("marker model = %q, want cli-model", model)
	}
	if effort, _ := got.Marker.Effort.Get(); effort != "cli-effort" {
		t.Errorf("marker effort = %q, want cli-effort", effort)
	}
}

// A CLI model reaches the harness exactly as a config model does: the same
// run with the value in the config and no override reports the same Resolver
// line and records the same marker fields. There is no allowlist on either
// side — the config passes both through verbatim and a bad id fails as the
// harness's own entitlement error — so this equivalence is the whole of the
// "validated like one" the flags promise.
func TestSettingsModelOverrideMatchesTheSameModelFromConfig(t *testing.T) {
	run := func(t *testing.T, cfg string, model, effort string) (string, string, string) {
		t.Helper()
		e := setup(t)
		e.git.staged = true
		if cfg != "" {
			e.git.show = map[string][]byte{e.base.SHA() + ":.github/crossrev.yml": []byte(cfg)}
		}
		e.addReview(t, defaultFindings(), "issues-remain")

		got := e.runReq(t, Request{PR: 42, Repo: e.slug, Trigger: TriggerHuman,
			ModelOverride: model, EffortOverride: effort})
		if got.Err != nil {
			t.Fatalf("Run: %v", got.Err)
		}
		if got.Outcome != OutcomeComplete {
			t.Fatalf("Outcome = %q, want complete", got.Outcome)
		}
		var header string
		for _, line := range got.Messages {
			if len(line.Text) >= 10 && line.Text[:10] == "Resolver: " {
				header = line.Text
			}
		}
		modelGot, _ := got.Marker.Model.Get()
		effortGot, _ := got.Marker.Effort.Get()
		return header, modelGot, effortGot
	}

	fromConfig, modelConfig, effortConfig := run(t,
		"version: 2\nresolver:\n  harness: claude\n  model: shared-model\n  effort: shared-effort\n", "", "")
	fromFlag, modelFlag, effortFlag := run(t, "", "shared-model", "shared-effort")

	if fromConfig != fromFlag {
		t.Errorf("header = %q from the flag, want the config's %q", fromFlag, fromConfig)
	}
	if modelFlag != modelConfig || modelConfig != "shared-model" {
		t.Errorf("marker model = %q from the flag and %q from the config, want shared-model both", modelFlag, modelConfig)
	}
	if effortFlag != effortConfig || effortConfig != "shared-effort" {
		t.Errorf("marker effort = %q from the flag and %q from the config, want shared-effort both", effortFlag, effortConfig)
	}
}
