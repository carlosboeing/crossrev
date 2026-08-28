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
