package prstate_test

import (
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/prstate"
)

func TestValidNamespaceRefusesEverythingThatCouldMakeABranchOrATag(t *testing.T) {
	for _, bad := range []string{
		"refs/heads", "refs/heads/crossrev", "refs/tags", "refs/tags/x",
		"refs/pull", "refs/remotes/origin", "crossrev", "", "refs",
		"refs/crossrev/..", "refs/crossrev/.hidden", "refs/crossrev/x.lock",
		"refs/crossrev/a b", "refs/crossrev/a~1", "refs/crossrev/a^", "refs/crossrev/a:b",
		"refs/crossrev/a?", "refs/crossrev/a*", "refs/crossrev/a[", "refs/crossrev/a\\b",
		"refs/crossrev/a@{b", "refs/crossrev/a\x01b", "refs/crossrev/trailing/",
	} {
		if err := prstate.ValidNamespace(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	for _, ok := range []string{"refs/crossrev", "refs/internal/crossrev", "refs/x/y/z"} {
		if err := prstate.ValidNamespace(ok); err != nil {
			t.Errorf("refused %q: %v", ok, err)
		}
	}
}

func TestValidSlotRefusesPathSeparatorsAndControlCharacters(t *testing.T) {
	for _, bad := range []string{"", "a/b", ".hidden", "a b", "a\x00", strings.Repeat("x", 65), "-leading"} {
		if err := prstate.ValidSlot(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	for _, ok := range []string{"reviewer1", "a", "A", "slot-1", "slot.2", "slot_3", strings.Repeat("x", 64)} {
		if err := prstate.ValidSlot(ok); err != nil {
			t.Errorf("refused %q: %v", ok, err)
		}
	}
}

// The formula itself, against the promise it has to keep.
func TestRefNameNeverProducesABranchOrATag(t *testing.T) {
	if _, err := prstate.RefName("refs/heads", prstate.SlotRef{Number: 42, Slot: "reviewer1"}); err == nil {
		t.Fatal("refs/heads/pr/42/reviewer1/coverage is a branch")
	}
	got, err := prstate.RefName("refs/crossrev", prstate.SlotRef{Number: 42, Slot: "reviewer1"})
	if err != nil {
		t.Fatalf("RefName failed: %v", err)
	}
	want := "refs/crossrev/pr/42/reviewer1/coverage"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
