package config_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/config"
	"github.com/carlosboeing/crossrev/internal/core"
)

func resolveBacklog(t *testing.T, tree files, repoYAML string, base core.Revision) string {
	t.Helper()
	if repoYAML != "" {
		if tree[""] == nil {
			tree[""] = map[string]string{}
		}
		tree[""][".github/crossrev.yml"] = repoYAML
	}
	loaded := mustLoad(t, core.Revision{}, tree)
	resolved, err := loaded.ResolveBacklog(context.Background(), base, loaded.Get(".backlog.destination"))
	if err != nil {
		t.Fatalf("ResolveBacklog: %v", err)
	}
	return resolved.String()
}

// Nothing declared and nothing installed resolves to none. `auto` falls to none
// rather than creating a location nobody asked for (lib/config.sh:501).
func TestAutoFallsToNone(t *testing.T) {
	if got := resolveBacklog(t, files{"": {}}, "", core.Revision{}); got != "none" {
		t.Errorf("resolved to %q, want %q", got, "none")
	}
}

// An empty destination is `auto`, not `none`. cfg_resolve_backlog opens with
// `want="${2:-auto}"` at lib/config.sh:414, and every caller passes
// `cfg_get '.backlog.destination'`, which renders an absent, null or false key
// as the empty string — so the `""` arm of the Bash `case` is unreachable and
// the empty string means auto. Collapsing the two drops deferred work instead
// of writing it.
func TestAnEmptyDestinationResolvesTheWayAutoDoes(t *testing.T) {
	for name, document := range map[string]string{
		"a null destination":  "version: 1\nbacklog:\n  destination: null\n",
		"a false destination": "version: 1\nbacklog:\n  destination: false\n",
	} {
		t.Run(name, func(t *testing.T) {
			tree := files{"": {"BACKLOG.md": ""}}
			if got := resolveBacklog(t, tree, document, core.Revision{}); got != "repository file BACKLOG.md" {
				t.Errorf("resolved to %q, want the sniff auto would have run", got)
			}
		})
	}

	// The resolver called with the empty string directly, which is the shape
	// lib/run.sh:310, lib/init.sh:115 and bin/crossrev:159 all call it in.
	loaded := mustLoad(t, core.Revision{}, files{"": {"BACKLOG.md": ""}})
	resolved, err := loaded.ResolveBacklog(context.Background(), core.Revision{}, "")
	if err != nil {
		t.Fatalf("ResolveBacklog: %v", err)
	}
	if got := resolved.String(); got != "repository file BACKLOG.md" {
		t.Errorf("an empty destination resolved to %q", got)
	}

	// The arm that must not move with it: an explicit none is still none.
	tree := files{"": {"BACKLOG.md": ""}}
	if got := resolveBacklog(t, tree, "version: 1\nbacklog:\n  destination: none\n", core.Revision{}); got != "none" {
		t.Errorf("an explicit none resolved to %q", got)
	}
}

// Backlog discovery is read behind `[[ -e ]]` at lib/config.sh:479, so a
// directory at backlog/config.yml counts as the convention being installed.
// Configuration is read behind `[[ -f ]]` and does not.
func TestTheSniffCountsAPathThatIsNotAFile(t *testing.T) {
	tree := files{"": {"backlog/config.yml": isDirectory}}
	if got := resolveBacklog(t, tree, "", core.Revision{}); got != "repository folder backlog/tasks" {
		t.Errorf("resolved to %q, want a directory to count", got)
	}
}

// A read that fails is reported, and names the path it was reading. Nothing in
// the Bash can fail this way — `git show` failing means the path is not at that
// revision — but a native implementation reads the filesystem and can.
func TestASniffReadFailureNamesThePath(t *testing.T) {
	tree := files{"": {
		".github/crossrev.yml": "version: 1\n",
		"backlog.config.yml":   readFails,
	}}
	_, err := mustLoad(t, core.Revision{}, tree).ResolveBacklog(context.Background(), core.Revision{}, "auto")
	if err == nil {
		t.Fatal("expected the read failure to be reported")
	}
	if !strings.Contains(err.Error(), "backlog.config.yml") {
		t.Errorf("error = %q, want it to name the path", err)
	}
}

