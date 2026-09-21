package prstate_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/prstate"
)

func FuzzSchema2RoundTrip(f *testing.F) {
	f.Add("a.go", "modified", "no_issue", "reviewer", "verified", "full_text", false, "full")
	f.Add("b.go", "added", "", "reviewer", "waiting", "full_text", false, "full")
	f.Add("c.go", "modified", "finding", "reviewer", "found bug", "diff_only", true, "full")
	f.Add("d.go", "deleted", "not_affected", "reviewer", "deleted", "full_text", false, "compact")
	f.Add("e.go", "renamed", "could_not_review", "reviewer", "binary", "diff_only", false, "compact")

	f.Fuzz(func(t *testing.T, path, change, verdict, source, reason, suppliedForm string, suppliedTruncated bool, form string) {
		if !utf8.ValidString(path) || !utf8.ValidString(reason) || strings.Contains(path, "\x00") || len(path) == 0 {
			t.Skip("invalid UTF-8 or empty or NUL path")
		}
		if form != prstate.GenerationCompact {
			form = prstate.GenerationFull
		}
		if !validFuzzChange(change) {
			change = "modified"
		}
		if !validFuzzSource(source) {
			source = "reviewer"
		}
		if suppliedForm != prstate.SuppliedFormFullText && suppliedForm != prstate.SuppliedFormDiffOnly {
			suppliedForm = prstate.SuppliedFormFullText
		}

		unitID := string(core.FileUnitID(path))
		bodySum := sha256.Sum256([]byte(path + "\n" + change))
		bodyDigest := hex.EncodeToString(bodySum[:])

		suppliedSum := sha256.Sum256([]byte("content of " + path))
		suppliedDigest := hex.EncodeToString(suppliedSum[:])

		baseRev, err := core.NewRevision(strings.Repeat("1", 40))
		if err != nil {
			t.Fatal(err)
		}
		headRev, err := core.NewRevision(strings.Repeat("2", 40))
		if err != nil {
			t.Fatal(err)
		}

		var record prstate.Record
		if verdict == "" {
			record = prstate.Record{
				Type:        prstate.CoverageRecordOutstanding,
				UnitID:      unitID,
				PathIndex:   0,
				Kind:        prstate.CoverageGranularityFile,
				Change:      change,
				BodyDigest:  bodyDigest,
				Verdict:     prstate.Null[string](),
				FindingIDs:  nil,
				Evidence:    []prstate.Evidence{},
				Reason:      prstate.Some(reasonOr(reason, "outstanding")),
				Supplied:    prstate.Null[prstate.SuppliedInput](),
				Reaction:    prstate.UnimplementedReaction(),
			}
		} else {
			if !validFuzzVerdict(verdict) {
				verdict = "no_issue"
			}
			record = prstate.Record{
				Type:        prstate.CoverageRecordUnit,
				UnitID:      unitID,
				PathIndex:   0,
				Kind:        prstate.CoverageGranularityFile,
				Change:      change,
				BodyDigest:  bodyDigest,
				Verdict:     prstate.Some(verdict),
				FindingIDs:  []string{},
				Evidence: []prstate.Evidence{{
					Path:      path,
					Revision:  prstate.Some(headRev.SHA()),
					StartLine: prstate.Some(1),
					EndLine:   prstate.Some(1),
					Source:    source,
					Note:      prstate.Null[string](),
				}},
				Reason:   prstate.Null[string](),
				Supplied: prstate.Some(prstate.SuppliedInput{Digest: suppliedDigest, Form: suppliedForm, Truncated: suppliedTruncated}),
				Reaction: prstate.UnimplementedReaction(),
			}
			if verdict == "not_affected" || verdict == "could_not_review" {
				record.Reason = prstate.Some(reasonOr(reason, "reason for verdict"))
			}
		}

		gen := prstate.Generation{
			Gen:         1,
			Revision:    core.RevisionPair{Base: baseRev, Head: headRev},
			Engine:      core.FileEngineID(),
			Slot:        prstate.DefaultSlot,
			Producer:    prstate.Producer{Harness: "agy", Model: "gemini-2.5", Effort: "high", Endpoint: "https://example.com"},
			Form:        form,
			Paths:       []string{path},
			Records:     []prstate.Record{record},
			Advisory:    prstate.Advisory{Count: 0, Rules: []string{"convention"}, Limits: []prstate.AdvisoryLimit{}},
			Excluded:    []prstate.CoverageExclusion{},
			ScopeReport: prstate.ScopeReport{ExaminedScope: "scope", KnownLimits: []string{}},
		}

		if form == prstate.GenerationCompact {
			gen = prstate.CompactGeneration(gen)
		}

		manifest, records, err := prstate.EncodeGenerationV2(gen)
		if err != nil {
			t.Fatalf("encode: %v", err)
		}

		decoded, err := prstate.DecodeGenerationV2(manifest, records)
		if err != nil {
			t.Fatalf("decode: %v", err)
		}

		if !reflect.DeepEqual(gen, decoded) {
			t.Fatalf("roundtrip mismatch:\n want: %+v\n  got: %+v", gen, decoded)
		}

		againManifest, againRecords, err := prstate.EncodeGenerationV2(decoded)
		if err != nil {
			t.Fatalf("re-encode: %v", err)
		}
		if !bytes.Equal(manifest, againManifest) {
			t.Fatal("re-encoded manifest differed")
		}
		if !bytes.Equal(records, againRecords) {
			t.Fatal("re-encoded records differed")
		}

		// Tamper with records: must refuse
		if len(records) > 0 {
			tamperedRecords := append([]byte(nil), records...)
			tamperedRecords[len(tamperedRecords)-1] ^= 0xff
			if _, err := prstate.DecodeGenerationV2(manifest, tamperedRecords); err == nil {
				t.Fatal("tampered records decoded")
			}
		}

		// Tamper with manifest: must refuse
		if len(manifest) > 0 {
			tamperedManifest := append([]byte(nil), manifest...)
			tamperedManifest[len(tamperedManifest)-1] ^= 0xff
			if _, err := prstate.DecodeGenerationV2(tamperedManifest, records); err == nil {
				t.Fatal("tampered manifest decoded")
			}
		}
	})
}

