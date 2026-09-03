package config

// ---------------------------------------------------------------------------
// Where deferred work goes
// ---------------------------------------------------------------------------
//
// Inventing a folder in someone else's repository is the wrong default, so
// CrossRev does not. Three tiers, first hit wins, and the last one only fires
// when a repository backlog was explicitly asked for (lib/config.sh:364-370).

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/carlosboeing/crossrev/internal/core"
)

// Destination is where deferred work goes.
type Destination string

// The four destinations `backlog.destination` accepts, plus the resolved
// values. `auto` is a request to discover one, never a destination itself.
const (
	DestinationNone         Destination = "none"
	DestinationGitHubIssues Destination = "github_issues"
	DestinationRepository   Destination = "repository"
	DestinationAuto         Destination = "auto"
)

// Layout is how a repository backlog is stored.
type Layout string

// The two layouts, plus the "any" the sniff uses when the repository config
// stated none (lib/config.sh:483).
const (
	LayoutFolder Layout = "folder"
	LayoutFile   Layout = "file"
	layoutAny    Layout = "any"
)

// Backlog is a resolved destination.
type Backlog struct {
	Destination Destination
	// Layout and Path are set only for DestinationRepository.
	Layout Layout
	Path   string
}

// String is the one line cfg_resolve_backlog prints at lib/config.sh:406:
// "github_issues", "repository <layout> <path>", or "none".
func (b Backlog) String() string {
	if b.Destination == DestinationRepository {
		return fmt.Sprintf("%s %s %s", b.Destination, b.Layout, b.Path)
	}
	return string(b.Destination)
}

// ResolveBacklog resolves `backlog.destination` to a concrete destination.
//
// It reproduces cfg_resolve_backlog at lib/config.sh:413-469. The two refusals
// below cannot be reached from a configured value, because assertBacklog
// refuses both at load; they stay as a guard against a caller passing a literal
// this does not know.
func (c *Config) ResolveBacklog(ctx context.Context, base core.Revision, want string) (Backlog, error) {
	// An empty destination is `auto`, not `none`. Every caller reads the value
	// through cfg_get, which renders an absent, null or false key as the empty
	// string, and cfg_resolve_backlog substitutes `auto` for an empty second
	// argument at lib/config.sh:414 — so the `""` arm of the Bash `case` is
	// unreachable. Collapsing the two would drop deferred work instead of
	// writing it, and assertBacklog already validates the same key as `auto`.
	if want == "" {
		want = string(DestinationAuto)
	}
	switch Destination(want) {
	case DestinationNone:
		return Backlog{Destination: DestinationNone}, nil
	case DestinationGitHubIssues:
		return Backlog{Destination: DestinationGitHubIssues}, nil
	case DestinationRepository:
		return c.resolveRepositoryBacklog(ctx, base)
	case DestinationAuto:
	default:
		return Backlog{}, unknownDestination(want)
	}

	// Tier 1 — the repository's own declaration.
	tracker, found, err := ProjectMapTracker(ctx, base, c.show)
	if err != nil {
		return Backlog{}, err
	}
	if found {
		if resolved, decided := trackerDestination(tracker); decided {
			return resolved, nil
		}
	}

	// Tiers 2 and 3.
	return c.sniffRepositoryBacklog(ctx, base, false, layoutAny)
}

