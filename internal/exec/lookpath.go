package exec

import (
	"os"
	"path/filepath"
	"strings"
)

// LookPath is `command -v` over PATH (lib/run.sh:524, lib/preflight.sh:56,
// lib/harnesses.sh:90, lib/init.sh:771).
//
// It lives here because this is the package that starts a process, and because
// four packages had written the search out privately — internal/preflight,
// internal/review, internal/resolve and internal/initcmd. Three of those four
// answered with a directory named like the tool, so `crossrev doctor` on a
// machine with a `~/bin/claude/` directory would have reported claude
// installed. One copy, measured against bash, replaces all four.
//
// It is not os/exec.LookPath. That one answers only an executable, so it has no
// fallback slot at all, and every row below that finds a non-executable file
// would come back as a refusal.
//
// # The contract, measured rather than reasoned about
//
// bash's search is executable-preferred rather than first-match. With `zzt`
// planted as a directory (nd), a mode-0644 file (f1), a mode-0755 file (x1) and
// a mode-0010 file the caller owns (xg):
//
//	PATH=nd          → exit 1        PATH=f1          → f1/zzt
//	PATH=nd:f1       → exit 1        PATH=f1:nd       → f1/zzt
//	PATH=nd:x1       → x1/zzt        PATH=f1:x1       → x1/zzt
//	PATH=nd:f1:x1    → x1/zzt        PATH=x1:f1       → x1/zzt
//	PATH=xg          → xg/zzt        PATH=nd:xg       → exit 1
//	PATH=xg:x1       → x1/zzt
//
// Which is: the first PATH entry holding a regular file the caller may execute
// wins, from anywhere in the list. With none, the answer is the first entry
// holding anything at all — and it is an answer only if that first thing is a
// regular file. A directory is not skipped; it takes the fallback slot and then
// loses it, which is why `nd:f1` finds nothing while `f1:nd` finds f1.
//
// "May execute" is access(2), not the mode. xg carries an execute bit, so a
// mode test calls it a program; the caller owns it and the owner bit is clear,
// so access(2) refuses and bash never prefers it. It still takes the fallback
// slot, which is why `xg` alone answers and `nd:xg` does not.
//
// An empty PATH element is the current directory, and bash answers with the
// bytes it built rather than a cleaned path: measured from a directory holding
// an executable `tool`, `PATH=":/nonexistent"` and `PATH="."` both answer
// `./tool`.
//
// Two shapes are measured and deliberately not ported, because both are bash's
// own handling of a PATH that is absent rather than of an element within one,
// and no caller here runs without a PATH: an unset PATH answers `./tool`, and a
// PATH set to the empty string answers `$PWD/tool`. This function refuses both.
//
// A name carrying a separator is not searched and takes no fallback: measured,
// `command -v ./f` on a mode-0644 file exits 1 where `./x` answers `./x`.
//
// os.Stat rather than os.Lstat, because a symlink to a program is a program:
// measured, `command -v gh` over a symlink pointing at an executable answers
// with the symlink's own path.
func LookPath(name string) (string, error) {
	if name == "" {
		return "", os.ErrNotExist
	}
	if strings.ContainsRune(name, os.PathSeparator) {
		if !isProgram(name) {
			return "", os.ErrNotExist
		}
		return name, nil
	}
	fallback, fallbackIsFile := "", false
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		candidate := shellJoin(dir, name)
		info, err := os.Stat(candidate)
		if err != nil {
			continue
		}
		if info.Mode().IsRegular() && isExecutable(candidate) {
			return candidate, nil
		}
		if fallback == "" {
			fallback, fallbackIsFile = candidate, info.Mode().IsRegular()
		}
	}
	if fallbackIsFile {
		return fallback, nil
	}
	return "", os.ErrNotExist
}

// shellJoin is the join bash performs rather than filepath.Join's.
//
// bash drops one trailing separator from the element and glues the name on, so
// an empty element — the current directory — answers `./name` where
// filepath.Join would clean it down to `name`. Measured: `PATH="dir/"` answers
// `dir/tool`, `PATH="dir//"` answers `dir//tool`, and a `//` inside the element
// survives, all of which one TrimSuffix and no Clean reproduce.
func shellJoin(dir, name string) string {
	if dir == "" {
		dir = "."
	}
	separator := string(os.PathSeparator)
	return strings.TrimSuffix(dir, separator) + separator + name
}

// isProgram reports whether a path names a regular file this caller may
// execute. isExecutable is the platform's answer to the second half.
func isProgram(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular() && isExecutable(path)
}
