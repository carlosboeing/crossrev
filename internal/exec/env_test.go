package exec_test

import (
	"slices"
	"testing"

	"github.com/carlosboeing/crossrev/internal/exec"
)

// The three names the Bash adapters strip before starting a model-facing
// process, in lib/adapters/*.sh. Parity means Go withholds the same ones.
var forgeCredentials = []string{"GH_TOKEN", "GITHUB_TOKEN", "GH_ENTERPRISE_TOKEN"}

func TestInheritWithholdsForgeCredentials(t *testing.T) {
	for _, name := range forgeCredentials {
		t.Setenv(name, "secret-"+name)
	}
	t.Setenv("PATH", "/usr/bin")
	t.Setenv("HOME", "/home/runner")

	inherited := exec.Inherit([]string{"PATH", "HOME"})

	if len(inherited) != 2 {
		t.Fatalf("expected 2 inherited entries, got %d: %v", len(inherited), inherited)
	}
	if !slices.Contains(inherited, "PATH=/usr/bin") {
		t.Errorf("allowed name PATH was not inherited: %v", inherited)
	}
	if !slices.Contains(inherited, "HOME=/home/runner") {
		t.Errorf("allowed name HOME was not inherited: %v", inherited)
	}
	for _, entry := range inherited {
		for _, credential := range forgeCredentials {
			if len(entry) > len(credential) && entry[:len(credential)+1] == credential+"=" {
				t.Errorf("credential %s reached a model-facing environment: %v", credential, inherited)
			}
		}
	}
}

// The allowlist withholds a name nobody wrote down. A strip-list would pass it.
func TestInheritWithholdsAnUnnamedCredential(t *testing.T) {
	t.Setenv("SOME_FUTURE_HARNESS_TOKEN", "secret")
	t.Setenv("PATH", "/usr/bin")

	inherited := exec.Inherit([]string{"PATH"})

	if !slices.Equal(inherited, []string{"PATH=/usr/bin"}) {
		t.Fatalf("expected only PATH, got %v", inherited)
	}
}

func TestInheritOfNothingIsNothing(t *testing.T) {
	t.Setenv("PATH", "/usr/bin")

	if got := exec.Inherit(nil); got != nil {
		t.Errorf("expected nil for a nil allowlist, got %v", got)
	}
	if got := exec.Inherit([]string{}); got != nil {
		t.Errorf("expected nil for an empty allowlist, got %v", got)
	}
}

func TestInheritIgnoresAnAbsentName(t *testing.T) {
	t.Setenv("PATH", "/usr/bin")

	inherited := exec.Inherit([]string{"PATH", "CROSSREV_NAME_THAT_IS_NOT_SET"})

	if !slices.Equal(inherited, []string{"PATH=/usr/bin"}) {
		t.Fatalf("expected only the name that is set, got %v", inherited)
	}
}
