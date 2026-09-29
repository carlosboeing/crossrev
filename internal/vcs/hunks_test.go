package vcs_test

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/diff"
	"github.com/carlosboeing/crossrev/internal/exec"
	"github.com/carlosboeing/crossrev/internal/intel"
	"github.com/carlosboeing/crossrev/internal/vcs"
)

// hunkStub answers git version from text and every other call with the
// canned diff, recording every argv so a test pins the exact git invocation.
type hunkStub struct {
	versionText string
	diffStdout  string
	diffCode    int
	diffStderr  string
	calls       [][]string
}

func (s *hunkStub) Run(_ context.Context, spec exec.Spec) exec.Result {
	s.calls = append(s.calls, append([]string(nil), spec.Args...))
	if len(spec.Args) > 0 && spec.Args[0] == "version" {
		return exec.Result{ExitCode: 0, Stdout: []byte(s.versionText)}
	}
	return exec.Result{ExitCode: s.diffCode, Stdout: []byte(s.diffStdout), Stderr: []byte(s.diffStderr)}
}

func hunkRepo(t *testing.T, s *hunkStub) *vcs.Repository {
	t.Helper()
	return vcs.New(s, nil).At(t.TempDir())
}

func hunkRevisions(t *testing.T) (base, head core.Revision) {
	t.Helper()
	return mustRevision(t, "1111111111111111111111111111111111111111"),
		mustRevision(t, "2222222222222222222222222222222222222222")
}

func hunkArgv(calls [][]string) []string {
	for _, c := range calls {
		if len(c) > 0 && c[0] != "version" {
			return c
		}
	}
	return nil
}

func argvHas(argv []string, want string) bool {
	for _, a := range argv {
		if a == want {
			return true
		}
	}
	return false
}

func itoaClip(n int) string {
	return strconv.Itoa(n)
}

// gutterStats reads the old gutter column back out of numbered output: the
// smallest old line shown and how many body lines there are. Headers carry
// no gutter, so only hunk lines count.
func gutterStats(t *testing.T, numbered []byte) (minOld, body int) {
	t.Helper()
	minOld = -1
	for _, line := range strings.Split(string(numbered), "\n") {
		bar := strings.IndexByte(line, '|')
		if bar < 0 {
			continue
		}
		fields := strings.Fields(line[:bar])
		if len(fields) != 2 {
			t.Fatalf("gutter line without two columns: %q", line)
		}
		if fields[0] == "-" {
			body++
			continue
		}
		n, err := strconv.Atoi(fields[0])
		if err != nil {
			t.Fatalf("gutter old %q is not a number: %q", fields[0], line)
		}
		if minOld < 0 || n < minOld {
			minOld = n
		}
		body++
	}
	return minOld, body
}

// A modified file under 8 KiB reads in full: one `git diff -U<line count>`
// call over the literal path, no function-context flag, and the shaped
// diff back unchanged.
func TestShapeFileDiffSmallModifiedReadsInFull(t *testing.T) {
	body := []byte("package a\n\nfunc A() int {\n\treturn 1\n}\n")
	canned := "diff --git a/a.go b/a.go\nindex 1111111..2222222 100644\n--- a/a.go\n+++ b/a.go\n@@ -1,5 +1,5 @@\n package a\n \n func A() int {\n-\treturn 1\n+\treturn 2\n }\n"
	stub := &hunkStub{versionText: "git version 2.50.1\n", diffStdout: canned}
	repo := hunkRepo(t, stub)
	base, head := hunkRevisions(t)

	got, err := repo.ShapeFileDiff(context.Background(), base, head,
		core.FileChange{Path: "a.go", Kind: core.ChangeModified}, body, false, "", true)
	if err != nil {
		t.Fatalf("ShapeFileDiff: %v", err)
	}
	if got.Form != intel.FormFullText {
		t.Errorf("Form = %q, want full_text", got.Form)
	}
	if string(got.Diff) != canned {
		t.Errorf("shaped diff differs from the whole-file hunk:\n%s", got.Diff)
	}
	argv := hunkArgv(stub.calls)
	for _, want := range []string{"-U5", "--no-ext-diff", "--no-textconv", "-M", ":(literal)a.go"} {
		if !argvHas(argv, want) {
			t.Errorf("argv %q lacks %q", argv, want)
		}
	}
	if argvHas(argv, "-W") {
		t.Errorf("argv %q carries -W for a whole-file read", argv)
	}
}

