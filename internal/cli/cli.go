package cli

import (
	"context"
	"errors"
	"os"

	"github.com/carlosboeing/crossrev/internal/harness"
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
// the table Run dispatches over, and the harness names the usage lines carry.
//
// Help and version are wired. Every other command reports itself unwired until
// it is filled. It has to live in this package because cmd/crossrev may import
// internal/cli and nothing else, and wiring a leg means reaching a forge
// client, a harness adapter and a run log.
func compose(out *ui.IO) (Commands, []string) {
	// The names the `--harness` lines render. A descriptor that failed to
	// parse leaves the list empty, which prints the shape of the flag instead
	// — the arm the shell takes when jq is missing (bin/crossrev:68-72). The
	// command that needs a working descriptor refuses on its own.
	document, _ := harness.Descriptors()
	names := document.Names()

	return Commands{
		Help: func(context.Context, HelpRequest) (int, error) {
			return Help(out, names)
		},
		Version: func(context.Context, VersionRequest) (int, error) {
			return Version(out, installedVersion())
		},
	}, names
}

// installedVersion is the text `crossrev version` prints.
//
// The shell reads it out of the VERSION file at the root of its checkout
// (bin/crossrev:26, :64), and a binary has no checkout to read, so the bytes
// are compiled in — see assets.go. Version deletes the whitespace, which is
// where the shell's `tr -d` happens.
func installedVersion() string { return embeddedVersion }
