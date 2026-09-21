package main

import (
	"fmt"
	"slices"
	"testing"

	"github.com/carlosboeing/crossrev/internal/config"
	"github.com/carlosboeing/crossrev/internal/exec"
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
