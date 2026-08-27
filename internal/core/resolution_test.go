package core

import "testing"

// schemas/resolve.schema.json enumerates the five resolutions.
func TestResolutionVocabularyMatchesTheResolveSchema(t *testing.T) {
	want := []string{"fixed", "skipped", "deferred", "disputed", "escalated"}
	got := Resolutions()
	if len(got) != len(want) {
		t.Fatalf("Resolutions() = %v, want %v", got, want)
	}
	for i := range want {
		if string(got[i]) != want[i] {
			t.Fatalf("Resolutions() = %v, want %v", got, want)
		}
	}
	for _, in := range want {
		if _, err := ParseResolution(in); err != nil {
			t.Fatalf("ParseResolution(%q): %v", in, err)
		}
	}
}

// `rebutted` was the pre-migration spelling. The migration lives in the marker
// decoder, so the value type refuses it outright.
func TestParseResolutionRefusesTheRetiredVocabulary(t *testing.T) {
	for _, in := range []string{"", "rebutted", "Fixed", "wontfix"} {
		if _, err := ParseResolution(in); err == nil {
			t.Fatalf("ParseResolution(%q) = nil error, want a refusal", in)
		}
	}
}

// lib/legs.sh:174 reads `crossrev_tracked` three ways: absent, present and
// empty, and present and set. Only the middle one means "deferred and not
// filed anywhere", which is what keeps the cycle alive for another pass.
func TestTrackedCarriesThreeDistinctStates(t *testing.T) {
	absent := TrackedAbsent()
	if absent.Present() {
		t.Fatal("TrackedAbsent().Present() = true")
	}
	if absent.Unfiled() {
		t.Fatal("TrackedAbsent().Unfiled() = true; absent is not the same as present and empty")
	}
	if got := absent.Value(); got != "" {
		t.Fatalf("TrackedAbsent().Value() = %q, want empty", got)
	}

	unfiled := TrackedUnfiled()
	if !unfiled.Present() {
		t.Fatal("TrackedUnfiled().Present() = false")
	}
	if !unfiled.Unfiled() {
		t.Fatal("TrackedUnfiled().Unfiled() = false")
	}
	if got := unfiled.Value(); got != "" {
		t.Fatalf("TrackedUnfiled().Value() = %q, want empty", got)
	}

	filed := NewTracked("acme/widget#7")
	if !filed.Present() {
		t.Fatal("NewTracked(...).Present() = false")
	}
	if filed.Unfiled() {
		t.Fatal("NewTracked(\"acme/widget#7\").Unfiled() = true")
	}
	if got := filed.Value(); got != "acme/widget#7" {
		t.Fatalf("Value() = %q", got)
	}
}

func TestNewTrackedWithAnEmptyStringIsThePresentEmptyState(t *testing.T) {
	if got := NewTracked(""); !got.Present() || !got.Unfiled() {
		t.Fatalf("NewTracked(\"\") = %+v, want present and unfiled", got)
	}
	if TrackedAbsent() == NewTracked("") {
		t.Fatal("the absent and present-empty states compare equal")
	}
}

// The zero value must read as absent, so a struct field nobody set does not
// silently claim the pull request has an unfiled deferral on it.
func TestZeroTrackedIsAbsent(t *testing.T) {
	var zero Tracked
	if zero.Present() || zero.Unfiled() {
		t.Fatalf("zero Tracked = %+v, want absent", zero)
	}
	if zero != TrackedAbsent() {
		t.Fatal("the zero Tracked is not the absent state")
	}
}
