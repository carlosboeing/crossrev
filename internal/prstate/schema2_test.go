package prstate_test

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/prstate"
)

func mustRev(t *testing.T, sha string) core.Revision {
	if t != nil {
		t.Helper()
	}
	rev, err := core.NewRevision(sha)
	if err != nil {
		if t != nil {
			t.Fatal(err)
		}
		panic(err)
	}
	return rev
}

func validFixtureGeneration(t *testing.T, form string) prstate.Generation {
	if t != nil {
		t.Helper()
	}
	baseRev := mustRev(t, strings.Repeat("1", 40))
	headRev := mustRev(t, strings.Repeat("2", 40))
	full := prstate.Generation{
		Gen:      1,
		Revision: core.RevisionPair{Base: baseRev, Head: headRev},
		Engine:   core.FileEngineID(),
		Slot:     prstate.DefaultSlot,
		Producer: prstate.Producer{
			Harness:  "agy",
			Model:    "gemini-2.5",
			Effort:   "high",
			Endpoint: "https://example.com",
		},
		Form:  prstate.GenerationFull,
		Paths: []string{"a.go", "b.go"},
		Records: []prstate.Record{
			{
				Type:       prstate.CoverageRecordUnit,
				UnitID:     "0123456789abcdef",
				PathIndex:  0,
				Kind:       prstate.CoverageGranularityFile,
				Change:     "modified",
				BodyDigest: strings.Repeat("a", 64),
				Verdict:    prstate.Some("no_issue"),
				FindingIDs: []string{},
				Evidence: []prstate.Evidence{
					{
						Path:      "a.go",
						Revision:  prstate.Some(headRev.SHA()),
						StartLine: prstate.Some(1),
						EndLine:   prstate.Some(10),
						Source:    "reviewer",
						Note:      prstate.Null[string](),
					},
				},
				Reason:   prstate.Null[string](),
				Supplied: prstate.Some(prstate.SuppliedInput{Digest: strings.Repeat("c", 64), Form: "full_text", Truncated: false}),
				Reaction: prstate.UnimplementedReaction(),
			},
			{
				Type:        prstate.CoverageRecordOutstanding,
				UnitID:      "fedcba9876543210",
				PathIndex:   1,
				Kind:        prstate.CoverageGranularityFile,
				Change:      "added",
				BodyDigest:  strings.Repeat("b", 64),
				Verdict:     prstate.Null[string](),
				FindingIDs:  nil,
				Evidence:    []prstate.Evidence{},
				Reason:      prstate.Some("budget exhausted"),
				Supplied:    prstate.Null[prstate.SuppliedInput](),
				Reaction:    prstate.UnimplementedReaction(),
			},
		},
		Advisory:    prstate.Advisory{Count: 0, Rules: []string{"convention"}, Limits: []prstate.AdvisoryLimit{}},
		Excluded:    []prstate.CoverageExclusion{{Path: "vendored.go", Reason: "vendored"}},
		ScopeReport: prstate.ScopeReport{ExaminedScope: "scope", KnownLimits: []string{}},
	}
	if form == prstate.GenerationCompact {
		return prstate.CompactGeneration(full)
	}
	return full
}

