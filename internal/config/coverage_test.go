package config_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/config"
)

// load runs Load over one repository-layer document at the base revision,
// with no operator file.
func load(t *testing.T, yaml string) (*config.Config, error) {
	t.Helper()
	base := revision(t, baseSHA)
	tree := files{"": {}, baseSHA: {".github/crossrev.yml": yaml}}
	return config.Load(context.Background(), base, tree.show())
}

// loadYAML is load for a document that must be accepted.
func loadYAML(t *testing.T, yaml string) *config.Config {
	t.Helper()
	loaded, err := load(t, yaml)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return loaded
}

func TestCoverageDefaultsApplyWithNothingConfigured(t *testing.T) {
	want := config.Coverage{Store: "auto", RefNamespace: "refs/crossrev", OnOverflow: "degrade"}
	if got := loadYAML(t, "version: 2\n").Coverage(); got != want {
		t.Fatalf("coverage defaults: got %+v, want %+v", got, want)
	}
}

// The frozen oracle pins the merged object. This test states why the defaults
// are applied at read time, so nobody tidies them into Defaults() and takes a
// read-only fixture down with them.
func TestCoverageKeysAreNotInTheMergedDefaults(t *testing.T) {
	merged, _ := loadYAML(t, "version: 2\n").MergedJSON()
	for _, key := range []string{"coverage", "reviewers"} {
		if bytes.Contains(merged, []byte(`"`+key+`"`)) {
			t.Fatalf("%q reached the merged defaults; tests/fixtures/parity/config_merge.json is frozen", key)
		}
	}
}

func TestCoverageRefusesAnUnknownStoreOverflowOrNamespace(t *testing.T) {
	for _, bad := range []string{
		"coverage:\n  store: comments\n",
		"coverage:\n  on_overflow: ignore\n",
		"coverage:\n  ref_namespace: refs/heads\n",
		"coverage:\n  ref_namespace: refs/tags/crossrev\n",
		"coverage:\n  ref_namespace: crossrev\n",
	} {
		if _, err := load(t, "version: 2\n"+bad); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
}

func TestReviewersRefusesAnUnsafeSlotID(t *testing.T) {
	if _, err := load(t, "version: 2\nreviewers:\n  - id: a/b\n    harness: codex\n"); err == nil {
		t.Fatal("a slot id with a path separator loaded")
	}
}

func TestReviewerShorthandMapsToOneSlot(t *testing.T) {
	got := loadYAML(t, "version: 2\nreviewer:\n  harness: codex\n").Reviewers()
	if len(got) != 1 || got[0].ID != "reviewer1" || got[0].Harness != "codex" {
		t.Fatalf("shorthand mapping: %+v", got)
	}
}

// The plural shape parses, validates and round-trips so no config breaks when
// panels arrive. Nothing in this release executes one, and a configuration
// that parses while nothing runs it is worse than one that refuses (C7).
func TestTwoReviewersAreRefusedAsNotYetSupported(t *testing.T) {
	_, err := load(t, "version: 2\nreviewers:\n  - id: reviewer1\n    harness: codex\n  - id: reviewer2\n    harness: claude\n")
	if err == nil {
		t.Fatal("a two-reviewer config loaded; nothing would execute it")
	}
	if !strings.Contains(err.Error(), "one reviewer") {
		t.Fatalf("the refusal does not say what is unsupported: %v", err)
	}
}
