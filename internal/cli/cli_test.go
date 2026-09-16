package cli

import (
	"testing"

	"github.com/carlosboeing/crossrev/internal/buildinfo"
)

func TestVersionStringMarksALocalBuild(t *testing.T) {
	for _, row := range []struct {
		name     string
		base     string
		local    string
		revision string
		modified bool
		want     string
	}{
		{"a release build is untouched", "0.6.2", "", "4b92d65aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", false, "0.6.2"},
		{"a local build carries its commit", "0.6.2", "1", "4b92d65aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", false, "0.6.2-4b92d65"},
		{"a dirty local build says so", "0.6.2", "1", "4b92d65aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", true, "0.6.2-4b92d65-dirty"},
		// A local build with no VCS stamp cannot name a commit, and inventing
		// one would be worse than the base version.
		{"an unstamped local build falls back", "0.6.2", "1", "", false, "0.6.2"},
		// The embedded file ends in a newline; the suffix lands on the
		// version, not after it.
		{"a local build strips the embedded whitespace before suffixing", "0.6.2\n", "1", "4b92d65aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", false, "0.6.2-4b92d65"},
	} {
		t.Run(row.name, func(t *testing.T) {
			got := composeVersion(row.base, row.local, buildinfo.Info{
				Revision: row.revision,
				Modified: row.modified,
			})
			if got != row.want {
				t.Errorf("composeVersion = %q, want %q", got, row.want)
			}
		})
	}
}

// A build-time override replaces the whole version string, the way
// QUOTACAP_VERSION_OVERRIDE does for QuotaCap. Nothing sets it yet; it
// exists so the convention has no hole in it.
func TestInstalledVersionHonoursABuildTimeOverride(t *testing.T) {
	old := buildinfo.VersionOverride
	buildinfo.VersionOverride = "9.9.9-test"
	defer func() { buildinfo.VersionOverride = old }()
	if got := InstalledVersion(); got != "9.9.9-test" {
		t.Errorf("InstalledVersion = %q, want the override %q", got, "9.9.9-test")
	}
}