// The whole-file read renders once as one numbered hunk: every content line
// numbered, a single @@ header.
func TestShapeFileDiffSmallModifiedNumbersAsOneHunk(t *testing.T) {
	body := []byte("package a\n\nfunc A() int {\n\treturn 1\n}\n")
	canned := "diff --git a/a.go b/a.go\nindex 1111111..2222222 100644\n--- a/a.go\n+++ b/a.go\n@@ -1,5 +1,5 @@\n package a\n \n func A() int {\n-\treturn 1\n+\treturn 2\n }\n"
	stub := &hunkStub{versionText: "git version 2.50.1\n", diffStdout: canned}
	repo := hunkRepo(t, stub)
	base, head := hunkRevisions(t)

	got, err := repo.ShapeFileDiff(context.Background(), base, head,
		core.FileChange{Path: "a.go", Kind: core.ChangeModified}, body, false, "", true)
	if err != nil {
		t.Fatalf("ShapeFileDiff: %v", err)
	}
	numbered := string(diff.Parse(got.Diff, core.RevisionPair{}).Numbered())
	headers := 0
	for _, line := range strings.Split(numbered, "\n") {
		if strings.HasPrefix(line, "@@") {
			headers++
		}
	}
	if headers != 1 {
		t.Errorf("numbered hunk carries %d @@ headers, want 1:\n%s", headers, numbered)
	}
	if !strings.Contains(numbered, "-\treturn 1") || !strings.Contains(numbered, "+\treturn 2") {
		t.Errorf("numbered hunk lost the changed lines:\n%s", numbered)
	}
}

// An added file counts its head lines; a deleted file its base lines, read
// through the old path.
func TestShapeFileDiffAddedAndDeletedCountTheirOwnLines(t *testing.T) {
	canned := "diff --git a/new.go b/new.go\nnew file mode 100644\nindex 0000000..2222222\n--- /dev/null\n+++ b/new.go\n@@ -0,0 +1,3 @@\n+package new\n+\n+func N() {}\n"
	stub := &hunkStub{versionText: "git version 2.50.1\n", diffStdout: canned}
	repo := hunkRepo(t, stub)
	base, head := hunkRevisions(t)

	got, err := repo.ShapeFileDiff(context.Background(), base, head,
		core.FileChange{Path: "new.go", Kind: core.ChangeAdded}, []byte("package new\n\nfunc N() {}\n"), false, "", true)
	if err != nil {
		t.Fatalf("ShapeFileDiff: %v", err)
	}
	if got.Form != intel.FormFullText {
		t.Errorf("added Form = %q, want full_text", got.Form)
	}
	if argv := hunkArgv(stub.calls); !argvHas(argv, "-U3") {
		t.Errorf("added argv %q lacks -U3 for its three head lines", argv)
	}

	stub.calls = nil
	stub.diffStdout = "diff --git a/old.go b/old.go\ndeleted file mode 100644\nindex 1111111..0000000\n--- a/old.go\n+++ /dev/null\n@@ -1,2 +1,0 @@\n-package old\n-func O() {}\n"
	got, err = repo.ShapeFileDiff(context.Background(), base, head,
		core.FileChange{Path: "old.go", OldPath: "old.go", Kind: core.ChangeDeleted}, []byte("package old\nfunc O() {}\n"), false, "", true)
	if err != nil {
		t.Fatalf("ShapeFileDiff: %v", err)
	}
	if got.Form != intel.FormFullText {
		t.Errorf("deleted Form = %q, want full_text", got.Form)
	}
	if argv := hunkArgv(stub.calls); !argvHas(argv, "-U2") || !argvHas(argv, ":(literal)old.go") {
		t.Errorf("deleted argv %q lacks -U2 at the old path", argv)
	}
}

