package cli

import (
	"strconv"
	"strings"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/ui"
)

// The parsed requests, one per command.
//
// Each holds what its argument loop in the shell collects, under the name of
// the flag that sets it. A flag the shell accepts and throws away — `--pass` on
// the legs, `--harness`, `--trigger` and `--no-tips` on `status` and
// `watchdog` — is consumed by the parser and appears in none of them, which is
// what "accepted and ignored" means.

// CycleRequest is `crossrev cycle` (lib/run.sh:2895-2916).
type CycleRequest struct {
	PR              int
	Repo            core.Slug
	Trigger         string
	HarnessOverride string
	NoTips          bool
	KeepTranscripts bool
}

// ReviewRequest is `crossrev review` (lib/run.sh:913-941).
type ReviewRequest struct {
	PR              int
	Repo            core.Slug
	Trigger         string
	HarnessOverride string
	Continuation    bool
	NoTips          bool
	KeepTranscripts bool
}

// ResolveRequest is `crossrev resolve` (lib/run.sh:1730-1756).
//
// It takes no `--continuation`: the flag exists so a cycle can tell the review
// leg it is not the first of the loop, and the resolve leg has no such state.
type ResolveRequest struct {
	PR              int
	Repo            core.Slug
	Trigger         string
	HarnessOverride string
	NoTips          bool
	KeepTranscripts bool
}

// StatusRequest is `crossrev status` (lib/run.sh:3034-3049).
type StatusRequest struct {
	PR   int
	Repo core.Slug
}

// InitRequest is `crossrev init` (lib/init.sh:34-46).
type InitRequest struct {
	Owner   string
	Repo    core.Slug
	DryRun  bool
	Upgrade bool
	Yes     bool
}

// WatchdogRequest is `crossrev watchdog` (lib/run.sh:3666-3681).
type WatchdogRequest struct {
	Repo core.Slug
	// Timeout is `--timeout` as the shell's `timeout` variable holds it: the
	// raw string, never empty, because the variable starts at 1800 and
	// `${2:-1800}` puts the same digits back when the flag arrives empty
	// (lib/run.sh:3667, :3671).
	//
	// It is a string and not a number because the shell does not convert it
	// here. The value is only evaluated at `(( age < timeout ))`
	// (lib/run.sh:3719), which a sweep reaches only once a pull request is
	// waiting, so `--timeout abc` is harmless on a repository with nothing
	// waiting. Converting at the flag would refuse a command the shell runs.
	// cmd/crossrev converts it where bash's arithmetic does.
	Timeout string
}

// ConfigRequest is `crossrev config show` and `crossrev config backlog`. The
// shell reads its first argument and nothing else (bin/crossrev:157-161).
type ConfigRequest struct{}

// DoctorRequest is `crossrev doctor`, which reads none of its arguments
// (bin/crossrev:163-180).
type DoctorRequest struct{}

// AuthStatusRequest is `crossrev auth status`, which has no argument loop and
// so refuses nothing (lib/auth.sh:373).
type AuthStatusRequest struct{}

// AuthLoginRequest is `crossrev auth login` (lib/auth.sh:511-520).
type AuthLoginRequest struct {
	Owner string
	Name  string
	// Role is `--role`, and it is "loop" rather than empty when the flag is
	// absent: the shell's parser starts from `local role="loop"`
	// (lib/auth.sh:512).
	Role string
}

// AuthInstallRequest is `crossrev auth install` (lib/auth.sh:792-800).
type AuthInstallRequest struct {
	Owner string
	Role  string
}

// AuthRotateRequest is `crossrev auth rotate` (lib/auth.sh:879-889).
type AuthRotateRequest struct {
	Owner   string
	Role    string
	KeyFile string
}

// AuthRefreshRequest is `crossrev auth refresh` (lib/auth.sh:998-1009).
//
// Repo is a string rather than a slug because the refresher writes a secret
// through `gh` against whatever it is given, and `--org` is the other half of
// the same choice.
type AuthRefreshRequest struct {
	Harness string
	Repo    string
	Org     string
	Secret  string
}

// HelpRequest is `crossrev help` and its three spellings (bin/crossrev:123).
type HelpRequest struct{}

// VersionRequest is `crossrev version` and its two spellings
// (bin/crossrev:124).
type VersionRequest struct{}

// The usage line each argument loop prints when it does not recognise an
// option. They are the strings the shell prints, measured with
// `NO_COLOR=1 bash bin/crossrev <command> --bogus`.
const (
	usageCycle    = "Usage: crossrev cycle --pr <number> [--trigger human|automatic] [--no-tips] [--keep-transcripts]"
	usageStatus   = "Usage: crossrev status --pr <number>"
	usageWatchdog = "Usage: crossrev watchdog [--repo owner/name] [--timeout <seconds>]"
	usageInit     = "Usage: crossrev init [--owner <owner>] [--upgrade] [--dry-run] [--yes]"

	usageAuthLogin   = "Run: crossrev auth login [--owner <owner>] [--role loop|refresher] [--name <name>]"
	usageAuthInstall = "Run: crossrev auth install [--owner <owner>] [--role loop|refresher]"
	usageAuthRotate  = "Run: crossrev auth rotate [--owner <owner>] [--role loop|refresher] [--key <downloaded.pem>]"
	usageAuthRefresh = "Run: crossrev auth refresh [--harness <name>] [--repo owner/name | --org owner] [--secret NAME]"
)

