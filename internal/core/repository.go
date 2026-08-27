package core

import (
	"errors"
	"fmt"
	"strings"
)

// ErrSlug is returned when a string is not `owner/repo`.
var ErrSlug = errors.New("a repository slug must be owner/name")

// Slug is a repository's `owner/name` pair.
type Slug struct {
	Owner string
	Name  string
}

// NewSlug validates the two halves and returns the slug they form.
func NewSlug(owner, name string) (Slug, error) {
	if !isSlugPart(owner) || !isSlugPart(name) {
		return Slug{}, fmt.Errorf("%w: %q/%q", ErrSlug, owner, name)
	}
	return Slug{Owner: owner, Name: name}, nil
}

// ParseSlug splits `owner/name`, refusing anything with a different shape.
func ParseSlug(s string) (Slug, error) {
	owner, name, found := strings.Cut(s, "/")
	if !found {
		return Slug{}, fmt.Errorf("%w: %q", ErrSlug, s)
	}
	return NewSlug(owner, name)
}

// String renders the slug the way GitHub and `gh` spell it.
func (s Slug) String() string { return s.Owner + "/" + s.Name }

// PathKey is the single implementation of the on-disk slug rule.
//
// The shipped tool writes it twice as `slug="${repo//\//-}"` — once for the run
// log directory at lib/log.sh:46 and once for the worktree at lib/run.sh:60 —
// and both feed a path under $XDG_STATE_HOME. A second spelling of this rule
// would move every run log and every reusable worktree without a word.
//
// The Bash form is a global replacement, so it is reproduced as one here: a
// slug carrying more than one slash still collapses to a single path segment
// rather than growing a directory level.
func (s Slug) PathKey() string { return strings.ReplaceAll(s.String(), "/", "-") }

// IsZero reports whether either half is missing.
func (s Slug) IsZero() bool { return s.Owner == "" || s.Name == "" }

// Repository is the checkout a leg runs against.
type Repository struct {
	Slug          Slug
	Root          string
	DefaultBranch string
	Remotes       []string
}

// isSlugPart reports whether a half of a slug is non-empty and carries neither
// a separator nor whitespace. GitHub allows neither in an owner or a repository
// name, and both would reach a filesystem path through PathKey.
func isSlugPart(s string) bool {
	if s == "" {
		return false
	}
	return !strings.ContainsAny(s, "/\\ \t\n\r")
}
