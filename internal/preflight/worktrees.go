package preflight

import (
	"os"
	"slices"
)

// ReportWorktrees names the tool-owned worktrees left behind by failed resolve
// runs (preflight_report_worktrees, lib/preflight.sh:328-342).
//
// A clean resolve run removes its worktree; a failed run leaves it behind so
// the uncommitted edits and reflog can be inspected. Accumulation is reported
// here so leftover worktrees are discoverable rather than silent. It never
// fails: the Bash function returns 0 on every path.
func (c *Checker) ReportWorktrees() {
	base := worktreeBase()
	worktrees := worktreesUnder(base)
	if len(worktrees) == 0 {
		return
	}
	c.io().Section("Tool-owned worktrees")
	for _, worktree := range worktrees {
		c.io().Opt(worktree)
	}
	c.io().Line("   Left behind by failed resolve runs. Safe to remove if no run is in progress.")
}

// worktreeBase is `${XDG_STATE_HOME:-$HOME/.local/state}/crossrev/worktrees`
// (lib/preflight.sh:329).
//
// Built by concatenation rather than by joining, for the reason
// vcs.WorktreeDir gives: a trailing slash in XDG_STATE_HOME survives into the
// path find prints, and cleaning it here would make this and the shell disagree
// on a path that names the same directory. The `:-` form takes its fallback on
// an empty value as well as an unset one, and an unset HOME leaves the shell
// with a leading `/.local/state` — which is copied rather than corrected.
func worktreeBase() string {
	state := os.Getenv("XDG_STATE_HOME")
	if state == "" {
		state = os.Getenv("HOME") + "/.local/state"
	}
	return state + "/crossrev/worktrees"
}

// worktreesUnder is `find "$base" -mindepth 2 -maxdepth 2 -type d | sort`: the
// directories exactly two levels down, in order.
//
// A symlink is not a directory to `find -P` and is not one to fs.DirEntry
// either, so neither lists one. An unreadable directory is skipped, which is
// the `2>/dev/null`. The order is byte order; see quarantineContents for what
// the shell's locale does to it.
func worktreesUnder(base string) []string {
	slugs, err := os.ReadDir(base)
	if err != nil {
		return nil
	}
	var worktrees []string
	for _, slug := range slugs {
		if !slug.IsDir() {
			continue
		}
		// Concatenated, not joined: filepath.Join cleans, and a doubled
		// separator from XDG_STATE_HOME has to survive into the report.
		slugDir := base + "/" + slug.Name()
		entries, err := os.ReadDir(slugDir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() {
				worktrees = append(worktrees, slugDir+"/"+entry.Name())
			}
		}
	}
	slices.Sort(worktrees)
	return worktrees
}
