package runlog

import (
	"os"
	"path/filepath"
	"time"
)

// DefaultRetentionDays is how long a run directory survives when nothing says
// otherwise (lib/log.sh:194).
const DefaultRetentionDays = 14

// RetentionDays reads a configured window. Anything that is not a run of
// digits falls back to the default rather than failing: the sweep runs from
// log_init, and a typo in a config key must not stop a run from starting
// (lib/log.sh:197).
func RetentionDays(value string) int {
	if value == "" {
		return DefaultRetentionDays
	}
	days := 0
	for _, c := range value {
		if c < '0' || c > '9' {
			return DefaultRetentionDays
		}
		days = days*10 + int(c-'0')
	}
	return days
}

// Sweep ages out run directories (log_sweep, lib/log.sh:193).
//
// Everything under the runs base answers to one rule — run logs and
// transcripts alike — so nothing needs its own lifetime. The mtime of a
// directory moves with every file written into it, so a run in progress never
// reads as old.
//
// Two passes, and the depths are the whole design. The first removes run
// directories at exactly depth three, so the repository-slug and pull-request
// levels are never matched for deletion by it. The second removes what the
// first left empty, at depths one and two, and it applies the same age window
// so a directory another process has just made and not yet written into
// survives.
//
// It reports nothing. The Bash function is called from log_init and returns
// zero whatever happens, because a sweep that cannot read a directory must not
// stop the run that triggered it.
func Sweep(base string, days int, now time.Time) {
	if base == "" {
		return
	}
	if info, err := os.Stat(base); err != nil || !info.IsDir() {
		return
	}
	removeAgedRuns(base, 0, days, now)
	removeEmptyAged(base, 0, days, now)
}

// runDepth is the depth of a run directory below the runs base:
// <slug>/pr-<n>/<run id>.
const runDepth = 3

func removeAgedRuns(dir string, depth, days int, now time.Time) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		// IsDir here reads the directory entry's own type, so a symbolic link
		// to a directory is not one — the same answer find's -type d gives
		// without -L.
		if !entry.IsDir() {
			continue
		}
		child := filepath.Join(dir, entry.Name())
		if depth+1 == runDepth {
			if agedOut(child, days, now) {
				_ = os.RemoveAll(child)
			}
			continue
		}
		removeAgedRuns(child, depth+1, days, now)
	}
}

// removeEmptyAged is depth-first, which is what `find -delete` is, and it
// re-reads each directory after its children rather than before. Both matter:
// removing a child updates the parent's mtime to now, so a parent emptied by
// this very sweep reads as fresh and survives until the next one. That is the
// behaviour of the Bash sweep, verified against the find on this platform, not
// an accident of the traversal order.
func removeEmptyAged(dir string, depth, days int, now time.Time) {
	if depth >= runDepth-1 {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		child := filepath.Join(dir, entry.Name())
		removeEmptyAged(child, depth+1, days, now)
		if isEmptyDir(child) && agedOut(child, days, now) {
			_ = os.Remove(child)
		}
	}
}

func isEmptyDir(dir string) bool {
	entries, err := os.ReadDir(dir)
	return err == nil && len(entries) == 0
}

// agedOut answers find's `-mtime +days`: the age in whole days, truncated,
// must be greater than the window. A directory exactly at the window survives,
// and one a second past the next whole day does not.
func agedOut(path string, days int, now time.Time) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	age := now.Sub(info.ModTime())
	if age < 0 {
		return false
	}
	return int(age/(24*time.Hour)) > days
}
