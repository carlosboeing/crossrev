package exec

import (
	"errors"
	"fmt"
	osexec "os/exec"
)

// ErrPipesAbandoned reports that the child was gone but something it had
// spawned still held the captured streams open, so the capture was cut short.
//
// It has no Bash counterpart, because the adapters redirect to files
// (lib/adapters/claude.sh:106) and a file needs no reader — an orphan writing
// into it after the parent exits costs the shell nothing. A pipe does need one,
// so the Go runner stops waiting rather than hanging a leg forever.
var ErrPipesAbandoned = errors.New("the child exited but left its output streams held open")

// CredentialError reports that a model-facing Spec carried a forge credential
// in its environment, so no child was started.
//
// The variable is named and its value is not, here and in every string this
// type produces. A refusal that printed the token would put it in a run log, a
// terminal and a CI transcript — three more places than the one the refusal
// exists to keep it out of.
type CredentialError struct {
	// Name is the environment variable that was refused.
	Name string
	// Path is Spec.Path as the caller gave it.
	Path string
}

func (e *CredentialError) Error() string {
	return fmt.Sprintf("refusing to start %q: %s is a GitHub credential and this process is model-facing", e.Path, e.Name)
}

// Is makes errors.Is(err, ErrForgeCredential) answer for every CredentialError.
func (e *CredentialError) Is(target error) bool { return target == ErrForgeCredential }

// ErrForgeCredential is what every CredentialError matches, so a caller can ask
// the question without naming the type.
var ErrForgeCredential = errors.New("a GitHub credential was passed to a model-facing process")

// StartError reports that no child process was created. The program was not on
// the PATH, the working directory did not exist, or the kernel refused the fork.
type StartError struct {
	// Path is Spec.Path as the caller gave it.
	Path string
	// Dir is Spec.Dir as the caller gave it, empty for the caller's own.
	Dir string
	// Err is the underlying reason.
	Err error
}

func (e *StartError) Error() string {
	if e.Dir != "" {
		return fmt.Sprintf("could not start %q in %q: %v", e.Path, e.Dir, e.Err)
	}
	return fmt.Sprintf("could not start %q: %v", e.Path, e.Err)
}

func (e *StartError) Unwrap() error { return e.Err }

// IsNotFound reports that a start failed because the program is not installed.
//
// It is the Go answer to the `command -v claude >/dev/null 2>&1 || ui_die`
// guard at lib/adapters/claude.sh:19, which tells the operator to install the
// CLI or point the leg at another harness. Distinguishing this from any other
// start failure is what lets the caller print that message instead of a errno.
func IsNotFound(err error) bool {
	return errors.Is(err, osexec.ErrNotFound)
}