// harnessOption is the `--harness` fragment the review and resolve usage lines
// carry.
//
// Bash renders the installed names only when jq is present and `harness_names`
// answers, and falls back to the shape of the flag otherwise
// (lib/run.sh:926-931, :1742-1747, bin/crossrev:68-72). Here the caller decides
// what it knows, and an empty list is that fallback.
func harnessOption(harnesses []string) string {
	if len(harnesses) == 0 {
		return "--harness <harness>"
	}
	return "--harness <one of: " + strings.Join(harnesses, "|") + ">"
}

func usageReview(harnesses []string) string {
	return "Usage: crossrev review --pr <number> [" + harnessOption(harnesses) +
		"] [--no-tips] [--keep-transcripts]"
}

func usageResolve(harnesses []string) string {
	return "Usage: crossrev resolve --pr <number> [" + harnessOption(harnesses) +
		"] [--trigger human|automatic] [--keep-transcripts]"
}

// scanner walks an argument list the way `while (( $# ))` walks "$@".
type scanner struct {
	args []string
	at   int
}

func (s *scanner) more() bool   { return s.at < len(s.args) }
func (s *scanner) flag() string { return s.args[s.at] }

// skip is `shift`: a flag that carries no value.
func (s *scanner) skip() { s.at++ }

// value is `x="${2:-}"; shift 2`.
//
// `shift 2` fails when the flag is the last argument, and under `set -e` that
// ends the process without a word. errNoValue is that stop.
func (s *scanner) value() (string, error) {
	if len(s.args)-s.at < 2 {
		return "", errNoValue
	}
	v := s.args[s.at+1]
	s.at += 2
	return v, nil
}

// discard is `shift 2` on a flag that is accepted and thrown away.
func (s *scanner) discard() error {
	_, err := s.value()
	return err
}

// required is `x="${2:?--flag needs a value}"; shift 2`.
//
// The `:?` form fires when the value is missing or empty, and it says which
// flag it was. Bash prints that through its own error path, naming the library
// file and line; here it goes through the voice the rest of the tool prints in.
func (s *scanner) required(out *ui.IO, flag, usage string) (string, error) {
	if len(s.args)-s.at < 2 || s.args[s.at+1] == "" {
		return "", out.Die(flag+" needs a value", usage)
	}
	v := s.args[s.at+1]
	s.at += 2
	return v, nil
}

// unknownOption is the arm every argument loop ends with.
func unknownOption(out *ui.IO, command, option, usage string) error {
	return out.Die("unknown option for "+command+": "+option, usage)
}

// requireTrigger is the `case "$trigger" in human|automatic` guard the three
// commands that read the flag apply after their loop (lib/run.sh:937-940,
// :1753-1756, :2913-2916).
func requireTrigger(out *ui.IO, command, trigger string) error {
	switch trigger {
	case "human", "automatic":
		return nil
	}
	return out.Die("unknown "+command+" trigger: "+trigger,
		"Use --trigger human or --trigger automatic.")
}

// requirePR is `[[ -n "$pr" ]] || ui_die` plus the conversion the shell never
// makes.
//
// Bash keeps the number as a string and hands it to `gh`, so `--pr abc` reaches
// the API and comes back as "could not read owner/name#abc". Nothing on this
// side holds a pull request as a string — every request type and every forge
// call takes an int — so a value that is not one is refused here instead. The
// text is this parser's own; the shell has none to copy.
func requirePR(out *ui.IO, command, raw string) (int, error) {
	if raw == "" {
		return 0, out.Die("crossrev "+command+" needs a pull request number",
			"Usage: crossrev "+command+" --pr 42")
	}
	pr, err := strconv.Atoi(raw)
	if err != nil {
		return 0, out.Die("--pr must be a number, and it was: "+raw,
			"Pass the pull request number, for example: --pr 42")
	}
	return pr, nil
}

// optionalSlug converts `--repo`, and an absent flag stays the zero slug, which
// means "ask the forge which repository this checkout is".
//
// The shell accepts any string and lets the first API call fail on it. A slug
// refuses anything that is not owner/name, and this is where that refusal lands
// — as internal/initcmd's Request already says it does.
func optionalSlug(out *ui.IO, raw string) (core.Slug, error) {
	if raw == "" {
		return core.Slug{}, nil
	}
	slug, err := core.ParseSlug(raw)
	if err != nil {
		return core.Slug{}, out.Die("--repo must be owner/name, and it was: "+raw,
			"Pass the repository as owner/name, for example: --repo carlosboeing/crossrev")
	}
	return slug, nil
}

// WatchdogDefaultTimeout is `timeout=1800` at lib/run.sh:3667, and the same
// digits `${2:-1800}` puts back for an empty `--timeout` at lib/run.sh:3671.
//
// It is the string the shell holds rather than a duration, because nothing
// converts it until the sweep compares against it. cmd/crossrev does that
// conversion, and it exports this so the number is written down once.
const WatchdogDefaultTimeout = "1800"
