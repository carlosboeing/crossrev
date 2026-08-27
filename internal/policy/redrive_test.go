package policy_test

import (
	"encoding/json"
	"testing"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/policy"
)

// The marker shapes the generated cases carry as JSON text. Decoding them here
// rather than in the package under test keeps policy pure: the marker codec is
// prstate's, and these fields are only what lib/legs.sh reads with jq.
type wireResolution struct {
	Resolution string  `json:"resolution"`
	Tracked    *string `json:"crossrev_tracked"`
}

type wireResolveMarker struct {
	Blocked     bool             `json:"blocked"`
	CommitSHA   *string          `json:"commit_sha"`
	Resolutions []wireResolution `json:"resolutions"`
}

type wireReviewMarker struct {
	State   string `json:"state"`
	Verdict string `json:"verdict"`
}

func decodeResolveMarker(t *testing.T, raw string) policy.ResolveMarker {
	t.Helper()
	var w wireResolveMarker
	if err := json.Unmarshal([]byte(raw), &w); err != nil {
		t.Fatalf("decode resolve marker %q: %v", raw, err)
	}
	m := policy.ResolveMarker{Blocked: w.Blocked}
	if w.CommitSHA != nil {
		m.CommitSHA = *w.CommitSHA
	}
	for _, r := range w.Resolutions {
		rec := policy.ResolutionRecord{Resolution: core.Resolution(r.Resolution)}
		if r.Tracked != nil {
			rec.Tracked = core.NewTracked(*r.Tracked)
		}
		m.Resolutions = append(m.Resolutions, rec)
	}
	return m
}

func decodeReviewMarker(t *testing.T, raw string) *policy.ReviewMarker {
	t.Helper()
	if raw == "" {
		return nil
	}
	var w wireReviewMarker
	if err := json.Unmarshal([]byte(raw), &w); err != nil {
		t.Fatalf("decode review marker %q: %v", raw, err)
	}
	return &policy.ReviewMarker{State: core.PassState(w.State), Verdict: core.Verdict(w.Verdict)}
}

// TestParityResolveRedrivable runs the table generated from
// tests/test-legs.sh's [policy-table: legs_resolve_redrivable] block.
func TestParityResolveRedrivable(t *testing.T) {
	for _, tc := range parityResolveRedrivableCases {
		t.Run(tc.desc, func(t *testing.T) {
			if got := policy.ResolveRedrivable(decodeResolveMarker(t, tc.marker)); got != tc.want {
				t.Errorf("ResolveRedrivable = %t, want %t", got, tc.want)
			}
		})
	}
}

// TestParityReviewRedrivable runs the table generated from
// tests/test-legs.sh's [policy-table: legs_review_redrivable] block.
func TestParityReviewRedrivable(t *testing.T) {
	for _, tc := range parityReviewRedrivableCases {
		t.Run(tc.desc, func(t *testing.T) {
			if got := policy.ReviewRedrivable(decodeReviewMarker(t, tc.marker)); got != tc.want {
				t.Errorf("ReviewRedrivable = %t, want %t", got, tc.want)
			}
		})
	}
}

// TestTrackedTriState pins the distinction lib/legs.sh:174 draws with jq: only
// a `crossrev_tracked` key that is present and empty re-drives. An absent key
// is a legacy marker, or one written with no backlog configured, and both read
// as settled. The generated table carries the present-and-empty case only.
func TestTrackedTriState(t *testing.T) {
	deferred := func(tr core.Tracked) policy.ResolveMarker {
		return policy.ResolveMarker{Resolutions: []policy.ResolutionRecord{
			{Resolution: core.ResolutionDeferred, Tracked: tr},
		}}
	}
	cases := []struct {
		desc string
		in   policy.ResolveMarker
		want bool
	}{
		{"present and empty re-drives", deferred(core.TrackedUnfiled()), true},
		{"absent reads as settled", deferred(core.TrackedAbsent()), false},
		{"a filed deferral reads as settled", deferred(core.NewTracked("owner/repo#7")), false},
	}
	for _, tc := range cases {
		if got := policy.ResolveRedrivable(tc.in); got != tc.want {
			t.Errorf("%s: ResolveRedrivable = %t, want %t", tc.desc, got, tc.want)
		}
	}
}

