package intel_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/intel"
)

// oracleCase is one row of tests/fixtures/intelligence/file-units.json, which
// is frozen before the implementation and never generated from it.
type oracleCase struct {
	Name        string `json:"name"`
	Path        string `json:"path"`
	OldPath     string `json:"old_path"`
	Change      string `json:"change"`
	Revision    string `json:"revision"`
	Body        string `json:"body"`
	Available   bool   `json:"available"`
	Binary      bool   `json:"binary"`
	Reason      string `json:"reason"`
	Error       string `json:"error"`
	UnitID      string `json:"unit_id"`
	BodyDigest  string `json:"body_digest"`
}

type oracleFile struct {
	Engine   string       `json:"engine"`
	EngineID string       `json:"engine_id"`
	Cases    []oracleCase `json:"cases"`
}

func loadOracle(t *testing.T) oracleFile {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("no go.mod above the test directory")
		}
		dir = parent
	}
	raw, err := os.ReadFile(filepath.Join(dir, "tests", "fixtures", "intelligence", "file-units.json"))
	if err != nil {
		t.Fatalf("read the frozen oracle: %v", err)
	}
	var oracle oracleFile
	if err := json.Unmarshal(raw, &oracle); err != nil {
		t.Fatalf("decode the frozen oracle: %v", err)
	}
	return oracle
}

var (
	stubBaseSHA = "1111111111111111111111111111111111111111"
	stubHeadSHA = "2222222222222222222222222222222222222222"
)

func stubRevisions(t *testing.T) (base, head core.Revision) {
	t.Helper()
	var err error
	if base, err = core.NewRevision(stubBaseSHA); err != nil {
		t.Fatalf("base revision: %v", err)
	}
	if head, err = core.NewRevision(stubHeadSHA); err != nil {
		t.Fatalf("head revision: %v", err)
	}
	return base, head
}

// stubReader serves oracle bodies by revision and path, reporting the cased
// error for the inaccessible path.
type stubReader struct {
	bodies map[string]map[string]oracleCase
}

func (s stubReader) Read(_ context.Context, revision core.Revision, path string) (intel.FileBody, error) {
	for _, c := range s.bodies[revision.SHA()] {
		if c.Path == path || c.OldPath == path {
			if !c.Available {
				return intel.FileBody{Unavailable: true, Reason: c.Error}, nil
			}
			return intel.FileBody{Data: []byte(c.Body)}, nil
		}
	}
	return intel.FileBody{Unavailable: true, Reason: "no such path at " + revision.Short()}, nil
}

// TestRequiredFilesMatchesTheFrozenOracle replays every frozen case through
// RequiredFiles and requires the path, kind, content revision, UnitID and
// body digest to match the oracle exactly.
func TestRequiredFilesMatchesTheFrozenOracle(t *testing.T) {
	oracle := loadOracle(t)
	if len(oracle.Cases) != 8 {
		t.Fatalf("oracle holds %d cases, want 8", len(oracle.Cases))
	}
	base, head := stubRevisions(t)

	bySHA := map[string]map[string]oracleCase{stubBaseSHA: {}, stubHeadSHA: {}}
	var changes []core.FileChange
	for _, c := range oracle.Cases {
		kind, err := core.ParseChangeKind(c.Change)
		if err != nil {
			t.Fatalf("oracle case %q names %q: %v", c.Name, c.Change, err)
		}
		changes = append(changes, core.FileChange{OldPath: c.OldPath, Path: c.Path, Kind: kind})
		sha := stubHeadSHA
		if c.Revision == "base" {
			sha = stubBaseSHA
		}
		bySHA[sha][c.Path] = c
		if c.OldPath != "" {
			bySHA[sha][c.OldPath] = c
		}
	}

	scope, err := intel.RequiredFiles(context.Background(), changes, stubReader{bodies: bySHA}, base, head, nil)
	if err != nil {
		t.Fatalf("RequiredFiles: %v", err)
	}
	if scope.Engine != oracle.Engine {
		t.Errorf("scope engine = %q, want %q", scope.Engine, oracle.Engine)
	}
	if scope.EngineID != oracle.EngineID {
		t.Errorf("scope engine id = %q, want %q", scope.EngineID, oracle.EngineID)
	}
	if len(scope.Excluded) != 0 {
		t.Errorf("scope excludes %v, want none", scope.Excluded)
	}
	if len(scope.Required) != len(oracle.Cases) {
		t.Fatalf("scope holds %d required units, want %d", len(scope.Required), len(oracle.Cases))
	}
	if !sort.SliceIsSorted(scope.Required, func(i, j int) bool {
		return scope.Required[i].Path < scope.Required[j].Path
	}) {
		t.Error("required units are not sorted by current path")
	}
	byPath := map[string]oracleCase{}
	for _, c := range oracle.Cases {
		byPath[c.Path] = c
	}
	for _, unit := range scope.Required {
		want, ok := byPath[unit.Path]
		if !ok {
			t.Errorf("unexpected required unit %q", unit.Path)
			continue
		}
		t.Run(want.Name, func(t *testing.T) {
			if string(unit.Change) != want.Change {
				t.Errorf("change = %q, want %q", unit.Change, want.Change)
			}
			wantSHA := stubHeadSHA
			if want.Revision == "base" {
				wantSHA = stubBaseSHA
			}
			if unit.ContentRevision.SHA() != wantSHA {
				t.Errorf("content revision = %q, want %q", unit.ContentRevision.SHA(), wantSHA)
			}
			if string(unit.ID) != want.UnitID {
				t.Errorf("unit id = %q, want %q", unit.ID, want.UnitID)
			}
			if unit.BodyDigest != want.BodyDigest {
				t.Errorf("body digest = %q, want %q", unit.BodyDigest, want.BodyDigest)
			}
			if unit.Available != want.Available {
				t.Errorf("available = %v, want %v", unit.Available, want.Available)
			}
			if unit.Binary != want.Binary {
				t.Errorf("binary = %v, want %v", unit.Binary, want.Binary)
			}
			if unit.Available && string(unit.Body) != want.Body {
				t.Errorf("body = %q, want %q", unit.Body, want.Body)
			}
			if !unit.Available && unit.Reason != want.Reason {
				t.Errorf("reason = %q, want %q", unit.Reason, want.Reason)
			}
		})
	}
}