// trackerDestination reads a Project Map Tracker value as a destination, and
// reports whether it decided one. Linear, Jira and friends decide nothing:
// there is somewhere real to look, and nothing there to write to
// (lib/config.sh:447-464).
func trackerDestination(tracker string) (Backlog, bool) {
	lowered := strings.ToLower(tracker)
	switch {
	case lowered == string(DestinationNone):
		return Backlog{Destination: DestinationNone}, true
	case inOrder(lowered, "github", "issue"):
		return Backlog{Destination: DestinationGitHubIssues}, true
	case strings.HasPrefix(lowered, "http://"), strings.HasPrefix(lowered, "https://"):
		// A URL is a hosted tracker, not a path. It has to be caught before
		// the slash arm below, which would otherwise turn
		// `https://linear.app/acme/team/ENG` into a directory of that name
		// inside the checkout — relative, so the inside-the-repo guard waves
		// it through. Same outcome as a bare `Linear`: somewhere real,
		// nothing to write to (lib/config.sh:450-455).
		return Backlog{}, false
	case strings.Contains(lowered, "/"), strings.HasSuffix(lowered, ".md"):
		// One arm, because lib/config.sh:456 is one pattern, `*/*|*.md`,
		// matched against the lowercased value. The layout inside it is
		// decided on the original: `[[ "$tracker" == *.md ]]` at
		// lib/config.sh:457 is case-sensitive, so `docs/TODO.MD` is a folder
		// there and splitting the arm in two would make it a file here.
		if strings.HasSuffix(tracker, ".md") {
			return Backlog{Destination: DestinationRepository, Layout: LayoutFile, Path: tracker}, true
		}
		return Backlog{Destination: DestinationRepository, Layout: LayoutFolder, Path: tracker}, true
	default:
		return Backlog{}, false
	}
}

// inOrder reports whether every needle appears in the haystack, each one after
// the last. It is the Bash glob `*github*issue*` at lib/config.sh:449, which is
// ordered: `Issues on GitHub` does not match it, and a test for two substrings
// in any order would say it does.
func inOrder(haystack string, needles ...string) bool {
	for _, needle := range needles {
		at := strings.Index(haystack, needle)
		if at < 0 {
			return false
		}
		haystack = haystack[at+len(needle):]
	}
	return true
}

// resolveRepositoryBacklog handles an explicitly configured repository backlog
// (lib/config.sh:419-438).
func (c *Config) resolveRepositoryBacklog(ctx context.Context, base core.Revision) (Backlog, error) {
	layout := Layout(alternativeString(lookup(c.Merged, ".backlog.repository.layout"), string(LayoutFolder)))
	path := c.Get(".backlog.repository.path")
	switch layout {
	case LayoutFolder, LayoutFile:
	default:
		return Backlog{}, unknownLayout(string(layout))
	}
	if path != "" {
		return Backlog{Destination: DestinationRepository, Layout: layout, Path: path}, nil
	}
	// A repository key is explicit only when it appears in the repository
	// config, not when it arrives through the merged defaults
	// (lib/config.sh:432). A stated layout constrains the candidates rather
	// than reinterpreting a path that belongs to another one.
	constrained := layoutAny
	if c.Repo.Object("backlog").Object("repository").Has("layout") {
		constrained = layout
	}
	return c.sniffRepositoryBacklog(ctx, base, true, constrained)
}

// sniffRepositoryBacklog looks for a convention already in use.
//
// `explicit` is true when a repository destination was asked for, in which case
// tier 3 applies; an automatic resolution falls to none rather than creating a
// location nobody asked for (_cfg_sniff_repository_backlog,
// lib/config.sh:482-502).
func (c *Config) sniffRepositoryBacklog(ctx context.Context, base core.Revision, explicit bool, layout Layout) (Backlog, error) {
	if layout == layoutAny || layout == LayoutFolder {
		for _, candidate := range []string{"backlog.config.yml", "backlog/config.yml", ".backlog/config.yml"} {
			exists, err := c.pathExists(ctx, base, candidate)
			if err != nil {
				return Backlog{}, err
			}
			if exists {
				return Backlog{Destination: DestinationRepository, Layout: LayoutFolder, Path: "backlog/tasks"}, nil
			}
		}
	}
	if layout == layoutAny || layout == LayoutFile {
		for _, candidate := range []string{"BACKLOG.md", "TODO.md"} {
			exists, err := c.pathExists(ctx, base, candidate)
			if err != nil {
				return Backlog{}, err
			}
			if exists {
				return Backlog{Destination: DestinationRepository, Layout: LayoutFile, Path: candidate}, nil
			}
		}
	}
	if explicit {
		if layout == LayoutFile {
			return Backlog{Destination: DestinationRepository, Layout: LayoutFile, Path: ".crossrev/backlog.md"}, nil
		}
		return Backlog{Destination: DestinationRepository, Layout: LayoutFolder, Path: ".crossrev/backlog"}, nil
	}
	return Backlog{Destination: DestinationNone}, nil
}

