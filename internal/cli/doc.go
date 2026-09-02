// Package cli is the argument parser and the dispatcher: bin/crossrev's
// `main`, and the argument loop of every command it dispatches to.
//
// # What it is
//
// One function turns an argument list into an Invocation — which command, and
// that command's own request — and one hands the request to a field of
// Commands. Commands is a table of function values supplied by the caller, so
// nothing here imports a command package. That is the tier rule (internal/cycle,
// internal/review, internal/resolve, internal/app, internal/initcmd and
// internal/preflight are all tier 3, as is this) and it is also what keeps the
// tests honest: a parser test fills the table with recorders, so it never
// starts a leg, takes a lock or reaches GitHub.
//
// # The parse rule
//
// Written down at bin/crossrev:111-139 rather than discovered later. A first
// argument beginning with `-` is the default cycle, and the options are not
// shifted because they are the cycle's own. Anything else is a sub-command, and
// one that is not recognised is an error — `crossrev reviw --pr 3` names the
// typo instead of silently running the most expensive operation the tool
// offers. `help` and `version` are special-cased ahead of the rule, because
// they are the two dash-arguments nobody means as a cycle and because both must
// answer with no harness and no `gh` installed. A bare `crossrev` prints help;
// it does not cycle.
//
// # Where this diverges from the shell, and why
//
// Three places, each because a Go value is stricter than a Bash string:
//
//   - `--pr` is converted here. Bash keeps it as a string and lets `gh` fail on
//     it, so `--pr abc` comes back as "could not read owner/name#abc". Every
//     request type on this side holds an int, so a value that is not a number
//     is refused at the flag.
//   - `--repo` is parsed into a core.Slug here, for the same reason.
//     internal/initcmd's Request already says this is where it happens.
//   - `config` looks at its sub-command before anything is loaded. The shell
//     runs `preflight_require_yq` and `cfg_load` first (bin/crossrev:155-156),
//     so on a machine with no yq the two refusals arrive in the other order.
//
// `--timeout` was a fourth and is not one any more. Converting it here refused
// `crossrev watchdog --timeout abc` on a repository the shell sweeps to a clean
// exit, because bash stores the flag as written (lib/run.sh:3671) and only
// evaluates it at `(( age < timeout ))` (lib/run.sh:3719). WatchdogRequest
// carries the raw string and cmd/crossrev converts it where the arithmetic is.
//
// # Where the version comes from
//
// The shell reads `$ROOT/VERSION` at run time, where ROOT is the checkout it
// was invoked from (bin/crossrev:26, :64). A binary has no checkout, so the
// bytes are compiled in from a generated copy beside this package — assets.go,
// kept in step by scripts/sync-embedded-assets.sh. That is the same route
// internal/harness takes for the descriptor and internal/initcmd for the
// workflow templates. The printed bytes are identical; the source is not, so a
// binary reports the version it was built from rather than the version of
// whatever checkout it is standing in.
//
// One shell behaviour is reproduced rather than improved on. An argument loop
// that reaches `shift 2` with one argument left fails the shift, and `set -euo
// pipefail` ends the process without printing a word: `crossrev review --pr`
// is an empty terminal and status 1. It is observable, so it is here, as
// errNoValue. The commands whose loops use `${2:?…}` instead — `init` and every
// `auth` sub-command — say which flag is missing, and this package says it in
// the voice the rest of the tool prints in rather than through Bash's own
// error, which names a library file and a line number.
package cli
