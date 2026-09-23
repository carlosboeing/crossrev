package vcs_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/exec"
	"github.com/carlosboeing/crossrev/internal/vcs"
)

// requireCheckAttrSource skips when the machine's git predates
// check-attr --source, which arrived in 2.40.
func requireCheckAttrSource(t *testing.T, repo *vcs.Repository) {
	t.Helper()
	output, err := repo.Run(context.Background(), "version")
	if err != nil || !output.OK() {
		t.Fatalf("git version: %v %s", err, output.Stderr)
	}
	version, ok := vcs.ParseGitVersion(output.Text())
	if !ok {
		t.Fatalf("unparseable git version %q", output.Text())
	}
	if !version.AtLeast(2, 40) {
		t.Skipf("git %s has no check-attr --source", version.Token)
	}
}

// attributesRepo builds the fixture repository: root and nested
// .gitattributes covering set, true, false, negated and unspecified paths,
// committed at the base revision.
func attributesRepo(t *testing.T) (*vcs.Repository, core.Revision) {
	t.Helper()
	git := testGit(t)
	repo := initRepo(t, git, filepath.Join(realTempDir(t), "clone"))
	write(t, repo.Dir(), ".gitattributes", "dist/** linguist-generated\ngen/** linguist-generated=true\nlocked/** -linguist-generated\ndata/** linguist-generated=false\n\"my dir/**\" linguist-generated\n")
	write(t, repo.Dir(), "dist/a.js", "var a = 1\n")
	write(t, repo.Dir(), "gen/b.js", "var b = 1\n")
	write(t, repo.Dir(), "locked/c.go", "package c\n")
	write(t, repo.Dir(), "data/d.bin", "x\n")
	write(t, repo.Dir(), "src/other.go", "package src\n")
	write(t, repo.Dir(), "src/nested/.gitattributes", "*.txt linguist-generated\n")
	write(t, repo.Dir(), "src/nested/special.txt", "special\n")
	write(t, repo.Dir(), "my dir/file.txt", "spaced\n")
	mustGit(t, repo, "add", "-A")
	mustGit(t, repo, append(append([]string{}, testIdentity...), "commit", "-q", "-m", "base")...)
	base, err := repo.Head(context.Background())
	if err != nil {
		t.Fatalf("read HEAD: %v", err)
	}
	requireCheckAttrSource(t, repo)
	return repo, base
}

func TestGeneratedAttributesReadsTheBaseTree(t *testing.T) {
	repo, base := attributesRepo(t)
	paths := []string{
		"dist/a.js",
		"gen/b.js",
		"locked/c.go",
		"data/d.bin",
		"src/other.go",
		"src/nested/special.txt",
		"my dir/file.txt",
	}
	answers, warning, err := repo.GeneratedAttributes(context.Background(), base, paths)
	if err != nil {
		t.Fatalf("GeneratedAttributes: %v", err)
	}
	if warning != nil {
		t.Fatalf("warning = %v, want none on a new git", warning)
	}
	want := map[string]vcs.AttributeDecision{
		"dist/a.js":              vcs.AttributeSet,     // set
		"gen/b.js":               vcs.AttributeSet,     // true
		"locked/c.go":            vcs.AttributeNegated, // -linguist-generated
		"data/d.bin":             vcs.AttributeNegated, // linguist-generated=false
		"src/other.go":           vcs.AttributeUnspecified,
		"src/nested/special.txt": vcs.AttributeSet, // nested .gitattributes
		"my dir/file.txt":        vcs.AttributeSet, // a space survives the NUL framing
	}
	for _, path := range paths {
		if answers[path] != want[path] {
			t.Errorf("answers[%q] = %v, want %v", path, answers[path], want[path])
		}
	}
}

// A PR that rewrites .gitattributes in its head changes nothing: the pass
// reads the attribute from the base revision (ADR 0003).
func TestGeneratedAttributesAnswersFromTheBaseNotTheHead(t *testing.T) {
	repo, base := attributesRepo(t)
	write(t, repo.Dir(), ".gitattributes", "src/other.go linguist-generated\n")
	mustGit(t, repo, "add", "-A")
	mustGit(t, repo, append(append([]string{}, testIdentity...), "commit", "-q", "-m", "head rewrites the marks")...)

	answers, warning, err := repo.GeneratedAttributes(context.Background(), base, []string{"dist/a.js", "src/other.go"})
	if err != nil {
		t.Fatalf("GeneratedAttributes: %v", err)
	}
	if warning != nil {
		t.Fatalf("warning = %v, want none", warning)
	}
	if answers["dist/a.js"] != vcs.AttributeSet {
		t.Errorf("dist/a.js = %v, want set: the base tree still marks it", answers["dist/a.js"])
	}
	if answers["src/other.go"] != vcs.AttributeUnspecified {
		t.Errorf("src/other.go = %v, want unspecified: the head's mark must not count", answers["src/other.go"])
	}
}