// Tier 2, the sniff, in the order lib/config.sh:482-494 probes it.
func TestTheSniffKeepsItsOrder(t *testing.T) {
	tests := []struct {
		name    string
		present []string
		want    string
	}{
		{"a Backlog config at the root", []string{"backlog.config.yml"}, "repository folder backlog/tasks"},
		{"a Backlog config in a folder", []string{"backlog/config.yml"}, "repository folder backlog/tasks"},
		{"a Backlog config in a dot folder", []string{".backlog/config.yml"}, "repository folder backlog/tasks"},
		{"a root BACKLOG.md", []string{"BACKLOG.md"}, "repository file BACKLOG.md"},
		{"a root TODO.md", []string{"TODO.md"}, "repository file TODO.md"},
		{"BACKLOG.md before TODO.md", []string{"TODO.md", "BACKLOG.md"}, "repository file BACKLOG.md"},
		{"a folder convention before a file one", []string{"BACKLOG.md", "backlog.config.yml"}, "repository folder backlog/tasks"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tree := files{"": {}}
			for _, path := range test.present {
				tree[""][path] = ""
			}
			if got := resolveBacklog(t, tree, "", core.Revision{}); got != test.want {
				t.Errorf("resolved to %q, want %q", got, test.want)
			}
		})
	}
}

// A configured path wins outright (lib/config.sh:428-431).
func TestAConfiguredPathWins(t *testing.T) {
	tree := files{"": {"BACKLOG.md": ""}}
	repo := "version: 1\nbacklog:\n  destination: repository\n  repository:\n    path: docs/deferred\n"
	if got := resolveBacklog(t, tree, repo, core.Revision{}); got != "repository folder docs/deferred" {
		t.Errorf("resolved to %q", got)
	}
}

// A repository key is explicit only when it appears in the repository config,
// not when it arrives through the merged defaults (lib/config.sh:432). With
// neither key stated the sniff decides both.
func TestAnExplicitRepositoryDestinationSniffsBothLayouts(t *testing.T) {
	tree := files{"": {"BACKLOG.md": ""}}
	repo := "version: 1\nbacklog:\n  destination: repository\n"
	if got := resolveBacklog(t, tree, repo, core.Revision{}); got != "repository file BACKLOG.md" {
		t.Errorf("resolved to %q, want the sniffed file convention", got)
	}
}

// A stated layout constrains the candidates rather than reinterpreting a path
// that belongs to another one, and tier 3 then fires because a repository
// destination was explicitly asked for (lib/config.sh:495-500).
func TestAStatedLayoutConstrainsTheSniff(t *testing.T) {
	folder := files{"": {"BACKLOG.md": ""}}
	repoFolder := "version: 1\nbacklog:\n  destination: repository\n  repository:\n    layout: folder\n"
	if got := resolveBacklog(t, folder, repoFolder, core.Revision{}); got != "repository folder .crossrev/backlog" {
		t.Errorf("an explicit folder layout resolved to %q", got)
	}

	file := files{"": {"backlog/config.yml": ""}}
	repoFile := "version: 1\nbacklog:\n  destination: repository\n  repository:\n    layout: file\n"
	if got := resolveBacklog(t, file, repoFile, core.Revision{}); got != "repository file .crossrev/backlog.md" {
		t.Errorf("an explicit file layout resolved to %q", got)
	}
}

