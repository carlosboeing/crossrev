package core

import (
	"strings"
	"testing"
)

// The vectors below are frozen: they were computed independently of this
// package and match tests/fixtures/intelligence/file-units.json. A change to
// the UnitID formula must update the oracle first, not these copies.
func TestFileUnitIDMatchesFrozenVectors(t *testing.T) {
	vectors := map[string]UnitID{
		"src/added.go":   "e7d250f4226ea120",
		"assets/logo.bin": "c853e1fb8e77ad26",
		"src/empty.go":   "4c3b0b21ac81c3e4",
	}
	for path, want := range vectors {
		if got := FileUnitID(path); got != want {
			t.Errorf("FileUnitID(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestFileUnitIDIsSixteenLowerHex(t *testing.T) {
	for _, path := range []string{"a.go", "dir/space name.go", "x"} {
		id := string(FileUnitID(path))
		if len(id) != 16 {
			t.Errorf("FileUnitID(%q) has length %d, want 16", path, len(id))
		}
		if strings.Trim(id, "0123456789abcdef") != "" {
			t.Errorf("FileUnitID(%q) = %q, want lowercase hex", path, id)
		}
	}
}

func TestParseChangeKindRoundTrips(t *testing.T) {
	for _, kind := range []ChangeKind{ChangeAdded, ChangeModified, ChangeDeleted, ChangeRenamed, ChangeTypeChanged} {
		parsed, err := ParseChangeKind(string(kind))
		if err != nil {
			t.Errorf("ParseChangeKind(%q): %v", kind, err)
		} else if parsed != kind {
			t.Errorf("ParseChangeKind(%q) = %q", kind, parsed)
		}
	}
	for _, bad := range []string{"", "Added", "copied", "unknown"} {
		if _, err := ParseChangeKind(bad); err == nil {
			t.Errorf("ParseChangeKind(%q) succeeded, want an error", bad)
		}
	}
}

func TestParseDispositionRoundTrips(t *testing.T) {
	for _, d := range []Disposition{DispositionNoIssue, DispositionFinding, DispositionNotAffected, DispositionCouldNotReview} {
		parsed, err := ParseDisposition(string(d))
		if err != nil {
			t.Errorf("ParseDisposition(%q): %v", d, err)
		} else if parsed != d {
			t.Errorf("ParseDisposition(%q) = %q", d, parsed)
		}
	}
	if _, err := ParseDisposition("pending"); err == nil {
		t.Error("ParseDisposition(pending) succeeded, want an error")
	}
}

func TestParseUnitIDStrict(t *testing.T) {
	if _, err := ParseUnitID("e7d250f4226ea120"); err != nil {
		t.Errorf("ParseUnitID(valid): %v", err)
	}
	for _, bad := range []string{"", "E7D250F4226EA120", "e7d250f4226ea12", "e7d250f4226ea1200", "not-an-id!!!!!!!!"} {
		if _, err := ParseUnitID(bad); err == nil {
			t.Errorf("ParseUnitID(%q) succeeded, want an error", bad)
		}
	}
}

func TestFileEngineIDMatchesFrozenValue(t *testing.T) {
	const want = "e2b455e748711e52"
	if got := FileEngineID(); got != want {
		t.Errorf("FileEngineID() = %q, want %q", got, want)
	}
	if FileEngineVersion != "file-v1" {
		t.Errorf("FileEngineVersion = %q, want file-v1", FileEngineVersion)
	}
}

func TestBodyDigestIsFullSHA256(t *testing.T) {
	const want = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
	if got := BodyDigestHex([]byte("abc")); got != want {
		t.Errorf("BodyDigestHex(abc) = %q, want %q", got, want)
	}
	if got := BodyDigestHex(nil); len(got) != 64 {
		t.Errorf("BodyDigestHex(nil) has length %d, want 64", len(got))
	}
}
