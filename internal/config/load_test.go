package config_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/config"
	"github.com/carlosboeing/crossrev/internal/core"
)

const otherSHA = "2c4a46cb321db01826d116b5ef2add6b0284d68c"

func revision(t *testing.T, sha string) core.Revision {
	t.Helper()
	built, err := core.NewRevision(sha)
	if err != nil {
		t.Fatalf("build a revision: %v", err)
	}
	return built
}

func mustLoad(t *testing.T, base core.Revision, tree files) *config.Config {
	t.Helper()
	loaded, err := config.Load(context.Background(), base, tree.show())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return loaded
}

func refusalFrom(t *testing.T, base core.Revision, tree files) *config.Refusal {
	t.Helper()
	_, err := config.Load(context.Background(), base, tree.show())
	if err == nil {
		t.Fatal("expected a refusal, got none")
	}
	refusal, ok := err.(*config.Refusal)
	if !ok {
		t.Fatalf("expected a *config.Refusal, got %T: %v", err, err)
	}
	return refusal
}

// A config absent at the base revision resolves to the defaults, exit 0,
// nothing printed. Most repositories have no config at their base revision at
// all (lib/config.sh:153-154).
func TestBaseRevisionAbsentConfigIsSilent(t *testing.T) {
	base := revision(t, baseSHA)
	loaded := mustLoad(t, base, files{"": {}, baseSHA: {}})

	if got := loaded.Get(".policy.max_passes_per_cycle"); got != "3" {
		t.Errorf("max_passes_per_cycle = %q, want the default %q", got, "3")
	}
	if got := loaded.Get(".policy.min_fix_severity"); got != "medium" {
		t.Errorf("min_fix_severity = %q, want the default %q", got, "medium")
	}
	if loaded.Repo.Len() != 0 {
		t.Errorf("the repository layer holds %d keys, want none", loaded.Repo.Len())
	}
}

// A config present and empty at the base revision resolves the same way. git
// show returns exit 0 and no bytes for it, and a file that exists and holds
// nothing states no policy (lib/config.sh:67-68).
func TestBaseRevisionEmptyConfigIsSilent(t *testing.T) {
	base := revision(t, baseSHA)
	for _, name := range []string{".github/crossrev.yml", ".crossrev.yml"} {
		t.Run(name, func(t *testing.T) {
			loaded := mustLoad(t, base, files{"": {}, baseSHA: {name: ""}})
			if got := loaded.Get(".policy.max_passes_per_cycle"); got != "3" {
				t.Errorf("max_passes_per_cycle = %q, want the default %q", got, "3")
			}
			if got := loaded.Get(".policy.min_fix_severity"); got != "medium" {
				t.Errorf("min_fix_severity = %q, want the default %q", got, "medium")
			}
		})
	}
}

// A config that will not parse at the base revision stops the run with one
// refusal naming both the file and the revision, and a hint that reads the
// revision rather than the working tree (lib/config.sh:75-85).
//
// Before issue 140 this fell back to `{}`, so every value the repository stated
// reverted to a default with exit 0 and nothing printed.
func TestBaseRevisionMalformedConfigRefusesByNameAndRevision(t *testing.T) {
	base := revision(t, baseSHA)
	broken := "version: 1\npolicy:\n  - this is not\n  a mapping: [unclosed\n"
	refusal := refusalFrom(t, base, files{"": {}, baseSHA: {".github/crossrev.yml": broken}})

	if want := "could not parse .github/crossrev.yml at base revision " + baseSHA; refusal.Message != want {
		t.Errorf("message = %q, want %q", refusal.Message, want)
	}
	if want := "It must be valid YAML. Check it with: git show " + baseSHA + ":.github/crossrev.yml | yq '.'"; refusal.Hint != want {
		t.Errorf("hint = %q, want %q", refusal.Hint, want)
	}
	// The hint must read the revision, not the working tree: the file on disk
	// may parse, may differ, or may not be there at all.
	if strings.Contains(refusal.Hint, "yq '.' .github") {
		t.Errorf("the hint checks the working tree: %q", refusal.Hint)
	}
}

// A working-tree config that will not parse is refused with the working-tree
// hint instead (lib/config.sh:43-46).
func TestWorkingTreeMalformedConfigRefusesWithTheWorkingTreeHint(t *testing.T) {
	broken := "version: 1\npolicy:\n  - this is not\n  a mapping: [unclosed\n"
	refusal := refusalFrom(t, core.Revision{}, files{"": {".github/crossrev.yml": broken}})

	if want := "could not parse .github/crossrev.yml"; refusal.Message != want {
		t.Errorf("message = %q, want %q", refusal.Message, want)
	}
	if want := "It must be valid YAML. Check it with: yq '.' .github/crossrev.yml"; refusal.Hint != want {
		t.Errorf("hint = %q, want %q", refusal.Hint, want)
	}
}

