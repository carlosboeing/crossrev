package archtest_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Public documentation describes Review Intelligence as review completion,
// never as verification. The coverage manifest records verification.status
// not_implemented, and crossrev/converged means the current review
// obligation is complete — not that checks passed or the code is correct.
// A doc that calls the result verified, or that omits the not_implemented
// statement, lets a reader believe the loop checked something it never ran.
func TestPublicDocsDescribeCoverageNotVerification(t *testing.T) {
	docs := []string{
		"README.md",
		"docs/usage.md",
		"docs/configuration.md",
		"docs/troubleshooting.md",
		"docs/architecture.md",
	}
	root := findRepoRoot(t)
	joined := ""
	for _, name := range docs {
		raw, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatalf("reading %s: %v", name, err)
		}
		joined += "\n" + string(raw)
	}
	lowered := strings.ToLower(joined)

	for _, banned := range []string{
		"verified convergence",
		"verify convergence",
		"verification passed",
		"checks passed",
		"all checks pass",
		"no_checks_discovered",
	} {
		if strings.Contains(lowered, banned) {
			t.Errorf("public docs claim %q, which states a verification this release never runs", banned)
		}
	}
	if !strings.Contains(joined, "not_implemented") {
		t.Error("public docs never name verification.status not_implemented, so nothing says checks are out of scope")
	}
	// The six reserved envelope states are decoder vocabulary for another
	// writer's bytes, not states this release documents as its own outcomes.
	for _, state := range []string{
		"verification.status: passed",
		"verification.status: failed",
		"verification.status: pending",
		"verification.status: unavailable",
		"verification.status passed",
		"verification.status failed",
	} {
		if strings.Contains(joined, state) {
			t.Errorf("public docs document out-of-scope verification state %q as a documented outcome", state)
		}
	}
}

// The configuration break is user-visible: files that still declare the
// superseded version are refused. A current example that still shows the
// old version teaches a shape this build refuses.
func TestPublicDocsShowCurrentConfigVersion(t *testing.T) {
	root := findRepoRoot(t)
	for _, name := range []string{
		"README.md",
		"docs/configuration.md",
		"docs/usage.md",
	} {
		raw, err := os.ReadFile(filepath.Join(root, name)) //nolint:gosec // a path this test names
		if err != nil {
			t.Fatalf("reading %s: %v", name, err)
		}
		for _, line := range strings.Split(string(raw), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "version: 1") {
				t.Errorf("%s shows %q, but current files declare version: 2", name, trimmed)
			}
		}
	}
}

// Each clause of the verdict has a fixture, because each was removable with
// the suite still green: dropping the banned-phrase list keeps the docs'
// own text passing while a new "verified" sentence slips in, and dropping
// the required-phrase halves keeps it passing while the not_implemented
// sentence is deleted.
func TestPublicDocsAuditVerdict(t *testing.T) {
	tests := []struct {
		name string
		doc  string
		// wantBanned counts banned phrases the text must hold; wantMissing
		// counts required statements it must lack. Both zero means the text
		// is acceptable.
		wantBanned  int
		wantMissing int
	}{
		{
			name:        "review completion with the status named",
			doc:         "crossrev/converged means review completion. verification.status stays not_implemented.",
			wantBanned:  0,
			wantMissing: 0,
		},
		{
			name:        "a verified claim",
			doc:         "the pass reports verified convergence with verification.status not_implemented.",
			wantBanned:  1,
			wantMissing: 0,
		},
		{
			name:        "the status omitted",
			doc:         "crossrev/converged means review completion.",
			wantBanned:  0,
			wantMissing: 1,
		},
		{
			name:        "a documented out-of-scope state",
			doc:         "review completion. verification.status: passed is recorded. verification.status stays not_implemented.",
			wantBanned:  1,
			wantMissing: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := len(auditDocsVerdict(tt.doc)); got != tt.wantBanned {
				t.Errorf("banned phrases = %d, want %d", got, tt.wantBanned)
			}
			if got := len(auditDocsMissing(tt.doc)); got != tt.wantMissing {
				t.Errorf("missing statements = %d, want %d", got, tt.wantMissing)
			}
		})
	}
}

// auditDocsVerdict reports every coverage-not-verification violation in one
// document's text: a banned verified phrase, or a documented out-of-scope
// verification state.
func auditDocsVerdict(doc string) []string {
	var found []string
	lowered := strings.ToLower(doc)
	for _, banned := range []string{
		"verified convergence",
		"verify convergence",
		"verification passed",
		"checks passed",
		"all checks pass",
		"no_checks_discovered",
		"verification.status: passed",
		"verification.status: failed",
		"verification.status: pending",
		"verification.status: unavailable",
		"verification.status passed",
		"verification.status failed",
	} {
		if strings.Contains(lowered, banned) {
			found = append(found, banned)
		}
	}
	return found
}

// auditDocsMissing reports the required coverage-not-verification statements
// one document's text lacks.
func auditDocsMissing(doc string) []string {
	if strings.Contains(doc, "not_implemented") {
		return nil
	}
	return []string{"verification.status not_implemented"}
}
