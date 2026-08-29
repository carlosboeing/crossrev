package sandbox

import (
	"os"
	"path/filepath"
)

// Quarantine moves repository-provided harness configuration out of the way,
// and reports the paths it moved. It is sandbox_quarantine at
// lib/sandbox.sh:53-73.
//
// # Why the checkout is sanitised rather than a flag passed
//
// A pull request branch does not only contain content to review. It contains
// files that configure the thing reviewing it: settings, instruction files,
// hooks, MCP server definitions, agents. A hook is arbitrary code execution
// before the model ever sees a token.
//
// Two mechanisms were available and only one is usable. Claude Code's `--bare`
// gives project-config isolation and refuses subscription auth, and the design's
// headline is that it runs on subscriptions. Codex requires persisted trust
// before running a hook and exposes a flag to skip that check, which CrossRev
// never passes. So sanitising the checkout is the mechanism, and the flags are
// defence in depth where they are free — a file that is not there cannot be
// read by anything, whereas a flag that changes name in the next release fails
// open (lib/sandbox.sh:4-25).
//
// # Quarantined rather than deleted
//
// Easy to miss, and the reason is that a pull request which ADDS a hook is
// exactly the pull request a reviewer should be flagging. The diff still
// carries the text, and the files stay readable at a path no harness auto-loads
// (lib/sandbox.sh:27-30).
//
// # os.Rename where the shell has mv, and it is stricter
//
// Not the same operation, in one case that matters. Where a stranded quarantine
// already holds the path and the checkout holds it again, `mv .claude
// .crossrev-quarantine/.claude` moves the second one INSIDE the first and exits
// zero, leaving `.crossrev-quarantine/.claude/.claude` and no sign anything went
// wrong. os.Rename refuses with "file exists", so Quarantine fails and says so.
// Louder, and deliberately kept: silent nesting produces a restore that puts
// back a directory holding a copy of itself.
//
// # Why the directory is listed rather than stat'ed
//
// `test -e` matches case-insensitively on macOS, so it cannot tell CLAUDE.md
// from claude.md and the move would rename the operator's file. The descriptor
// lists both spellings, and reading the directory and comparing names byte for
// byte is what makes listing every spelling safe (lib/sandbox.sh:58-61).
func Quarantine(root string, paths []string) ([]string, error) {
	quarantine := filepath.Join(root, QuarantineDir)
	if err := os.MkdirAll(quarantine, 0o755); err != nil {
		return nil, err
	}

	var moved []string
	for _, path := range paths {
		source, err := resolveInside(root, path)
		if err != nil {
			return moved, err
		}
		if !existsExactly(source) {
			continue
		}
		destination, err := resolveInside(quarantine, path)
		if err != nil {
			return moved, err
		}
		if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
			return moved, err
		}
		if err := os.Rename(source, destination); err != nil {
			return moved, err
		}
		moved = append(moved, path)
	}

	// An empty quarantine directory is itself a repository-provided path the
	// harness might notice, and it is noise in `git status`. os.Remove refuses
	// a directory that is not empty, which is the whole of `rmdir … || true`.
	_ = os.Remove(quarantine)
	return moved, nil
}

// existsExactly reports whether the last element of path is spelled on disk
// exactly the way it is written here.
//
// The shell asks `find "$dir" -maxdepth 1 -name "$base"`, which is
// case-sensitive on both BSD and GNU. `-name` takes a glob, and this takes a
// literal; no path in the descriptor holds a glob character, and a literal is
// the stricter reading of the two.
func existsExactly(path string) bool {
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		return false
	}
	name := filepath.Base(path)
	for _, entry := range entries {
		if entry.Name() == name {
			return true
		}
	}
	return false
}
