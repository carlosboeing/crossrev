package exec_test

import (
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/exec"
)

// The value nobody sets is the one that refuses.
func TestZeroSpecIsModelFacing(t *testing.T) {
	var spec exec.Spec
	if spec.Audience != exec.AudienceModelFacing {
		t.Fatalf("the zero Spec is audience %v, want AudienceModelFacing", spec.Audience)
	}
	if exec.AudienceModelFacing == exec.AudienceOrchestrator {
		t.Fatal("the two audiences must be distinguishable")
	}
}

// An Audience cannot be built out of an integer, a JSON document, or anything
// else a package outside internal/exec can produce.
//
// This is the route that ended the enumeration of spellings. Audience was an
// integer, the confinement rule in internal/archtest scanned for the syntax
// that wrote the opt-out value, and
//
//	var a exec.Audience
//	_ = json.Unmarshal([]byte("1"), &a)
//
// named nothing, converted nothing, declared no constant and assigned to no
// target the scan could read, while leaving the child orchestrator-facing at
// run time. The field is unexported now, so encoding/json ignores it — an
// unmarshal that names it explicitly succeeds and changes nothing — and every
// other spelling is a compile error. The value stays model-facing, so the run
// is refused.
func TestAnAudienceCannotBeUnmarshalled(t *testing.T) {
	for _, document := range []string{`1`, `true`, `{"orchestrator":true}`} {
		var audience exec.Audience
		// The error is not asserted: `1` and `true` fail to decode and the
		// object decodes cleanly into nothing. What matters is the value after.
		_ = json.Unmarshal([]byte(document), &audience)

		if audience != exec.AudienceModelFacing {
			t.Errorf("unmarshalling %s produced audience %v, want AudienceModelFacing", document, audience)
		}

		spec := helperSpec("exit", "0")
		spec.Audience = audience
		spec.Env = append(spec.Env, "GH_TOKEN=not-a-real-token")
		if result := run(t, spec); !errors.Is(result.Err, exec.ErrForgeCredential) {
			t.Errorf("a child whose audience came from %s was not refused: %v", document, result.Err)
		}
	}
}

// Reassigning the exported audience variables cannot switch the refusal off.
//
// Audience is a struct, so its two values are variables rather than constants —
// a struct value cannot be a Go constant — and an exported variable is writable
// from any package in the binary. That would matter if Run compared against
// one. It reads spec.Audience's own field instead (internal/exec/osrunner.go),
// so a Spec that never set the field is refused whatever these two hold.
func TestRunDoesNotDecideThroughTheExportedAudienceVariables(t *testing.T) {
	modelFacing, orchestrator := exec.AudienceModelFacing, exec.AudienceOrchestrator
	t.Cleanup(func() {
		exec.AudienceModelFacing, exec.AudienceOrchestrator = modelFacing, orchestrator
	})

	exec.AudienceModelFacing = orchestrator
	exec.AudienceOrchestrator = modelFacing

	spec := helperSpec("exit", "0")
	spec.Env = append(spec.Env, "GH_TOKEN=not-a-real-token")
	if result := run(t, spec); !errors.Is(result.Err, exec.ErrForgeCredential) {
		t.Errorf("a model-facing child was started after the audience variables were swapped: %v", result.Err)
	}
}

// Each of the four names the Bash adapters strip. An allowlist cannot stop
// these, because a caller reached them by name.
//
// The list is forgeCredentials, written out in env_test.go in this same
// package. It is deliberately not read from spec.go: a test that read the
// production list would lose a name from itself in the edit that lost it from
// production, and pass. tests/test-permissions.sh:264-271 keeps its own copy
// of the same four names for the same reason.
func TestRunRefusesAForgeCredentialForAModelFacingChild(t *testing.T) {
	const secret = "ghp_a_token_that_must_never_be_printed"

	for _, name := range forgeCredentials {
		t.Run(name, func(t *testing.T) {
			spec := helperSpec("lookup", name)
			spec.Env = append(spec.Env, name+"="+secret)

			result := run(t, spec)

			if !errors.Is(result.Err, exec.ErrForgeCredential) {
				t.Fatalf("Err = %v, want a forge-credential refusal", result.Err)
			}
			var refused *exec.CredentialError
			if !errors.As(result.Err, &refused) {
				t.Fatalf("Err = %v (%T), want a *exec.CredentialError", result.Err, result.Err)
			}
			if refused.Name != name {
				t.Errorf("CredentialError.Name = %q, want %q", refused.Name, name)
			}
			if result.ExitCode != -1 {
				t.Errorf("ExitCode = %d, want -1 for a child that never started", result.ExitCode)
			}
			if len(result.Stdout) != 0 {
				t.Errorf("a refused Spec produced output, so a child ran: %q", result.Stdout)
			}

			message := result.Err.Error()
			if !strings.Contains(message, name) {
				t.Errorf("the refusal does not name the variable: %q", message)
			}
			if strings.Contains(message, secret) {
				t.Error("the refusal printed the credential's value")
			}
		})
	}
}

