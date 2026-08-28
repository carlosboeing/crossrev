package sandbox_test

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/sandbox"
)

// realTempDir is a temporary directory with every symlink already resolved.
// t.TempDir answers under /var on macOS, and /var is a symlink to /private/var;
// a containment test rooted at the unresolved path compares a resolved child
// against an unresolved root and reports an escape that is only the symlink.
func realTempDir(t *testing.T) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve the temporary root: %v", err)
	}
	return resolved
}

func write(t *testing.T, dir, name, content string) string {
	t.Helper()
	full := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("make the parent of %s: %v", full, err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", full, err)
	}
	return full
}

func exists(t *testing.T, path string) bool {
	t.Helper()
	_, err := os.Lstat(path)
	return err == nil
}

// existsExactly reports whether a directory holds an entry spelled this way,
// byte for byte. os.Stat cannot answer it on a case-insensitive filesystem,
// which is the whole reason the shell asks find instead of `test -e`
// (lib/sandbox.sh:58-61).
func existsExactly(t *testing.T, dir, name string) bool {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if entry.Name() == name {
			return true
		}
	}
	return false
}

// A branch that plants every surface a harness is known to read.
func plantABranch(t *testing.T, root string) {
	t.Helper()
	write(t, root, ".claude/settings.json", `{"hooks":{}}`+"\n")
	write(t, root, ".claude/hooks/evil.sh", "#!/bin/sh\nexit 0\n")
	write(t, root, ".codex/config.toml", "x\n")
	write(t, root, ".gemini/settings.json", "x\n")
	write(t, root, ".grok/config.toml", "x\n")
	write(t, root, ".mcp.json", "{}\n")
	write(t, root, ".github/copilot-instructions.md", "x\n")
	// The planted text is inert on purpose: the quarantine matches on path and
	// never reads a byte of content.
	write(t, root, "CLAUDE.md", "branch-supplied instruction file, quarantined by path\n")
	write(t, root, "AGENTS.md", "branch-supplied instruction file, quarantined by path\n")
	write(t, root, "app.ts", "real source\n")
}

func TestQuarantineAndRestoreRoundTrip(t *testing.T) {
	root := realTempDir(t)
	plantABranch(t, root)
	paths := shippedDescriptor(t).Paths()

	moved, err := sandbox.Quarantine(root, paths)
	if err != nil {
		t.Fatalf("Quarantine: %v", err)
	}

	for _, gone := range []string{".claude/settings.json", ".claude/hooks/evil.sh", ".codex", ".gemini", ".grok/config.toml", ".mcp.json", ".github/copilot-instructions.md", "CLAUDE.md", "AGENTS.md"} {
		if exists(t, filepath.Join(root, gone)) {
			t.Errorf("%s is still where a harness would load it", gone)
		}
	}
	if !exists(t, filepath.Join(root, "app.ts")) {
		t.Error("source under review was moved")
	}

	// Quarantined rather than deleted: a pull request that adds a hook is
	// exactly the pull request a reviewer should be flagging, so the files stay
	// readable at a path no harness auto-loads (lib/sandbox.sh:27-30).
	for _, kept := range []string{".claude/settings.json", ".grok/config.toml", "CLAUDE.md"} {
		if !exists(t, filepath.Join(root, sandbox.QuarantineDir, kept)) {
			t.Errorf("%s is not readable in the quarantine", kept)
		}
	}

	sorted := append([]string{}, moved...)
	sort.Strings(sorted)
	if strings.Join(sorted, "|") != strings.Join(moved, "|") {
		t.Errorf("the moved list is not in descriptor order: %v", moved)
	}

	clobbered, warning, err := sandbox.Restore(root, paths)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if len(clobbered) != 0 {
		t.Errorf("clobbered = %v, want none", clobbered)
	}
	if warning != nil {
		t.Errorf("warning = %v, want none", warning)
	}
	for _, back := range []string{".claude/settings.json", ".claude/hooks/evil.sh", ".codex/config.toml", ".gemini/settings.json", ".grok/config.toml", ".mcp.json", ".github/copilot-instructions.md", "CLAUDE.md", "AGENTS.md"} {
		if !exists(t, filepath.Join(root, back)) {
			t.Errorf("%s was not put back", back)
		}
	}
	if exists(t, filepath.Join(root, sandbox.QuarantineDir)) {
		t.Error("the quarantine directory survived the restore")
	}
}

// An empty quarantine directory is itself a repository-provided path the
// harness might notice, and it is noise in `git status` (lib/sandbox.sh:69-71).
func TestQuarantineLeavesNothingOnACleanCheckout(t *testing.T) {
	root := realTempDir(t)
	write(t, root, "app.ts", "real source\n")

	moved, err := sandbox.Quarantine(root, shippedDescriptor(t).Paths())
	if err != nil {
		t.Fatalf("Quarantine: %v", err)
	}
	if len(moved) != 0 {
		t.Errorf("moved = %v, want none", moved)
	}
	if exists(t, filepath.Join(root, sandbox.QuarantineDir)) {
		t.Error("a clean checkout gained a quarantine directory")
	}
}

