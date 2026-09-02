package cli

import (
	"context"
	"errors"
	"os"

	"github.com/carlosboeing/crossrev/internal/ui"
)

// Run parses args, runs the command they name, and answers the status the
// process should end with.
//
// It is `main "$@"` (bin/crossrev:193) with the two things a Go program has to
// do for itself: the status is returned rather than reached by `exit`, and a
// refusal travels up as an error rather than ending the process from inside the
// function that found it. See ui.FatalError for why that matters.
//
// harnesses is the list of installed harness names, which the review and
// resolve usage lines carry. An empty list falls back to the shape of the flag,
// the way the shell does when jq is missing (lib/run.sh:926-931).
func Run(ctx context.Context, args []string, cmds Commands, out *ui.IO, harnesses []string) int {
	inv, err := Parse(args, out, harnesses)
	if err != nil {
		// The silent stop prints nothing, because `shift 2` failing under
		// `set -e` prints nothing. Every other refusal has already printed.
		if errors.Is(err, errNoValue) {
			return ExitFailure
		}
		return exitFor(ExitFailure, err)
	}
	return exitFor(Dispatch(ctx, inv, cmds, out))
}

// Main is the process entry point. cmd/crossrev calls it and nothing else does.
func Main() int {
	out := &ui.IO{Out: os.Stdout, Err: os.Stderr, Palette: ui.PaletteFor(ui.IsTerminal(os.Stdout), os.Getenv("NO_COLOR"))}
	cmds, harnesses := compose(out)
	return Run(context.Background(), os.Args[1:], cmds, out, harnesses)
}

// compose is the composition root: it opens what each command needs and returns
// the table Run dispatches over.
//
// It is empty here, and every command reports itself unwired until it is
// filled. It has to live in this package because cmd/crossrev may import
// internal/cli and nothing else, and wiring a leg means reaching a forge
// client, a harness adapter and a run log.
func compose(out *ui.IO) (Commands, []string) {
	_ = out
	return Commands{}, nil
}