// pathExists asks whether a path exists, in the base revision when there is
// one and on disk when there is not (_cfg_path_exists, lib/config.sh:476-480).
// The Bash test is `-e`, so a directory counts.
func (c *Config) pathExists(ctx context.Context, base core.Revision, path string) (bool, error) {
	_, status, err := c.show(ctx, base, path)
	if err != nil {
		return false, fmt.Errorf("read %s: %w", path, err)
	}
	return status != NotFound, nil
}

// AssertPathInsideRepo bounds a configured backlog path.
//
// The value ends in a file write, so it is bounded rather than trusted: a `../`
// sequence, an absolute path or a symlink out of the checkout must fail loudly
// instead of landing somewhere surprising (lib/config.sh:504-541).
//
// Resolving physically rather than lexically is one of the two deliberate
// divergences approved for the native port, and it answers differently in two
// places rather than one.
//
// The first is the divergence itself. The Bash guard resolves lexically and
// asks the filesystem nothing, so a symlink inside the checkout pointing
// outside it passes (issue 128). Here the deepest resolvable ancestor is
// resolved physically, the remainder is rejoined lexically, and the result is
// compared against the resolved repository root. A containment check a symlink
// walks through is not doing its job.
//
// The second follows from it and is refused under a different family. A loop of
// symlinks that never leaves the checkout resolves to nothing here, so it takes
// unresolvableRefusal — where the Bash, resolving lexically, sees a path inside
// the checkout and allows it. That is not the same fault as a symlink pointing
// outside the checkout, and it is not covered by the sentence above: a write to
// a loop lands nowhere at all.
//
// root must be an absolute path, which is what `git rev-parse --show-toplevel`
// prints and what every Bash caller passes (lib/config.sh:509). It is cleaned
// here so that a trailing slash does not make the separator test below report
// every path outside, and a relative root is refused under its own message
// rather than misreported as an escape.
func AssertPathInsideRepo(root, path string) error {
	if root == "" {
		return &Refusal{
			Message: "not inside a git repository",
			Hint:    "Run crossrev from a checkout of the repository under review.",
		}
	}
	if !filepath.IsAbs(root) {
		return &Refusal{
			Message: fmt.Sprintf("the repository root '%s' is not an absolute path", root),
			Hint:    "A backlog path is bounded against the checkout root, which git rev-parse --show-toplevel prints as an absolute path.",
		}
	}
	root = filepath.Clean(root)
	if strings.HasPrefix(path, "/") {
		return &Refusal{
			Message: fmt.Sprintf("the backlog path '%s' is absolute", path),
			Hint:    "Backlog paths are repository-relative, so that CrossRev cannot write outside the checkout.",
		}
	}

	resolved := root
	if path != "" && path != "." {
		resolved = lexicalResolve(root + "/" + path)
	}
	if resolved == "" {
		return &Refusal{
			Message: fmt.Sprintf("the backlog path '%s' escapes the repository", path),
			Hint:    "It climbs above the checkout root. Backlog paths must stay inside the repository.",
		}
	}
	if !inside(root, resolved) {
		return outsideRefusal(path, resolved)
	}

	physicalRoot, ok := physicalResolve(root, maxSymlinkHops)
	if !ok {
		return unresolvableRefusal(path)
	}
	physical, ok := physicalResolve(resolved, maxSymlinkHops)
	if !ok {
		return unresolvableRefusal(path)
	}
	if !inside(physicalRoot, physical) {
		return outsideRefusal(path, physical)
	}
	return nil
}

func unresolvableRefusal(path string) *Refusal {
	return &Refusal{
		Message: fmt.Sprintf("the backlog path '%s' cannot be resolved", path),
		Hint:    "It runs through a loop of symlinks, so no write to it can land anywhere. Point it at a real location inside the repository.",
	}
}

