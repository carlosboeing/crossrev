// Command crossrev is the native binary: the composition root for every
// package under internal/.
//
// It is the one place in the module that may import a tier-3 package, and the
// only place two of them meet. internal/cli parses and dispatches over a table
// of function values; this file fills the table. Everything else here is the
// wiring one command needs — which runner, which environment, which clock —
// stated once rather than defaulted.
//
// # What the process itself decides
//
// Three things, and nothing else. The palette, from whether stdout is a
// terminal and what NO_COLOR holds. The exit status, which is 0, 1, or 130 for
// an interrupt and never anything else. And the descriptor check the shell
// makes at load: a descriptor naming a harness with no adapter refuses before
// any command runs.
package main

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"syscall"

	"github.com/carlosboeing/crossrev/internal/cli"
	"github.com/carlosboeing/crossrev/internal/harness"
	"github.com/carlosboeing/crossrev/internal/ui"
)

func main() { os.Exit(run(os.Args[1:])) }

// run is main with the arguments and the status handed back rather than taken
// from the process, so a test can drive it.
func run(args []string) int {
	// The palette is decided once, and --yes is decided per command, so this
	// IO answers every question. compose builds a second one for the commands
	// that carry the flag.
	out := newIO(false)

	ctx, stop := interruptible()
	defer stop()

	doc, refusal := descriptor(out)
	if refusal != nil {
		return cli.ExitFailure
	}

	cmds, names := compose(out, doc)
	return cli.Run(ctx, args, cmds, out, names)
}

// interruptible is run_trap_install (lib/run.sh:88), which traps INT and TERM.
//
// The shell sets CROSSREV_INTERRUPTED=1 and lets the current command finish its
// own cleanup; here the signal cancels a context, every child is started with
// it, and internal/cli turns the resulting error into status 130. Same shape:
// the signal is recorded rather than acted on where it lands.
func interruptible() (context.Context, func()) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

// descriptor is the check lib/harnesses.sh:106-114 makes when the library
// loads: every harness the descriptor names must have an adapter.
//
// The shell looks for lib/adapters/<name>.sh on disk and dies naming the
// harness when there is none. A binary has no lib/adapters/ to look in, so
// harness.Adapters is the directory, and the refusal is the same one at the
// same moment — before any command runs, rather than when a leg reaches for
// the adapter it turns out not to have.
//
// A descriptor that failed to parse at all is the other arm. `help` and
// `version` still answer in that state, because they answer with no harness
// list at all (bin/crossrev:68-72), so the parse failure is not fatal here and
// the commands that need a working descriptor refuse for themselves.
func descriptor(out *ui.IO) (harness.Document, error) {
	doc, err := descriptorDocument()
	if err != nil {
		return doc, nil
	}
	if _, err := harness.Adapters(doc); err != nil {
		var refusal *harness.Refusal
		if errors.As(err, &refusal) {
			return doc, out.Die(refusal.Reason, refusal.Action)
		}
		return doc, out.Die(err.Error(), "Add the adapter, or remove the entry.")
	}
	return doc, nil
}

// descriptorDocument is _harness_file (lib/harnesses.sh:19-22): the compiled-in
// descriptor unless CROSSREV_HARNESS_FILE names another.
//
// The shell reads the override for everything the descriptor decides — which
// harnesses exist, which credential each one carries, which secret the
// token-refresh workflow passes. Reading only the embedded one made `crossrev
// init` render that workflow with the embedded refresher's secret name whatever
// the operator's file said, so the job passed one variable and looked up
// another.
//
// A path that cannot be read answers the same as a descriptor that fails to
// parse: the error travels, and the caller decides. help and version still
// answer with no harness list at all.
func descriptorDocument() (harness.Document, error) {
	path := os.Getenv("CROSSREV_HARNESS_FILE")
	if path == "" {
		return harness.Descriptors()
	}
	raw, err := os.ReadFile(path) //nolint:gosec // the operator named this path
	if err != nil {
		return harness.Document{}, err
	}
	return harness.Load(raw)
}
