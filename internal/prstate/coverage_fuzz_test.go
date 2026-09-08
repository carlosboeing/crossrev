package prstate_test

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/prstate"
)

// FuzzCoverageDigestRoundTrip feeds arbitrary record fields through the shard
// codec and requires that whatever encodes decodes back to the same values
// with its digest intact, and that whatever the strict decoder refuses never
// decodes. It fails before the change because there is no coverage digest
// contract to round-trip through.
func FuzzCoverageDigestRoundTrip(f *testing.F) {
	f.Add("a.go", "modified", "no_issue", "git", "Read a.go.")
	f.Add("b.go", "added", "", "search", "")
	f.Add("", "deleted", "could_not_review", "reviewer", "binary content is not shown")

	f.Fuzz(func(t *testing.T, path, change, disposition, source, reason string) {
		if strings.Contains(path, "\x00") {
			t.Skip("a NUL path cannot round-trip through a comment body")
		}
		unitID := string(core.FileUnitID(path))
		bodySum := sha256.Sum256([]byte(path + "\n" + change))
		bodyDigest := hex.EncodeToString(bodySum[:])
		if !validFuzzChange(change) {
			change = "modified"
		}
		if !validFuzzSource(source) {
			source = "git"
		}
		var record prstate.Record
		if disposition == "" {
			record = prstate.OutstandingRecord(unitID, 0, change, bodyDigest, reasonOr(reason, "outstanding for the next pass"))
		} else {
			if !validFuzzDisposition(disposition) {
				disposition = "no_issue"
			}
			record = prstate.Record{
				Type:        prstate.CoverageRecordUnit,
				UnitID:      unitID,
				PathIndex:   0,
				Kind:        prstate.CoverageGranularityFile,
				Change:      change,
				BodyDigest:  bodyDigest,
				Disposition: prstate.Some(disposition),
				FindingIDs:  []string{},
				Evidence: []prstate.Evidence{{
					Path:     pathOr(path, "a.go"),
					Revision: prstate.Some(coverageHeadSHA()),
					Source:   source,
					Note:     prstate.Null[string](),
				}},
				Reason: prstate.Null[string](),
			}
			if disposition == "not_affected" {
				record.Reason = prstate.Some(reasonOr(reason, "read and needs no change"))
			}
		}
		shard := prstate.BuildShard(0, []prstate.Record{record})
		body, err := prstate.EncodeCoverageShard(shard)
		if err != nil {
			t.Fatalf("encoding a fuzz shard: %v", err)
		}
		decoded, ok := prstate.DecodeCoverageShard(body)
		if !ok {
			t.Fatalf("decoding the shard just written: %s", body)
		}
		if decoded.Digest == "" || !isFuzzHex64(decoded.Digest) {
			t.Fatalf("shard digest is %q, want full SHA-256 hex", decoded.Digest)
		}
		if len(decoded.Records) != 1 {
			t.Fatalf("shard holds %d records", len(decoded.Records))
		}
		got := decoded.Records[0]
		if got.UnitID != unitID || got.BodyDigest != bodyDigest || got.Change != change {
			t.Errorf("record changed identity: %+v", got)
		}
		again, err := prstate.EncodeCoverageShard(decoded)
		if err != nil {
			t.Fatalf("re-encoding a decoded shard: %v", err)
		}
		if again != body {
			t.Errorf("re-encoding changed the bytes\n got %s\nwant %s", again, body)
		}
		// A flipped byte refuses rather than decoding to other values.
		tampered := strings.Replace(body, `"pos":0`, `"pos":1`, 1)
		if tampered != body {
			if _, ok := prstate.DecodeCoverageShard(tampered); ok {
				t.Error("a shard with a mismatched digest decoded")
			}
		}
	})
}

func validFuzzChange(s string) bool {
	switch s {
	case "added", "modified", "deleted", "renamed", "type_changed":
		return true
	}
	return false
}

func validFuzzDisposition(s string) bool {
	switch s {
	case "no_issue", "finding", "not_affected", "could_not_review":
		return true
	}
	return false
}

func validFuzzSource(s string) bool {
	switch s {
	case "git", "search", "convention", "reviewer":
		return true
	}
	return false
}

func reasonOr(reason, fallback string) string {
	if reason == "" {
		return fallback
	}
	return reason
}

func pathOr(path, fallback string) string {
	if path == "" {
		return fallback
	}
	return path
}

func isFuzzHex64(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
