//go:build unix

package exec_test

import (
	"fmt"
	"os"
	osexec "os/exec"
	"strconv"
	"syscall"
	"time"
)

var helperSignals = map[string]syscall.Signal{
	"INT":  syscall.SIGINT,
	"TERM": syscall.SIGTERM,
}

// helperRaise sends the named signal to the helper itself. Go's runtime has no
// handler for either of these, so it resets the disposition and re-raises,
// which is what makes the parent see a signalled child rather than an exit
// status the helper chose.
func helperRaise(name string) {
	signal, ok := helperSignals[name]
	if !ok {
		fmt.Fprintln(os.Stderr, "helper: unknown signal", name)
		os.Exit(2)
	}
	if err := syscall.Kill(os.Getpid(), signal); err != nil {
		fmt.Fprintln(os.Stderr, "helper:", err)
		os.Exit(2)
	}
	// The signal is asynchronous. Returning here would race it and exit zero.
	time.Sleep(30 * time.Second)
	os.Exit(3)
}

func helperProcessGroup() int {
	group, err := syscall.Getpgid(0)
	if err != nil {
		fmt.Fprintln(os.Stderr, "helper:", err)
		os.Exit(2)
	}
	return group
}

// helperSpawn starts a grandchild in the helper's own process group and prints
// its pid, so a cancellation test can check that the kill reached past the
// child it started.
//
// The grandchild's streams go to the null device rather than to the inherited
// pipe. A grandchild holding the pipe open is a separate condition with a
// separate result (ErrPipesAbandoned), and one test should not depend on which
// of the two fired.
func helperSpawn(msArg string) {
	if _, err := strconv.Atoi(msArg); err != nil {
		fmt.Fprintln(os.Stderr, "helper: bad duration", msArg)
		os.Exit(2)
	}
	null, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		fmt.Fprintln(os.Stderr, "helper:", err)
		os.Exit(2)
	}
	grandchild := osexec.Command(os.Args[0], "-test.run=TestHelperProcess", "--", "sleep", msArg)
	grandchild.Env = []string{helperMarker + "=" + helperOn}
	grandchild.Stdin, grandchild.Stdout, grandchild.Stderr = null, null, null
	if err := grandchild.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "helper:", err)
		os.Exit(2)
	}
	fmt.Fprint(os.Stdout, grandchild.Process.Pid)

	ms, _ := strconv.Atoi(msArg)
	time.Sleep(time.Duration(ms) * time.Millisecond)
}

// helperOrphan starts a grandchild that INHERITS the captured streams, prints
// the grandchild's pid, and exits at once.
//
// The pipes then outlive the process the runner holds a handle on, which is the
// one thing Cmd.WaitDelay exists to bound. The pid goes to stdout so the test
// can clean up whatever the kill did not reach.
func helperOrphan(msArg string) {
	orphanGrandchild(msArg, "both", 0, 0)
}

// helperOrphanStdout starts a grandchild that inherits only the captured
// stdout, prints its pid, and exits at once.
//
// This is the half of the orphan case the both-streams helper cannot cover:
// the grandchild holds no stderr, so os/exec's own stderr copier reaches EOF
// and Cmd.WaitDelay has nothing to bound. Only the drain beside Wait can
// notice this orphan.
func helperOrphanStdout(msArg string) {
	orphanGrandchild(msArg, "stdout", 0, 0)
}

// helperOrphanStderr starts a grandchild that inherits only the captured
// stderr, prints its pid, and exits at once.
//
// This is the mirror of the stdout-only case: the grandchild holds no
// stdout, so the streamed drain reaches EOF on its own and only os/exec's
// own stderr copier trips the grace.
func helperOrphanStderr(msArg string) {
	orphanGrandchild(msArg, "stderr", 0, 0)
}

// helperOrphanExit starts a grandchild holding the which streams ("both",
// "stdout" or "stderr"), prints its pid, and exits with the given code
// instead of zero, so a test can ask what a non-zero exit reports when an
// orphan holds the pipes.
func helperOrphanExit(msArg, which, codeArg string) {
	code, err := strconv.Atoi(codeArg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "helper: bad exit code", codeArg)
		os.Exit(2)
	}
	orphanGrandchild(msArg, which, code, 0)
}

// helperOrphanSleep starts a grandchild holding the which streams, prints
// its pid, and sleeps instead of exiting, so a cancellation or deadline
// test has a live child to kill while the grandchild holds the pipes past
// the kill.
func helperOrphanSleep(msArg, which, sleepArg string) {
	sleepMs, err := strconv.Atoi(sleepArg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "helper: bad duration", sleepArg)
		os.Exit(2)
	}
	orphanGrandchild(msArg, which, 0, sleepMs)
}

func orphanGrandchild(msArg string, which string, code int, sleepMs int) {
	if _, err := strconv.Atoi(msArg); err != nil {
		fmt.Fprintln(os.Stderr, "helper: bad duration", msArg)
		os.Exit(2)
	}
	null, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		fmt.Fprintln(os.Stderr, "helper:", err)
		os.Exit(2)
	}
	grandchild := osexec.Command(os.Args[0], "-test.run=TestHelperProcess", "--", "hold", msArg)
	grandchild.Env = []string{helperMarker + "=" + helperOn}
	grandchild.Stdin = null
	grandchild.Stdout, grandchild.Stderr = null, null
	switch which {
	case "both":
		grandchild.Stdout, grandchild.Stderr = os.Stdout, os.Stderr
	case "stdout":
		grandchild.Stdout = os.Stdout
	case "stderr":
		grandchild.Stderr = os.Stderr
	default:
		fmt.Fprintln(os.Stderr, "helper: bad streams", which)
		os.Exit(2)
	}
	// Its own group, so the runner's cancellation kill does not reach it. This
	// case is about a child that exited cleanly and left its pipes held, not
	// about a cancellation.
	grandchild.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := grandchild.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "helper:", err)
		os.Exit(2)
	}
	fmt.Fprint(os.Stdout, grandchild.Process.Pid)
	if sleepMs > 0 {
		time.Sleep(time.Duration(sleepMs) * time.Millisecond)
	}
	os.Exit(code)
}
