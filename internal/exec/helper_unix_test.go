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
