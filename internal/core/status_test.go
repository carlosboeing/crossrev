package core

import "testing"

// schemas/findings.schema.json enumerates the three verdicts a review leg may
// return.
func TestVerdictVocabularyMatchesTheFindingsSchema(t *testing.T) {
	want := []string{"converged", "issues-remain", "blocked"}
	got := Verdicts()
	if len(got) != len(want) {
		t.Fatalf("Verdicts() = %v, want %v", got, want)
	}
	for i := range want {
		if string(got[i]) != want[i] {
			t.Fatalf("Verdicts() = %v, want %v", got, want)
		}
	}
	for _, in := range want {
		if _, err := ParseVerdict(in); err != nil {
			t.Fatalf("ParseVerdict(%q): %v", in, err)
		}
	}
	for _, in := range []string{"", "declined", "issues_remain", "Blocked"} {
		if _, err := ParseVerdict(in); err == nil {
			t.Fatalf("ParseVerdict(%q) = nil error, want a refusal", in)
		}
	}
}

// A pass a cap refused writes `verdict:"declined"` onto the marker
// (lib/run.sh:1055). No harness ever returns it, so it is a marker value
// rather than a schema one.
func TestMarkerVerdictsAddDeclinedToTheSchemaThree(t *testing.T) {
	want := []string{"converged", "issues-remain", "blocked", "declined"}
	got := MarkerVerdicts()
	if len(got) != len(want) {
		t.Fatalf("MarkerVerdicts() = %v, want %v", got, want)
	}
	for i := range want {
		if string(got[i]) != want[i] {
			t.Fatalf("MarkerVerdicts() = %v, want %v", got, want)
		}
	}
	if _, err := ParseMarkerVerdict("declined"); err != nil {
		t.Fatalf("ParseMarkerVerdict(\"declined\"): %v", err)
	}
	if _, err := ParseMarkerVerdict("halted"); err == nil {
		t.Fatal("ParseMarkerVerdict(\"halted\") = nil error, want a refusal")
	}
	if VerdictDeclined != "declined" {
		t.Fatalf("VerdictDeclined = %q", VerdictDeclined)
	}
}

// `crossrev status` prints one of five words for the loop, in the precedence
// order at lib/run.sh:3112. The two-word states carry a space, not a hyphen:
// the hyphenated forms are label names, which policy owns.
func TestLoopStateVocabularyMatchesWhatStatusPrints(t *testing.T) {
	want := []string{"stopped", "halted", "converged", "awaiting resolution", "awaiting review"}
	got := LoopStates()
	if len(got) != len(want) {
		t.Fatalf("LoopStates() = %v, want %v", got, want)
	}
	for i := range want {
		if string(got[i]) != want[i] {
			t.Fatalf("LoopStates() = %v, want %v", got, want)
		}
	}
	if LoopStopped != "stopped" || LoopHalted != "halted" || LoopConverged != "converged" {
		t.Fatalf("terminal states are %q, %q, %q", LoopStopped, LoopHalted, LoopConverged)
	}
	if LoopAwaitingResolution != "awaiting resolution" || LoopAwaitingReview != "awaiting review" {
		t.Fatalf("awaiting states are %q and %q", LoopAwaitingResolution, LoopAwaitingReview)
	}
}

func TestParseLoopStateRefusesTheLabelSpellings(t *testing.T) {
	for _, in := range []string{"awaiting-review", "awaiting-resolution", "", "running"} {
		if _, err := ParseLoopState(in); err == nil {
			t.Fatalf("ParseLoopState(%q) = nil error, want a refusal", in)
		}
	}
	for _, in := range []string{"stopped", "halted", "converged", "awaiting resolution", "awaiting review"} {
		if _, err := ParseLoopState(in); err != nil {
			t.Fatalf("ParseLoopState(%q): %v", in, err)
		}
	}
}
