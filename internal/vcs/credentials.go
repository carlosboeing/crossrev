package vcs

import (
	"context"
	"fmt"
	"regexp"
	"strings"
)

// runnerCredentialArgs authenticates git to the forge host for one invocation.
//
// The pair is scoped to github.com: the empty assignment clears the helpers
// configured for that host, and the `gh` helper answers in their place. An
// unscoped reset would clear every host's helpers, and a scoped helper with
// no reset would lose to them — git tries helpers in accumulation order with
// no specificity precedence, so a self-hosted runner's file helper answers
// first. Scoped this way, another host's helpers are never consulted for
// github.com and never disturbed for their own host.
//
// `gh` answers only the hosts it holds a token for from the environment,
// github.com among them, so other hosts keep whatever the runner configured.
// An enterprise host gets no answer from GH_TOKEN — `gh auth git-credential`
// exits without one — so a leg pushing to one fails loudly on authentication
// rather than silently on identity.
//
// -c rather than GIT_CONFIG_COUNT: -c is parsed by every git old enough to
// run the checkout, while the environment form needs 2.31 and older git
// ignores it without a warning — and the push that follows then fails with
// no credential at all.
var runnerCredentialArgs = []string{
	"-c", "credential.https://github.com.helper=",
	"-c", "credential.https://github.com.helper=!gh auth git-credential",
}

// withRunnerCredentials returns args with the runner credential pair ahead of
// them. The prebuilt slice is copied out, never appended into: appending
// would write the caller's args into shared backing storage.
func withRunnerCredentials(args []string) []string {
	prefixed := make([]string, 0, len(runnerCredentialArgs)+len(args))
	prefixed = append(prefixed, runnerCredentialArgs...)
	return append(prefixed, args...)
}

// extraHeaderPattern matches the config keys a checkout persists a token
// under: one http.<url>.extraheader per host, where the URL is the server
// origin with a trailing slash (actions/checkout v6, git-auth-helper.ts).
// The pattern names no host and no file, so it covers the v5 layout in the
// local config, the v6 layout in the separate credential file, and an
// enterprise host as well as github.com.
const extraHeaderPattern = `^http\..*\.extraheader$`

// extraHeaderKey validates a key parsed back out of git's own listing before
// anything is unset. The listing was already filtered by the pattern above,
// so a key failing here is a record that did not parse — and unsetting a key
// that did not parse is how the wrong entry gets removed.
var extraHeaderKey = regexp.MustCompile(extraHeaderPattern)

// checkoutCredentialPrefix is the value form actions/checkout persists, on
// both layouts (`AUTHORIZATION: basic …`, git-auth-helper.ts). An extraheader
// holding anything else — a proxy or LFS header the operator configured —
// is not a checkout credential and is left alone.
const checkoutCredentialPrefix = "AUTHORIZATION:"

// unsetValuePattern removes only the checkout's values from a key, leaving
// any other value on it in place.
const unsetValuePattern = "^AUTHORIZATION:"

// RemovedCredential names one persisted credential entry the scrub removed:
// the config key and the file it was removed from, as git reported it. The
// value is never carried: it is the token, and nothing downstream needs it.
type RemovedCredential struct {
	Key  string
	File string
}

// RemovedCredentialLine describes what the scrub removed for the run log: the
// count and the files, never the values.
func RemovedCredentialLine(removed []RemovedCredential) string {
	var files []string
	seen := make(map[string]bool)
	for _, r := range removed {
		if !seen[r.File] {
			seen[r.File] = true
			files = append(files, r.File)
		}
	}
	return fmt.Sprintf("removed %d persisted checkout credential entries from %s", len(removed), strings.Join(files, ", "))
}

