package config

import (
	"fmt"

	"github.com/carlosboeing/crossrev/internal/core"
)

// named is the value a refusal quotes, with the word the Bash `${v:-unset}`
// substitutes when there is nothing to quote.
func named(value string) string {
	if value == "" {
		return "unset"
	}
	return value
}

// wholeAboveZero is the Bash `[[ "$v" =~ ^[0-9]+$ ]] && (( v > 0 ))` test. It is
// deliberately text-first: `5.0`, `-1` and `fortnight` all fail the pattern
// before any arithmetic runs.
func wholeAboveZero(value string) bool {
	if value == "" {
		return false
	}
	nonZero := false
	for i := 0; i < len(value); i++ {
		if value[i] < '0' || value[i] > '9' {
			return false
		}
		if value[i] != '0' {
			nonZero = true
		}
	}
	return nonZero
}

// checkVersion refuses a version key that is present and not this one.
//
// A version that is present and wrong is a refusal, not a warning: the whole
// point of the key is that a future shape can be rejected by an old binary
// (cfg_check_version, lib/config.sh:294-301).
func checkVersion(layer *Object, where string) error {
	declared := alternative(lookup(layer, ".version"))
	if declared == "" || declared == Version {
		return nil
	}
	return &Refusal{
		Message: fmt.Sprintf("%s declares version %s, and this crossrev understands version %s", where, declared, Version),
		Hint:    fmt.Sprintf("Upgrade crossrev, or set version: %s in that file if it really is the current shape.", Version),
	}
}

// assertMinFixSeverity refuses a threshold the ranking table cannot rank.
//
// Left to the table an unrecognised value ranks zero, and zero meets nothing —
// so `min_fix_severity: medum` counts no finding as actionable, the pass
// reports converged, and the cycle stops with a high-severity finding still on
// the pull request. A typo would look exactly like a clean review
// (lib/config.sh:245-253).
func (c *Config) assertMinFixSeverity() error {
	value := c.Get(".policy.min_fix_severity")
	if _, err := core.ParseSeverity(value); err == nil {
		return nil
	}
	return &Refusal{
		Message: fmt.Sprintf("policy.min_fix_severity is '%s', which is not one of high, medium or low", named(value)),
		Hint:    "It names the lowest severity the resolve leg may change code for unattended. Set it to high, medium or low in the repository config, or remove it to take the default of medium.",
	}
}

// assertMaxPassesPerCycle refuses a pass bound that is not a count of passes.
//
// Zero is already spoken for inside the orchestrator as its own sentinel for
// "no pass bound applies to this invocation". An operator writing 0 or -1 lands
// on that sentinel from the other direction, and the two readers then disagree:
// the automatic loop takes it as no bound and keeps starting passes, while the
// cycle command stops before the first one (lib/config.sh:255-272).
func (c *Config) assertMaxPassesPerCycle() error {
	value := c.Get(".policy.max_passes_per_cycle")
	if wholeAboveZero(value) {
		return nil
	}
	return &Refusal{
		Message: fmt.Sprintf("policy.max_passes_per_cycle is '%s', which is not a whole number of passes above zero", named(value)),
		Hint:    "It bounds how many passes the loop runs by itself before a person has to ask for another, so the smallest meaningful value is 1. Set it to 1 or more in the repository config, or remove it to take the default of 3. To stop crossrev reviewing a repository at all, remove its workflows rather than setting the bound to zero.",
	}
}

// assertGitHooks refuses a third value for a two-value switch.
//
// Read leniently, `hooks: skipp` falls through to whichever branch is not
// `run`, so a repository that meant to keep its hooks running keeps committing
// without them and nothing ever says so (lib/config.sh:274-290, ADR 0017).
func (c *Config) assertGitHooks() error {
	switch value := c.Get(".git.hooks"); value {
	case "skip", "run":
		return nil
	default:
		return &Refusal{
			Message: fmt.Sprintf("git.hooks is '%s', which is not one of skip or run", named(value)),
			Hint:    "It decides whether the resolver's commit and push run the repository's own git hooks. Set it to skip or run in the repository config, or remove it to take the default of skip.",
		}
	}
}

// assertLogs refuses either run-record value where it can still be named.
//
// Read leniently, `retention_days: fourteen` would sweep by a default nobody
// stated and `keep_transcripts: yes` would read as false while the config says
// keep — a typo landing silently in the direction that loses evidence
// (lib/config.sh:217-237).
func (c *Config) assertLogs() error {
	days := c.Get(".logs.retention_days")
	if !wholeAboveZero(days) {
		return &Refusal{
			Message: fmt.Sprintf("logs.retention_days is '%s', which is not a whole number of days above zero", named(days)),
			Hint:    "It bounds how long run logs and kept transcripts stay under the state directory before a sweep removes them. Set it to 1 or more in the repository config, or remove it to take the default of 14.",
		}
	}
	// Read through the null test rather than the alternative operator: jq's
	// `//` treats false as empty, so the legitimate default would report the
	// key unset and refuse every config (lib/config.sh:229-231).
	keep := notNull(lookup(c.Merged, ".logs.keep_transcripts"))
	switch keep {
	case "true", "false":
		return nil
	default:
		return &Refusal{
			Message: fmt.Sprintf("logs.keep_transcripts is '%s', which is not true or false", named(keep)),
			Hint:    "It decides whether harness transcripts survive a successful leg rather than only a failed one. Set it to true or false in the repository config, or remove it to take the default of false.",
		}
	}
}

// assertBacklog refuses a backlog destination or layout CrossRev does not
// recognise.
//
// The refusal lives at load rather than at resolution because every caller
// reads the resolver through a command substitution in Bash, where a refusal
// would end only that subshell and let the caller continue on an empty
// resolution (lib/config.sh:193-215). The Go port keeps the refusal in the same
// place so the two implementations refuse at the same moment.
func (c *Config) assertBacklog() error {
	want := alternativeString(lookup(c.Merged, ".backlog.destination"), string(DestinationAuto))
	switch want {
	case "", string(DestinationNone), string(DestinationGitHubIssues), string(DestinationRepository), string(DestinationAuto):
	default:
		return unknownDestination(want)
	}

	layout := alternativeString(lookup(c.Merged, ".backlog.repository.layout"), string(LayoutFolder))
	switch layout {
	case string(LayoutFolder), string(LayoutFile):
		return nil
	default:
		return unknownLayout(layout)
	}
}

func unknownDestination(want string) *Refusal {
	return &Refusal{
		Message: fmt.Sprintf("backlog.destination is '%s', which CrossRev does not recognise", want),
		Hint:    "Set it to github_issues, repository, none or auto in the repository config.",
	}
}

func unknownLayout(layout string) *Refusal {
	return &Refusal{
		Message: fmt.Sprintf("backlog.repository.layout is '%s', which is not folder or file", layout),
		Hint:    "Set it to folder or file in the repository config.",
	}
}