func outsideRefusal(path, resolved string) *Refusal {
	return &Refusal{
		Message: fmt.Sprintf("the backlog path '%s' resolves outside the repository", path),
		Hint:    fmt.Sprintf("It resolves to %s. Backlog paths must stay inside the checkout.", resolved),
	}
}

// inside is the containment test at lib/config.sh:534-535: the root itself, or
// anything under it. The separator is load-bearing: without it `<root>-evil`
// prefixes `<root>` and reads as inside the checkout.
func inside(root, candidate string) bool {
	if candidate == root {
		return true
	}
	prefix := root
	if !strings.HasSuffix(prefix, string(filepath.Separator)) {
		prefix += string(filepath.Separator)
	}
	return strings.HasPrefix(candidate, prefix)
}

// lexicalResolve is the jq reduction at lib/config.sh:524-532. It returns the
// empty string when the path climbs above the root, which is the escape the
// caller refuses by name.
func lexicalResolve(candidate string) string {
	var stack []string
	escaped := false
	for _, part := range strings.Split(candidate, "/") {
		if escaped {
			break
		}
		if part == "" || part == "." {
			continue
		}
		if part == ".." {
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			} else {
				escaped = true
			}
			continue
		}
		stack = append(stack, part)
	}
	if escaped || len(stack) == 0 {
		return ""
	}
	return "/" + strings.Join(stack, "/")
}

// maxSymlinkHops bounds how many dangling links physicalResolve will follow. A
// loop of them resolves to nothing on the filesystem either, so the answer is a
// refusal rather than an unbounded walk. The kernel's own limit is in the same
// range: 40 on Linux, 32 on Darwin.
const maxSymlinkHops = 32

// physicalResolve resolves a path to where a write to it would land: the
// deepest ancestor that resolves, with the part that does not exist yet
// rejoined lexically. The remainder is joined lexically because there is
// nothing on disk to resolve it against, which is the common case — the backlog
// folder is usually created by the write this guard is protecting.
//
// It reports false when the path cannot be resolved at all, which a loop of
// symlinks is.
//
// filepath.EvalSymlinks fails on a dangling symlink and os.Lstat succeeds on
// one, so returning the unresolved path when EvalSymlinks fails would fail
// open: the containment test would then run against where the link is spelled
// rather than where the write lands, which is the escape this guard exists to
// close. A dangling link is read with os.Readlink and its target resolved
// instead.
func physicalResolve(candidate string, hops int) (string, bool) {
	var remainder []string
	current := candidate
	for {
		if resolved, err := filepath.EvalSymlinks(current); err == nil {
			return rejoin(resolved, remainder), true
		}
		if info, err := os.Lstat(current); err == nil && info.Mode()&os.ModeSymlink != 0 {
			resolved, ok := followDanglingLink(current, hops)
			if !ok {
				return "", false
			}
			return rejoin(resolved, remainder), true
		}
		parent := filepath.Dir(current)
		if parent == current {
			// Nothing on the way up resolved — an empty filesystem root, or
			// an ancestor that cannot be read. The lexical comparison has
			// already run, so the path is still bounded by it.
			return rejoin(current, remainder), true
		}
		remainder = append([]string{filepath.Base(current)}, remainder...)
		current = parent
	}
}

// followDanglingLink resolves the target of a link whose target does not
// resolve. A relative target is joined against the link's own resolved
// directory, which is how the kernel reads it.
func followDanglingLink(link string, hops int) (string, bool) {
	if hops <= 0 {
		return "", false
	}
	target, err := os.Readlink(link)
	if err != nil {
		return "", false
	}
	if !filepath.IsAbs(target) {
		parent := filepath.Dir(link)
		if resolvedParent, err := filepath.EvalSymlinks(parent); err == nil {
			parent = resolvedParent
		}
		target = filepath.Join(parent, target)
	}
	return physicalResolve(target, hops-1)
}

func rejoin(resolved string, remainder []string) string {
	if len(remainder) == 0 {
		return resolved
	}
	return filepath.Join(append([]string{resolved}, remainder...)...)
}
