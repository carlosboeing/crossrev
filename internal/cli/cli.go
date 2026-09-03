package cli

import (
	"context"
	"errors"

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
// the way the shell does when jq is missing (lib/run.sh:932-937).
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

// InstalledVersion is the text `crossrev version` prints: the VERSION file at
// the root of the checkout this binary was built from, compiled in.
//
// The shell reads it off disk at run time (bin/crossrev:26, :64). A binary has
// no checkout, so the bytes come from assets.go. Exported because the
// composition root is cmd/crossrev and this package may not reach a tier-3
// peer to build the table itself.
func InstalledVersion() string { return installedVersion() }

// installedVersion is the embedded copy. Version deletes the whitespace, which
// is where the shell's `tr -d '[:space:]'` happens.
func installedVersion() string { return embeddedVersion }
