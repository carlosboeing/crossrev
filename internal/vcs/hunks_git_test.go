package vcs_test

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/diff"
	"github.com/carlosboeing/crossrev/internal/intel"
	"github.com/carlosboeing/crossrev/internal/vcs"
)

// commitTwo writes baseFiles, commits, writes headFiles, commits, and
// answers the two revisions shaping diffs between.
func commitTwo(t *testing.T, baseFiles, headFiles map[string]string) (*vcs.Repository, core.Revision, core.Revision) {
	t.Helper()
	git := testGit(t)
	repo := initRepo(t, git, filepath.Join(realTempDir(t), "clone"))
	for path, content := range baseFiles {
		write(t, repo.Dir(), path, content)
	}
	mustGit(t, repo, "add", "-A")
	mustGit(t, repo, append(append([]string{}, testIdentity...), "commit", "-q", "-m", "base")...)
	base, err := repo.Head(context.Background())
	if err != nil {
		t.Fatalf("read base HEAD: %v", err)
	}
	for path, content := range headFiles {
		write(t, repo.Dir(), path, content)
	}
	mustGit(t, repo, "add", "-A")
	mustGit(t, repo, append(append([]string{}, testIdentity...), "commit", "-q", "-m", "head")...)
	head, err := repo.Head(context.Background())
	if err != nil {
		t.Fatalf("read head HEAD: %v", err)
	}
	return repo, base, head
}

func hunkSupport(t *testing.T, repo *vcs.Repository) bool {
	t.Helper()
	support, _, err := repo.HunkDiffSupport(context.Background())
	if err != nil {
		t.Fatalf("HunkDiffSupport: %v", err)
	}
	return support
}

// Against a real git, a small modified file shapes to one hunk: the -U
// line count covers the file, so the numbered diff carries one header.
func TestShapeFileDiffFullTextAgainstRealGit(t *testing.T) {
	base := "package a\n\nfunc A() int {\n\treturn 1\n}\n"
	head := "package a\n\nfunc A() int {\n\treturn 2\n}\n"
	repo, baseRev, headRev := commitTwo(t, map[string]string{"a.go": base}, map[string]string{"a.go": head})

	got, err := repo.ShapeFileDiff(context.Background(), baseRev, headRev,
		core.FileChange{Path: "a.go", Kind: core.ChangeModified}, []byte(head), false, "", hunkSupport(t, repo))
	if err != nil {
		t.Fatalf("ShapeFileDiff: %v", err)
	}
	if got.Form != intel.FormFullText {
		t.Fatalf("Form = %q, want full_text", got.Form)
	}
	numbered := string(diff.Parse(got.Diff, core.RevisionPair{}).Numbered())
	headers := 0
	for _, line := range strings.Split(numbered, "\n") {
		if strings.HasPrefix(line, "@@") {
			headers++
		}
	}
	if headers != 1 {
		t.Errorf("numbered diff carries %d @@ headers, want 1:\n%s", headers, numbered)
	}
	if !strings.Contains(numbered, "+\treturn 2") {
		t.Errorf("numbered diff lost the change:\n%s", numbered)
	}
}

// Against a real git, a large Go file shapes through -W and clips: the
// change's enclosing function stays, the other 299 go.
func TestShapeFileDiffLargeGoAgainstRealGit(t *testing.T) {
	var base, head strings.Builder
	base.WriteString("package big\n\n")
	head.WriteString("package big\n\n")
	for i := 1; i <= 300; i++ {
		ret := i
		if i == 150 {
			fmt.Fprintf(&base, "func F%03d() int {\n\treturn %d\n}\n\n", i, ret)
			fmt.Fprintf(&head, "func F%03d() int {\n\treturn %d\n}\n\n", i, 9999)
			continue
		}
		fmt.Fprintf(&base, "func F%03d() int {\n\treturn %d\n}\n\n", i, ret)
		fmt.Fprintf(&head, "func F%03d() int {\n\treturn %d\n}\n\n", i, ret)
	}
	repo, baseRev, headRev := commitTwo(t,
		map[string]string{"big.go": base.String()},
		map[string]string{"big.go": head.String()})

	got, err := repo.ShapeFileDiff(context.Background(), baseRev, headRev,
		core.FileChange{Path: "big.go", Kind: core.ChangeModified}, []byte(head.String()), false, "", hunkSupport(t, repo))
	if err != nil {
		t.Fatalf("ShapeFileDiff: %v", err)
	}
	if got.Form != intel.FormHunksContext {
		t.Fatalf("Form = %q, want hunks_context", got.Form)
	}
	clipped := string(got.Diff)
	if !strings.Contains(clipped, "return 9999") {
		t.Fatalf("clipped hunks lost the change:\n%s", clipped[:500])
	}
	if strings.Contains(clipped, "return 1\n") && strings.Contains(clipped, "return 300\n") {
		t.Errorf("clipped hunks kept both far ends of the file")
	}
	minOld, body := gutterStats(t, diff.Parse(got.Diff, core.RevisionPair{}).Numbered())
	if minOld < 1 {
		t.Errorf("no old lines numbered")
	}
	if body > 260 {
		t.Errorf("clipped hunks hold %d body lines, want about the 100-line window", body)
	}
	_ = minOld
}

