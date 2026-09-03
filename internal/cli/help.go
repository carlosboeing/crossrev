package cli

import (
	"fmt"
	"strings"

	"github.com/carlosboeing/crossrev/internal/ui"
)

// helpBlock is the heredoc `usage` prints (bin/crossrev:73-108), transcribed
// byte for byte down to the leading blank line and the trailing one.
//
// The single verb is the `--harness` fragment, which is the only thing the
// shell substitutes into it. Nothing else in the block is a percent sign, so
// the text can be printed through fmt without escaping anything.
//
// The lowercase `crossrev` in the title line and in doctor's description is
// the shell's own wording, kept as it is. Parity wins over the writing rule
// here: changing it would change what a reader sees for no reason a reader
// asked for.
const helpBlock = `
  crossrev — cross-model PR review loop

  Review a pull request with one model, resolve the findings with another.

  USAGE
    crossrev <command> [options]
    crossrev --pr <n>         A cycle, which is what you want most of the time

  COMMANDS
    cycle --pr <n>           The whole loop in one process, up to max_passes_per_cycle
    review --pr <n>          One review leg: post findings as inline comments
    resolve --pr <n>         Verify each finding, fix or push back, reply, push
    status --pr <n>          Read the loop's state off the pull request
    init                     Set up automated mode: App, secrets, labels, workflows
    watchdog                 Find pull requests stuck waiting on a leg, and retry once
    config show|backlog      The merged config, and where deferred work goes
    auth status              Which Apps are configured, and where installed
    auth login               Register a GitHub App and install it
    auth install             Install an already-registered App somewhere
    auth rotate              Replace an App's private key, guided
    auth refresh             Refresh a rotating harness credential (the refresher job)
    doctor                   Check that everything crossrev needs is installed
    version                  Print the installed version

  OPTIONS on cycle, review and resolve
    %s   Override the harness the config names for that leg
    --repo owner/name        Target another repository than this checkout
    --no-tips                Suppress the automated-mode suggestion
    --keep-transcripts       Keep the harness transcript even when the leg succeeds

  Automated mode needs a GitHub App. Local runs do not — they use the gh
  authentication you already have.

`

// Help prints the usage block and answers the status the shell answers for it:
// zero (bin/crossrev:123).
//
// harnesses is the list the `--harness` line names. The shell renders the
// installed names only when jq is present and `harness_names` answers, and
// prints the shape of the flag otherwise (bin/crossrev:68-72); an empty list is
// that second arm. The names come from the descriptor rather than from a
// literal, so a harness added to lib/harnesses.json appears here.
//
// The block goes to stdout, because `cat <<EOF` does.
func Help(out *ui.IO, harnesses []string) (int, error) {
	printRaw(out, fmt.Sprintf(helpBlock, harnessOption(harnesses)))
	return ExitOK, nil
}

// Version prints the installed version and answers zero (bin/crossrev:124).
//
// raw is the contents of the VERSION file at the root of the checkout. The
// shell reads it as `tr -d '[:space:]' <"$ROOT/VERSION"` and then `echo`
// (bin/crossrev:64), so every whitespace byte is deleted — not trimmed from the
// ends — and one newline is added back.
//
// A build that carries no version cannot print one. The shell cannot reach
// `echo` at all in that case: `tr` fails to open the file, `set -euo pipefail`
// ends the process at status 1, and Bash prints its own message naming an
// absolute path. That message cannot be reproduced without inventing the path,
// so the same stop is said in the voice the rest of the tool prints in.
func Version(out *ui.IO, raw string) (int, error) {
	text := strings.Map(func(r rune) rune {
		// The six bytes POSIX [:space:] names, and no more. unicode.IsSpace
		// would also delete a non-breaking space and the Unicode separators,
		// which `tr` in a checkout does not.
		if strings.ContainsRune(" \t\n\v\f\r", r) {
			return -1
		}
		return r
	}, raw)
	if text == "" {
		return ExitFailure, out.Die("this build carries no version",
			"Reinstall CrossRev from a checkout that has its VERSION file.")
	}
	printRaw(out, text+"\n")
	return ExitOK, nil
}

// printRaw writes bytes to stdout with nothing added.
//
// Every other line this tool prints goes through one of internal/ui's helpers,
// which put a gutter or a prefix in front of it. The usage block and the
// version are the two things the shell writes straight to stdout — `cat <<EOF`
// and `echo` — so they are written the same way here. A half-wired IO discards,
// which is internal/ui's own contract for its zero value.
func printRaw(out *ui.IO, text string) {
	if out == nil || out.Out == nil {
		return
	}
	fmt.Fprint(out.Out, text)
}