// A rename shapes over both paths, so git still sees the pair and reports
// the similarity rather than a delete-plus-add.
func TestShapeFileDiffRenamePassesBothPaths(t *testing.T) {
	canned := "diff --git a/old.go b/new.go\nsimilarity index 76%\nrename from old.go\nrename to new.go\nindex 1111111..2222222 100644\n--- a/old.go\n+++ b/new.go\n@@ -1,5 +1,5 @@\n package old\n \n func A() int {\n-\treturn 1\n+\treturn 2\n }\n"
	stub := &hunkStub{versionText: "git version 2.50.1\n", diffStdout: canned}
	repo := hunkRepo(t, stub)
	base, head := hunkRevisions(t)

	body := []byte("package old\n\nfunc A() int {\n\treturn 2\n}\n")
	got, err := repo.ShapeFileDiff(context.Background(), base, head,
		core.FileChange{Path: "new.go", OldPath: "old.go", Kind: core.ChangeRenamed}, body, false, "", true)
	if err != nil {
		t.Fatalf("ShapeFileDiff: %v", err)
	}
	if got.Form != intel.FormFullText {
		t.Errorf("small renamed Form = %q, want full_text", got.Form)
	}
	argv := hunkArgv(stub.calls)
	if !argvHas(argv, ":(literal)old.go") || !argvHas(argv, ":(literal)new.go") {
		t.Errorf("rename argv %q names only one side of the pair", argv)
	}
}

// A large edited file reads through `git diff -W` under the embedded
// attributes and the base tree, then clips to the 100-line window.
func TestShapeFileDiffLargeGoReadsFunctionContextAndClips(t *testing.T) {
	var raw strings.Builder
	raw.WriteString("diff --git a/big.go b/big.go\nindex 1111111..2222222 100644\n--- a/big.go\n+++ b/big.go\n@@ -1,303 +1,303 @@ func big()\n")
	for i := 1; i <= 303; i++ {
		if i == 150 {
			raw.WriteString("-line 150 old\n+line 150 new\n")
			continue
		}
		raw.WriteString(" line " + itoaClip(i) + "\n")
	}
	stub := &hunkStub{versionText: "git version 2.50.1\n", diffStdout: raw.String()}
	repo := hunkRepo(t, stub)
	base, head := hunkRevisions(t)

	got, err := repo.ShapeFileDiff(context.Background(), base, head,
		core.FileChange{Path: "big.go", Kind: core.ChangeModified}, make([]byte, 8*1024+1), false, "", true)
	if err != nil {
		t.Fatalf("ShapeFileDiff: %v", err)
	}
	if got.Form != intel.FormHunksContext {
		t.Fatalf("Form = %q, want hunks_context", got.Form)
	}
	argv := hunkArgv(stub.calls)
	for _, want := range []string{"-W", "--attr-source=1111111111111111111111111111111111111111", "--no-ext-diff", "--no-textconv", "-M"} {
		if !argvHas(argv, want) {
			t.Errorf("argv %q lacks %q", argv, want)
		}
	}
	clipped := string(got.Diff)
	if !strings.Contains(clipped, "@@ -50,201 +50,201 @@") {
		t.Errorf("clipped hunk header is not the 100-line window:\n%s", clipped[:400])
	}
	if strings.Contains(clipped, "@@ -1,") {
		t.Errorf("clipped hunk still widens to line 1")
	}
	if !strings.Contains(clipped, "-line 150 old") || !strings.Contains(clipped, "+line 150 new") {
		t.Error("clipped hunk lost the changed lines")
	}
}

