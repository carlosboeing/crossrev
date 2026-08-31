package resolve

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/exec"
)

func TestNilRunnerRefusesAForgeCredential(t *testing.T) {
	l := &Leg{}
	res := l.runner().Run(context.Background(), exec.Spec{
		Path: "true",
		Env:  []string{"PATH=/usr/bin:/bin", "GH_TOKEN=secret-must-not-print"},
	})
	if !errors.Is(res.Err, exec.ErrForgeCredential) {
		t.Fatalf("Err = %v, want a forge-credential refusal from NewOSRunner", res.Err)
	}
	if res.ExitCode != -1 {
		t.Errorf("ExitCode = %d, want -1", res.ExitCode)
	}
	if res.Err != nil && strings.Contains(res.Err.Error(), "secret-must-not-print") {
		t.Error("the refusal printed the credential")
	}
}
