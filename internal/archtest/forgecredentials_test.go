package archtest_test

import (
	"slices"
	"testing"

	"github.com/carlosboeing/crossrev/internal/config"
	"github.com/carlosboeing/crossrev/internal/exec"
)

// The config layer refuses the same four credentials a model-facing process is
// stripped of.
//
// An endpoint's token_env is read and its value handed to the harness under a
// vendor variable name, so it travels past a strip list that removes the GitHub
// name and never sees the value again. internal/config refuses those four names
// at load for that reason (assertEndpoints, internal/config/validate.go).
//
// Two lists in two packages, because internal/config is tier 2 and may not
// import internal/exec — the same separation lib/config.sh keeps from
// lib/adapters/*.sh. So they are asserted equal here rather than each asserted
// correct alone: a fifth name added to the strip list and not to the config
// layer would leave the config layer accepting the one thing the adapters
// strip. tests/test-permissions.sh:276-285 states the same rule over the Bash.
//
// Order is compared too, not only membership. Both lists are `gh help
// environment`'s order of precedence, and a reordering is a sign one of them
// was rewritten from memory rather than from that document.
func TestTheConfigLayerRefusesTheCredentialsAHarnessIsStrippedOf(t *testing.T) {
	stripped := exec.ForgeCredentialNames()
	refused := config.ForgeCredentialNames()
	if !slices.Equal(refused, stripped) {
		t.Errorf("config.ForgeCredentialNames() = %v, exec.ForgeCredentialNames() = %v", refused, stripped)
	}
}
