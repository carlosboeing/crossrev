package core

import "testing"

func TestParseSlugSplitsOwnerFromName(t *testing.T) {
	s, err := ParseSlug("carlosboeing/crossrev")
	if err != nil {
		t.Fatalf("ParseSlug: %v", err)
	}
	if s.Owner != "carlosboeing" || s.Name != "crossrev" {
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

// PathKey is the single implementation of the slug rule the run log and the
// worktree path already share: `slug="${repo//\//-}"` at lib/log.sh:46 and
// lib/run.sh:60.
func TestPathKeyReplacesTheSlashWithAHyphen(t *testing.T) {
	tests := []struct {
		name string
		slug Slug
		want string
	}{
		{name: "ordinary", slug: Slug{Owner: "carlosboeing", Name: "crossrev"}, want: "carlosboeing-crossrev"},
		{name: "owner already hyphenated", slug: Slug{Owner: "acme-corp", Name: "widget"}, want: "acme-corp-widget"},
		{name: "name with a dot", slug: Slug{Owner: "acme", Name: "widget.js"}, want: "acme-widget.js"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.slug.PathKey(); got != tt.want {
				t.Fatalf("PathKey() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPathKeyLeavesNoSlashBehind(t *testing.T) {
	// The Bash form is a global replacement, so a slug carrying more than one
	// slash still yields a single path segment. Reproducing that keeps the Go
	// path key from ever growing a directory the Bash one did not.
	s := Slug{Owner: "acme", Name: "team/widget"}
	if got, want := s.PathKey(), "acme-team-widget"; got != want {
		t.Fatalf("PathKey() = %q, want %q", got, want)
	}
}

func TestSlugIsZeroWhenEitherHalfIsMissing(t *testing.T) {
	if !(Slug{}).IsZero() {
		t.Fatal("IsZero() = false for the zero slug")
	}
	if !(Slug{Owner: "acme"}).IsZero() {
		t.Fatal("IsZero() = false for a slug with no name")
	}
	if (Slug{Owner: "acme", Name: "widget"}).IsZero() {
		t.Fatal("IsZero() = true for a complete slug")
	}
}

func TestRepositoryCarriesTheSlugAndItsCheckoutFacts(t *testing.T) {
	repo := Repository{
		Slug:          Slug{Owner: "acme", Name: "widget"},
		Root:          "/checkout",
		DefaultBranch: "main",
		Remotes:       []string{"origin"},
	}
	if got, want := repo.Slug.PathKey(), "acme-widget"; got != want {
		t.Fatalf("Slug.PathKey() = %q, want %q", got, want)
	}
	if repo.Root != "/checkout" || repo.DefaultBranch != "main" || len(repo.Remotes) != 1 {
		t.Fatalf("Repository = %+v, want the fields it was built with", repo)
	}
}
