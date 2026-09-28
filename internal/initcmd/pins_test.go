package initcmd_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// A third-party action reference is a pin only in the ADR 0009 form: a full
// 40-character SHA with the tag riding in a trailing comment, the same form
// .github/workflows/ uses. A tag alone looks immutable and is not one:
// `git tag -f` plus a force push moves it.
var pinnedUses = regexp.MustCompile(`^uses:\s*[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+@[0-9a-f]{40}\s+#\s*v[0-9][A-Za-z0-9.+_-]*\s*$`)

// The delivery pin is rendered at init time: `crossrev init` replaces the two
// placeholders with the source SHA and ref (render.go), so the template
// carries the shape of a pin rather than one.
var sourceUses = regexp.MustCompile(`^uses:\s*carlosboeing/crossrev@__SOURCE_SHA__\s+#\s*__SOURCE_REF__\s*$`)

// TestTemplatesPinActionsBySHA walks the canonical templates/ directory and
// refuses any `uses:` that is not a SHA pin with a version comment. The
// delivery pin is allowed in its placeholder form only: render substitutes a
// real SHA and ref on the way out.
func TestTemplatesPinActionsBySHA(t *testing.T) {
	dir := filepath.Join(repoRoot, templateDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading %s: %v", dir, err)
	}
	checked := 0
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".yml" {
			continue
		}
		body, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			t.Fatalf("reading %s: %v", entry.Name(), err)
		}
		for _, line := range strings.Split(string(body), "\n") {
			trimmed := strings.TrimSpace(line)
			trimmed = strings.TrimPrefix(trimmed, "- ")
			if !strings.HasPrefix(trimmed, "uses:") {
				continue
			}
			checked++
			if pinnedUses.MatchString(trimmed) || sourceUses.MatchString(trimmed) {
				continue
			}
			t.Errorf("%s: %q is not a SHA pin with a version comment (owner/repo@<40-char-sha> # vX.Y.Z)", entry.Name(), trimmed)
		}
	}
	if checked == 0 {
		t.Fatalf("%s holds no uses: lines, so this test proves nothing", dir)
	}
}