// github_issues and none are answered without probing anything.
func TestTheTwoLiteralDestinations(t *testing.T) {
	tree := files{"": {"BACKLOG.md": ""}}
	if got := resolveBacklog(t, tree, "version: 1\nbacklog:\n  destination: github_issues\n", core.Revision{}); got != "github_issues" {
		t.Errorf("resolved to %q", got)
	}
	tree = files{"": {"BACKLOG.md": ""}}
	if got := resolveBacklog(t, tree, "version: 1\nbacklog:\n  destination: none\n", core.Revision{}); got != "none" {
		t.Errorf("resolved to %q", got)
	}
}

// The sniff reads the base revision when there is one, like every other policy
// read (lib/config.sh:476-480).
func TestTheSniffReadsTheBaseRevision(t *testing.T) {
	base := revision(t, baseSHA)
	tree := files{
		"":      {"TODO.md": ""},
		baseSHA: {"BACKLOG.md": ""},
	}
	if got := resolveBacklog(t, tree, "", base); got != "repository file BACKLOG.md" {
		t.Errorf("resolved to %q, want the base revision's convention", got)
	}
}

// The two guards stay reachable for a caller passing a literal the resolver
// does not know, even though a configured value is refused at load
// (lib/config.sh:408-412).
func TestTheResolverStillGuardsALiteralItDoesNotKnow(t *testing.T) {
	loaded := mustLoad(t, core.Revision{}, files{"": {}})
	if _, err := loaded.ResolveBacklog(context.Background(), core.Revision{}, "elsewhere"); err == nil {
		t.Fatal("expected a refusal for an unknown destination")
	} else if refusal, ok := err.(*config.Refusal); !ok {
		t.Fatalf("expected a *config.Refusal, got %T", err)
	} else if refusal.Message != "backlog.destination is 'elsewhere', which CrossRev does not recognise" {
		t.Errorf("message = %q", refusal.Message)
	}
}

// --- the backlog path guard -------------------------------------------------

func guardRefusal(t *testing.T, root, path string) *config.Refusal {
	t.Helper()
	err := config.AssertPathInsideRepo(root, path)
	if err == nil {
		t.Fatalf("AssertPathInsideRepo(%q, %q) allowed the path", root, path)
	}
	refusal, ok := err.(*config.Refusal)
	if !ok {
		t.Fatalf("expected a *config.Refusal, got %T: %v", err, err)
	}
	return refusal
}

// The cases lib/config.sh:516-541 already decides, which the divergence must
// leave exactly as they are.
func TestThePathGuardKeepsItsLexicalDecisions(t *testing.T) {
	root := realTempDir(t)

	for _, allowed := range []string{"backlog/tasks", "sub/../sibling", ".", "", "docs/BACKLOG.md"} {
		if err := config.AssertPathInsideRepo(root, allowed); err != nil {
			t.Errorf("AssertPathInsideRepo(%q) refused: %v", allowed, err)
		}
	}

	if got := guardRefusal(t, root, "/etc").Message; got != "the backlog path '/etc' is absolute" {
		t.Errorf("an absolute path: %q", got)
	}
	// Traversal that lands somewhere real above the checkout resolves, so it
	// is refused by the arm that can name where it landed. Only a path that
	// pops past `/` leaves the resolution empty and reaches the other arm
	// (lib/config.sh:534-540).
	if got := guardRefusal(t, root, "../../etc").Message; got != "the backlog path '../../etc' resolves outside the repository" {
		t.Errorf("traversal above the checkout: %q", got)
	}
	if got := guardRefusal(t, root, "sub/../../outside").Message; got != "the backlog path 'sub/../../outside' resolves outside the repository" {
		t.Errorf("traversal that re-enters and then leaves: %q", got)
	}
	climbs := strings.Repeat("../", 40) + "etc"
	if got := guardRefusal(t, root, climbs).Message; got != "the backlog path '"+climbs+"' escapes the repository" {
		t.Errorf("traversal past the filesystem root: %q", got)
	}
	if got := guardRefusal(t, "", "backlog/tasks").Message; got != "not inside a git repository" {
		t.Errorf("no repository root: %q", got)
	}
	// The separator in the containment test is load-bearing: `<root>-evil`
	// prefixes `<root>` and is not inside it.
	sibling := "../" + filepath.Base(root) + "-evil"
	if got := guardRefusal(t, root, sibling).Message; got != "the backlog path '"+sibling+"' resolves outside the repository" {
		t.Errorf("a sibling sharing the root's prefix: %q", got)
	}
}