func generationOfPaths(t *testing.T, n int) prstate.Generation {
	t.Helper()
	paths := make([]string, n)
	records := make([]prstate.Record, n)
	baseRev := mustRev(t, strings.Repeat("1", 40))
	headRev := mustRev(t, strings.Repeat("2", 40))
	for i := 0; i < n; i++ {
		paths[i] = fmt.Sprintf("dir/file_%04d.go", i)
		var verdict prstate.Opt[string]
		var recType string
		switch i % 5 {
		case 0:
			recType = prstate.CoverageRecordOutstanding
			verdict = prstate.Null[string]()
		case 1:
			recType = prstate.CoverageRecordUnit
			verdict = prstate.Some("no_issue")
		case 2:
			recType = prstate.CoverageRecordUnit
			verdict = prstate.Some("finding")
		case 3:
			recType = prstate.CoverageRecordUnit
			verdict = prstate.Some("not_affected")
		case 4:
			recType = prstate.CoverageRecordUnit
			verdict = prstate.Some("could_not_review")
		}
		var findingIDs []string
		if recType == prstate.CoverageRecordUnit {
			findingIDs = []string{}
		}
		records[i] = prstate.Record{
			Type:       recType,
			UnitID:     fmt.Sprintf("%016x", i),
			PathIndex:  i,
			Kind:       prstate.CoverageGranularityFile,
			Change:     "modified",
			BodyDigest: strings.Repeat("a", 64),
			Verdict:    verdict,
			FindingIDs: findingIDs,
			Evidence:   []prstate.Evidence{},
			Reason:     prstate.Null[string](),
			Supplied:   prstate.Null[prstate.SuppliedInput](),
			Reaction:   prstate.UnimplementedReaction(),
		}
	}
	return prstate.Generation{
		Gen:         1,
		Revision:    core.RevisionPair{Base: baseRev, Head: headRev},
		Engine:      core.FileEngineID(),
		Slot:        prstate.DefaultSlot,
		Producer:    prstate.Producer{Harness: "agy", Model: "gemini-2.5", Effort: "high", Endpoint: "https://example.com"},
		Form:        prstate.GenerationFull,
		Paths:       paths,
		Records:     records,
		Advisory:    prstate.Advisory{Count: 0, Rules: []string{"convention"}, Limits: []prstate.AdvisoryLimit{}},
		Excluded:    []prstate.CoverageExclusion{},
		ScopeReport: prstate.ScopeReport{ExaminedScope: "scope", KnownLimits: []string{}},
	}
}

func TestSchema2RoundTripsFullAndCompact(t *testing.T) {
	for _, form := range []string{prstate.GenerationFull, prstate.GenerationCompact} {
		gen := validFixtureGeneration(t, form)
		manifest, records, err := prstate.EncodeGenerationV2(gen)
		if err != nil {
			t.Fatalf("encode %s: %v", form, err)
		}
		back, err := prstate.DecodeGenerationV2(manifest, records)
		if err != nil {
			t.Fatalf("decode %s: %v", form, err)
		}
		if !reflect.DeepEqual(gen, back) {
			t.Errorf("%s does not round-trip\n want: %+v\n  got: %+v", form, gen, back)
		}
	}
}

func TestCompactRecordsArePositionalAndSmall(t *testing.T) {
	gen := prstate.CompactGeneration(generationOfPaths(t, 900))
	_, records, err := prstate.EncodeGenerationV2(gen)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) > 2048 {
		t.Fatalf("900 compact records encode to %d bytes; the positional form measures 955 and an object per record measures 48491", len(records))
	}
}

func TestSchema2RefusesMismatchedRecordsDigest(t *testing.T) {
	manifest, records, err := prstate.EncodeGenerationV2(validFixtureGeneration(t, prstate.GenerationFull))
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	altered := bytes.Replace(records, []byte(`"no_issue"`), []byte(`"finding"`), 1)
	if _, err := prstate.DecodeGenerationV2(manifest, altered); err == nil {
		t.Fatal("altered records decoded; the manifest digest does not bind them")
	}
}

