package exec_test

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The child every runner test drives is this test binary, re-entered in helper
// mode. It is deliberately not a system utility: `head -c`, `sleep`, `printf`
// and `env` differ in flags or behaviour between the Darwin the operator runs
// on and the ubuntu-latest CI runs on, and a test that passes on one and not
// the other proves nothing about the runner.
const (
	helperMarker = "CROSSREV_EXEC_HELPER"
	helperOn     = "1"
)

// helperSpecArgs builds the argument array that re-enters this binary in helper
// mode with the given command and its own arguments.
func helperSpecArgs(command string, args ...string) []string {
	out := []string{"-test.run=TestHelperProcess", "--", command}
	return append(out, args...)
}

// helperEnv is the child environment for a helper invocation.
//
// It is an explicit map, never a copy of this process's environment. The
// boundary test in internal/archtest confines os.Environ to
// internal/exec/env.go, and a test that read the environment to build a child's
// would be the first thing to reintroduce the leak it guards.
func helperEnv(extra map[string]string) []string {
	env := []string{helperMarker + "=" + helperOn}
	for name, value := range extra {
		env = append(env, name+"="+value)
	}
	return env
}

// TestHelperProcess is not a test. It is the child process, and it exits before
// the testing package can print anything of its own.
func TestHelperProcess(t *testing.T) {
	if os.Getenv(helperMarker) != helperOn {
		return
	}

	args := os.Args
	for i, arg := range args {
		if arg == "--" {
			args = args[i+1:]
			break
		}
	}
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "helper: no command")
		os.Exit(2)
	}

	command, rest := args[0], args[1:]
	switch command {
	case "argv":
		// JSON, because the point of the test is that a space, a newline and the
		// empty string survive as argv entries. Any line-oriented encoding would
		// lose the newline case.
		encoded, err := json.Marshal(rest)
		if err != nil {
			fmt.Fprintln(os.Stderr, "helper:", err)
			os.Exit(2)
		}
		os.Stdout.Write(encoded)

	case "lookup":
		// Reads named variables only. Enumerating the environment is what
		// internal/exec/env.go exists to be the only place for.
		for _, name := range rest {
			if value, ok := os.LookupEnv(name); ok {
				fmt.Printf("%s=%s\n", name, value)
			} else {
				fmt.Printf("%s<unset>\n", name)
			}
		}

	case "stdin":
		read, err := io.Copy(os.Stdout, os.Stdin)
		if err != nil {
			fmt.Fprintln(os.Stderr, "helper:", err)
			os.Exit(2)
		}
		fmt.Fprint(os.Stderr, read)

	case "pwd":
		dir, err := os.Getwd()
		if err != nil {
			fmt.Fprintln(os.Stderr, "helper:", err)
			os.Exit(2)
		}
		fmt.Fprint(os.Stdout, dir)

	case "spew":
		out, err := strconv.Atoi(rest[0])
		if err != nil {
			os.Exit(2)
		}
		errCount, err := strconv.Atoi(rest[1])
		if err != nil {
			os.Exit(2)
		}
		os.Stdout.WriteString(strings.Repeat("o", out))
		os.Stderr.WriteString(strings.Repeat("e", errCount))

	case "exit":
		code, err := strconv.Atoi(rest[0])
		if err != nil {
			os.Exit(2)
		}
		os.Exit(code)

	case "sleep":
		ms, err := strconv.Atoi(rest[0])
		if err != nil {
			os.Exit(2)
		}
		// Announced before sleeping so a cancellation test can wait for the
		// child to be running rather than racing its start-up.
		os.Stdout.WriteString("ready")
		time.Sleep(time.Duration(ms) * time.Millisecond)

	case "raise":
		helperRaise(rest[0])

	case "pgid":
		fmt.Fprint(os.Stdout, helperProcessGroup())

	case "spawn":
		helperSpawn(rest[0])

	case "orphan":
		helperOrphan(rest[0])

	case "hold":
		// A grandchild that holds whatever descriptors it inherited and says
		// nothing, so the parent's captured streams stay open with no output to
		// confuse an assertion.
		ms, err := strconv.Atoi(rest[0])
		if err != nil {
			os.Exit(2)
		}
		time.Sleep(time.Duration(ms) * time.Millisecond)

	default:
		fmt.Fprintln(os.Stderr, "helper: unknown command", command)
		os.Exit(2)
	}
	os.Exit(0)
}
