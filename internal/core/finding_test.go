package core

import "testing"

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
	if _, err := ParsePriorStatus("rebutted"); err == nil {
		t.Fatal("ParsePriorStatus(\"rebutted\") = nil error, want a refusal")
	}
}

// lib/state.sh:242 cuts the sha256 of the identity bytes to 16 characters, and
// `cut -c1-16` over `shasum` output yields lowercase hex.
func TestFindingIDIsSixteenLowercaseHexCharacters(t *testing.T) {
	var good FindingID = "0123456789abcdef"
	if !good.Valid() {
		t.Fatalf("FindingID(%q).Valid() = false", good)
	}
	bad := []FindingID{
		"",
		"0123456789abcde",
		"0123456789abcdef0",
		"0123456789ABCDEF",
		"0123456789abcdeg",
	}
	for _, id := range bad {
		if id.Valid() {
			t.Fatalf("FindingID(%q).Valid() = true, want false", id)
		}
	}
}

func TestFindingIDStringsItself(t *testing.T) {
	var id FindingID = "aaaa000000000001"
	if got := id.String(); got != "aaaa000000000001" {
		t.Fatalf("String() = %q", got)
	}
}
