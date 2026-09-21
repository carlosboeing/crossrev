package preflight_test

import (
	"context"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/preflight"
)

// coverageChecker is a doctorChecker that already answered the permission
// probe the way the case wants, so each case only names the config and the
// lines it expects. An empty permission leaves the probe unanswered.
func coverageChecker(t *testing.T, permission, yaml string) (*preflight.Checker, *strings.Builder) {
	t.Helper()
	r := coreVersions(newRecorder())
	r.answer("gh api user --jq .login", "carlosboeing\n", 0)
	r.answer("claude --version", "2.1.258\n", 0)
	r.answer("codex --version", "codex-cli 0.152.1\n", 0)
	if permission != "" {
		r.answer("gh repo view --json viewerPermission --jq .viewerPermission", permission+"\n", 0)
	}
	c, buf := doctorChecker(t, r, onPath("git", "gh", "jq", "yq", "openssl", "claude", "codex"), yaml)
	return c, buf
}

// Doctor names the store in force, the namespace it would write, the overflow
// behaviour, and whether each came from the config or the default — so an
// operator who expected refs and got the fallback learns it here rather than
// from a halt on a large pull request three weeks later.
func TestDoctorNamesTheStoreInForceAndWhy(t *testing.T) {
	base := "version: \"2\"\nrunner: github-hosted\nreviewer:\n  harness: codex\nresolver:\n  harness: claude\n"
	for _, tt := range []struct {
		name      string
		yaml      string
		want      []string
		notWanted []string
	}{
		{
			name: "the default is auto under the default namespace",
			yaml: base,
			want: []string{
				"store auto (the default)",
				"falls back to the marker comment when a ref write is refused",
				"namespace refs/crossrev",
				"refs/crossrev/pr/<number>/<slot>/coverage",
				"on_overflow degrade (the default)",
			},
		},
		{
			name: "refs is required loudly",
			yaml: base + "coverage:\n  store: refs\n",
			want: []string{
				"store refs (from coverage.store)",
				"a refused write fails the pass loudly rather than degrading silently",
				"namespace refs/crossrev",
			},
		},
		{
			name: "marker never touches refs, so there is no namespace to name",
			yaml: base + "coverage:\n  store: marker\n",
			want: []string{
				"store marker (from coverage.store)",
				"never touches refs at all",
			},
			notWanted: []string{"namespace refs/"},
		},
		{
			name: "a configured namespace and overflow are reported as configured",
			yaml: base + "coverage:\n  store: auto\n  ref_namespace: refs/acme-review\n  on_overflow: halt\n",
			want: []string{
				"store auto (from coverage.store)",
				"namespace refs/acme-review",
				"refs/acme-review/pr/<number>/<slot>/coverage",
				"on_overflow halt (from coverage.on_overflow)",
				"halts the pass with ledger_exhausted",
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c, buf := coverageChecker(t, "WRITE", tt.yaml)
			if code := c.Doctor(context.Background()); code != 0 {
				t.Errorf("Doctor = %d, want 0", code)
			}
			report := buf.String()
			if !strings.Contains(report, "\n◇  Coverage ledger\n") {
				t.Fatalf("no coverage section in the report:\n%s", report)
			}
			for _, want := range tt.want {
				if !strings.Contains(report, want) {
					t.Errorf("report does not name %q:\n%s", want, report)
				}
			}
			for _, unwanted := range tt.notWanted {
				if strings.Contains(report, unwanted) {
					t.Errorf("report names %q, which this store never writes:\n%s", unwanted, report)
				}
			}
		})
	}
}

