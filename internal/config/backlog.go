package config

// ---------------------------------------------------------------------------
// Where deferred work goes
// ---------------------------------------------------------------------------
//
// Inventing a folder in someone else's repository is the wrong default, so
// CrossRev does not. Three tiers, first hit wins, and the last one only fires
// when a repository backlog was explicitly asked for (lib/config.sh:328-334).

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
// stated none (lib/config.sh:447).
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

// String is the one line cfg_resolve_backlog prints at lib/config.sh:370:
// "github_issues", "repository <layout> <path>", or "none".
func (b Backlog) String() string {
	if b.Destination == DestinationRepository {
		return fmt.Sprintf("%s %s %s", b.Destination, b.Layout, b.Path)
	}
	return string(b.Destination)
}

// ResolveBacklog resolves `backlog.destination` to a concrete destination.
//
// It reproduces cfg_resolve_backlog at lib/config.sh:377-433. The two refusals
// below cannot be reached from a configured value, because assertBacklog
// refuses both at load; they stay as a guard against a caller passing a literal
// this does not know.
func (c *Config) ResolveBacklog(ctx context.Context, base core.Revision, want string) (Backlog, error) {
	switch Destination(want) {
	case DestinationNone, "":
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
// (lib/config.sh:411-428).
func trackerDestination(tracker string) (Backlog, bool) {
	lowered := strings.ToLower(tracker)
	switch {
	case lowered == string(DestinationNone):
		return Backlog{Destination: DestinationNone}, true
	case strings.Contains(lowered, "github") && strings.Contains(lowered, "issue"):
		return Backlog{Destination: DestinationGitHubIssues}, true
	case strings.HasPrefix(lowered, "http://"), strings.HasPrefix(lowered, "https://"):
		// A URL is a hosted tracker, not a path. It has to be caught before
		// the slash arm below, which would otherwise turn
		// `https://linear.app/acme/team/ENG` into a directory of that name
		// inside the checkout — relative, so the inside-the-repo guard waves
		// it through. Same outcome as a bare `Linear`: somewhere real,
		// nothing to write to (lib/config.sh:414-419).
		return Backlog{}, false
	case strings.HasSuffix(lowered, ".md"):
		return Backlog{Destination: DestinationRepository, Layout: LayoutFile, Path: tracker}, true
	case strings.Contains(lowered, "/"):
		return Backlog{Destination: DestinationRepository, Layout: LayoutFolder, Path: tracker}, true
	default:
		return Backlog{}, false
	}
}

// resolveRepositoryBacklog handles an explicitly configured repository backlog
// (lib/config.sh:383-402).
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
	// (lib/config.sh:396). A stated layout constrains the candidates rather
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
// lib/config.sh:446-466).
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
// one and on disk when there is not (_cfg_path_exists, lib/config.sh:440-444).
// The Bash test is `-e`, so a directory counts.
func (c *Config) pathExists(ctx context.Context, base core.Revision, path string) (bool, error) {
	_, status, err := c.show(ctx, base, path)
	if err != nil {
		return false, err
	}
	return status != NotFound, nil
}

// AssertPathInsideRepo bounds a configured backlog path.
//
// The value ends in a file write, so it is bounded rather than trusted: a `../`
// sequence, an absolute path or a symlink out of the checkout must fail loudly
// instead of landing somewhere surprising (lib/config.sh:468-505).
//
// This is one of the two deliberate divergences approved for the native port.
// The Bash guard resolves lexically and asks the filesystem nothing, so a
// symlink inside the checkout pointing outside it passes (issue 128). Here the
// deepest existing ancestor is resolved with filepath.EvalSymlinks, the
// remainder is rejoined lexically, and the result is compared against the
// resolved repository root. A containment check a symlink walks through is not
// doing its job.
func AssertPathInsideRepo(root, path string) error {
	if root == "" {
		return &Refusal{
			Message: "not inside a git repository",
			Hint:    "Run crossrev from a checkout of the repository under review.",
		}
	}
	if strings.HasPrefix(path, "/") {
		return &Refusal{
			Message: fmt.Sprintf("the backlog path '%s' is absolute", path),
			Hint:    "Backlog paths are repository-relative, so that crossrev cannot write outside the checkout.",
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

	physicalRoot := evalSymlinks(root)
	if physical := physicalResolve(resolved); !inside(physicalRoot, physical) {
		return outsideRefusal(path, physical)
	}
	return nil
}

func outsideRefusal(path, resolved string) *Refusal {
	return &Refusal{
		Message: fmt.Sprintf("the backlog path '%s' resolves outside the repository", path),
		Hint:    fmt.Sprintf("It resolves to %s. Backlog paths must stay inside the checkout.", resolved),
	}
}

// inside is the containment test at lib/config.sh:498-499: the root itself, or
// anything under it.
func inside(root, candidate string) bool {
	return candidate == root || strings.HasPrefix(candidate, root+string(filepath.Separator))
}

// lexicalResolve is the jq reduction at lib/config.sh:488-496. It returns the
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

// physicalResolve resolves the deepest existing ancestor of a path and rejoins
// the part that does not exist yet. The remainder is joined lexically because
// there is nothing on disk to resolve it against, which is the common case: the
// backlog folder is usually created by the write this guard is protecting.
func physicalResolve(candidate string) string {
	var remainder []string
	current := candidate
	for {
		if _, err := os.Lstat(current); err == nil {
			break
		}
		parent := filepath.Dir(current)
		if parent == current {
			return candidate
		}
		remainder = append([]string{filepath.Base(current)}, remainder...)
		current = parent
	}
	resolved := evalSymlinks(current)
	if len(remainder) == 0 {
		return resolved
	}
	return filepath.Join(append([]string{resolved}, remainder...)...)
}

// evalSymlinks resolves a path that exists, and returns it unchanged when it
// cannot be resolved. A guard that fails open on an unreadable ancestor would
// be no guard, but the lexical comparison has already run by the time this is
// called, so the unresolved path is still bounded.
func evalSymlinks(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return path
}
