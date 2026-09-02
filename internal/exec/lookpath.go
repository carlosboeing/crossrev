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
// planted as a directory (nd), a mode-0644 file (f1) and a mode-0755 file (x1):
//
//	PATH=nd          → exit 1        PATH=f1          → f1/zzt
//	PATH=nd:f1       → exit 1        PATH=f1:nd       → f1/zzt
//	PATH=nd:x1       → x1/zzt        PATH=f1:x1       → x1/zzt
//	PATH=nd:f1:x1    → x1/zzt        PATH=x1:f1       → x1/zzt
//
// Which is: the first PATH entry holding an executable regular file wins, from
// anywhere in the list. With none, the answer is the first entry holding
// anything at all — and it is an answer only if that first thing is a regular
// file. A directory is not skipped; it takes the fallback slot and then loses
// it, which is why `nd:f1` finds nothing while `f1:nd` finds f1.
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
		if dir == "" {
			continue
		}
		candidate := filepath.Join(dir, name)
		info, err := os.Stat(candidate)
		if err != nil {
			continue
		}
		if isExecutableFile(info) {
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

// isProgram reports whether a path names a regular file somebody may execute.
//
// Any of the three execute bits, not the caller's own: the shell's search asks
// the same question of the mode and lets the exec fail if the bit that is set
// belongs to a group the caller is not in.
func isProgram(path string) bool {
	info, err := os.Stat(path)
	return err == nil && isExecutableFile(info)
}

// isExecutableFile is the same question asked of a mode already read.
func isExecutableFile(info os.FileInfo) bool {
	return info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0
}