// RemovePersistedCredentials removes the checkout-persisted tokens from this
// checkout's git configuration: the v5 entries in the local config and the
// v6 entries in the separate credential file.
//
// The listing is --local with --includes, so system and global config are
// never read and nothing outside this checkout is touched. It runs from the
// top of the worktree, because git reports the local config relative to the
// top while --file resolves against the working directory. Only values in
// the checkout's own form are removed.
//
// Only the copy on disk is removed; the step's own environment is untouched.
// Outside a runner this is a no-op that runs no git at all: there is no
// checkout token to remove, and the operator's own configuration is not
// ours to edit.
//
// A failure refuses rather than warns. The entries could not be listed or
// could not be removed, and a leg that cannot confirm the checkout is clean
// must not start its harness. A cancelled context is returned as the
// interruption it is, so the legs skip their fatal report for it.
func (r *Repository) RemovePersistedCredentials(ctx context.Context) ([]RemovedCredential, error) {
	if !r.git.ActionsRunner {
		return nil, nil
	}
	// The listing and the unsets run from the top of the worktree: see above.
	top, err := r.TopLevel(ctx)
	if err != nil {
		return nil, r.credentialFailure(ctx, err, "could not list persisted git credentials", "CrossRev removes the token the checkout persisted before the leg starts. Check that git runs there, then try again.")
	}
	scoped := r.git.At(top)
	output, err := scoped.Run(ctx, "config", "--local", "--includes", "--show-origin", "--null", "--get-regexp", extraHeaderPattern)
	if err != nil {
		return nil, r.credentialFailure(ctx, err, "could not list persisted git credentials", "CrossRev removes the token the checkout persisted before the leg starts. Check that git runs there, then try again.")
	}
	if !output.OK() {
		// Exit 1 is no match: nothing was persisted, so there is nothing
		// to remove. Anything else is git failing.
		if output.ExitCode == 1 {
			return nil, nil
		}
		return nil, r.credentialFailure(ctx, fmt.Errorf("exit %d: %s", output.ExitCode, strings.TrimSpace(output.Stderr)), "could not list persisted git credentials", "CrossRev removes the token the checkout persisted before the leg starts. Check that git runs there, then try again.")
	}
	// Parsed first, removed second: an entry that does not parse refuses
	// before anything is unset.
	entries, err := persistedEntries(output.Stdout)
	if err != nil {
		return nil, err
	}
	var removed []RemovedCredential
	for _, entry := range entries {
		// The origin path is passed back as git reported it: an absolute
		// one ignores the working directory, and a relative one resolves
		// against the top level this runs from.
		unset, err := scoped.Run(ctx, "config", "--file", entry.File, "--unset-all", entry.Key, unsetValuePattern)
		if err != nil {
			return nil, r.credentialFailure(ctx, err, fmt.Sprintf("could not remove the persisted git credential %s from %s", entry.Key, entry.File), "CrossRev removes the token the checkout persisted before the leg starts. Check that the file is writable, then try again.")
		}
		if !unset.OK() {
			return nil, r.credentialFailure(ctx, fmt.Errorf("exit %d: %s", unset.ExitCode, strings.TrimSpace(unset.Stderr)), fmt.Sprintf("could not remove the persisted git credential %s from %s", entry.Key, entry.File), "CrossRev removes the token the checkout persisted before the leg starts. Check that the file is writable, then try again.")
		}
		removed = append(removed, entry)
	}
	return removed, nil
}

// credentialFailure folds a scrub failure into the interruption it is when
// the context is cancelled, and into a refusal carrying its cause otherwise.
func (r *Repository) credentialFailure(ctx context.Context, cause error, message, hint string) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return &Refusal{
		Message: fmt.Sprintf("%s — %v", message, cause),
		Hint:    hint,
	}
}

// persistedEntries parses the --null listing into the entries to remove.
//
// Each entry is two NUL-terminated records — `file:<path>`, then
// `<key>\n<value>` — so a path holding non-ASCII bytes arrives raw rather
// than C-quoted, and a key holding a space splits from its value on the
// newline. Entries deduplicate: the listing holds one record per configured
// value, and unsetting the same key twice fails the second time with the key
// already gone.
func persistedEntries(listing string) ([]RemovedCredential, error) {
	records := strings.Split(listing, "\x00")
	if len(records) > 0 && records[len(records)-1] == "" {
		records = records[:len(records)-1]
	}
	if len(records)%2 != 0 {
		return nil, unparsableEntry()
	}
	var entries []RemovedCredential
	seen := make(map[RemovedCredential]bool)
	for i := 0; i < len(records); i += 2 {
		file, ok := strings.CutPrefix(records[i], "file:")
		if !ok {
			// A persisted entry from anywhere else — the command line or
			// the environment — is not on disk, so there is no copy to
			// remove. Nothing here sets one; git's own levels are the
			// only way one arrives.
			continue
		}
		key, value, ok := strings.Cut(records[i+1], "\n")
		if !ok || !extraHeaderKey.MatchString(key) {
			return nil, unparsableEntry()
		}
		if !strings.HasPrefix(value, checkoutCredentialPrefix) {
			continue
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
