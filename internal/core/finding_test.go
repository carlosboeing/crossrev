package core

import (
	"encoding/json"
	"testing"
)

// schemas/findings.schema.json enumerates severity, category and side; the
// three sets below are transcribed from it and must not drift.
func TestSeverityVocabularyMatchesTheFindingsSchema(t *testing.T) {
	want := []Severity{SeverityHigh, SeverityMedium, SeverityLow}
	if got := Severities(); !sameSeverities(got, want) {
		t.Fatalf("Severities() = %v, want %v", got, want)
	}
	if SeverityHigh != "high" || SeverityMedium != "medium" || SeverityLow != "low" {
		t.Fatalf("severities are %q, %q, %q", SeverityHigh, SeverityMedium, SeverityLow)
	}
	for _, in := range []string{"high", "medium", "low"} {
		if _, err := ParseSeverity(in); err != nil {
			t.Fatalf("ParseSeverity(%q): %v", in, err)
		}
	}
	for _, in := range []string{"", "critical", "info", "High"} {
		if _, err := ParseSeverity(in); err == nil {
			t.Fatalf("ParseSeverity(%q) = nil error, want a refusal", in)
		}
	}
}

func sameSeverities(got, want []Severity) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range want {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func TestCategoryVocabularyMatchesTheFindingsSchema(t *testing.T) {
	want := []string{"correctness", "security", "performance", "maintainability", "testing", "docs"}
	got := Categories()
	if len(got) != len(want) {
		t.Fatalf("Categories() = %v, want %v", got, want)
	}
	for i := range want {
		if string(got[i]) != want[i] {
			t.Fatalf("Categories() = %v, want %v", got, want)
		}
	}
	for _, in := range want {
		got, err := ParseCategory(in)
		if err != nil || string(got) != in {
			t.Fatalf("ParseCategory(%q) = %q, %v; want %q and no error", in, got, err, in)
		}
	}
	for _, in := range []string{"", "style", "Correctness"} {
		if _, err := ParseCategory(in); err == nil {
			t.Fatalf("ParseCategory(%q) = nil error, want a refusal", in)
		}
	}
}

func TestSideVocabularyIsTheTwoUppercaseDiffSides(t *testing.T) {
	if SideLeft != "LEFT" || SideRight != "RIGHT" {
		t.Fatalf("sides are %q and %q, want LEFT and RIGHT", SideLeft, SideRight)
	}
	for _, in := range []string{"LEFT", "RIGHT"} {
		if _, err := ParseSide(in); err != nil {
			t.Fatalf("ParseSide(%q): %v", in, err)
		}
	}
	// The schema's enum is uppercase, so a lowercase side is not the same value.
	for _, in := range []string{"", "left", "right", "BOTH"} {
		if _, err := ParseSide(in); err == nil {
			t.Fatalf("ParseSide(%q) = nil error, want a refusal", in)
		}
	}
}

// The `prior` array's status enum, also from schemas/findings.schema.json.
func TestPriorStatusVocabularyMatchesTheFindingsSchema(t *testing.T) {
	want := []string{"addressed", "credibly-disputed", "still-open", "regressed"}
	got := PriorStatuses()
	if len(got) != len(want) {
		t.Fatalf("PriorStatuses() = %v, want %v", got, want)
	}
	for i := range want {
		if string(got[i]) != want[i] {
			t.Fatalf("PriorStatuses() = %v, want %v", got, want)
		}
	}
	for _, in := range want {
		got, err := ParsePriorStatus(in)
		if err != nil || string(got) != in {
			t.Fatalf("ParsePriorStatus(%q) = %q, %v; want %q and no error", in, got, err, in)
		}
	}
	for _, in := range []string{"", "rebutted", "Addressed", "credibly disputed"} {
		if _, err := ParsePriorStatus(in); err == nil {
			t.Fatalf("ParsePriorStatus(%q) = nil error, want a refusal", in)
		}
	}
}

// lib/state.sh:242 cuts the sha256 of the identity bytes to 16 characters, and
// `cut -c1-16` over `shasum` output yields lowercase hex.
//
// The ids below are decoded rather than written as literals. Only
// internal/prstate may mint one, and internal/archtest/findingid_test.go fails
// on a constant of this type anywhere else — decoding is also how an id reaches
// this type in production, off a marker.
func TestFindingIDIsSixteenLowercaseHexCharacters(t *testing.T) {
	var good FindingID
	if err := json.Unmarshal([]byte(`"0123456789abcdef"`), &good); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !good.Valid() {
		t.Fatalf("FindingID(%q).Valid() = false", good)
	}
	if got := good.String(); got != "0123456789abcdef" {
		t.Fatalf("String() = %q, want 0123456789abcdef", got)
	}

	var bad []FindingID
	const raw = `["","0123456789abcde","0123456789abcdef0","0123456789ABCDEF","0123456789abcdeg"]`
	if err := json.Unmarshal([]byte(raw), &bad); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if len(bad) != 5 {
		t.Fatalf("decoded %d ids, want 5", len(bad))
	}
	for _, id := range bad {
		if id.Valid() {
			t.Fatalf("FindingID(%q).Valid() = true, want false", id)
		}
	}

	// The zero id is the absence of one, which is not a valid shape either.
	var zero FindingID
	if zero.Valid() || zero.String() != "" {
		t.Fatalf("the zero id reports Valid()=%t String()=%q", zero.Valid(), zero.String())
	}
}