// `.github/crossrev.yml` beats `.crossrev.yml` (lib/config.sh:147-160).
func TestGithubConfigBeatsDotCrossrev(t *testing.T) {
	tree := files{"": {
		".github/crossrev.yml": "version: 1\npolicy:\n  max_passes_per_cycle: 5\n",
		".crossrev.yml":        "version: 1\npolicy:\n  max_passes_per_cycle: 9\n",
	}}
	if got := mustLoad(t, core.Revision{}, tree).Get(".policy.max_passes_per_cycle"); got != "5" {
		t.Errorf("max_passes_per_cycle = %q, want %q", got, "5")
	}

	base := revision(t, baseSHA)
	atBase := files{"": {}, baseSHA: {
		".github/crossrev.yml": "version: 1\npolicy:\n  max_passes_per_cycle: 5\n",
		".crossrev.yml":        "version: 1\npolicy:\n  max_passes_per_cycle: 9\n",
	}}
	if got := mustLoad(t, base, atBase).Get(".policy.max_passes_per_cycle"); got != "5" {
		t.Errorf("at the base revision max_passes_per_cycle = %q, want %q", got, "5")
	}
}

// A broken `.github/crossrev.yml` is named rather than skipped in favour of the
// other file. Falling through would run the repository under a policy it did
// not state.
func TestBrokenGithubConfigIsNamedRatherThanSkipped(t *testing.T) {
	broken := "version: 1\npolicy:\n  - this is not\n  a mapping: [unclosed\n"
	tree := files{"": {
		".github/crossrev.yml": broken,
		".crossrev.yml":        "version: 1\npolicy:\n  max_passes_per_cycle: 9\n",
	}}
	if got := refusalFrom(t, core.Revision{}, tree).Message; got != "could not parse .github/crossrev.yml" {
		t.Errorf("message = %q, want the .github file named", got)
	}
}

// The operator file is refused by its own path, because it is the layer a
// review, resolve or cycle run reads whether or not there is a base revision
// (lib/config.sh:166).
func TestOperatorConfigIsRefusedByItsOwnPath(t *testing.T) {
	// The path is pinned rather than substituted from OperatorPath on both
	// sides of the assertion, which would hold however wrong OperatorPath was.
	t.Setenv("XDG_CONFIG_HOME", "/xdg")
	const operatorPath = "/xdg/crossrev/config.yml"
	if got := config.OperatorPath(); got != operatorPath {
		t.Fatalf("OperatorPath() = %q, want %q", got, operatorPath)
	}
	broken := "version: 1\npolicy:\n  - this is not\n  a mapping: [unclosed\n"
	base := revision(t, baseSHA)
	refusal := refusalFrom(t, base, files{"": {operatorPath: broken}, baseSHA: {}})

	if want := "could not parse " + operatorPath; refusal.Message != want {
		t.Errorf("message = %q, want %q", refusal.Message, want)
	}
}

// The operator file's version is checked before the merge, like the repository
// layer's (lib/config.sh:169).
func TestOperatorVersionMismatchIsRefusedByPath(t *testing.T) {
	operatorPath := config.OperatorPath()
	refusal := refusalFrom(t, core.Revision{}, files{"": {operatorPath: "version: 99\n"}})
	if want := operatorPath + " declares version 99, and this crossrev understands version 1"; refusal.Message != want {
		t.Errorf("message = %q, want %q", refusal.Message, want)
	}
}

func TestOperatorPathFollowsXDGConfigHome(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/xdg")
	if got, want := config.OperatorPath(), filepath.Join("/xdg", "crossrev", "config.yml"); got != want {
		t.Errorf("OperatorPath() = %q, want %q", got, want)
	}
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "/home/someone")
	if got, want := config.OperatorPath(), filepath.Join("/home/someone", ".config", "crossrev", "config.yml"); got != want {
		t.Errorf("OperatorPath() = %q, want %q", got, want)
	}
}

// Policy comes from the base revision, never the pull request head (ADR 0003,
// lib/config.sh:50-55). A head that raised the pass bound must not be read.
func TestPolicyIsReadFromTheBaseRevisionNotTheWorkingTree(t *testing.T) {
	base := revision(t, baseSHA)
	tree := files{
		"":      {".github/crossrev.yml": "version: 1\npolicy:\n  max_passes_per_cycle: 99\n"},
		baseSHA: {".github/crossrev.yml": "version: 1\npolicy:\n  max_passes_per_cycle: 7\n"},
	}
	if got := mustLoad(t, base, tree).Get(".policy.max_passes_per_cycle"); got != "7" {
		t.Errorf("max_passes_per_cycle = %q, want the base revision's %q", got, "7")
	}
}

