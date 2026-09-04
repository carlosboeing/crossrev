package core

import "testing"

func TestParseSlugSplitsOwnerFromName(t *testing.T) {
	s, err := ParseSlug("carlosboeing/crossrev")
	if err != nil {
		t.Fatalf("ParseSlug: %v", err)
	}
	if s.Owner() != "carlosboeing" || s.Name() != "crossrev" {
		t.Fatalf("ParseSlug = %+v, want owner carlosboeing and name crossrev", s)
	}
	if got := s.String(); got != "carlosboeing/crossrev" {
		t.Fatalf("String() = %q, want carlosboeing/crossrev", got)
	}
}

func TestParseSlugRefusesAnythingThatIsNotOwnerSlashName(t *testing.T) {
	for _, in := range []string{"", "crossrev", "/crossrev", "carlosboeing/", "a/b/c", "carlos boeing/crossrev"} {
		t.Run(in, func(t *testing.T) {
			if _, err := ParseSlug(in); err == nil {
				t.Fatalf("ParseSlug(%q) = nil error, want a refusal", in)
			}
		})
	}
}

// The fields are unexported, so NewSlug and ParseSlug are the only routes to a
// value and the refusal below cannot be walked around with a struct literal.
func TestNewSlugRefusesAHalfThatWouldReachTheFilesystem(t *testing.T) {
	for _, tt := range []struct{ owner, name string }{
		{owner: "", name: "widget"},
		{owner: "acme", name: ""},
		{owner: "acme", name: "team/widget"},
		{owner: "acme", name: "../evil"},
		{owner: "a b", name: "widget"},
		{owner: "acme", name: "wid\tget"},
	} {
		t.Run(tt.owner+"|"+tt.name, func(t *testing.T) {
			if _, err := NewSlug(tt.owner, tt.name); err == nil {
				t.Fatalf("NewSlug(%q, %q) = nil error, want a refusal", tt.owner, tt.name)
			}
		})
	}
	// `..` on its own carries no separator, so it is a name GitHub could not
	// issue but PathKey would render harmlessly as one segment.
	if _, err := NewSlug("acme", ".."); err != nil {
		t.Fatalf("NewSlug(acme, ..) = %v, want no error", err)
	}
}

// PathKey is the single implementation of the slug rule the run log and the
// worktree path already share: `slug="${repo//\//-}"` at lib/log.sh:46 and
// lib/run.sh:60.
func TestPathKeyReplacesTheSlashWithAHyphen(t *testing.T) {
	tests := []struct {
		name  string
		owner string
		repo  string
		want  string
	}{
		{name: "ordinary", owner: "carlosboeing", repo: "crossrev", want: "carlosboeing-crossrev"},
		{name: "owner already hyphenated", owner: "acme-corp", repo: "widget", want: "acme-corp-widget"},
		{name: "name with a dot", owner: "acme", repo: "widget.js", want: "acme-widget.js"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, err := NewSlug(tt.owner, tt.repo)
			if err != nil {
				t.Fatalf("NewSlug: %v", err)
			}
			if got := s.PathKey(); got != tt.want {
				t.Fatalf("PathKey() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSlugIsIncompleteOnlyWhenAConstructorDidNotProduceIt(t *testing.T) {
	if !(Slug{}).Incomplete() {
		t.Fatal("Incomplete() = false for the zero slug")
	}
	s, err := NewSlug("acme", "widget")
	if err != nil {
		t.Fatalf("NewSlug: %v", err)
	}
	if s.Incomplete() {
		t.Fatal("Incomplete() = true for a constructed slug")
	}
}

// A Repository with no slug shares one run-log directory and one reusable
// worktree with every other repository that has none, because PathKey collapses
// both to the same string.
func TestNewRepositoryRefusesASlugOrARootItCannotUse(t *testing.T) {
	good, err := NewSlug("acme", "widget")
	if err != nil {
		t.Fatalf("NewSlug: %v", err)
	}
	if _, err := NewRepository(Slug{}, "/checkout", "main", nil); err == nil {
		t.Fatal("NewRepository with no slug = nil error, want a refusal")
	}
	if _, err := NewRepository(good, "", "main", nil); err == nil {
		t.Fatal("NewRepository with no root = nil error, want a refusal")
	}
}

func TestNewRepositoryCarriesTheSlugAndItsCheckoutFacts(t *testing.T) {
	slug, err := NewSlug("acme", "widget")
	if err != nil {
		t.Fatalf("NewSlug: %v", err)
	}
	// The default branch and the remotes are not required: a checkout read
	// before either has been discovered is still a repository.
	repo, err := NewRepository(slug, "/checkout", "", nil)
	if err != nil {
		t.Fatalf("NewRepository: %v", err)
	}
	if got, want := repo.Slug.PathKey(), "acme-widget"; got != want {
		t.Fatalf("Slug.PathKey() = %q, want %q", got, want)
	}
}
