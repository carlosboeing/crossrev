package core

import (
	"errors"
	"fmt"
	"strings"
)

// ErrSlug is returned when a string is not `owner/repo`.
var ErrSlug = errors.New("a repository slug must be owner/name")

// Slug is a repository's `owner/name` pair.
//
// The halves are unexported so the only route to a value is a constructor that
// validates, the way Revision is built. A literal would otherwise reach
// PathKey, and PathKey builds a directory under $XDG_STATE_HOME: `Slug{Owner:
// "a b", Name: "../evil"}` compiled, and the zero value gave every repository
// that never set a slug the same run-log directory and the same reusable
// worktree.
type Slug struct {
	owner string
	name  string
}

// NewSlug validates the two halves and returns the slug they form.
func NewSlug(owner, name string) (Slug, error) {
	if !isSlugPart(owner) || !isSlugPart(name) {
		return Slug{}, fmt.Errorf("%w: %q/%q", ErrSlug, owner, name)
	}
	return Slug{owner: owner, name: name}, nil
}

// ParseSlug splits `owner/name`, refusing anything with a different shape.
func ParseSlug(s string) (Slug, error) {
	owner, name, found := strings.Cut(s, "/")
	if !found {
		return Slug{}, fmt.Errorf("%w: %q", ErrSlug, s)
	}
	return NewSlug(owner, name)
}

// Owner is the account half of the slug, or the empty string on the zero value.
func (s Slug) Owner() string { return s.owner }

// Name is the repository half of the slug, or the empty string on the zero
// value.
func (s Slug) Name() string { return s.name }

// String renders the slug the way GitHub and `gh` spell it.
func (s Slug) String() string { return s.owner + "/" + s.name }

// PathKey is the single implementation of the on-disk slug rule.
//
// The shipped tool writes it twice as `slug="${repo//\//-}"` — once for the run
// log directory at lib/log.sh:46 and once for the worktree at lib/run.sh:60 —
// and both feed a path under $XDG_STATE_HOME. A second spelling of this rule
// would move every run log and every reusable worktree without a word.
//
// The Bash form is a global replacement and is reproduced as one here, though
// only the separator between the two halves can ever match: isSlugPart refuses
// a half carrying a slash, so a slug a constructor produced holds exactly one.
//
// Only meaningful for a slug a constructor produced. The zero value renders as
// `-`, which is why Incomplete is the guard a caller building a path runs
// first.
func (s Slug) PathKey() string { return strings.ReplaceAll(s.String(), "/", "-") }

// Incomplete reports whether either half is missing, which for a slug that went
// through a constructor means it is the zero value.
//
// Deliberately not named IsZero. Go 1.24 and later call IsZero to implement the
// `omitzero` struct tag, and a half-set value is not a zero value: a slug
// answering true here still carries an owner a marker would then drop.
func (s Slug) Incomplete() bool { return s.owner == "" || s.name == "" }

// Repository is the checkout a leg runs against.
type Repository struct {
	Slug          Slug
	Root          string
	DefaultBranch string
	Remotes       []string
}

// ErrRepository is returned when a repository is missing something every reader
// of it needs.
var ErrRepository = errors.New("a repository needs a slug and a root")

// NewRepository validates the two facts nothing downstream can work without.
//
// The slug reaches a filesystem path through PathKey, and a repository with no
// root has no checkout to run a leg in. The default branch and the remotes are
// not required: a checkout read before either has been discovered is still a
// repository.
func NewRepository(slug Slug, root, defaultBranch string, remotes []string) (Repository, error) {
	if slug.Incomplete() {
		return Repository{}, fmt.Errorf("%w: the slug is %q", ErrRepository, slug)
	}
	if root == "" {
		return Repository{}, fmt.Errorf("%w: no root for %s", ErrRepository, slug)
	}
	return Repository{Slug: slug, Root: root, DefaultBranch: defaultBranch, Remotes: remotes}, nil
}

// isSlugPart reports whether a half of a slug is non-empty and carries neither
// a separator nor whitespace. GitHub allows neither in an owner or a repository
// name, and both would reach a filesystem path through PathKey.
//
// The shipped CLI refuses such a slug later and differently: ctx_load at
// lib/run.sh:234-242 hands `--repo` straight to gh_pr_json, which dies with
// `could not read foo#42`. That is a divergence in refusal text, not in review
// semantics, and closing it belongs with the port of the CLI's own argument
// handling rather than here.
func isSlugPart(s string) bool {
	if s == "" {
		return false
	}
	return !strings.ContainsAny(s, "/\\ \t\n\r")
}