// The descriptor lists CLAUDE.md, Claude.md and claude.md separately, and a
// case-insensitive existence test cannot tell them apart — it would rename the
// operator's file on macOS. Listing the directory and comparing bytes is what
// makes naming every spelling safe (lib/sandbox.sh:58-61).
func TestQuarantineIsCaseSensitive(t *testing.T) {
	paths := shippedDescriptor(t).Paths()

	for _, spelling := range []string{"claude.md", "CLAUDE.md", "Claude.md"} {
		t.Run(spelling, func(t *testing.T) {
			root := realTempDir(t)
			write(t, root, spelling, "instruction file\n")

			if _, err := sandbox.Quarantine(root, paths); err != nil {
				t.Fatalf("Quarantine: %v", err)
			}
			if _, _, err := sandbox.Restore(root, paths); err != nil {
				t.Fatalf("Restore: %v", err)
			}

			if !existsExactly(t, root, spelling) {
				entries, _ := os.ReadDir(root)
				names := []string{}
				for _, entry := range entries {
					names = append(names, entry.Name())
				}
				t.Fatalf("%s did not survive with its spelling: the root holds %v", spelling, names)
			}
			for _, other := range []string{"claude.md", "CLAUDE.md", "Claude.md"} {
				if other != spelling && existsExactly(t, root, other) {
					t.Errorf("%s appeared alongside %s", other, spelling)
				}
			}
		})
	}
}

// A harness write into a quarantined path was written blind: the quarantine
// moved the real file away before the harness started, so the agent never read
// it. Discarding that write is correct, and it must not be silent — a finding
// the resolver "fixed" by writing there is reported as fixed, lands in no
// commit, and the "reported fixes but changed no files" guard stays quiet
// because other files did change (lib/sandbox.sh:83-90).
func TestRestoreWarnsAboutADiscardedWrite(t *testing.T) {
	root := realTempDir(t)
	write(t, root, "CLAUDE.md", "the repository's own\n")
	write(t, root, ".mcp.json", "{}\n")
	paths := shippedDescriptor(t).Paths()

	if _, err := sandbox.Quarantine(root, paths); err != nil {
		t.Fatalf("Quarantine: %v", err)
	}
	write(t, root, "CLAUDE.md", "written blind by the harness\n")
	write(t, root, ".mcp.json", "written blind by the harness\n")

	clobbered, warning, err := sandbox.Restore(root, paths)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if strings.Join(clobbered, "|") != ".mcp.json|CLAUDE.md" {
		t.Errorf("clobbered = %v, want the two paths in descriptor order", clobbered)
	}
	if warning == nil {
		t.Fatal("a discarded write said nothing")
	}
	if want := "the harness wrote to quarantined path(s): .mcp.json CLAUDE.md"; warning.Message != want {
		t.Errorf("message = %q, want %q", warning.Message, want)
	}
	if !strings.Contains(warning.Hint, "is not fixed and is in no commit") {
		t.Errorf("hint = %q", warning.Hint)
	}

	// The repository's own file is what survives, not the blind write.
	raw, err := os.ReadFile(filepath.Join(root, "CLAUDE.md"))
	if err != nil {
		t.Fatalf("read CLAUDE.md: %v", err)
	}
	if string(raw) != "the repository's own\n" {
		t.Errorf("CLAUDE.md = %q, want the quarantined original", string(raw))
	}
}

// Restoring a checkout that was never quarantined is a no-op, which is the
// `[[ -d "$q" ]] || return 0` at lib/sandbox.sh:79. Stranded quarantine stays
// discoverable: nothing here deletes a quarantine it did not restore from.
func TestRestoreWithoutAQuarantine(t *testing.T) {
	root := realTempDir(t)
	write(t, root, "CLAUDE.md", "untouched\n")

	clobbered, warning, err := sandbox.Restore(root, shippedDescriptor(t).Paths())
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if len(clobbered) != 0 || warning != nil {
		t.Errorf("clobbered = %v, warning = %v, want neither", clobbered, warning)
	}
	if !exists(t, filepath.Join(root, "CLAUDE.md")) {
		t.Error("a file was removed by a restore with nothing to restore")
	}
}

// Nothing outside the root may be moved or removed, whatever a path says.
func TestQuarantineStaysInsideTheRoot(t *testing.T) {
	outer := realTempDir(t)
	root := filepath.Join(outer, "checkout")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("make the checkout: %v", err)
	}
	neighbour := write(t, outer, "CLAUDE.md", "the operator's own, outside the checkout\n")

	if _, err := sandbox.Quarantine(root, []string{"../CLAUDE.md"}); err == nil {
		t.Error("a climbing path was quarantined")
	}
	if !exists(t, neighbour) {
		t.Error("a file outside the checkout was moved")
	}

	if _, _, err := sandbox.Restore(root, []string{"../CLAUDE.md"}); err == nil {
		t.Error("a climbing path was restored")
	}
	if !exists(t, neighbour) {
		t.Error("a file outside the checkout was removed")
	}
}