// Only the revision the caller names is read. Nothing falls back to another one.
func TestOnlyTheNamedRevisionIsRead(t *testing.T) {
	base := revision(t, baseSHA)
	tree := files{
		"":       {},
		otherSHA: {".github/crossrev.yml": "version: 1\npolicy:\n  max_passes_per_cycle: 7\n"},
		baseSHA:  {},
	}
	if got := mustLoad(t, base, tree).Get(".policy.max_passes_per_cycle"); got != "3" {
		t.Errorf("max_passes_per_cycle = %q, want the default %q", got, "3")
	}
}

// A directory at a config path states no policy, because the Bash read is
// behind `[[ -f ]]` (lib/config.sh:37).
func TestADirectoryAtAConfigPathStatesNoPolicy(t *testing.T) {
	show := func(_ context.Context, _ core.Revision, path string) ([]byte, config.FileStatus, error) {
		if path == ".github/crossrev.yml" {
			return nil, config.IsOther, nil
		}
		return nil, config.NotFound, nil
	}
	loaded, err := config.Load(context.Background(), core.Revision{}, show)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := loaded.Get(".policy.max_passes_per_cycle"); got != "3" {
		t.Errorf("max_passes_per_cycle = %q, want the default %q", got, "3")
	}
}

// A document that parses and holds a sequence, a string, a number or a boolean
// is a malformed config rather than an empty one, and is refused by name.
//
// The vectors replay this through `.github/crossrev.yml` on both paths. Two
// files they do not replay take the same working-tree form under their own
// path: the fallback `.crossrev.yml` and the operator file, which is always
// read from the working tree however the run was invoked (lib/config.sh:204,
// 213-214). Their expected text is derived from the frozen vector rather than
// written out again.
func TestANonMappingIsRefusedByTheNameOfTheFileThatHeldIt(t *testing.T) {
	vector := refusalVectorNamed(t, "non-mapping-sequence")
	operatorPath := config.OperatorPath()

	for _, test := range []struct {
		name string
		path string
		tree files
	}{
		{"the fallback file", ".crossrev.yml", files{"": {".crossrev.yml": vector.Config}}},
		{"the operator file", operatorPath, files{"": {operatorPath: vector.Config}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			want := strings.ReplaceAll(vector.Error, ".github/crossrev.yml", test.path)
			if got := refusalFrom(t, core.Revision{}, test.tree).Rendered(); got != want {
				t.Errorf("refusal text differs\n want: %q\n  got: %q", want, got)
			}
		})
	}

	// Every non-mapping shape reaches it, and the shape refusal comes before
	// the version check, which would otherwise index a document it cannot.
	for name, document := range map[string]string{
		"a sequence":                   "- a\n- b\n",
		"a string":                     "hello\n",
		"a number":                     "42\n",
		"a boolean":                    "true\n",
		"a versioned-looking sequence": "- version: 99\n",
	} {
		t.Run(name, func(t *testing.T) {
			tree := files{"": {".github/crossrev.yml": document}}
			if got := refusalFrom(t, core.Revision{}, tree).Message; got != ".github/crossrev.yml is not a mapping" {
				t.Errorf("message = %q", got)
			}
		})
	}
}

// Skipped and parsed-as-empty are different answers, and only a case where the
// two differ separates them: a directory at `.github/crossrev.yml` states no
// policy and the search moves on, so the valid `.crossrev.yml` beside it is
// what gets read (lib/config.sh:147-160).
func TestADirectoryAtTheFirstConfigPathFallsThroughToTheSecond(t *testing.T) {
	tree := files{"": {
		".github/crossrev.yml": isDirectory,
		".crossrev.yml":        "version: 1\npolicy:\n  max_passes_per_cycle: 9\n",
	}}
	if got := mustLoad(t, core.Revision{}, tree).Get(".policy.max_passes_per_cycle"); got != "9" {
		t.Errorf("max_passes_per_cycle = %q, want the second file's %q", got, "9")
	}
}

