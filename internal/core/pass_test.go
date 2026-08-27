package core

import "testing"

// The marker vocabulary comes from the writers in lib/run.sh: `leg:"review"`
// at lib/run.sh:1098 and `leg:"resolve"` at lib/run.sh:1960.
func TestLegVocabularyIsReviewAndResolve(t *testing.T) {
	if LegReview != "review" || LegResolve != "resolve" {
		t.Fatalf("legs are %q and %q, want review and resolve", LegReview, LegResolve)
	}
	if got := Legs(); len(got) != 2 || got[0] != LegReview || got[1] != LegResolve {
		t.Fatalf("Legs() = %v, want [review resolve]", got)
	}
}

func TestParseLegAcceptsOnlyTheTwoWrittenValues(t *testing.T) {
	for _, in := range []string{"review", "resolve"} {
		got, err := ParseLeg(in)
		if err != nil || string(got) != in {
			t.Fatalf("ParseLeg(%q) = %q, %v; want %q and no error", in, got, err, in)
		}
	}
	for _, in := range []string{"", "reviewer", "resolver", "Review", "cycle"} {
		if _, err := ParseLeg(in); err == nil {
			t.Fatalf("ParseLeg(%q) = nil error, want a refusal", in)
		}
	}
}

// The configuration keys are `reviewer` and `resolver`; the marker vocabulary
// is `review` and `resolve`. lib/run.sh:518 converts between them.
func TestLegRoleMapsOntoTheMarkerLeg(t *testing.T) {
	if got, err := RoleReviewer.Leg(); err != nil || got != LegReview {
		t.Fatalf("RoleReviewer.Leg() = %q, %v; want review", got, err)
	}
	if got, err := RoleResolver.Leg(); err != nil || got != LegResolve {
		t.Fatalf("RoleResolver.Leg() = %q, %v; want resolve", got, err)
	}
	if _, err := LegRole("reviewery").Leg(); err == nil {
		t.Fatal("LegRole(\"reviewery\").Leg() = nil error, want a refusal")
	}
	if RoleReviewer != "reviewer" || RoleResolver != "resolver" {
		t.Fatalf("roles are %q and %q, want reviewer and resolver", RoleReviewer, RoleResolver)
	}
}

// lib/state.sh distinguishes three marker states: `started` (an open claim,
// lib/state.sh:313), `complete` (lib/state.sh:290) and `declined` (a pass a cap
// refused to start, lib/state.sh:268).
func TestPassStateVocabularyIsStartedCompleteDeclined(t *testing.T) {
	want := []PassState{PassStarted, PassComplete, PassDeclined}
	got := PassStates()
	if len(got) != len(want) {
		t.Fatalf("PassStates() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("PassStates() = %v, want %v", got, want)
		}
	}
	if PassStarted != "started" || PassComplete != "complete" || PassDeclined != "declined" {
		t.Fatalf("states are %q, %q, %q", PassStarted, PassComplete, PassDeclined)
	}
}

func TestParsePassStateRefusesAnythingElse(t *testing.T) {
	for _, in := range []string{"started", "complete", "declined"} {
		if got, err := ParsePassState(in); err != nil || string(got) != in {
			t.Fatalf("ParsePassState(%q) = %q, %v", in, got, err)
		}
	}
	for _, in := range []string{"", "running", "Complete", "refused"} {
		if _, err := ParsePassState(in); err == nil {
			t.Fatalf("ParsePassState(%q) = nil error, want a refusal", in)
		}
	}
}

// A declined marker records a pass that did not happen, so pass numbering,
// revision detection and the daily cap all skip it (lib/state.sh:260-268).
func TestDeclinedIsTheOnlyStateThatDidNotRun(t *testing.T) {
	if !PassDeclined.Declined() {
		t.Fatal("PassDeclined.Declined() = false")
	}
	for _, s := range []PassState{PassStarted, PassComplete, PassState("")} {
		if s.Declined() {
			t.Fatalf("PassState(%q).Declined() = true", s)
		}
	}
}

func TestPassNumbersStartAtOne(t *testing.T) {
	if _, err := NewPassNumber(0); err == nil {
		t.Fatal("NewPassNumber(0) = nil error, want a refusal")
	}
	if _, err := NewPassNumber(-1); err == nil {
		t.Fatal("NewPassNumber(-1) = nil error, want a refusal")
	}
	p, err := NewPassNumber(3)
	if err != nil {
		t.Fatalf("NewPassNumber(3): %v", err)
	}
	if int(p) != 3 {
		t.Fatalf("NewPassNumber(3) = %d", p)
	}
}

// Every marker the shipped tool writes opens with `v:1` (lib/run.sh:1098).
func TestMarkerVersionIsOne(t *testing.T) {
	if MarkerVersion != 1 {
		t.Fatalf("MarkerVersion = %d, want 1", MarkerVersion)
	}
}