func FuzzSchema2Decode(f *testing.F) {
	genFull := validFixtureGeneration(nil, prstate.GenerationFull)
	manFull, recFull, err := prstate.EncodeGenerationV2(genFull)
	if err == nil {
		f.Add(manFull, recFull)
	}

	genCompact := validFixtureGeneration(nil, prstate.GenerationCompact)
	manCompact, recCompact, err := prstate.EncodeGenerationV2(genCompact)
	if err == nil {
		f.Add(manCompact, recCompact)
	}

	// Read seeds from testdata/schema2 if present
	corpusDir := filepath.Join("testdata", "schema2")
	if mData, err := os.ReadFile(filepath.Join(corpusDir, "manifest_full.json")); err == nil {
		if rData, err := os.ReadFile(filepath.Join(corpusDir, "records_full.json")); err == nil {
			f.Add(mData, rData)
		}
	}
	if mData, err := os.ReadFile(filepath.Join(corpusDir, "manifest_compact.json")); err == nil {
		if rData, err := os.ReadFile(filepath.Join(corpusDir, "records_compact.json")); err == nil {
			f.Add(mData, rData)
		}
	}

	f.Add([]byte("{}"), []byte("{}"))
	f.Add([]byte(""), []byte(""))
	f.Add([]byte("not json"), []byte("not json"))

	f.Fuzz(func(t *testing.T, manifest, records []byte) {
		gen, err := prstate.DecodeGenerationV2(manifest, records)
		if err == nil {
			_, _, reErr := prstate.EncodeGenerationV2(gen)
			if reErr != nil {
				t.Fatalf("decoded generation failed to re-encode: %v", reErr)
			}
		}
	})
}
