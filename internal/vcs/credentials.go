package vcs

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
)

// onActionsRunner reports whether this process runs on a GitHub Actions
// runner, hosted or self-hosted. Both run the same generated workflows, so
// both need the same treatment; a laptop needs none of it.
func onActionsRunner() bool { return os.Getenv("GITHUB_ACTIONS") != "" }

// ghCredentialHelper authenticates git over HTTPS with whatever token `gh`
// itself would use: GH_TOKEN first, then gh's own stored login. git appends
// the credential operation — get, store, erase — so the helper runs as
// `gh auth git-credential get`, which answers username and password from the
// environment without prompting.
const ghCredentialHelper = "!gh auth git-credential"

// withCredentialHelper returns env with git's credential helpers replaced for
// one invocation: first an empty assignment, which clears every helper the
// configuration carries, then the `gh` helper above.
//
// The reset is what makes the order safe. A self-hosted runner's own helper,
// in file configuration or in earlier environment entries, would otherwise be
// tried first; the empty assignment clears the accumulated list, so only the
// `gh` helper answers. Entries already present stay present — they are
// appended after rather than overwritten — so unrelated environment
// configuration keeps working.
//
// A malformed GIT_CONFIG_COUNT is read as zero. Counting existing entries
// wrong would collide with them, and zero collides with nothing: git reads
// the entries the count names, and ours are the ones it names.
func withCredentialHelper(env []string) []string {
	base := 0
	kept := make([]string, 0, len(env)+5)
	for _, entry := range env {
		name, value, ok := strings.Cut(entry, "=")
		if ok && name == "GIT_CONFIG_COUNT" {
			if n, err := strconv.Atoi(value); err == nil && n >= 0 {
				base = n
			}
			continue
		}
		kept = append(kept, entry)
	}
	at := func(i int) (string, string) {
		n := strconv.Itoa(i)
		return "GIT_CONFIG_KEY_" + n, "GIT_CONFIG_VALUE_" + n
	}
	key, value := at(base)
	resetKey, resetValue := at(base + 1)
	return append(kept,
		"GIT_CONFIG_COUNT="+strconv.Itoa(base+2),
		key+"=credential.helper",
		value+"=",
		resetKey+"=credential.helper",
		resetValue+"="+ghCredentialHelper,
	)
}

// extraHeaderPattern matches the config keys a checkout persists a token
// under: one http.<url>.extraheader per host. actions/checkout v5 writes it
// into the checkout's own git config; v6 writes it into a separate credential
// file the config pulls in through includeIf. The pattern covers both, and a
// GitHub Enterprise host as well as github.com, because it names no file and
// no host.
const extraHeaderPattern = `^http\..*\.extraheader$`

// extraHeaderKey validates a key parsed back out of git's own listing before
// anything is unset. The listing was already filtered by the pattern above,
// so a key failing here is a line that did not parse — and unsetting a key
// that did not parse is how the wrong entry gets removed.
var extraHeaderKey = regexp.MustCompile(extraHeaderPattern)

// RemovedCredential names one persisted credential entry the scrub removed:
// the config key and the file it was removed from, as git reported it. The
// value is never carried: it is the token, and nothing downstream needs it.
type RemovedCredential struct {
	Key  string
	File string
}

// RemovePersistedCredentials removes every http extraheader entry from this
// checkout's git configuration: the token actions/checkout persisted, on the
// v5 layout in the local config and on the v6 layout in the separate file.
//
// Only the copy on disk is removed; the step's own environment is untouched.
// Locally this is a no-op that runs no git at all: there is no checkout
// token to remove, and the operator's own configuration is not ours to edit.
//
// A failure refuses rather than warns. The entries could not be listed or
// could not be removed, and a leg that cannot confirm the checkout is clean
// must not start its harness.
func (r *Repository) RemovePersistedCredentials(ctx context.Context) ([]RemovedCredential, error) {
	if !onActionsRunner() {
		return nil, nil
	}
	output, err := r.Run(ctx, "config", "--show-origin", "--get-regexp", extraHeaderPattern)
	if err != nil {
		return nil, &Refusal{
			Message: fmt.Sprintf("could not list persisted git credentials in %s", r.displayDir()),
			Hint:    "CrossRev removes the token the checkout persisted before the leg starts. Check that git runs there, then try again.",
		}
	}
	if !output.OK() {
		// Exit 1 is no match: nothing was persisted, so there is nothing
		// to remove. Anything else is git failing.
		if output.ExitCode == 1 {
			return nil, nil
		}
		return nil, &Refusal{
			Message: fmt.Sprintf("could not list persisted git credentials in %s — %s", r.displayDir(), strings.TrimSpace(output.Stderr)),
			Hint:    "CrossRev removes the token the checkout persisted before the leg starts. Check that git runs there, then try again.",
		}
	}
	// Parsed first, removed second: an entry that does not parse refuses
	// before anything is unset.
	entries, err := persistedEntries(output.Lines())
	if err != nil {
		return nil, err
	}
	var removed []RemovedCredential
	for _, entry := range entries {
		// The origin path is passed back as git reported it: a relative
		// one resolves against the same directory this runs in.
		unset, err := r.Run(ctx, "config", "--file", entry.File, "--unset-all", entry.Key)
		if err != nil || !unset.OK() {
			return nil, &Refusal{
				Message: fmt.Sprintf("could not remove the persisted git credential %s from %s", entry.Key, entry.File),
				Hint:    "CrossRev removes the token the checkout persisted before the leg starts. Check that the file is writable, then try again.",
			}
		}
		removed = append(removed, entry)
	}
	return removed, nil
}

// persistedEntries parses the --show-origin --get-regexp listing into the
// entries to remove, deduplicated: one line per configured value, and
// --unset-all removes every value of a key at once, so a repeated entry
// would fail the second removal with the key already gone.
func persistedEntries(lines []string) ([]RemovedCredential, error) {
	var entries []RemovedCredential
	seen := make(map[RemovedCredential]bool)
	for _, line := range lines {
		// file:.git/config\thttp.https://github.com/.extraheader AUTHORIZATION: …
		origin, rest, ok := strings.Cut(line, "\t")
		if !ok {
			return nil, unparsableEntry()
		}
		file, ok := strings.CutPrefix(origin, "file:")
		if !ok {
			// A persisted entry from anywhere else — the command line or
			// the environment — is not on disk, so there is no copy to
			// remove. Nothing here sets one; git's own levels are the
			// only way one arrives.
			continue
		}
		key, _, _ := strings.Cut(rest, " ")
		if !extraHeaderKey.MatchString(key) {
			return nil, unparsableEntry()
		}
		entry := RemovedCredential{Key: key, File: file}
		if seen[entry] {
			continue
		}
		seen[entry] = true
		entries = append(entries, entry)
	}
	return entries, nil
}

func unparsableEntry() *Refusal {
	return &Refusal{
		Message: "could not parse a persisted git credential entry",
		Hint:    "CrossRev removes the token the checkout persisted before the leg starts, and one entry did not parse, so nothing was removed. Check the checkout's git config for an http extraheader entry git itself cannot list, then try again.",
	}
}