// TestResolveUnpushedFix transcribes lib/legs.sh:110-114.
func TestResolveUnpushedFix(t *testing.T) {
	fixed := []policy.ResolutionRecord{{Resolution: core.ResolutionFixed}}
	skipped := []policy.ResolutionRecord{{Resolution: core.ResolutionSkipped}}
	cases := []struct {
		desc string
		in   policy.ResolveMarker
		want bool
	}{
		{"a fix with no commit", policy.ResolveMarker{Resolutions: fixed}, true},
		{"a fix that pushed", policy.ResolveMarker{CommitSHA: "d81a3f2abc", Resolutions: fixed}, false},
		{"no fix claimed", policy.ResolveMarker{Resolutions: skipped}, false},
		{"no resolutions claims no fix", policy.ResolveMarker{}, false},
	}
	for _, tc := range cases {
		if got := policy.ResolveUnpushedFix(tc.in); got != tc.want {
			t.Errorf("%s: ResolveUnpushedFix = %t, want %t", tc.desc, got, tc.want)
		}
	}
}

// TestResolveUnrecorded transcribes lib/legs.sh:135-139.
func TestResolveUnrecorded(t *testing.T) {
	cases := []struct {
		desc string
		in   policy.ResolveMarker
		want bool
	}{
		{"nothing recorded and nothing pushed", policy.ResolveMarker{}, true},
		{"nothing recorded but pushed", policy.ResolveMarker{CommitSHA: "d81a3f2abc"}, false},
		{"something recorded", policy.ResolveMarker{
			Resolutions: []policy.ResolutionRecord{{Resolution: core.ResolutionDisputed}}}, false},
	}
	for _, tc := range cases {
		if got := policy.ResolveUnrecorded(tc.in); got != tc.want {
			t.Errorf("%s: ResolveUnrecorded = %t, want %t", tc.desc, got, tc.want)
		}
	}
}

// TestReviewRedrivableFailsClosed pins the closed side of lib/legs.sh:187-192:
// only a complete pass with verdict blocked re-drives, so a state or verdict
// word the marker should not carry re-drives nothing.
func TestReviewRedrivableFailsClosed(t *testing.T) {
	cases := []struct {
		desc string
		in   *policy.ReviewMarker
		want bool
	}{
		{"no marker", nil, false},
		{"complete and blocked", &policy.ReviewMarker{State: core.PassComplete, Verdict: core.VerdictBlocked}, true},
		{"declined is not complete", &policy.ReviewMarker{State: core.PassDeclined, Verdict: core.VerdictBlocked}, false},
		{"an unknown state re-drives nothing", &policy.ReviewMarker{State: core.PassState("wedged"), Verdict: core.VerdictBlocked}, false},
		{"an unknown verdict re-drives nothing", &policy.ReviewMarker{State: core.PassComplete, Verdict: core.Verdict("stuck")}, false},
	}
	for _, tc := range cases {
		if got := policy.ReviewRedrivable(tc.in); got != tc.want {
			t.Errorf("%s: ReviewRedrivable = %t, want %t", tc.desc, got, tc.want)
		}
	}
}

// TestResolveRedrivableUnknownResolution pins the closed side of
// lib/legs.sh:157-176: a resolution word the schema does not list matches no
// re-drive arm, so a pass carrying one stays refused.
func TestResolveRedrivableUnknownResolution(t *testing.T) {
	m := policy.ResolveMarker{
		CommitSHA:   "d81a3f2abc",
		Resolutions: []policy.ResolutionRecord{{Resolution: core.Resolution("rebutted")}},
	}
	if policy.ResolveRedrivable(m) {
		t.Error("an unrecognised resolution re-drove the pass")
	}
}