// A deletion is answered at its old path; a rename out of a marked directory
// is answered at its current path, so moving generated output into source
// makes it a review obligation again.
func TestGeneratedAttributesForADeletionAndARename(t *testing.T) {
	repo, base := attributesRepo(t)
	mustGit(t, repo, "rm", "-q", "gen/b.js")
	mustGit(t, repo, "mv", "dist/a.js", "src/a.js")
	mustGit(t, repo, append(append([]string{}, testIdentity...), "commit", "-q", "-m", "delete one, rename one")...)

	answers, warning, err := repo.GeneratedAttributes(context.Background(), base, []string{"gen/b.js", "src/a.js"})
	if err != nil {
		t.Fatalf("GeneratedAttributes: %v", err)
	}
	if warning != nil {
		t.Fatalf("warning = %v, want none", warning)
	}
	if answers["gen/b.js"] != vcs.AttributeSet {
		t.Errorf("deleted gen/b.js = %v, want set at its old path", answers["gen/b.js"])
	}
	if answers["src/a.js"] != vcs.AttributeUnspecified {
		t.Errorf("renamed src/a.js = %v, want unspecified at its new path", answers["src/a.js"])
	}
}

// attrStubRunner answers git version and check-attr from a script, so the
// version gate and the failure paths are exercised without a real git.
type attrStubRunner struct {
	versionText string
	versionCode int
	attrStdout  string
	attrCode    int
	attrStderr  string
	attrCalls   int
	stdin       []byte
	calls       []string
}

func (s *attrStubRunner) Run(_ context.Context, spec exec.Spec) exec.Result {
	s.calls = append(s.calls, strings.Join(spec.Args, " "))
	switch spec.Args[0] {
	case "version":
		return exec.Result{ExitCode: s.versionCode, Stdout: []byte(s.versionText)}
	case "cat-file":
		return exec.Result{ExitCode: 0}
	default:
		s.attrCalls++
		s.stdin = spec.Stdin
		return exec.Result{ExitCode: s.attrCode, Stdout: []byte(s.attrStdout), Stderr: []byte(s.attrStderr)}
	}
}

func stubGit(t *testing.T, s *attrStubRunner) *vcs.Repository {
	t.Helper()
	return vcs.New(s, nil).At(t.TempDir())
}

func TestGeneratedAttributesWarnsOnceBelowGit240(t *testing.T) {
	stub := &attrStubRunner{versionText: "git version 2.39.2\n", versionCode: 0}
	repo := stubGit(t, stub)
	base := mustRevision(t, "1111111111111111111111111111111111111111")
	answers, warning, err := repo.GeneratedAttributes(context.Background(), base, []string{"a.go", "b.go"})
	if err != nil {
		t.Fatalf("GeneratedAttributes: %v", err)
	}
	if warning == nil {
		t.Fatal("no warning for a git without check-attr --source")
	}
	if !strings.Contains(warning.Message, "2.39.2") {
		t.Errorf("warning does not name the version: %q", warning.Message)
	}
	for _, path := range []string{"a.go", "b.go"} {
		if answers[path] != vcs.AttributeUnspecified {
			t.Errorf("answers[%q] = %v, want unspecified (built-ins decide)", path, answers[path])
		}
	}
	if stub.attrCalls != 0 {
		t.Errorf("check-attr ran %d times on a git that lacks --source", stub.attrCalls)
	}
}

func TestGeneratedAttributesRejectsAMalformedVersion(t *testing.T) {
	stub := &attrStubRunner{versionText: "git is confused\n", versionCode: 0}
	repo := stubGit(t, stub)
	base := mustRevision(t, "1111111111111111111111111111111111111111")
	if _, _, err := repo.GeneratedAttributes(context.Background(), base, []string{"a.go"}); err == nil {
		t.Fatal("a malformed git version passed silently, want an error")
	}
	if stub.attrCalls != 0 {
		t.Errorf("check-attr ran after the version parse failed")
	}
}

func TestGeneratedAttributesFailsWhenCheckAttrFails(t *testing.T) {
	stub := &attrStubRunner{
		versionText: "git version 2.50.1\n",
		versionCode: 0,
		attrCode:    128,
		attrStderr:  "fatal: bad object 1111111111111111111111111111111111111111",
	}
	repo := stubGit(t, stub)
	base := mustRevision(t, "1111111111111111111111111111111111111111")
	if _, _, err := repo.GeneratedAttributes(context.Background(), base, []string{"a.go"}); err == nil {
		t.Fatal("a failed check-attr passed silently, want an error")
	}
}

