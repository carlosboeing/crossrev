package sandbox_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/sandbox"
)

const (
	pathsFixture     = "tests/fixtures/parity/paths.json"
	descriptorSource = "assets/harnesses.json"
)

type pathsVectors struct {
	QuarantineDir     string   `json:"quarantine_dir"`
	QuarantinedPaths  []string `json:"quarantined_paths"`
	SandboxArgsByName []struct {
		Harness string `json:"harness"`
		Args    string `json:"args"`
	} `json:"sandbox_args"`
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("no go.mod above %s", dir)
		}
		dir = parent
	}
}

func loadVectors(t *testing.T) pathsVectors {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repoRoot(t), pathsFixture))
	if err != nil {
		t.Fatalf("read %s: %v", pathsFixture, err)
	}
	var vectors pathsVectors
	if err := json.Unmarshal(raw, &vectors); err != nil {
		t.Fatalf("decode %s: %v", pathsFixture, err)
	}
	return vectors
}

func shippedDescriptor(t *testing.T) sandbox.Descriptor {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repoRoot(t), descriptorSource))
	if err != nil {
		t.Fatalf("read %s: %v", descriptorSource, err)
	}
	descriptor, err := sandbox.LoadDescriptor(raw)
	if err != nil {
		t.Fatalf("load %s: %v", descriptorSource, err)
	}
	return descriptor
}

func TestQuarantineDirParity(t *testing.T) {
	if want := loadVectors(t).QuarantineDir; sandbox.QuarantineDir != want {
		t.Errorf("QuarantineDir = %q, want %q", sandbox.QuarantineDir, want)
	}
}

// The list is every quarantine entry of every harness plus the shared ones,
// sorted and deduplicated. It is read from the descriptor the tool ships rather
// than written out here, so a harness added to assets/harnesses.json changes both
// sides at once.
func TestQuarantinedPathsParity(t *testing.T) {
	want := loadVectors(t).QuarantinedPaths
	if len(want) == 0 {
		t.Fatal("the fixture records no quarantined paths")
	}
	got := shippedDescriptor(t).Paths()
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("paths differ\n want: %v\n  got: %v", want, got)
	}
}

// The shell joins the arguments into one string because its adapters word-split
// it. An argv runner needs no join, so the argument list is the value and the
// join is only how the vector was frozen.
func TestSandboxArgsParity(t *testing.T) {
	vectors := loadVectors(t)
	if len(vectors.SandboxArgsByName) == 0 {
		t.Fatal("the fixture records no sandbox arguments")
	}
	descriptor := shippedDescriptor(t)
	for _, vector := range vectors.SandboxArgsByName {
		t.Run(vector.Harness, func(t *testing.T) {
			if got := strings.Join(descriptor.ArgsFor(vector.Harness), " "); got != vector.Args {
				t.Errorf("args = %q, want %q", got, vector.Args)
			}
		})
	}
}

// A name no harness carries yields nothing, which is what jq's `// empty`
// answers for an unknown selection (harness_get at lib/harnesses.sh:131).
func TestSandboxArgsForAnUnknownHarness(t *testing.T) {
	if got := shippedDescriptor(t).ArgsFor("nobody"); len(got) != 0 {
		t.Errorf("args = %v, want none", got)
	}
}

// The descriptor names paths that are handed to a move and a recursive delete
// (lib/harnesses.sh:5-8), so a path that is absolute, empty or climbs out of
// the checkout is refused before anything acts on it.
func TestLoadDescriptorRefusesAnEscapingPath(t *testing.T) {
	tests := []struct {
		name string
		json string
	}{
		{name: "absolute in a harness entry", json: `{"harnesses":[{"name":"claude","quarantine":["/etc/passwd"]}]}`},
		{name: "climbing in a harness entry", json: `{"harnesses":[{"name":"claude","quarantine":["../../etc/passwd"]}]}`},
		{name: "a climbing segment in the middle", json: `{"harnesses":[{"name":"claude","quarantine":["a/../../etc"]}]}`},
		{name: "empty in a harness entry", json: `{"harnesses":[{"name":"claude","quarantine":[""]}]}`},
		{name: "absolute in the shared list", json: `{"quarantine_shared":["/etc/passwd"]}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := sandbox.LoadDescriptor([]byte(tt.json)); err == nil {
				t.Error("the descriptor was accepted")
			}
		})
	}
}

func TestLoadDescriptorRefusesRubbish(t *testing.T) {
	if _, err := sandbox.LoadDescriptor([]byte("not json")); err == nil {
		t.Error("a descriptor that is not JSON was accepted")
	}
}
