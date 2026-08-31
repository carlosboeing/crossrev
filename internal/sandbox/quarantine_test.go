package sandbox_test

import (
	"errors"
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

	// The contents first and the order second. Comparing the list to a sorted
	// copy of itself asks only whether it is sorted, and the empty list is.
	wantMoved := []string{
		".claude", ".codex", ".gemini", ".github/copilot-instructions.md",
		".grok", ".mcp.json", "AGENTS.md", "CLAUDE.md",
	}
	if strings.Join(moved, "|") != strings.Join(wantMoved, "|") {
		t.Errorf("moved = %v, want %v", moved, wantMoved)
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

// The guard that stops a recursive delete of the checkout itself.
//
// `.` is the only input that reaches it. isRelativePath catches an absolute
// path and a `..` segment first, and `filepath.Join(root, ".")` is root — so
// without the guard, Restore reaches os.RemoveAll on the checkout.
func TestQuarantinePathThatNamesTheRootItself(t *testing.T) {
	root := realTempDir(t)
	write(t, root, "src/app.ts", "real source\n")
	write(t, root, "CLAUDE.md", "the repository's own\n")

	if _, err := sandbox.Quarantine(root, []string{"."}); !errors.Is(err, sandbox.ErrDescriptor) {
		t.Errorf("Quarantine(\".\") = %v, want an ErrDescriptor refusal", err)
	}
	if !exists(t, filepath.Join(root, "src/app.ts")) {
		t.Fatal("quarantining \".\" moved the checkout")
	}

	// A real quarantine has to exist for Restore to get past its own entry
	// check and reach the guard at all.
	if _, err := sandbox.Quarantine(root, []string{"CLAUDE.md"}); err != nil {
		t.Fatalf("Quarantine: %v", err)
	}
	if _, _, err := sandbox.Restore(root, []string{"."}); !errors.Is(err, sandbox.ErrDescriptor) {
		t.Errorf("Restore(\".\") = %v, want an ErrDescriptor refusal", err)
	}
	if !exists(t, filepath.Join(root, "src/app.ts")) {
		t.Fatal("restoring \".\" destroyed the checkout")
	}
	if !exists(t, filepath.Join(root, sandbox.QuarantineDir, "CLAUDE.md")) {
		t.Error("the quarantine was removed by a refused restore")
	}
}

// A quarantined path is a directory more often than not — .claude,
// .codex, .cursor and .agents are all directories — and a harness recreating
// one during a run is routine. os.Rename overwrites a file and refuses a
// non-empty directory, so the clobbered path has to be removed first.
func TestRestorePutsADirectoryBackOverOneTheHarnessRecreated(t *testing.T) {
	root := realTempDir(t)
	write(t, root, ".claude/settings.json", `{"hooks":{"SessionStart":[]}}`+"\n")
	paths := shippedDescriptor(t).Paths()

	if _, err := sandbox.Quarantine(root, paths); err != nil {
		t.Fatalf("Quarantine: %v", err)
	}
	if exists(t, filepath.Join(root, ".claude")) {
		t.Fatal(".claude was not quarantined")
	}

	// The harness writes into the path it could not read, blind.
	write(t, root, ".claude/hooks/evil.sh", "#!/bin/sh\nexit 0\n")

	clobbered, warning, err := sandbox.Restore(root, paths)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if strings.Join(clobbered, "|") != ".claude" {
		t.Errorf("clobbered = %v, want [.claude]", clobbered)
	}
	if warning == nil {
		t.Error("a discarded write into a quarantined directory said nothing")
	}

	raw, err := os.ReadFile(filepath.Join(root, ".claude/settings.json"))
	if err != nil {
		t.Fatalf("the operator's own settings file is gone: %v", err)
	}
	if !strings.Contains(string(raw), "SessionStart") {
		t.Errorf("settings.json = %q, want the quarantined original", string(raw))
	}
	if exists(t, filepath.Join(root, ".claude/hooks/evil.sh")) {
		t.Error("the harness's blind write survived the restore")
	}
	if exists(t, filepath.Join(root, sandbox.QuarantineDir)) {
		t.Error("the quarantine was left stranded in the checkout")
	}
}

// Nothing here asserts a mode anywhere else, and these two directories are
// created inside a checkout the operator owns. A narrower mode would break a
// second run under a different umask; a wider one is a permission change
// CrossRev made without saying so.
func TestQuarantineDirectoryModes(t *testing.T) {
	root := realTempDir(t)
	write(t, root, ".github/copilot-instructions.md", "x\n")
	paths := shippedDescriptor(t).Paths()

	if _, err := sandbox.Quarantine(root, paths); err != nil {
		t.Fatalf("Quarantine: %v", err)
	}
	for _, path := range []string{sandbox.QuarantineDir, sandbox.QuarantineDir + "/.github"} {
		info, err := os.Stat(filepath.Join(root, path))
		if err != nil {
			t.Fatalf("stat %s: %v", path, err)
		}
		if got := info.Mode().Perm(); got != 0o755 {
			t.Errorf("%s mode = %04o, want 0755", path, got)
		}
	}

	// And the parent the restore recreates for a nested path.
	if err := os.RemoveAll(filepath.Join(root, ".github")); err != nil {
		t.Fatalf("remove .github: %v", err)
	}
	if _, _, err := sandbox.Restore(root, paths); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	info, err := os.Stat(filepath.Join(root, ".github"))
	if err != nil {
		t.Fatalf("stat the recreated parent: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o755 {
		t.Errorf(".github mode = %04o, want 0755", got)
	}
}

// os.Rename is stricter than the shell's mv, and this pins the difference so it
// stays declared rather than drifting back.
//
// Where a stranded quarantine already holds the path and the checkout holds it
// again, `mv` moves the second inside the first and exits zero, leaving
// .crossrev-quarantine/.claude/.claude. os.Rename refuses, so Quarantine fails
// and says why.
func TestQuarantineRefusesToNestOverAStrandedQuarantine(t *testing.T) {
	root := realTempDir(t)
	write(t, root, ".claude/settings.json", "the run that was interrupted\n")
	paths := shippedDescriptor(t).Paths()

	// A run that was killed between quarantine and restore.
	if _, err := sandbox.Quarantine(root, paths); err != nil {
		t.Fatalf("Quarantine: %v", err)
	}
	if !exists(t, filepath.Join(root, sandbox.QuarantineDir, ".claude/settings.json")) {
		t.Fatal("the stranded quarantine was not created")
	}
	// The checkout holds the path again.
	write(t, root, ".claude/settings.json", "the next run's\n")

	_, err := sandbox.Quarantine(root, paths)
	if err == nil {
		t.Fatal("quarantining over a stranded quarantine succeeded")
	}
	if exists(t, filepath.Join(root, sandbox.QuarantineDir, ".claude/.claude")) {
		t.Error("the second directory was nested inside the first")
	}
}
