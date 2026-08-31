package harness_test

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/carlosboeing/crossrev/internal/harness"
)

// The opencode config paths, as lib/adapters/opencode.sh:123 and :151 spell
// them under the isolation directory.
const (
	opencodeConfigFile = "config.json"
	opencodeConfigDir  = "config-home"
)

// A leg opencode cannot be isolated for is refused, never run.
//
// The isolation config is the whole permission boundary for this harness:
// `"*": "deny"` under everything with the write flag flipping one key
// (lib/adapters/opencode.sh:110-151). opencode IGNORES a config it cannot read
// rather than refusing, so a Spec built after a failed write starts a leg with
// opencode's allow-by-default surface — on the reading leg, which is the one
// that reads attacker-authored diff text.
//
// Three `return nil` mutations survived here. Each is one of the cases below.
func TestOpencodeRefusesALegItCannotIsolate(t *testing.T) {
	doc := descriptors(t)
	adapter, known := harness.For(doc, "opencode")
	if !known {
		t.Fatal("the descriptor names no opencode adapter")
	}

	tests := []struct {
		name string
		// break makes the isolation write fail, and answers nothing.
		breakIt func(t *testing.T, inv *harness.Invocation)
	}{
		{
			name: "the scratch path is a file, so the config directory cannot be made",
			breakIt: func(t *testing.T, inv *harness.Invocation) {
				scratch := filepath.Join(t.TempDir(), "scratch")
				if err := os.WriteFile(scratch, []byte("not a directory"), 0o600); err != nil {
					t.Fatalf("writing the blocking file: %v", err)
				}
				inv.Scratch = scratch
			},
		},
		{
			name: "the config path is already a directory, so the config cannot be written",
			breakIt: func(t *testing.T, inv *harness.Invocation) {
				if err := os.MkdirAll(filepath.Join(inv.Scratch, opencodeConfigFile), 0o700); err != nil {
					t.Fatalf("writing the blocking directory: %v", err)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inv := invocation(t, "opencode", false)
			tt.breakIt(t, &inv)

			spec, err := adapter.Spec(inv)
			if err == nil {
				t.Fatalf("the leg was built with no isolation config: %v", spec.Args)
			}
			var refusal *harness.Refusal
			if !errors.As(err, &refusal) {
				t.Fatalf("err = %v, want a *harness.Refusal", err)
			}
			if refusal.Kind != harness.ErrScratch {
				t.Errorf("Kind = %v, want ErrScratch", refusal.Kind)
			}
			if spec.Path != "" || len(spec.Args) != 0 {
				t.Errorf("a refused leg still answered a spec: %+v", spec)
			}
		})
	}
}

// The isolation config and its directory carry the modes the Bash gives them.
//
// A config any other process on the machine may rewrite is not an isolation
// boundary, and neither mode is asserted anywhere else: both mutations survived.
// 0600 and 0700 are what `mktemp -d` plus a plain shell redirection produce
// under the default umask (lib/adapters/opencode.sh:122-151).
func TestOpencodeIsolationConfigIsWrittenPrivately(t *testing.T) {
	doc := descriptors(t)
	adapter, _ := harness.For(doc, "opencode")

	for _, write := range []bool{false, true} {
		leg := "review"
		if write {
			leg = "resolve"
		}
		t.Run(leg, func(t *testing.T) {
			inv := invocation(t, "opencode", write)
			if _, err := adapter.Spec(inv); err != nil {
				t.Fatalf("building the spec: %v", err)
			}

			configPath := filepath.Join(inv.Scratch, opencodeConfigFile)
			config, err := os.Stat(configPath)
			if err != nil {
				t.Fatalf("the isolation config was not written: %v", err)
			}
			if got := config.Mode().Perm(); got != fs.FileMode(0o600) {
				t.Errorf("config mode = %#o, want 0600: any other process could rewrite the permission block", got)
			}
			home, err := os.Stat(filepath.Join(inv.Scratch, opencodeConfigDir))
			if err != nil {
				t.Fatalf("the config directory was not made: %v", err)
			}
			if !home.IsDir() {
				t.Fatal("the config home is not a directory")
			}
			if got := home.Mode().Perm(); got != fs.FileMode(0o700) {
				t.Errorf("config home mode = %#o, want 0700", got)
			}

			// The file is only a boundary if it says what it is supposed to
			// say, so the write flag is read back out of it rather than
			// trusted.
			raw, err := os.ReadFile(configPath) //nolint:gosec // a path this test made
			if err != nil {
				t.Fatalf("reading the isolation config: %v", err)
			}
			var parsed map[string]any
			if err := json.Unmarshal(raw, &parsed); err != nil {
				t.Fatalf("the isolation config is not valid JSON: %v", err)
			}
			wantEdit := "deny"
			if write {
				wantEdit = "allow"
			}
			permission, _ := parsed["permission"].(map[string]any)
			if permission == nil {
				t.Fatal("the isolation config carries no permission block")
			}
			if got, _ := permission["edit"].(string); got != wantEdit {
				t.Errorf("permission.edit = %q, want %q for a %s leg", got, wantEdit, leg)
			}
			if got, _ := permission["*"].(string); got != "deny" {
				t.Errorf(`permission["*"] = %q, want deny under everything`, got)
			}
		})
	}
}
