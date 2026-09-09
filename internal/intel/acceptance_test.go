// Package intel_test acceptance oracle (Task D1): the frozen file-unit,
// advisory and batching vectors replayed as one named acceptance test.
//
// The expected sets are literal data read from
// tests/fixtures/intelligence/file-units.json and compared with production
// discovery, never generated from it. The oracle is frozen before the
// implementation; this test replays it as the specification's acceptance
// gate alongside the binary-driven shell suite.
package intel_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/intel"
)

// acceptanceOracle is the frozen contract this test replays: every required
// path, change kind, content revision, unit identity and body digest the
// production code must return, plus the advisory and batching budgets.
type acceptanceOracle struct {
	Engine   string `json:"engine"`
	EngineID string `json:"engine_id"`
	Cases    []struct {
		Name       string `json:"name"`
		Path       string `json:"path"`
		OldPath    string `json:"old_path"`
		Change     string `json:"change"`
		Revision   string `json:"revision"`
		Body       string `json:"body"`
		Available  bool   `json:"available"`
		Binary     bool   `json:"binary"`
		Reason     string `json:"reason"`
		Error      string `json:"error"`
		UnitID     string `json:"unit_id"`
		BodyDigest string `json:"body_digest"`
	} `json:"cases"`
	Advisory struct {
		MaxSearchHits int `json:"max_search_hits"`
		TermVectors   []struct {
			Body  string   `json:"body"`
			Terms []string `json:"terms"`
		} `json:"term_vectors"`
		Conventions []struct {
			Path       string   `json:"path"`
			Candidates []string `json:"candidates"`
		} `json:"conventions"`
	} `json:"advisory"`
	Batching struct {
		MaxFilesPerBatch int `json:"max_files_per_batch"`
		MaxUnitsPerPass  int `json:"max_units_per_pass"`
		MaxPromptBytes   int `json:"max_prompt_bytes"`
	} `json:"batching"`
}

func loadAcceptanceOracle(t *testing.T) acceptanceOracle {
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
	var oracle acceptanceOracle
	if err := json.Unmarshal(raw, &oracle); err != nil {
		t.Fatalf("decode the frozen oracle: %v", err)
	}
	return oracle
}