// AssertPathInsideRepo is exported and has no caller inside this package, so
// its precondition on root is stated rather than assumed. The Bash is always
// fed `git rev-parse --show-toplevel` (lib/config.sh:509).
func TestThePathGuardStatesItsPreconditionOnTheRoot(t *testing.T) {
	root := t.TempDir()
	if err := config.AssertPathInsideRepo(root+"/", "backlog/tasks"); err != nil {
		t.Errorf("a trailing slash on the root refused a path inside it: %v", err)
	}
	if got := guardRefusal(t, "relative/root", "backlog/tasks").Message; got != "the repository root 'relative/root' is not an absolute path" {
		t.Errorf("a relative root: %q", got)
	}
}

// realTempDir is t.TempDir() with every symlink component resolved away.
//
// The symlink cases below need a root that is already physical. On macOS
// t.TempDir() sits under /var, which is a symlink to /private/var, so the root
// resolves while a candidate under it does not — everything then looks outside
// the checkout and the guard refuses for the wrong reason, which makes an
// escape test pass without ever reproducing the escape.
func realTempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve the temporary directory: %v", err)
	}
	if again, err := filepath.EvalSymlinks(dir); err != nil || again != dir {
		t.Fatalf("%s is not a physically real path (%q, %v)", dir, again, err)
	}
	return dir
}

// twoDirs builds a physically real base holding a repository and a directory
// beside it, which is where the escape would land.
func twoDirs(t *testing.T) (root, outside string) {
	t.Helper()
	base := realTempDir(t)
	root, outside = filepath.Join(base, "repo"), filepath.Join(base, "outside")
	for _, dir := range []string{root, outside} {
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatalf("create %s: %v", dir, err)
		}
	}
	return root, outside
}

// The same escape as issue 128 arriving through a link whose target does not
// exist yet, which is the ordinary case: the backlog file is created by the
// write this guard is protecting. filepath.EvalSymlinks fails on a dangling
// link and os.Lstat succeeds on one, so a resolver that fell back to the
// unresolved path compared where the link is spelled rather than where the
// write lands, and the write then landed outside the checkout.
func TestThePathGuardRefusesADanglingSymlinkOutOfTheRepository(t *testing.T) {
	root, outside := twoDirs(t)
	target := filepath.Join(outside, "BACKLOG.md")
	if err := os.Symlink(target, filepath.Join(root, "notes.md")); err != nil {
		t.Fatalf("create the symlink: %v", err)
	}

	if got := guardRefusal(t, root, "notes.md").Message; got != "the backlog path 'notes.md' resolves outside the repository" {
		t.Errorf("a dangling symlink out of the repository: %q", got)
	}
	if _, err := os.Lstat(target); !os.IsNotExist(err) {
		t.Errorf("%s exists, so the guard let a write escape: %v", target, err)
	}
}

// The same defect in the other direction. Under a root reached through a
// symlink — which is every t.TempDir() on macOS — the root resolved and a
// dangling link under it did not, so a link pointing inside the repository was
// refused as an escape.
func TestThePathGuardAllowsADanglingSymlinkInsideTheRepository(t *testing.T) {
	root := t.TempDir()
	if err := os.Symlink(filepath.Join(root, "real"), filepath.Join(root, "link")); err != nil {
		t.Fatalf("create the symlink: %v", err)
	}
	for _, allowed := range []string{"link", "link/tasks"} {
		if err := config.AssertPathInsideRepo(root, allowed); err != nil {
			t.Errorf("AssertPathInsideRepo(%q) refused a link inside the checkout: %v", allowed, err)
		}
	}
}