// A JSON change carries no function line, so -W widens to the whole file
// and the clip — not the function flag — is what bounds the input.
func TestShapeFileDiffJSONClipsInsteadOfWideningToLineOne(t *testing.T) {
	var raw strings.Builder
	raw.WriteString("diff --git a/big.json b/big.json\nindex 1111111..2222222 100644\n--- a/big.json\n+++ b/big.json\n@@ -1,303 +1,303 @@\n")
	for i := 1; i <= 303; i++ {
		if i == 150 {
			raw.WriteString("-  \"k150\": 150,\n+  \"k150\": 9999,\n")
			continue
		}
		raw.WriteString("   line " + itoaClip(i) + "\n")
	}
	stub := &hunkStub{versionText: "git version 2.50.1\n", diffStdout: raw.String()}
	repo := hunkRepo(t, stub)
	base, head := hunkRevisions(t)

	got, err := repo.ShapeFileDiff(context.Background(), base, head,
		core.FileChange{Path: "big.json", Kind: core.ChangeModified}, make([]byte, 8*1024+1), false, "", true)
	if err != nil {
		t.Fatalf("ShapeFileDiff: %v", err)
	}
	if got.Form != intel.FormHunksContext {
		t.Fatalf("Form = %q, want hunks_context", got.Form)
	}
	minOld, body := gutterStats(t, diff.Parse(got.Diff, core.RevisionPair{}).Numbered())
	if minOld != 50 {
		t.Errorf("clipped window starts at old line %d, want 50 (100 lines above the change)", minOld)
	}
	if body > 220 {
		t.Errorf("clipped hunk holds %d body lines, want the 100-line window", body)
	}
}

// Binary content shapes header-only with its access reason, whatever git
// says around it.
func TestShapeFileDiffBinaryIsHeaderOnly(t *testing.T) {
	canned := "diff --git a/blob.bin b/blob.bin\nindex 1111111..2222222 100644\nBinary files a/blob.bin and b/blob.bin differ\n"
	stub := &hunkStub{versionText: "git version 2.50.1\n", diffStdout: canned}
	repo := hunkRepo(t, stub)
	base, head := hunkRevisions(t)

	got, err := repo.ShapeFileDiff(context.Background(), base, head,
		core.FileChange{Path: "blob.bin", Kind: core.ChangeModified}, []byte("GIF89a\x00"), true, "", true)
	if err != nil {
		t.Fatalf("ShapeFileDiff: %v", err)
	}
	if got.Form != intel.FormDiffOnly {
		t.Errorf("Form = %q, want diff_only", got.Form)
	}
	if !strings.Contains(string(got.Diff), "Binary files a/blob.bin and b/blob.bin differ") {
		t.Errorf("header-only diff lost the binary header:\n%s", got.Diff)
	}
	if got.Reason == "" {
		t.Error("diff_only carries no access reason")
	}
}

// A pure rename and a mode-only change carry headers and no hunks, so they
// shape header-only with the reason naming the changeless kind.
func TestShapeFileDiffPureRenameAndModeOnlyAreHeaderOnly(t *testing.T) {
	rename := "diff --git a/old.go b/new.go\nsimilarity index 100%\nrename from old.go\nrename to new.go\n"
	mode := "diff --git a/run.sh b/run.sh\nold mode 100644\nnew mode 100755\n"
	for _, tc := range []struct {
		name   string
		change core.FileChange
		canned string
		reason string
	}{
		{"pure rename", core.FileChange{Path: "new.go", OldPath: "old.go", Kind: core.ChangeRenamed}, rename, "renamed without edits"},
		{"mode-only", core.FileChange{Path: "run.sh", Kind: core.ChangeTypeChanged}, mode, "mode change with no content change"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stub := &hunkStub{versionText: "git version 2.50.1\n", diffStdout: tc.canned}
			repo := hunkRepo(t, stub)
			base, head := hunkRevisions(t)

			got, err := repo.ShapeFileDiff(context.Background(), base, head,
				tc.change, []byte("package x\n"), false, "", true)
			if err != nil {
				t.Fatalf("ShapeFileDiff: %v", err)
			}
			if got.Form != intel.FormDiffOnly {
				t.Errorf("Form = %q, want diff_only", got.Form)
			}
			if strings.Contains(string(got.Diff), "@@") {
				t.Errorf("header-only diff carries a hunk:\n%s", got.Diff)
			}
			if got.Reason != tc.reason {
				t.Errorf("Reason = %q, want %q", got.Reason, tc.reason)
			}
		})
	}
}