// The orchestrator's own tools must be able to hold one. lib/github.sh sets
// GH_TOKEN nowhere and gh inherits it ambiently at every call site, so refusing
// here would break the forge adapter rather than protect anything.
func TestRunAllowsAForgeCredentialForAnOrchestratorChild(t *testing.T) {
	const secret = "ghp_orchestrator_only"

	for _, name := range forgeCredentials {
		t.Run(name, func(t *testing.T) {
			spec := helperSpec("lookup", name)
			spec.Env = append(spec.Env, name+"="+secret)
			spec.Audience = exec.AudienceOrchestrator

			result := run(t, spec)

			if !result.OK() {
				t.Fatalf("an orchestrator child was refused: exit=%d err=%v", result.ExitCode, result.Err)
			}
			if got, want := string(result.Stdout), name+"="+secret+"\n"; got != want {
				t.Errorf("child saw %q, want %q", got, want)
			}
		})
	}
}

// A name that merely starts with one of the four is a different variable.
func TestRunAllowsANameThatOnlyResemblesACredential(t *testing.T) {
	spec := helperSpec("lookup", "GH_TOKEN_PATH")
	spec.Env = append(spec.Env, "GH_TOKEN_PATH=/tmp/nothing")

	result := run(t, spec)

	if result.Err != nil {
		t.Fatalf("GH_TOKEN_PATH was refused as a credential: %v", result.Err)
	}
	if got := string(result.Stdout); got != "GH_TOKEN_PATH=/tmp/nothing\n" {
		t.Errorf("child saw %q", got)
	}
}

// The route the security review used: an allowlist that names a credential.
// Inherit does exactly what it was asked to; the refusal is what stops it.
func TestRunRefusesACredentialThatInheritWasAskedFor(t *testing.T) {
	t.Setenv("GH_TOKEN", "ghp_named_in_an_allowlist")
	t.Setenv("PATH", "/usr/bin")

	spec := helperSpec("lookup", "GH_TOKEN")
	spec.Env = exec.Inherit([]string{"PATH", "GH_TOKEN"})

	result := run(t, spec)

	if !errors.Is(result.Err, exec.ErrForgeCredential) {
		t.Fatalf("Err = %v, want a forge-credential refusal", result.Err)
	}
}

// F5. The message an operator reads when a harness is not installed.
func TestStartErrorMessage(t *testing.T) {
	tests := []struct {
		name     string
		err      *exec.StartError
		contains []string
	}{
		{
			name:     "with a working directory",
			err:      &exec.StartError{Path: "claude", Dir: "/tmp/wt", Err: os.ErrNotExist},
			contains: []string{"claude", "/tmp/wt", os.ErrNotExist.Error()},
		},
		{
			name:     "without one",
			err:      &exec.StartError{Path: "claude", Err: os.ErrNotExist},
			contains: []string{"claude", os.ErrNotExist.Error()},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			message := tt.err.Error()
			for _, want := range tt.contains {
				if !strings.Contains(message, want) {
					t.Errorf("message %q does not contain %q", message, want)
				}
			}
			if !errors.Is(tt.err, os.ErrNotExist) {
				t.Error("StartError does not unwrap to its cause")
			}
		})
	}

	if got := (&exec.StartError{Path: "claude", Err: os.ErrNotExist}).Error(); strings.Contains(got, `in ""`) {
		t.Errorf("an absent working directory was printed as an empty one: %q", got)
	}
}

