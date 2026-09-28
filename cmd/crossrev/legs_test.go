package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/config"
	"github.com/carlosboeing/crossrev/internal/exec"
	"github.com/carlosboeing/crossrev/internal/resolve"
	"github.com/carlosboeing/crossrev/internal/ui"
)

// A repository config cannot put a forge credential on the leg's allowlist by
// naming one as an endpoint's token_env.
//
// Nothing leaked before this: exec.NewOSRunner refuses any Spec whose
// environment names one of the four, so a config saying `token_env: GH_TOKEN`
// stopped the run. But the refusal it raised names a runner fault, and the
// fault is the config's, so the message sent an operator to look at the wrong
// thing. Dropping the four here leaves that refusal for what it is for.
//
// Whether such a config should be refused when it is read, with the file and
// the key in the message, is a design decision this does not make.
func TestLegEnvironmentDropsAForgeCredentialAnEndpointNames(t *testing.T) {
	endpoints := config.NewObject()
	for at, credential := range exec.ForgeCredentialNames() {
		defined := config.NewObject()
		defined.Set("token_env", credential)
		endpoints.Set(fmt.Sprintf("forge%d", at), defined)
	}
	// The operator's own name, which is the shape the key exists for
	// (templates/operator-config.yml ships KIMI_API_KEY as the worked
	// example). It has to survive, or the test would pass against a
	// legEnvironment that had stopped reading token_env at all.
	kimi := config.NewObject()
	kimi.Set("token_env", "KIMI_API_KEY")
	endpoints.Set("kimi", kimi)

	merged := config.NewObject()
	merged.Set("endpoints", endpoints)

	names := legEnvironment(&config.Config{Merged: merged})
	for _, credential := range exec.ForgeCredentialNames() {
		if slices.Contains(names, credential) {
			t.Errorf("legEnvironment put %s on the allowlist", credential)
		}
	}
	if !slices.Contains(names, "KIMI_API_KEY") {
		t.Error("legEnvironment dropped the operator's own token_env")
	}
}

// An interrupted leg reports an interrupt on the terminal, not the
// harness-failure message and not the generic refusal. Both legs answer the
// cancellation joined with their own refusal type — the review leg a
// *ui.FatalError, the resolve leg a *resolve.Refusal — so refusalText keeps
// finding the typed refusal while errors.Is still sees context.Canceled. A
// bare context.Canceled would fall to the plain-error branch and print
// "error  context canceled" with the doctor hint, which is the regression
// this pins: reportLeg is the single exit every leg command returns through.
func TestAnInterruptedLegReportsAnInterrupt(t *testing.T) {
	for _, err := range []error{
		errors.Join(&ui.FatalError{
			Reason: "the harness was interrupted",
			Action: "Re-run the leg.",
		}, context.Canceled),
		errors.Join(&resolve.Refusal{
			Message: "the harness was interrupted",
			Hint:    "Re-run the leg.",
		}, context.Canceled),
	} {
		var stdout, stderr bytes.Buffer
		out := &ui.IO{Out: &stdout, Err: &stderr, Palette: ui.Plain()}
		_, _ = reportLeg(out, nil, err)
		if !strings.Contains(stderr.String(), "was interrupted") {
			t.Errorf("stderr = %q, want it to name the interrupt (%T)", stderr.String(), err)
		}
		for _, bad := range []string{"harness failed", "authentication", "doctor", "context canceled"} {
			if strings.Contains(stderr.String(), bad) {
				t.Errorf("stderr = %q, want no %q on an interrupt (%T)", stderr.String(), bad, err)
			}
		}
	}
}