// TestReviewIntelligenceAcceptanceOracle replays the frozen discovery oracle
// as the specification's acceptance gate: every required unit's identity,
// digest, revision and availability; the advisory term vectors and closed
// convention table; and the packing budgets. It fails on the pre-slice code
// because no complete change enumeration or UnitID exists there.
func TestReviewIntelligenceAcceptanceOracle(t *testing.T) {
	oracle := loadAcceptanceOracle(t)
	if len(oracle.Cases) != 8 {
		t.Fatalf("oracle holds %d cases, want 8", len(oracle.Cases))
	}
	base, err := core.NewRevision("1111111111111111111111111111111111111111")
	if err != nil {
		t.Fatalf("base revision: %v", err)
	}
	head, err := core.NewRevision("2222222222222222222222222222222222222222")
	if err != nil {
		t.Fatalf("head revision: %v", err)
	}

	// The engine identity is frozen beside the cases: a later change to
	// enumeration, evidence or disposition semantics must change the
	// literal and invalidate prior generations.
	if core.FileEngineVersion != oracle.Engine {
		t.Errorf("engine = %q, want frozen %q", core.FileEngineVersion, oracle.Engine)
	}
	if core.FileEngineID() != oracle.EngineID {
		t.Errorf("engine id = %q, want frozen %q", core.FileEngineID(), oracle.EngineID)
	}

	bySHA := map[string]map[string]struct {
		body      string
		available bool
		err       string
	}{"1111111111111111111111111111111111111111": {}, "2222222222222222222222222222222222222222": {}}
	var changes []core.FileChange
	want := map[string]int{}
	for _, c := range oracle.Cases {
		kind, err := core.ParseChangeKind(c.Change)
		if err != nil {
			t.Fatalf("oracle case %q names %q: %v", c.Name, c.Change, err)
		}
		changes = append(changes, core.FileChange{OldPath: c.OldPath, Path: c.Path, Kind: kind})
		sha := "2222222222222222222222222222222222222222"
		if c.Revision == "base" {
			sha = "1111111111111111111111111111111111111111"
		}
		bySHA[sha][c.Path] = struct {
			body      string
			available bool
			err       string
		}{body: c.Body, available: c.Available, err: c.Error}
		if c.OldPath != "" {
			bySHA[sha][c.OldPath] = struct {
				body      string
				available bool
				err       string
			}{body: c.Body, available: c.Available, err: c.Error}
		}
		want[c.Path]++
	}
	_ = want

	reader := acceptanceReader{bodies: map[string]map[string]acceptanceBody{}}
	for sha, paths := range bySHA {
		m := map[string]acceptanceBody{}
		for path, c := range paths {
			m[path] = acceptanceBody{body: c.body, available: c.available, reason: c.err}
		}
		reader.bodies[sha] = m
	}
	scope, err := intel.RequiredFiles(context.Background(), changes, reader, base, head, nil)
	if err != nil {
		t.Fatalf("RequiredFiles: %v", err)
	}
	if scope.Engine != oracle.Engine || scope.EngineID != oracle.EngineID {
		t.Errorf("scope engine = %q/%q, want frozen %q/%q", scope.Engine, scope.EngineID, oracle.Engine, oracle.EngineID)
	}
	if len(scope.Required) != len(oracle.Cases) {
		t.Fatalf("scope holds %d required units, want %d", len(scope.Required), len(oracle.Cases))
	}
	byPath := map[string]struct {
		change    string
		revision  string
		unitID    string
		digest    string
		available bool
		binary    bool
		body      string
		reason    string
	}{}
	for _, c := range oracle.Cases {
		byPath[c.Path] = struct {
			change    string
			revision  string
			unitID    string
			digest    string
			available bool
			binary    bool
			body      string
			reason    string
		}{change: c.Change, revision: c.Revision, unitID: c.UnitID, digest: c.BodyDigest, available: c.Available, binary: c.Binary, body: c.Body, reason: c.Reason}
	}
	for _, unit := range scope.Required {
		w, ok := byPath[unit.Path]
		if !ok {
			t.Errorf("unexpected required unit %q", unit.Path)
			continue
		}
		if string(unit.Change) != w.change {
			t.Errorf("%s: change = %q, want %q", unit.Path, unit.Change, w.change)
		}
		wantSHA := "2222222222222222222222222222222222222222"
		if w.revision == "base" {
			wantSHA = "1111111111111111111111111111111111111111"
		}
		if unit.ContentRevision.SHA() != wantSHA {
			t.Errorf("%s: content revision = %q, want %q", unit.Path, unit.ContentRevision.SHA(), wantSHA)
		}
		if string(unit.ID) != w.unitID {
			t.Errorf("%s: unit id = %q, want frozen %q", unit.Path, unit.ID, w.unitID)
		}
		if unit.BodyDigest != w.digest {
			t.Errorf("%s: body digest = %q, want frozen %q", unit.Path, unit.BodyDigest, w.digest)
		}
		if unit.Available != w.available {
			t.Errorf("%s: available = %v, want %v", unit.Path, unit.Available, w.available)
		}
		if unit.Binary != w.binary {
			t.Errorf("%s: binary = %v, want %v", unit.Path, unit.Binary, w.binary)
		}
		if unit.Available && string(unit.Body) != w.body {
			t.Errorf("%s: body = %q, want frozen %q", unit.Path, unit.Body, w.body)
		}
		if !unit.Available && unit.Reason != w.reason {
			t.Errorf("%s: reason = %q, want frozen %q", unit.Path, unit.Reason, w.reason)
		}
	}

	// The advisory vectors are literal: the identifier set over each body,
	// and the closed convention table over each path.
	for _, v := range oracle.Advisory.TermVectors {
		if got := intel.SearchTerms([]byte(v.Body)); !reflect.DeepEqual(got, v.Terms) {
			t.Errorf("SearchTerms(%q) = %q, want frozen %q", v.Body, got, v.Terms)
		}
	}
	for _, v := range oracle.Advisory.Conventions {
		if got := intel.AdjacentTestCandidates(v.Path); !reflect.DeepEqual(got, v.Candidates) {
			t.Errorf("AdjacentTestCandidates(%q) = %q, want frozen %q", v.Path, got, v.Candidates)
		}
	}
	if intel.MaxSearchHits != oracle.Advisory.MaxSearchHits {
		t.Errorf("MaxSearchHits = %d, want frozen %d", intel.MaxSearchHits, oracle.Advisory.MaxSearchHits)
	}

	// The batching budgets are literal: file, pass and rendered-byte bounds.
	if intel.MaxFilesPerBatch != oracle.Batching.MaxFilesPerBatch {
		t.Errorf("MaxFilesPerBatch = %d, want frozen %d", intel.MaxFilesPerBatch, oracle.Batching.MaxFilesPerBatch)
	}
	if intel.MaxUnitsPerPass != oracle.Batching.MaxUnitsPerPass {
		t.Errorf("MaxUnitsPerPass = %d, want frozen %d", intel.MaxUnitsPerPass, oracle.Batching.MaxUnitsPerPass)
	}
	if intel.MaxPromptBytes != oracle.Batching.MaxPromptBytes {
		t.Errorf("MaxPromptBytes = %d, want frozen %d", intel.MaxPromptBytes, oracle.Batching.MaxPromptBytes)
	}
}

// TestAcceptanceOracleMutationProvesTheGateIsLive flips one expected byte
// and asserts failure: an oracle compared against itself with one UnitID
// changed must not match, so a production change that alters one identity
// cannot pass silently.
func TestAcceptanceOracleMutationProvesTheGateIsLive(t *testing.T) {
	oracle := loadAcceptanceOracle(t)
	if len(oracle.Cases) == 0 {
		t.Fatal("oracle carries no cases")
	}
	mutated := oracle.Cases[0].UnitID
	if len(mutated) != 16 {
		t.Fatalf("unit id %q is not 16 hex", mutated)
	}
	flipped := "0"
	if mutated[:1] == "0" {
		flipped = "1"
	}
	mutated = flipped + mutated[1:]
	if mutated == oracle.Cases[0].UnitID {
		t.Fatal("the mutation did not change the expected byte")
	}
	got := string(core.FileUnitID(oracle.Cases[0].Path))
	if got == mutated {
		t.Fatalf("mutated id %q matches production %q; the red proof needs a byte the code does not emit", mutated, got)
	}
	if got != oracle.Cases[0].UnitID {
		t.Fatalf("production id %q != frozen %q before any mutation", got, oracle.Cases[0].UnitID)
	}
}

type acceptanceBody struct {
	body      string
	available bool
	reason    string
}

type acceptanceReader struct {
	bodies map[string]map[string]acceptanceBody
}

func (r acceptanceReader) Read(_ context.Context, revision core.Revision, path string) (intel.FileBody, error) {
	if m, ok := r.bodies[revision.SHA()]; ok {
		if c, ok := m[path]; ok {
			if !c.available {
				return intel.FileBody{Unavailable: true, Reason: c.reason}, nil
			}
			return intel.FileBody{Data: []byte(c.body)}, nil
		}
	}
	return intel.FileBody{Unavailable: true, Reason: "no such path at " + revision.Short()}, nil
}
