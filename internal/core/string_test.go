package core

import (
	"fmt"
	"testing"
)

// Every String method in the package, in one table. They are what a marker, a
// log line and a printed table are built from, so a method returning the wrong
// half of its value is a defect nothing else in these tests would catch.
//
// FindingID is absent on purpose: only internal/prstate may mint one, and
// TestFindingIDIsSixteenLowercaseHexCharacters covers its String through an id
// decoded off the wire.
func TestStringRendersTheSpellingEachValueIsWrittenWith(t *testing.T) {
	rev, err := NewRevision(shaA)
	if err != nil {
		t.Fatalf("NewRevision: %v", err)
	}
	slug, err := NewSlug("carlosboeing", "crossrev")
	if err != nil {
		t.Fatalf("NewSlug: %v", err)
	}

	tests := []struct {
		name string
		got  fmt.Stringer
		want string
	}{
		{name: "HarnessName", got: HarnessOpencode, want: "opencode"},
		{name: "WriteCapability", got: WriteYes, want: "yes"},
		{name: "Billing", got: BillingSubscription, want: "subscription"},
		{name: "CostSource", got: CostSourceTable, want: "table"},
		{name: "Severity", got: SeverityMedium, want: "medium"},
		{name: "Category", got: CategoryMaintainability, want: "maintainability"},
		{name: "Side", got: SideRight, want: "RIGHT"},
		{name: "PriorStatus", got: PriorCrediblyDisputed, want: "credibly-disputed"},
		{name: "Resolution", got: ResolutionEscalated, want: "escalated"},
		{name: "Verdict", got: VerdictIssuesRemain, want: "issues-remain"},
		{name: "LoopState", got: LoopAwaitingResolution, want: "awaiting resolution"},
		{name: "Leg", got: LegResolve, want: "resolve"},
		{name: "LegRole", got: RoleResolver, want: "resolver"},
		{name: "PassState", got: PassDeclined, want: "declined"},
		{name: "Revision", got: rev, want: shaA},
		{name: "Slug", got: slug, want: "carlosboeing/crossrev"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.got.String(); got != tt.want {
				t.Fatalf("String() = %q, want %q", got, tt.want)
			}
		})
	}
}

// A revision prints its full object name and not the abbreviation, because a
// log line that abbreviates is a comparison somebody will later make against
// seven characters.
func TestRevisionStringIsTheFullObjectName(t *testing.T) {
	rev, err := NewRevision(shaA)
	if err != nil {
		t.Fatalf("NewRevision: %v", err)
	}
	if got := rev.WithRef("refs/heads/main").String(); got != shaA {
		t.Fatalf("String() = %q, want %q", got, shaA)
	}
}