// Below git 2.41 CrossRev omits --attr-source and still shapes: the -W
// hunks read .gitattributes from the worktree, and the support check
// reports the gap.
func TestShapeFileDiffOmitsAttrSourceBelowGit241(t *testing.T) {
	stub := &hunkStub{versionText: "git version 2.40.5\n", diffStdout: "diff --git a/big.go b/big.go\nindex 1..2 100644\n--- a/big.go\n+++ b/big.go\n@@ -1,3 +1,3 @@\n a\n-b\n+c\n d\n"}
	repo := hunkRepo(t, stub)
	base, head := hunkRevisions(t)

	got, err := repo.ShapeFileDiff(context.Background(), base, head,
		core.FileChange{Path: "big.go", Kind: core.ChangeModified}, make([]byte, 8*1024+1), false, "", false)
	if err != nil {
		t.Fatalf("ShapeFileDiff: %v", err)
	}
	if got.Form != intel.FormHunksContext {
		t.Errorf("Form = %q, want hunks_context even without --attr-source", got.Form)
	}
	for _, argv := range stub.calls {
		for _, a := range argv {
			if strings.HasPrefix(a, "--attr-source=") {
				t.Errorf("argv %q carries --attr-source on git 2.40.5", argv)
			}
		}
	}
	if argv := hunkArgv(stub.calls); !argvHas(argv, "-W") {
		t.Errorf("argv %q lost -W with the flag omitted", argv)
	}
}

// HunkDiffSupport gates --attr-source on git 2.41: older gits shape
// without it plus one warning naming the version, and an unparseable
// version fails the pass rather than shaping blind.
func TestHunkDiffSupportGatesAttrSourceOnGit241(t *testing.T) {
	for _, tc := range []struct {
		version string
		support bool
		warns   bool
	}{
		{"git version 2.50.1\n", true, false},
		{"git version 2.41.0\n", true, false},
		{"git version 2.40.5\n", false, true},
		{"git version 2.39.3 (Apple Git-154)\n", false, true},
	} {
		t.Run(tc.version, func(t *testing.T) {
			stub := &hunkStub{versionText: tc.version}
			support, warning, err := hunkRepo(t, stub).HunkDiffSupport(context.Background())
			if err != nil {
				t.Fatalf("HunkDiffSupport: %v", err)
			}
			if support != tc.support {
				t.Errorf("support = %v, want %v", support, tc.support)
			}
			if (warning != nil) != tc.warns {
				t.Errorf("warning = %v, want warns=%v", warning, tc.warns)
			}
			if tc.warns && !strings.Contains(warning.Message, "2.4") {
				t.Errorf("warning does not name the version: %q", warning.Message)
			}
		})
	}

	stub := &hunkStub{versionText: "git is confused\n"}
	if _, _, err := hunkRepo(t, stub).HunkDiffSupport(context.Background()); err == nil {
		t.Fatal("a malformed git version passed silently, want an error")
	}
}

// The embedded attributes are a copy the sync script keeps identical to
// the canonical file: editing one without the other changes what shaping
// reads while the file a contributor edits stays as it was.
func TestReviewAttributesMatchTheCanonicalFile(t *testing.T) {
	raw, err := readCanonical(t, "assets/crossrev.gitattributes")
	if err != nil {
		t.Fatalf("read the canonical attributes: %v", err)
	}
	if string(vcs.ReviewAttributes()) != string(raw) {
		t.Error("the embedded attributes differ from assets/crossrev.gitattributes; run bash scripts/sync-embedded-assets.sh")
	}
}

// The embedded attributes pin function-context drivers so shaping does not
// depend on the operator's git config; the accessor hands back a copy.
func TestReviewAttributesAreEmbedded(t *testing.T) {
	first := vcs.ReviewAttributes()
	if len(first) == 0 {
		t.Fatal("ReviewAttributes is empty")
	}
	for _, want := range []string{"*.go", "diff="} {
		if !strings.Contains(string(first), want) {
			t.Errorf("embedded attributes lack %q:\n%s", want, first)
		}
	}
	first[0] = 'X'
	if again := vcs.ReviewAttributes(); again[0] == 'X' {
		t.Error("writing through the accessor's result changed the embedded copy")
	}
}