// Against a real git, a large JSON file widens under -W to the whole file
// and the clip pulls it back to the 100-line window.
func TestShapeFileDiffLargeJSONAgainstRealGit(t *testing.T) {
	// Seven hundred entries clear the 8 KiB whole-file threshold, so the
	// file shapes through -W rather than reading in full.
	var base, head strings.Builder
	base.WriteString("{\n")
	head.WriteString("{\n")
	for i := range 700 {
		fmt.Fprintf(&base, "  \"k%03d\": %d,\n", i, i)
		if i == 350 {
			fmt.Fprintf(&head, "  \"k%03d\": %d,\n", i, 9999)
		} else {
			fmt.Fprintf(&head, "  \"k%03d\": %d,\n", i, i)
		}
	}
	base.WriteString("  \"last\": 0\n}\n")
	head.WriteString("  \"last\": 0\n}\n")
	repo, baseRev, headRev := commitTwo(t,
		map[string]string{"big.json": base.String()},
		map[string]string{"big.json": head.String()})

	got, err := repo.ShapeFileDiff(context.Background(), baseRev, headRev,
		core.FileChange{Path: "big.json", Kind: core.ChangeModified}, []byte(head.String()), false, "", hunkSupport(t, repo))
	if err != nil {
		t.Fatalf("ShapeFileDiff: %v", err)
	}
	if got.Form != intel.FormHunksContext {
		t.Fatalf("Form = %q, want hunks_context", got.Form)
	}
	minOld, body := gutterStats(t, diff.Parse(got.Diff, core.RevisionPair{}).Numbered())
	if minOld == 1 {
		t.Errorf("clipped hunks still widen to line 1")
	}
	if body > 260 {
		t.Errorf("clipped hunks hold %d body lines, want about the 100-line window", body)
	}
	if !strings.Contains(string(got.Diff), "9999") {
		t.Error("clipped hunks lost the changed line")
	}
}

// Every diff= pin in the embedded attributes names a built-in driver from
// gitattributes(5). An unknown name silently falls back to the default
// funcname pattern: under `diff=go` a change in a Go import block heads
// its -W hunk with `package`, while `diff=golang` finds no enclosing
// function. The probes below are the built-in list on the installed git,
// checked by hand against `git help gitattributes`; check-attr under the
// embedded file must answer each one, and must leave the extensions git
// carries no driver for unspecified.
func TestReviewAttributesNameBuiltInDrivers(t *testing.T) {
	attrs := write(t, t.TempDir(), "review.gitattributes", string(vcs.ReviewAttributes()))
	git := testGit(t)
	repo := initRepo(t, git, filepath.Join(realTempDir(t), "clone"))

	probes := []struct{ path, driver string }{
		{"probe.ada", "ada"},
		{"probe.c", "cpp"},
		{"probe.h", "cpp"},
		{"probe.cc", "cpp"},
		{"probe.cpp", "cpp"},
		{"probe.cs", "csharp"},
		{"probe.css", "css"},
		{"probe.ex", "elixir"},
		{"probe.exs", "elixir"},
		{"probe.f", "fortran"},
		{"probe.f90", "fortran"},
		{"probe.go", "golang"},
		{"probe.html", "html"},
		{"probe.htm", "html"},
		{"probe.java", "java"},
		{"probe.m", "objc"},
		{"probe.mm", "objc"},
		{"probe.pas", "pascal"},
		{"probe.pl", "perl"},
		{"probe.pm", "perl"},
		{"probe.php", "php"},
		{"probe.py", "python"},
		{"probe.rb", "ruby"},
		{"probe.rs", "rust"},
		{"probe.tex", "tex"},
	}
	// git carries no driver for these, so the file must not pin them:
	// they stay unspecified and shape under the default pattern.
	unspecified := []string{"probe.d", "probe.js", "probe.jsx"}

	args := []string{"-c", "core.attributesFile=" + attrs, "check-attr", "diff", "--"}
	for _, probe := range probes {
		args = append(args, probe.path)
	}
	args = append(args, unspecified...)
	answers := map[string]string{}
	for _, line := range mustGit(t, repo, args...).Lines() {
		path, value, ok := strings.Cut(line, ": diff: ")
		if !ok {
			t.Fatalf("unparseable check-attr line %q", line)
		}
		answers[path] = value
	}
	for _, probe := range probes {
		if answers[probe.path] != probe.driver {
			t.Errorf("check-attr diff %s = %q, want built-in driver %q", probe.path, answers[probe.path], probe.driver)
		}
	}
	for _, path := range unspecified {
		if answers[path] != "unspecified" {
			t.Errorf("check-attr diff %s = %q, want unspecified (git carries no driver for it)", path, answers[path])
		}
	}

	// Every diff= pin in the file is covered above: a line pinning an
	// unlisted name passes check-attr silently, so the file cannot grow
	// one without this test naming it.
	pinned := map[string]bool{}
	for _, line := range strings.Split(string(vcs.ReviewAttributes()), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || !strings.HasPrefix(fields[1], "diff=") {
			continue
		}
		probe := "probe." + strings.TrimPrefix(fields[0], "*.")
		pinned[probe] = true
		want := ""
		for _, p := range probes {
			if p.path == probe {
				want = p.driver
			}
		}
		if want == "" {
			t.Errorf("embedded attributes pin %q, which no probe above covers", line)
		} else if want != strings.TrimPrefix(fields[1], "diff=") {
			t.Errorf("embedded attributes pin %q, want diff=%s", line, want)
		}
	}
	for _, probe := range probes {
		if !pinned[probe.path] {
			t.Errorf("probe path %s has no pin in the embedded attributes", probe.path)
		}
	}
}
