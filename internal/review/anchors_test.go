package review

import (
	"testing"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/diff"
)

// TestAnchorKindRoutesFindings pins the three anchor decisions: a valid
// hunk line stays line, a required path with no valid hunk line is file,
// and any other path is outside_diff with a reason.
func TestAnchorKindRoutesFindings(t *testing.T) {
	raw := []byte("diff --git a/app.go b/app.go\n--- a/app.go\n+++ b/app.go\n" +
		"@@ -1 +1,2 @@\n context\n+added\n")
	parsed := diff.Parse(raw, core.RevisionPair{})
	required := map[string]bool{"app.go": true}

	if kind, reason := anchorKind(parsed, required, "app.go", core.SideRight, 2); kind != AnchorLine || reason != "" {
		t.Errorf("hunk line kind = %q reason %q, want line with no reason", kind, reason)
	}
	if kind, reason := anchorKind(parsed, required, "app.go", core.SideRight, 99); kind != AnchorFile || reason == "" {
		t.Errorf("required path off-hunk kind = %q reason %q, want file with a reason", kind, reason)
	}
	if kind, reason := anchorKind(parsed, required, "docs/helper.go", core.SideRight, 1); kind != AnchorOutsideDiff || reason == "" {
		t.Errorf("untouched path kind = %q reason %q, want outside_diff with a reason", kind, reason)
	}
}

// TestAnchorKindWithoutRequiredSetAssumesOnDiff pins the frozen-path rule:
// with no required set every finding is presumed on-diff, so a hunkless
// diff still lands a file-level comment rather than a top-level one.
func TestAnchorKindWithoutRequiredSetAssumesOnDiff(t *testing.T) {
	parsed := diff.Parse([]byte("diff --git a/app.go b/app.go\n"), core.RevisionPair{})

	if kind, _ := anchorKind(parsed, nil, "app.go", core.SideRight, 1); kind != AnchorFile {
		t.Errorf("nil-set hunkless kind = %q, want file", kind)
	}
}