func TestSchema2RefusesUnknownKeyUnknownVersionAndShortVerdicts(t *testing.T) {
	// extra record key; "v":3; a verdicts string shorter than the path table;
	// a verdict character outside the enum
	t.Run("extra record key in full form", func(t *testing.T) {
		manifest, records, err := prstate.EncodeGenerationV2(validFixtureGeneration(t, prstate.GenerationFull))
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		altered := bytes.Replace(records, []byte(`"type":"unit"`), []byte(`"type":"unit","extra":"forbidden"`), 1)
		if _, err := prstate.DecodeGenerationV2(manifest, altered); err == nil {
			t.Fatal("records with extra key decoded")
		}
	})

	t.Run("extra manifest key", func(t *testing.T) {
		manifest, records, err := prstate.EncodeGenerationV2(validFixtureGeneration(t, prstate.GenerationFull))
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		altered := bytes.Replace(manifest, []byte(`"v":2`), []byte(`"v":2,"extra":"forbidden"`), 1)
		if _, err := prstate.DecodeGenerationV2(altered, records); err == nil {
			t.Fatal("manifest with extra key decoded")
		}
	})

	t.Run("manifest version 3", func(t *testing.T) {
		manifest, records, err := prstate.EncodeGenerationV2(validFixtureGeneration(t, prstate.GenerationFull))
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		altered := bytes.Replace(manifest, []byte(`"v":2`), []byte(`"v":3`), 1)
		if _, err := prstate.DecodeGenerationV2(altered, records); err == nil {
			t.Fatal("manifest with v=3 decoded")
		}
	})

	t.Run("records version 3", func(t *testing.T) {
		manifest, records, err := prstate.EncodeGenerationV2(validFixtureGeneration(t, prstate.GenerationFull))
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		altered := bytes.Replace(records, []byte(`"v":2`), []byte(`"v":3`), 1)
		if _, err := prstate.DecodeGenerationV2(manifest, altered); err == nil {
			t.Fatal("records with v=3 decoded")
		}
	})

	t.Run("short verdicts string in compact form", func(t *testing.T) {
		manifest, records, err := prstate.EncodeGenerationV2(validFixtureGeneration(t, prstate.GenerationCompact))
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		// replace 2-char verdicts with 1-char verdicts
		altered := bytes.Replace(records, []byte(`"verdicts":"10"`), []byte(`"verdicts":"1"`), 1)
		if _, err := prstate.DecodeGenerationV2(manifest, altered); err == nil {
			t.Fatal("compact records with short verdicts decoded")
		}
	})

	t.Run("long verdicts string in compact form", func(t *testing.T) {
		manifest, records, err := prstate.EncodeGenerationV2(validFixtureGeneration(t, prstate.GenerationCompact))
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		altered := bytes.Replace(records, []byte(`"verdicts":"10"`), []byte(`"verdicts":"100"`), 1)
		if _, err := prstate.DecodeGenerationV2(manifest, altered); err == nil {
			t.Fatal("compact records with long verdicts decoded")
		}
	})

	t.Run("verdict char outside enum in compact form", func(t *testing.T) {
		manifest, records, err := prstate.EncodeGenerationV2(validFixtureGeneration(t, prstate.GenerationCompact))
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		altered := bytes.Replace(records, []byte(`"verdicts":"10"`), []byte(`"verdicts":"19"`), 1)
		if _, err := prstate.DecodeGenerationV2(manifest, altered); err == nil {
			t.Fatal("compact records with verdict char 9 decoded")
		}
	})
}

func TestCompactGenerationKeepsTheEnvelope(t *testing.T) {
	gen := validFixtureGeneration(t, prstate.GenerationFull)
	compact := prstate.CompactGeneration(gen)
	if compact.Producer != gen.Producer || compact.Slot != gen.Slot || !reflect.DeepEqual(compact.Paths, gen.Paths) {
		t.Fatal("the compact form lost the envelope, so invalidation would stop working")
	}
	for _, r := range compact.Records {
		if len(r.Evidence) != 0 || len(r.FindingIDs) != 0 || r.BodyDigest != "" || r.Supplied.Present() {
			t.Fatalf("a compact record carries detail: %+v", r)
		}
	}
}

func TestSchema2CorpusSeeds(t *testing.T) {
	corpusDir := filepath.Join("testdata", "schema2")
	if err := os.MkdirAll(corpusDir, 0755); err != nil {
		t.Fatalf("mkdir testdata/schema2: %v", err)
	}
	for _, form := range []string{prstate.GenerationFull, prstate.GenerationCompact} {
		gen := validFixtureGeneration(t, form)
		manifest, records, err := prstate.EncodeGenerationV2(gen)
		if err != nil {
			t.Fatalf("encode %s: %v", form, err)
		}
		if form == prstate.GenerationFull {
			if err := os.WriteFile(filepath.Join(corpusDir, "manifest_full.json"), manifest, 0644); err != nil {
				t.Fatalf("write manifest_full.json: %v", err)
			}
			if err := os.WriteFile(filepath.Join(corpusDir, "records_full.json"), records, 0644); err != nil {
				t.Fatalf("write records_full.json: %v", err)
			}
		} else {
			if err := os.WriteFile(filepath.Join(corpusDir, "manifest_compact.json"), manifest, 0644); err != nil {
				t.Fatalf("write manifest_compact.json: %v", err)
			}
			if err := os.WriteFile(filepath.Join(corpusDir, "records_compact.json"), records, 0644); err != nil {
				t.Fatalf("write records_compact.json: %v", err)
			}
		}
	}
}