// A read that fails is reported rather than read as an absent file, and names
// the path it was reading. Nothing in the Bash can fail this way — `git show`
// failing means the path is not at that revision — but a native implementation
// reads the filesystem and can.
func TestAReadFailureNamesThePath(t *testing.T) {
	operatorPath := config.OperatorPath()
	for name, tree := range map[string]files{
		"the repository layer": {"": {".github/crossrev.yml": readFails}},
		"the fallback file":    {"": {".crossrev.yml": readFails}},
		"the operator layer":   {"": {operatorPath: readFails}},
	} {
		want := ".github/crossrev.yml"
		if name == "the fallback file" {
			want = ".crossrev.yml"
		} else if name == "the operator layer" {
			want = operatorPath
		}
		t.Run(name, func(t *testing.T) {
			_, err := config.Load(context.Background(), core.Revision{}, tree.show())
			if err == nil {
				t.Fatal("expected the read failure to be reported")
			}
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error = %q, want it to name %q", err, want)
			}
		})
	}
}

// A document resolving to null states no policy, which is exactly what an
// absent file states: the defaults, exit 0, nothing printed. This is the half
// of the shape question that is not a refusal, and the two halves have to stay
// apart — a null document is the one an empty file produces, and refusing it
// would refuse a file most repositories could legitimately hold.
//
// The empty file is worth its own case on both paths. The Bash reaches it two
// different ways: the working tree tests `[[ -f ]]`, which an existing empty
// file passes, so yq runs and answers null; the base revision tests the text,
// which is empty, and short-circuits before yq. The two paths used to disagree
// on that one file and now agree (lib/config.sh:36-37, 187-193).
func TestANullDocumentStatesNoPolicy(t *testing.T) {
	documents := map[string]string{
		"an existing empty file": "",
		"comment only":           "# just a comment\n",
		"whitespace only":        "   \n\n",
		"explicit null":          "null\n",
	}
	for name, document := range documents {
		t.Run(name, func(t *testing.T) {
			loaded := mustLoad(t, core.Revision{}, files{"": {".github/crossrev.yml": document}})
			if got := loaded.Get(".policy.max_passes_per_cycle"); got != "3" {
				t.Errorf("max_passes_per_cycle = %q, want the default %q", got, "3")
			}
		})
		t.Run(name+" at the base revision", func(t *testing.T) {
			base := revision(t, baseSHA)
			loaded := mustLoad(t, base, files{"": {}, baseSHA: {".github/crossrev.yml": document}})
			if got := loaded.Get(".policy.max_passes_per_cycle"); got != "3" {
				t.Errorf("max_passes_per_cycle = %q, want the default %q", got, "3")
			}
		})
	}
}

// Both layers are kept separately as well as merged, because `init` needs to
// know what the repository itself declared versus what it inherited
// (lib/config.sh:12-19).
func TestBothLayersAreKeptSeparately(t *testing.T) {
	operatorPath := config.OperatorPath()
	tree := files{"": {
		".github/crossrev.yml": "version: 1\nmode: automated\n",
		operatorPath:           "version: 1\nendpoints:\n  mine:\n    base_url: http://local/\n    token_env: TOKEN\n",
	}}
	loaded := mustLoad(t, core.Revision{}, tree)

	if got := loaded.Repo.Value("mode"); got != "automated" {
		t.Errorf("the repository layer holds mode = %v, want %q", got, "automated")
	}
	if loaded.Repo.Has("policy") {
		t.Error("the repository layer holds a policy key it never declared")
	}
	if loaded.Operator.Object("endpoints").Object("mine") == nil {
		t.Error("the operator layer lost its endpoint")
	}
}

// A tree at the configuration path is two different answers, because the Bash
// asks two different questions. `[[ -f ]]` in the working tree says no file, so
// the next path is tried; `git show <sha>:<path>` at a revision succeeds and
// prints the tree's listing, which yq reads as one multi-line string and the
// shape test then refuses. Reading both as absent loaded `.crossrev.yml` at
// exit 0 where the Bash exits 1.
func TestATreeAtTheConfigPathIsRefusedAtTheBaseRevision(t *testing.T) {
	base := revision(t, baseSHA)
	tree := files{"": {}, baseSHA: {
		".github/crossrev.yml": isDirectory,
		".crossrev.yml":        "version: 1\nmode: automated\n",
	}}
	refusal := refusalFrom(t, base, tree)
	want := ".github/crossrev.yml is not a mapping at base revision " + baseSHA
	if refusal.Message != want {
		t.Errorf("message = %q, want %q", refusal.Message, want)
	}
	// And the working tree still skips it, because there the question is
	// `[[ -f ]]`.
	working := files{"": {
		".github/crossrev.yml": isDirectory,
		".crossrev.yml":        "version: 1\nmode: automated\n",
	}}
	if got := mustLoad(t, core.Revision{}, working).Get(".mode"); got != "automated" {
		t.Errorf("mode = %q, want the fallback file read", got)
	}
}
