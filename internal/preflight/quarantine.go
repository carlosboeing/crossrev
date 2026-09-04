package preflight

import (
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/carlosboeing/crossrev/internal/sandbox"
)

// CheckQuarantine reports a stranded quarantine directory left behind by a
// killed run (preflight_check_quarantine, lib/preflight.sh:308-321).
//
// sandbox.Quarantine's restore removes .crossrev-quarantine/ when a run
// completes normally. A run killed by SIGKILL, a machine sleeping, or a crash
// before restore leaves the quarantine sitting in the tree and the real
// instruction files looking deleted in git status.
//
// It answers false when there is one, which is the Bash `return 1`.
func (c *Checker) CheckQuarantine() bool {
	// The Bash path is relative, so it resolves against the working directory.
	// Dir names the same checkout without depending on where the process
	// happens to be standing; the report still says the relative path, because
	// that is what the reader has to go and look at.
	quarantine := sandbox.QuarantineDir
	root := quarantine
	if c.Dir != "" {
		root = filepath.Join(c.Dir, quarantine)
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return true
	}

	c.io().No("stranded quarantine found at " + quarantine)
	if paths := quarantineContents(root); len(paths) > 0 {
		// `${paths[*]}` joins on the first character of IFS, which is a space.
		c.io().Line("   Files inside: " + strings.Join(paths, " "))
	}
	c.io().Line("   A previous run died before restoring the checkout. Move them back to restore your files.")
	return false
}

// quarantineContents is `find "$q" -mindepth 1 | sed "s|^$q/||" | sort`: every
// entry at any depth, named relative to the quarantine, in order.
//
// The order is byte order, which is what `sort` gives under LC_ALL=C. The shell
// takes the collation of whichever locale the session holds, so a machine set
// to en_US orders `.mcp.json` and `AGENTS.md` the other way round. There is no
// locale-aware collation in the standard library to match it with, and the set
// being ordered is a list of file names in a message.
//
// A directory that cannot be read is skipped rather than reported, which is the
// `2>/dev/null` on the find.
func quarantineContents(root string) []string {
	var paths []string
	_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			// Unreadable: skip what is under it and carry on.
			if entry != nil && entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if path == root {
			return nil // -mindepth 1
		}
		relative, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return nil
		}
		paths = append(paths, filepath.ToSlash(relative))
		return nil
	})
	slices.Sort(paths)
	return paths
}
