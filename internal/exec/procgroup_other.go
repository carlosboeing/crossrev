//go:build !unix

package exec

import (
	"os"
	osexec "os/exec"
)

// Process groups are a unix concept. Everything crossrev drives is a unix
// program invoked from Bash, so this exists to keep the package buildable
// rather than to support a host nobody runs it on.

func setProcessGroup(*osexec.Cmd) {}

func killProcessGroup(cmd *osexec.Cmd) error {
	if cmd.Process == nil {
		return os.ErrProcessDone
	}
	return cmd.Process.Kill()
}

func signalOf(*os.ProcessState) os.Signal { return nil }

func signalNumber(os.Signal) (int, bool) { return 0, false }