// A real git asked for attributes at a base object that does not exist must
// fail the pass, not fall through to unspecified.
func TestGeneratedAttributesFailsOnAMissingBaseObject(t *testing.T) {
	git := testGit(t)
	repo := initRepo(t, git, filepath.Join(realTempDir(t), "clone"))
	commitFile(t, repo, "app.ts", "x\n", "init")
	requireCheckAttrSource(t, repo)
	missing := mustRevision(t, "0000000000000000000000000000000000000000")
	if _, _, err := repo.GeneratedAttributes(context.Background(), missing, []string{"app.ts"}); err == nil {
		t.Fatal("a missing base object passed silently, want an error")
	}
}

func TestGeneratedAttributesFailsOnAMissingResponseRecord(t *testing.T) {
	stub := &attrStubRunner{
		versionText: "git version 2.50.1\n",
		versionCode: 0,
		// One record for two queried paths.
		attrStdout: "a.go\x00linguist-generated\x00unspecified\x00",
		attrCode:   0,
	}
	repo := stubGit(t, stub)
	base := mustRevision(t, "1111111111111111111111111111111111111111")
	if _, _, err := repo.GeneratedAttributes(context.Background(), base, []string{"a.go", "b.go"}); err == nil {
		t.Fatal("a missing response record passed silently, want an error")
	}
}

// check-attr speaks NUL-delimited triples, and the paths on the wire are the
// ones asked about, NUL-terminated.
func TestGeneratedAttributesSpeaksNulDelimitedTriples(t *testing.T) {
	stub := &attrStubRunner{
		versionText: "git version 2.50.1\n",
		versionCode: 0,
		attrStdout: "my dir/a.go\x00linguist-generated\x00set\x00b.go\x00linguist-generated\x00false\x00",
		attrCode:   0,
	}
	repo := stubGit(t, stub)
	base := mustRevision(t, "1111111111111111111111111111111111111111")
	answers, _, err := repo.GeneratedAttributes(context.Background(), base, []string{"my dir/a.go", "b.go"})
	if err != nil {
		t.Fatalf("GeneratedAttributes: %v", err)
	}
	if answers["my dir/a.go"] != vcs.AttributeSet {
		t.Errorf("answers[my dir/a.go] = %v, want set", answers["my dir/a.go"])
	}
	if answers["b.go"] != vcs.AttributeNegated {
		t.Errorf("answers[b.go] = %v, want negated", answers["b.go"])
	}
	if got, want := string(stub.stdin), "my dir/a.go\x00b.go\x00"; got != want {
		t.Errorf("stdin = %q, want %q", got, want)
	}
}

func TestParseGitVersion(t *testing.T) {
	for _, tt := range []struct {
		text        string
		token       string
		major, minor int
		ok          bool
	}{
		{"git version 2.50.1", "2.50.1", 2, 50, true},
		{"git version 2.39.3 (Apple Git-154)", "2.39.3", 2, 39, true},
		{"git version 2.40.0.windows.1", "2.40.0", 2, 40, true},
		{"git version 1.8.3.1", "1.8.3.1", 1, 8, true},
		{"git is confused", "", 0, 0, false},
		{"", "", 0, 0, false},
	} {
		version, ok := vcs.ParseGitVersion(tt.text)
		if ok != tt.ok {
			t.Errorf("ParseGitVersion(%q) ok = %v, want %v", tt.text, ok, tt.ok)
			continue
		}
		if !ok {
			continue
		}
		if version.Token != tt.token || version.Major != tt.major || version.Minor != tt.minor {
			t.Errorf("ParseGitVersion(%q) = %q %d.%d, want %q %d.%d", tt.text, version.Token, version.Major, version.Minor, tt.token, tt.major, tt.minor)
		}
	}
	// Numeric comparison, never string order: 2.9 is below 2.40.
	nine, _ := vcs.ParseGitVersion("git version 2.9.0")
	if nine.AtLeast(2, 40) {
		t.Error("2.9 passed AtLeast(2, 40) — string-order comparison crept in")
	}
	forty, _ := vcs.ParseGitVersion("git version 2.40.0")
	if !forty.AtLeast(2, 40) {
		t.Error("2.40.0 failed AtLeast(2, 40)")
	}
}

func mustRevision(t *testing.T, sha string) core.Revision {
	t.Helper()
	revision, err := core.NewRevision(sha)
	if err != nil {
		t.Fatalf("revision %q: %v", sha, err)
	}
	return revision
}