// The permission probe states plainly that it proves the permission and not
// the namespace: an organisation ruleset restricts ref creation, which only
// an attempted ref write discovers. A token without push fails doctor,
// because the loop cannot run without contents: write whatever the store.
func TestDoctorSaysAPermissionProbeDoesNotProveTheNamespace(t *testing.T) {
	t.Run("a granted probe carries its limit", func(t *testing.T) {
		c, buf := coverageChecker(t, "WRITE", defaultPairing)
		if code := c.Doctor(context.Background()); code != 0 {
			t.Errorf("Doctor = %d, want 0", code)
		}
		report := buf.String()
		if !strings.Contains(report, "token can push to this repository (contents: write)") {
			t.Errorf("the grant is not named:\n%s", report)
		}
		if !strings.Contains(report, "This proves the permission, not the namespace") {
			t.Errorf("the probe overclaims:\n%s", report)
		}
	})
	t.Run("a refused probe fails doctor", func(t *testing.T) {
		c, buf := coverageChecker(t, "READ", defaultPairing)
		if code := c.Doctor(context.Background()); code != 1 {
			t.Errorf("Doctor = %d, want 1", code)
		}
		if !strings.Contains(buf.String(), "token cannot push to this repository") {
			t.Errorf("the refusal is not named:\n%s", buf.String())
		}
	})
	t.Run("an unanswered probe is unprobed, not refused", func(t *testing.T) {
		c, buf := coverageChecker(t, "", defaultPairing)
		if code := c.Doctor(context.Background()); code != 0 {
			t.Errorf("Doctor = %d, want 0", code)
		}
		if !strings.Contains(buf.String(), "permission unprobed") {
			t.Errorf("the unprobed probe is not named:\n%s", buf.String())
		}
	})
}

// GHES is detected and named: API-compatible in principle, version floor
// unestablished, this path unproven there, store: marker the safe setting
// until someone proves otherwise. Unproven is reported, never failed.
func TestDoctorNamesGHESAsUnproven(t *testing.T) {
	r := coreVersions(newRecorder())
	r.answer("gh api user --jq .login", "carlosboeing\n", 0)
	r.answer("claude --version", "2.1.258\n", 0)
	r.answer("codex --version", "codex-cli 0.152.1\n", 0)
	r.answer("gh repo view --json viewerPermission --jq .viewerPermission", "WRITE\n", 0)
	c, buf := doctorChecker(t, r, onPath("git", "gh", "jq", "yq", "openssl", "claude", "codex"), defaultPairing)
	c.Env = []string{"GH_HOST=ghe.example.com"}

	if code := c.Doctor(context.Background()); code != 0 {
		t.Errorf("Doctor = %d, want 0", code)
	}
	report := buf.String()
	for _, want := range []string{"ghe.example.com", "unproven", "store: marker"} {
		if !strings.Contains(report, want) {
			t.Errorf("report does not name %q:\n%s", want, report)
		}
	}
}

// The resolved reviewer, by id and harness, so a plural configuration's
// effect is visible before a leg runs.
func TestDoctorNamesTheResolvedReviewer(t *testing.T) {
	t.Run("the shorthand resolves to reviewer1", func(t *testing.T) {
		c, buf := coverageChecker(t, "WRITE", defaultPairing)
		if code := c.Doctor(context.Background()); code != 0 {
			t.Errorf("Doctor = %d, want 0", code)
		}
		if !strings.Contains(buf.String(), "reviewer reviewer1 on codex") {
			t.Errorf("the resolved reviewer is not named:\n%s", buf.String())
		}
	})
	t.Run("the list wins over the shorthand", func(t *testing.T) {
		yaml := "version: \"2\"\nrunner: github-hosted\nreviewer:\n  harness: codex\nresolver:\n  harness: claude\nreviewers:\n  - id: main\n    harness: claude\n"
		c, buf := coverageChecker(t, "WRITE", yaml)
		if code := c.Doctor(context.Background()); code != 0 {
			t.Errorf("Doctor = %d, want 0", code)
		}
		if !strings.Contains(buf.String(), "reviewer main on claude") {
			t.Errorf("the resolved reviewer is not named:\n%s", buf.String())
		}
	})
}
