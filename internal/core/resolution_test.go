package core

import (
	"encoding/json"
	"testing"
)

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

// The three states the marker distinguishes have to survive a round trip
// through encoding/json, or internal/prstate reimplements the
// absent-versus-present-empty test that lib/legs.sh:171-174 and lib/legs.sh:241
// both depend on.
func TestTrackedRoundTripsAllThreeMarkerStates(t *testing.T) {
	type deferral struct {
		Resolution Resolution `json:"resolution"`
		Tracked    Tracked    `json:"crossrev_tracked,omitzero"`
	}

	tests := []struct {
		name    string
		value   Tracked
		want    string
		present bool
		unfiled bool
	}{
		{
			name:  "absent",
			value: TrackedAbsent(),
			want:  `{"resolution":"deferred"}`,
		},
		{
			name:    "present and empty",
			value:   TrackedUnfiled(),
			want:    `{"resolution":"deferred","crossrev_tracked":""}`,
			present: true,
			unfiled: true,
		},
		{
			name:    "present and filed",
			value:   NewTracked("acme/widget#7"),
			want:    `{"resolution":"deferred","crossrev_tracked":"acme/widget#7"}`,
			present: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, err := json.Marshal(deferral{Resolution: ResolutionDeferred, Tracked: tt.value})
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			if got := string(b); got != tt.want {
				t.Fatalf("Marshal = %s, want %s", got, tt.want)
			}
			var back deferral
			if err := json.Unmarshal(b, &back); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			if back.Tracked.Present() != tt.present {
				t.Fatalf("Present() = %t, want %t", back.Tracked.Present(), tt.present)
			}
			if back.Tracked.Unfiled() != tt.unfiled {
				t.Fatalf("Unfiled() = %t, want %t", back.Tracked.Unfiled(), tt.unfiled)
			}
			if back.Tracked.Value() != tt.value.Value() {
				t.Fatalf("Value() = %q, want %q", back.Tracked.Value(), tt.value.Value())
			}
		})
	}
}

// jq answers `.crossrev_tracked == ""` false for null, exactly as it does for a
// missing key, so a null decodes to the absent state rather than the unfiled
// one that keeps the cycle alive.
func TestTrackedReadsNullAsAbsentAndRefusesANonString(t *testing.T) {
	var t1 Tracked
	if err := json.Unmarshal([]byte(`null`), &t1); err != nil {
		t.Fatalf("Unmarshal(null): %v", err)
	}
	if t1.Present() || t1.Unfiled() {
		t.Fatalf("null decoded as present=%t unfiled=%t", t1.Present(), t1.Unfiled())
	}
	var t2 Tracked
	if err := json.Unmarshal([]byte(`7`), &t2); err == nil {
		t.Fatal("Unmarshal(7) = nil error, want a refusal")
	}
}

// IsZero is the genuine zero-value test the `omitzero` tag reads.
func TestTrackedIsZeroOnlyForTheAbsentState(t *testing.T) {
	if !TrackedAbsent().IsZero() {
		t.Fatal("IsZero() = false for the absent state")
	}
	if TrackedUnfiled().IsZero() {
		t.Fatal("IsZero() = true for the present-and-empty state")
	}
	if NewTracked("acme/widget#7").IsZero() {
		t.Fatal("IsZero() = true for a filed deferral")
	}
}