// F5. Path is what tells the operator which harness to install.
func TestRunReportsThePathThatCouldNotStart(t *testing.T) {
	const missing = "crossrev-no-such-program-4f1c9a"

	result := run(t, exec.Spec{Path: missing, Env: helperEnv(nil)})

	var start *exec.StartError
	if !errors.As(result.Err, &start) {
		t.Fatalf("Err = %v (%T), want a *exec.StartError", result.Err, result.Err)
	}
	if start.Path != missing {
		t.Errorf("StartError.Path = %q, want %q", start.Path, missing)
	}
	if !strings.Contains(result.Err.Error(), missing) {
		t.Errorf("the message does not name the program: %q", result.Err.Error())
	}
}

// F7. The guard exists for its message. os/exec would answer "exec: no
// command", which says nothing about a Spec.
func TestRunOfAnEmptyPathSaysNoProgramWasNamed(t *testing.T) {
	result := run(t, exec.Spec{})

	if result.Err == nil {
		t.Fatal("Err = nil for a Spec with no program")
	}
	message := result.Err.Error()
	if !strings.Contains(message, "no program named") {
		t.Errorf("message = %q, want it to say no program was named", message)
	}
	if strings.Contains(message, "no command") {
		t.Errorf("message = %q, which is the os/exec wording the guard exists to replace", message)
	}
}

// F6. Duration is a documented field, so it is asserted.
func TestRunReportsHowLongTheChildTook(t *testing.T) {
	fast := run(t, helperSpec("exit", "0"))
	if fast.Duration <= 0 {
		t.Fatalf("Duration = %s for a child that ran, want a positive time", fast.Duration)
	}

	const sleepFor = 400
	slow := run(t, helperSpec("sleep", strconv.Itoa(sleepFor)))
	if !slow.OK() {
		t.Fatalf("helper failed: exit=%d err=%v", slow.ExitCode, slow.Err)
	}
	if slow.Duration <= fast.Duration {
		t.Errorf("a 400ms child reported %s, no longer than an immediate one at %s", slow.Duration, fast.Duration)
	}
	if slow.Duration.Milliseconds() < sleepFor-50 {
		t.Errorf("Duration = %s, shorter than the %dms the child slept", slow.Duration, sleepFor)
	}
}

// ForgeCredentialNames is the same four, in the same order, for a caller that
// has to remove them from an environment before it reaches Run.
//
// It is compared against forgeCredentials, this package's own test-side copy,
// for the reason stated above the test that uses it: a check that read the
// production list would lose a name from itself in the edit that lost it from
// production, and pass.
func TestForgeCredentialNamesIsTheFourTheAdaptersStrip(t *testing.T) {
	got := exec.ForgeCredentialNames()
	if len(got) != len(forgeCredentials) {
		t.Fatalf("ForgeCredentialNames = %v, want %v", got, forgeCredentials)
	}
	for at, want := range forgeCredentials {
		if got[at] != want {
			t.Errorf("ForgeCredentialNames()[%d] = %q, want %q", at, got[at], want)
		}
	}
}

// The accessor answers a fresh slice each time. An exported slice variable is
// writable from any package in the binary, and shortening this one would widen
// the ADR 0001 boundary everywhere at once.
func TestForgeCredentialNamesCannotBeWrittenThrough(t *testing.T) {
	got := exec.ForgeCredentialNames()
	got[0] = "OVERWRITTEN"

	if second := exec.ForgeCredentialNames(); second[0] != forgeCredentials[0] {
		t.Errorf("writing through the accessor's result changed the list: %v", second)
	}
}

// And the list it answers is still the one Run refuses on. A copy that drifted
// from the private list would let a caller strip four names and be refused for
// a fifth it never heard of — or worse, strip four that no longer matter.
func TestEveryNameForgeCredentialNamesGivesIsOneRunRefuses(t *testing.T) {
	for _, name := range exec.ForgeCredentialNames() {
		spec := helperSpec("exit", "0")
		spec.Env = append(spec.Env, name+"=irrelevant")

		result := run(t, spec)
		if !errors.Is(result.Err, exec.ErrForgeCredential) {
			t.Errorf("a model-facing child carrying %s was not refused: %v", name, result.Err)
		}
	}
}
