//go:build unix

package exec

import (
	"os"
	osexec "os/exec"
	"syscall"
)

// setProcessGroup makes the child the leader of a new process group.
//
// One parity consequence is worth naming: a Ctrl-C at a terminal is delivered
// to the foreground process group, so with the child in a group of its own the
// keystroke reaches crossrev and not the harness.
//
// Two separate facts on the Bash side, each with its own line. The harness
// shares crossrev's process group because lib/adapters/claude.sh:106 runs it in
// a plain subshell and nothing under lib/ or bin/ calls setsid, so the
// keystroke reaches both. What crossrev then does with its own copy is
// lib/run.sh:87-88, which traps INT and TERM into a flag rather than dying.
//
// Relaying the signal is the caller's job, and it is the trade for being able
// to kill the whole tree on cancellation.
func setProcessGroup(cmd *osexec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// killProcessGroup kills the child and everything it started. A negative pid
// names the process group whose leader has that id, which is the child because
// setProcessGroup made it one.
func killProcessGroup(cmd *osexec.Cmd) error {
	if cmd.Process == nil {
		return os.ErrProcessDone
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err == nil {
		return nil
	}
	// The group is gone, or the child never became a leader. Fall back to the
	// one process there is a handle for.
	return cmd.Process.Kill()
}

func signalOf(state *os.ProcessState) os.Signal {
	status, ok := state.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() {
		return nil
	}
	return status.Signal()
}

func signalNumber(signal os.Signal) (int, bool) {
	number, ok := signal.(syscall.Signal)
	if !ok {
		return 0, false
	}
	return int(number), true
}
