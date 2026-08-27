package config_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/config"
	"github.com/carlosboeing/crossrev/internal/core"
)

// The parity vectors were captured from the Bash functions once, and Go reads
// the same file at the same path and never writes it. A test that could
// regenerate its own oracle is not an oracle.
const parityFixture = "tests/fixtures/parity/config_merge.json"

// baseSHA is the revision every base-revision case is replayed at. The vectors
// record the revision as the literal placeholder `<base_sha>`, because the real
// value depends on how the fixture repository was built; tests/test-parity.sh
// normalises the same way at tests/test-parity.sh:210.
const baseSHA = "0913bf7b99dcecf746d0e6fcef5a9c1d64aaf3b0"

var fortyHex = regexp.MustCompile(`[0-9a-f]{40}`)

type parityFile struct {
	Cases []struct {
		Name         string          `json:"name"`
		RepoYAML     json.RawMessage `json:"repo_yaml"`
		OperatorYAML json.RawMessage `json:"operator_yaml"`
		BaseSHA      json.RawMessage `json:"base_sha"`
		Merged       json.RawMessage `json:"merged"`
	} `json:"cases"`
	Refusals []struct {
		Name   string   `json:"name"`
		Family string   `json:"family"`
		Driver string   `json:"driver"`
		Call   []string `json:"call"`
		Config string   `json:"config"`
		Error  string   `json:"error"`
	} `json:"refusals"`
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

func loadParity(t *testing.T) parityFile {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repoRoot(t), parityFixture))
	if err != nil {
		t.Fatalf("read %s: %v", parityFixture, err)
	}
	var parsed parityFile
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("decode %s: %v", parityFixture, err)
	}
	return parsed
}

// optionalString reports the string a nullable fixture field holds, and whether
// it was present at all. `null` and `""` are different inputs: one means no
// file, the other means a file that exists and states nothing.
func optionalString(t *testing.T, raw json.RawMessage) (string, bool) {
	t.Helper()
	if len(raw) == 0 || string(raw) == "null" {
		return "", false
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatalf("decode fixture field %q: %v", string(raw), err)
	}
	return s, true
}

// files is a ShowFile over an in-memory tree. The empty revision key holds the
// working tree; a revision key holds what that revision has.
type files map[string]map[string]string

func (f files) show() config.ShowFile {
	return func(_ context.Context, revision core.Revision, path string) ([]byte, config.FileStatus, error) {
		tree, ok := f[revision.SHA()]
		if !ok {
			return nil, config.NotFound, nil
		}
		content, ok := tree[path]
		if !ok {
			return nil, config.NotFound, nil
		}
		return []byte(content), config.IsFile, nil
	}
}

func decodeJSON(t *testing.T, raw []byte) any {
	t.Helper()
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	return value
}

func TestConfigMergeParity(t *testing.T) {
	fixture := loadParity(t)
	if len(fixture.Cases) == 0 {
		t.Fatal("the fixture records no merge cases")
	}
	operatorPath := config.OperatorPath()

	for _, testCase := range fixture.Cases {
		t.Run(testCase.Name, func(t *testing.T) {
			repoYAML, repoPresent := optionalString(t, testCase.RepoYAML)
			operatorYAML, operatorPresent := optionalString(t, testCase.OperatorYAML)
			_, atBase := optionalString(t, testCase.BaseSHA)

			tree := files{"": {}}
			if operatorPresent {
				tree[""][operatorPath] = operatorYAML
			}

			base := core.Revision{}
			if atBase {
				revision, err := core.NewRevision(baseSHA)
				if err != nil {
					t.Fatalf("build the base revision: %v", err)
				}
				base = revision
				tree[baseSHA] = map[string]string{}
				if repoPresent {
					// The vectors replay a base-revision case through
					// `.crossrev.yml` (tests/test-parity.sh:136), which is
					// also what proves the fallback is read there.
					tree[baseSHA][".crossrev.yml"] = repoYAML
				}
			} else if repoPresent {
				tree[""][".github/crossrev.yml"] = repoYAML
			}

			loaded, err := config.Load(context.Background(), base, tree.show())
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			actual, err := loaded.MergedJSON()
			if err != nil {
				t.Fatalf("MergedJSON: %v", err)
			}
			if want, got := decodeJSON(t, testCase.Merged), decodeJSON(t, actual); !reflect.DeepEqual(want, got) {
				t.Errorf("merged config differs\n want: %s\n  got: %s", testCase.Merged, actual)
			}
		})
	}
}

func TestConfigRefusalParity(t *testing.T) {
	fixture := loadParity(t)
	if len(fixture.Refusals) == 0 {
		t.Fatal("the fixture records no refusals")
	}

	for _, vector := range fixture.Refusals {
		t.Run(vector.Name, func(t *testing.T) {
			tree := files{"": {}}
			base := core.Revision{}

			switch vector.Driver {
			case "load_at_base":
				revision, err := core.NewRevision(baseSHA)
				if err != nil {
					t.Fatalf("build the base revision: %v", err)
				}
				base = revision
				tree[baseSHA] = map[string]string{".github/crossrev.yml": vector.Config}
			default:
				tree[""][".github/crossrev.yml"] = vector.Config
			}

			loaded, err := config.Load(context.Background(), base, tree.show())
			if err == nil && vector.Driver == "call" {
				err = callRefusal(t, loaded, vector.Call)
			}
			if err == nil {
				t.Fatalf("expected a refusal, got none")
			}
			var refusal *config.Refusal
			if !asRefusal(err, &refusal) {
				t.Fatalf("expected a *config.Refusal, got %T: %v", err, err)
			}
			// A base-revision refusal names the revision it read, and the
			// revision depends on how the replay built its repository rather
			// than on the behaviour. Both sides carry the placeholder.
			actual := refusal.Rendered()
			if vector.Driver == "load_at_base" {
				actual = fortyHex.ReplaceAllString(actual, "<base_sha>")
			}
			if actual != vector.Error {
				t.Errorf("refusal text differs\n want: %q\n  got: %q", vector.Error, actual)
			}
		})
	}
}

func asRefusal(err error, target **config.Refusal) bool {
	refusal, ok := err.(*config.Refusal)
	if ok {
		*target = refusal
	}
	return ok
}

func callRefusal(t *testing.T, loaded *config.Config, call []string) error {
	t.Helper()
	if len(call) == 0 {
		t.Fatalf("the vector names no call")
	}
	switch call[0] {
	case "cfg_endpoint":
		_, err := loaded.Endpoint(call[1])
		return err
	default:
		t.Fatalf("the vector names a driver this test does not run: %q", call[0])
		return nil
	}
}