// A chain of links, a relative target, and a loop.
func TestThePathGuardFollowsAChainARelativeTargetAndALoop(t *testing.T) {
	root, outside := twoDirs(t)
	links := map[string]string{
		// A chain of dangling links ending outside the checkout.
		"chain": filepath.Join(root, "hop"),
		"hop":   filepath.Join(outside, "BACKLOG.md"),
		// Relative targets, read against the link's own directory.
		"up":   "../outside/BACKLOG.md",
		"here": "./BACKLOG.md",
		// A loop, which resolves to nothing on the filesystem either.
		"ping": filepath.Join(root, "pong"),
		"pong": filepath.Join(root, "ping"),
	}
	for name, target := range links {
		if err := os.Symlink(target, filepath.Join(root, name)); err != nil {
			t.Fatalf("create the %s symlink: %v", name, err)
		}
	}

	if got := guardRefusal(t, root, "chain").Message; got != "the backlog path 'chain' resolves outside the repository" {
		t.Errorf("a chain of links leaving the checkout: %q", got)
	}
	if got := guardRefusal(t, root, "up").Message; got != "the backlog path 'up' resolves outside the repository" {
		t.Errorf("a relative target leaving the checkout: %q", got)
	}
	if err := config.AssertPathInsideRepo(root, "here"); err != nil {
		t.Errorf("a relative target staying inside the checkout was refused: %v", err)
	}
	if got := guardRefusal(t, root, "ping").Message; got != "the backlog path 'ping' cannot be resolved" {
		t.Errorf("a loop of links: %q", got)
	}
}

// Issue 128, and the first of the two deliberate divergences approved for this
// port: a symlink inside the checkout pointing outside it used to pass, because
// the Bash guard resolves lexically and asks the filesystem nothing. The value
// ends in a file write, so the write would land outside the repository.
func TestThePathGuardRefusesASymlinkOutOfTheRepository(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "notes")); err != nil {
		t.Fatalf("create the symlink: %v", err)
	}

	refusal := guardRefusal(t, root, "notes")
	if refusal.Message != "the backlog path 'notes' resolves outside the repository" {
		t.Errorf("message = %q", refusal.Message)
	}
	if !strings.Contains(refusal.Hint, "must stay inside the checkout") {
		t.Errorf("hint = %q", refusal.Hint)
	}

	// The same link one level down, where the escape is the ancestor rather
	// than the path itself and nothing below it exists yet.
	if got := guardRefusal(t, root, "notes/deferred").Message; got != "the backlog path 'notes/deferred' resolves outside the repository" {
		t.Errorf("through a symlinked ancestor: %q", got)
	}
}

// A symlink that stays inside the checkout is still allowed. The guard bounds
// where a write lands, not how the path is spelled.
func TestThePathGuardAllowsASymlinkInsideTheRepository(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "real"), 0o755); err != nil {
		t.Fatalf("create the target: %v", err)
	}
	if err := os.Symlink(filepath.Join(root, "real"), filepath.Join(root, "linked")); err != nil {
		t.Fatalf("create the symlink: %v", err)
	}
	for _, allowed := range []string{"linked", "linked/tasks"} {
		if err := config.AssertPathInsideRepo(root, allowed); err != nil {
			t.Errorf("AssertPathInsideRepo(%q) refused: %v", allowed, err)
		}
	}
}

// The common case is a path nothing has created yet, and it must not be refused
// merely for not existing.
func TestThePathGuardAllowsAPathThatDoesNotExistYet(t *testing.T) {
	root := t.TempDir()
	if err := config.AssertPathInsideRepo(root, "deep/not/created/yet"); err != nil {
		t.Errorf("AssertPathInsideRepo refused a path that does not exist: %v", err)
	}
}