func TestRequiredFilesRecordsVisibleExclusions(t *testing.T) {
	base, head := stubRevisions(t)
	changes := []core.FileChange{
		{Path: "src/keep.go", Kind: core.ChangeModified},
		{Path: "docs/backlog/item.md", Kind: core.ChangeAdded},
		{Path: "docs/backlog/nested/other.md", Kind: core.ChangeAdded},
	}
	reader := stubReader{bodies: map[string]map[string]oracleCase{
		stubBaseSHA: {},
		stubHeadSHA: {
			"src/keep.go": {Path: "src/keep.go", Body: "package keep\n", Available: true},
		},
	}}
	excluded := []intel.Exclusion{{Path: "docs/backlog", Reason: "backlog destination"}}
	scope, err := intel.RequiredFiles(context.Background(), changes, reader, base, head, excluded)
	if err != nil {
		t.Fatalf("RequiredFiles: %v", err)
	}
	if len(scope.Required) != 1 || scope.Required[0].Path != "src/keep.go" {
		t.Fatalf("required = %v, want only src/keep.go", scope.Required)
	}
	if len(scope.Excluded) != 2 {
		t.Fatalf("excluded = %v, want both backlog paths", scope.Excluded)
	}
	for _, e := range scope.Excluded {
		if e.Reason != "backlog destination" {
			t.Errorf("exclusion of %q carries reason %q", e.Path, e.Reason)
		}
	}
}

func TestRequiredFilesRejectsResidualUnitIDCollisions(t *testing.T) {
	base, head := stubRevisions(t)
	changes := []core.FileChange{
		{Path: "src/dup.go", Kind: core.ChangeAdded},
		{Path: "src/dup.go", Kind: core.ChangeModified},
	}
	reader := stubReader{bodies: map[string]map[string]oracleCase{stubBaseSHA: {}, stubHeadSHA: {}}}
	if _, err := intel.RequiredFiles(context.Background(), changes, reader, base, head, nil); err == nil {
		t.Fatal("RequiredFiles accepted two changes with one UnitID, want an error")
	}
}

type failingReader struct{}

func (failingReader) Read(_ context.Context, _ core.Revision, _ string) (intel.FileBody, error) {
	return intel.FileBody{}, errors.New("quarantine store offline")
}

func TestRequiredFilesKeepsUnreadableContentAsAnObligation(t *testing.T) {
	base, head := stubRevisions(t)
	changes := []core.FileChange{{Path: "src/locked.go", Kind: core.ChangeModified}}
	scope, err := intel.RequiredFiles(context.Background(), changes, failingReader{}, base, head, nil)
	if err != nil {
		t.Fatalf("RequiredFiles: %v", err)
	}
	if len(scope.Required) != 1 {
		t.Fatalf("required holds %d units, want 1", len(scope.Required))
	}
	unit := scope.Required[0]
	if unit.Available {
		t.Error("unreadable unit reports available")
	}
	if unit.Reason == "" {
		t.Error("unreadable unit carries no access reason")
	}
	if unit.BodyDigest == "" {
		t.Error("unreadable unit carries no body digest")
	}
}
