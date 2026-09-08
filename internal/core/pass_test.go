package core

import "testing"

// The marker vocabulary comes from the writers in lib/run.sh: `leg:"review"`
// at lib/run.sh:1104 and `leg:"resolve"` at lib/run.sh:1966.
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
// is `review` and `resolve`. lib/run.sh:524 converts between them.
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

// lib/state.sh distinguishes four marker states: `started` (an open claim,
// lib/state.sh:313), `complete` (lib/state.sh:290), `incomplete` (a pass that
// ran and halted before settling, resumable at the same revision) and
// `declined` (a pass a cap refused to start, lib/state.sh:268).
func TestPassStateVocabularyIsStartedCompleteIncompleteDeclined(t *testing.T) {
	want := []PassState{PassStarted, PassComplete, PassIncomplete, PassDeclined}
	got := PassStates()
	if len(got) != len(want) {
		t.Fatalf("PassStates() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("PassStates() = %v, want %v", got, want)
		}
	}
	if PassStarted != "started" || PassComplete != "complete" || PassDeclined != "declined" || PassIncomplete != "incomplete" {
		t.Fatalf("states are %q, %q, %q, %q", PassStarted, PassComplete, PassDeclined, PassIncomplete)
	}
}

func TestParsePassStateRefusesAnythingElse(t *testing.T) {
	for _, in := range []string{"started", "complete", "incomplete", "declined"} {
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
// An incomplete marker records a pass that ran but halted before settling,
// so it counts as a pass that ran: it is not declined, complete or
// converged, and a re-drive resumes the same revision.
func TestDeclinedIsTheOnlyStateThatDidNotRun(t *testing.T) {
	if !PassDeclined.Declined() {
		t.Fatal("PassDeclined.Declined() = false")
	}
	for _, s := range []PassState{PassStarted, PassComplete, PassIncomplete, PassState("")} {
		if s.Declined() {
			t.Fatalf("PassState(%q).Declined() = true", s)
		}
	}
}

// Markers this release writes open with `v:2`. Markers at `v:1` remain
// readable for findings, pass numbering and prior resolutions, but their
// coverage reference is always absent and cannot satisfy convergence. The
// typed reader refuses anything above the current version.
func TestMarkerVersionIsTwo(t *testing.T) {
	if MarkerVersion != 2 {
		t.Fatalf("MarkerVersion = %d, want 2", MarkerVersion)
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
	if p.Int() != 3 {
		t.Fatalf("NewPassNumber(3).Int() = %d, want 3", p.Int())
	}
}

// Go cannot block the conversion, so a value that never went through
// NewPassNumber still has to be checkable. Zero is the absence of a pass
// (lib/state.sh:273), not a pass.
func TestPassNumberValidRefusesWhatNewPassNumberRefuses(t *testing.T) {
	for _, n := range []int{0, -1, -7} {
		if PassNumber(n).Valid() {
			t.Fatalf("PassNumber(%d).Valid() = true, want false", n)
		}
	}
	for _, n := range []int{1, 2, 99} {
		if !PassNumber(n).Valid() {
			t.Fatalf("PassNumber(%d).Valid() = false, want true", n)
		}
		if got := PassNumber(n).Int(); got != n {
			t.Fatalf("PassNumber(%d).Int() = %d", n, got)
		}
	}
}

// Every marker the shipped tool writes opens with `v:1` (lib/run.sh:1104).
func TestMarkerVersionIsOne(t *testing.T) {
	t.Skip("superseded by TestMarkerVersionIsTwo: this release writes v:2, and v:1 stays readable for context only")
}

// The three closed halt reasons and the two recorded limit reasons are the
// only words a marker may carry for why a pass stopped or what deferred it.
func TestHaltAndLimitVocabulariesAreClosed(t *testing.T) {
	if HaltCoverageIncomplete != "coverage_incomplete" || HaltLedgerExhausted != "ledger_exhausted" || HaltInputExceedsBudget != "input_exceeds_budget" {
		t.Fatalf("halt reasons are %q, %q, %q", HaltCoverageIncomplete, HaltLedgerExhausted, HaltInputExceedsBudget)
	}
	if LimitReviewBudgetReached != "review_budget_reached" || LimitTooCommon != "too_common" {
		t.Fatalf("limit reasons are %q and %q", LimitReviewBudgetReached, LimitTooCommon)
	}
	for _, in := range []string{"coverage_incomplete", "ledger_exhausted", "input_exceeds_budget"} {
		if _, err := ParseHaltReason(in); err != nil {
			t.Fatalf("ParseHaltReason(%q): %v", in, err)
		}
	}
	if _, err := ParseHaltReason("verification_failed"); err == nil {
		t.Fatal("ParseHaltReason(verification_failed) = nil error, want a refusal")
	}
	for _, in := range []string{"review_budget_reached", "too_common"} {
		if _, err := ParseLimitReason(in); err != nil {
			t.Fatalf("ParseLimitReason(%q): %v", in, err)
		}
	}
	if _, err := ParseLimitReason("fix_unverified"); err == nil {
		t.Fatal("ParseLimitReason(fix_unverified) = nil error, want a refusal")
	}
	if len(HaltReasons()) != 3 || len(LimitReasons()) != 2 {
		t.Fatalf("halt/limit counts are %d and %d", len(HaltReasons()), len(LimitReasons()))
	}
}

// ParseMarkerVersion accepts the current version and the superseded one kept
// for context, and refuses anything newer.
func TestParseMarkerVersionAcceptsOneAndTwoOnly(t *testing.T) {
	for _, v := range []int{1, 2} {
		if _, err := ParseMarkerVersion(v); err != nil {
			t.Fatalf("ParseMarkerVersion(%d): %v", v, err)
		}
	}
	for _, v := range []int{0, 3, 99} {
		if _, err := ParseMarkerVersion(v); err == nil {
			t.Fatalf("ParseMarkerVersion(%d) = nil error, want a refusal", v)
		}
	}
	if got := MarkerVersions(); len(got) != 2 || got[0] != 1 || got[1] != MarkerVersion {
		t.Fatalf("MarkerVersions() = %v", got)
	}
}
