package review_test

import (
	"context"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/exec"
	"github.com/carlosboeing/crossrev/internal/ui"
)

// --model without --harness overrides the configured leg's model rather than
// the harness's own default: the harness override wipes the configured model
// (settings, invoke.go), but a bare model override lands in the same field
// the config fills, so the run header, the marker and the child all name the
// operator's value.
func TestAModelOverrideWithoutAHarnessOverrideReplacesTheConfiguredModel(t *testing.T) {
	e := newEnv(t)
	e.cfg = mustConfig(t, "version: 2\nreviewer:\n  harness: claude\n  model: configured-model\n  effort: configured-effort\n")
	writeAppGo(t, e.dir)
	e.runner.script = []exec.Result{{ExitCode: 0, Stdout: claudeStdout(issuesPayload(twoFindings))}}

	// leg.Run directly rather than runLeg: the helper substitutes
	// HarnessOverride "claude" for an empty one, and an override wipes the
	// configured model, which is the other half of the setting under test.
	req := e.request(t)
	req.HarnessOverride = ""
	req.ModelOverride = "cli-model"
	req.EffortOverride = "cli-effort"
	leg := e.leg(t)
	got := leg.Run(context.Background(), req)
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}

	if !containsRun(got.Messages, []ui.Line{ui.Say("Reviewer: claude, cli-model, cli-effort effort")}) {
		t.Fatalf("the header does not name the CLI model and effort: %q", ui.Texts(got.Messages))
	}
	marker, err := got.Marker.MarshalJSON()
	if err != nil {
		t.Fatalf("marshalling the marker: %v", err)
	}
	if !strings.Contains(string(marker), `"model":"cli-model"`) {
		t.Errorf("the marker does not record the CLI model: %s", marker)
	}
	if !strings.Contains(string(marker), `"effort":"cli-effort"`) {
		t.Errorf("the marker does not record the CLI effort: %s", marker)
	}
	if strings.Contains(string(marker), "configured-model") || strings.Contains(string(marker), "configured-effort") {
		t.Errorf("the configured values survived the override: %s", marker)
	}
}

// A CLI model reaches the harness exactly as a config model does: the same
// run with the value in the config and no override reports the same Reviewer
// line and records the same marker fields. There is no allowlist on either
// side — the config passes both through verbatim and a bad id fails as the
// harness's own entitlement error — so this equivalence is the whole of the
// "validated like one" the flags promise.
func TestAModelOverrideMatchesTheSameModelFromConfig(t *testing.T) {
	run := func(t *testing.T, cfg string, model, effort string) (string, string) {
		t.Helper()
		e := newEnv(t)
		e.cfg = mustConfig(t, cfg)
		writeAppGo(t, e.dir)
		e.runner.script = []exec.Result{{ExitCode: 0, Stdout: claudeStdout(issuesPayload(twoFindings))}}

		req := e.request(t)
		req.HarnessOverride = ""
		req.ModelOverride = model
		req.EffortOverride = effort
		leg := e.leg(t)
		got := leg.Run(context.Background(), req)
		if got.Err != nil {
			t.Fatalf("Run: %v", got.Err)
		}
		var header string
		for _, line := range got.Messages {
			if strings.HasPrefix(line.Text, "Reviewer: ") {
				header = line.Text
			}
		}
		marker, err := got.Marker.MarshalJSON()
		if err != nil {
			t.Fatalf("marshalling the marker: %v", err)
		}
		return header, string(marker)
	}

	fromConfig, markerConfig := run(t,
		"version: 2\nreviewer:\n  harness: claude\n  model: shared-model\n  effort: shared-effort\n", "", "")
	fromFlag, markerFlag := run(t,
		"version: 2\nreviewer:\n  harness: claude\n", "shared-model", "shared-effort")

	if fromConfig != fromFlag {
		t.Errorf("header = %q from the flag, want the config's %q", fromFlag, fromConfig)
	}
	for _, want := range []string{`"model":"shared-model"`, `"effort":"shared-effort"`} {
		if !strings.Contains(markerConfig, want) {
			t.Fatalf("the config run did not record %s: %s", want, markerConfig)
		}
		if !strings.Contains(markerFlag, want) {
			t.Errorf("the flag run did not record %s: %s", want, markerFlag)
		}
	}
}
