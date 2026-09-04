package policy_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/policy"
)

// labelsFixture is tests/fixtures/parity/labels.json, captured from
// legs_label_colour and legs_label_description before any Go was written. It is
// read and never written: a test that can regenerate its own oracle is not one.
type labelsFixture struct {
	Function string `json:"function"`
	Labels   []struct {
		Label       string `json:"label"`
		Colour      string `json:"colour"`
		Description string `json:"description"`
	} `json:"labels"`
}

// TestLabelsMatchParityFixture compares every triple in labels.json against
// LabelColour and LabelDescription. Nine entries cover the seven mapped arms of
// lib/legs.sh:295-333 plus the fallback.
func TestLabelsMatchParityFixture(t *testing.T) {
	path := filepath.Join("..", "..", "tests", "fixtures", "parity", "labels.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var fx labelsFixture
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	if len(fx.Labels) == 0 {
		t.Fatalf("%s carries no labels", path)
	}
	for _, l := range fx.Labels {
		if got := policy.LabelColour(l.Label); got != l.Colour {
			t.Errorf("LabelColour(%q) = %q, want %q", l.Label, got, l.Colour)
		}
		if got := policy.LabelDescription(l.Label); got != l.Description {
			t.Errorf("LabelDescription(%q) = %q, want %q", l.Label, got, l.Description)
		}
	}
}

// TestLabelsColourContract pins the two properties the colour map exists for
// (lib/legs.sh:275-294): six loop-state hues, no two alike, and red reserved for
// the one label a human applies. crossrev/watchdog-retried is the watchdog's own
// bookkeeping and sits outside the six (ADR 0008).
func TestLabelsColourContract(t *testing.T) {
	six := append(policy.FixedLabels(), policy.PassLabelName(core.PassNumber(1)))
	if len(six) != 6 {
		t.Fatalf("the loop-state contract is six labels, got %d: %v", len(six), six)
	}
	seen := map[string]string{}
	for _, l := range six {
		c := policy.LabelColour(l)
		if c == "" || c == "ededed" {
			t.Errorf("%s fell through to the fallback colour", l)
		}
		if prev, dup := seen[c]; dup {
			t.Errorf("%s and %s are both %s", prev, l, c)
		}
		seen[c] = l
	}
	if policy.LabelColour(policy.LabelStop) != "cf222e" {
		t.Errorf("red is reserved for %s", policy.LabelStop)
	}
	for _, l := range six {
		if l != policy.LabelStop && policy.LabelColour(l) == "cf222e" {
			t.Errorf("%s took the colour reserved for %s", l, policy.LabelStop)
		}
	}
	if policy.LabelColour(policy.LabelWatchdogRetried) == "ededed" {
		t.Errorf("%s is mapped, not a fallback", policy.LabelWatchdogRetried)
	}
	for _, l := range six {
		if policy.LabelColour(policy.LabelWatchdogRetried) == policy.LabelColour(l) {
			t.Errorf("the watchdog label shares %s's colour", l)
		}
	}
}

// TestLabelsPassArm pins the wildcard arms of lib/legs.sh:302 and 329: every
// pass number takes the grey and a description naming its own number.
func TestLabelsPassArm(t *testing.T) {
	for _, n := range []int{1, 3, 7, 42} {
		l := policy.PassLabelName(core.PassNumber(n))
		if got := policy.LabelColour(l); got != "57606a" {
			t.Errorf("LabelColour(%q) = %q, want 57606a", l, got)
		}
		want := "crossrev: reached pass " + strconv.Itoa(n)
		if got := policy.LabelDescription(l); got != want {
			t.Errorf("LabelDescription(%q) = %q, want %q", l, got, want)
		}
	}
}

// TestLabelsAwaiting transcribes lib/legs.sh:95-101 and the `label` block at
// tests/test-legs.sh:82-83.
// The leg is named for the verb and the label for the noun, so resolve waits
// behind awaiting-resolution.
func TestLabelsAwaiting(t *testing.T) {
	cases := []struct {
		leg  core.Leg
		want string
	}{
		{core.LegReview, "crossrev/awaiting-review"},
		{core.LegResolve, "crossrev/awaiting-resolution"},
		{core.Leg("watchdog"), "crossrev/awaiting-watchdog"},
	}
	for _, tc := range cases {
		if got := policy.AwaitingLabel(tc.leg); got != tc.want {
			t.Errorf("AwaitingLabel(%q) = %q, want %q", tc.leg, got, tc.want)
		}
	}
}

// TestLabelsPassLabel transcribes the `pass_label` block at
// tests/test-legs.sh:486-491 over lib/legs.sh:261-271.
func TestLabelsPassLabel(t *testing.T) {
	cases := []struct {
		desc       string
		verdict    core.Verdict
		actionable int
		escalated  int
		want       policy.PassLabelState
	}{
		{"a converged verdict with actionable findings is overridden", core.VerdictConverged, 2, 0, policy.PassAwaitingResolution},
		{"a converged verdict after an escalation still exits the halt", core.VerdictConverged, 0, 2, policy.PassConverged},
		{"a blocked review halts", core.VerdictBlocked, 0, 0, policy.PassHalted},
		{"actionable findings owe the resolve leg", core.VerdictIssuesRemain, 2, 0, policy.PassAwaitingResolution},
		{"an empty pass with nothing open converges", core.VerdictIssuesRemain, 0, 0, policy.PassConverged},
		{"an empty pass while an escalation stands halts", core.VerdictIssuesRemain, 0, 1, policy.PassHalted},
		{"blocked outranks actionable findings", core.VerdictBlocked, 3, 0, policy.PassHalted},
	}
	for _, tc := range cases {
		if got := policy.PassLabel(tc.verdict, tc.actionable, tc.escalated); got != tc.want {
			t.Errorf("%s: PassLabel = %q, want %q", tc.desc, got, tc.want)
		}
	}
}

// TestLabelsResolvePassLabel transcribes the `resolve_label` block at
// tests/test-legs.sh:376-412 over lib/legs.sh:234-248.
func TestLabelsResolvePassLabel(t *testing.T) {
	rec := func(r core.Resolution) policy.ResolutionRecord {
		return policy.ResolutionRecord{Resolution: r}
	}
	deferredTracked := policy.ResolutionRecord{
		Resolution: core.ResolutionDeferred, Tracked: core.NewTracked("o/r#7"),
	}
	deferredUnfiled := policy.ResolutionRecord{
		Resolution: core.ResolutionDeferred, Tracked: core.TrackedUnfiled(),
	}
	cases := []struct {
		desc           string
		marker         policy.ResolveMarker
		otherEscalated int
		want           policy.PassLabelState
	}{
		{"an all-disputed pass converges",
			policy.ResolveMarker{Resolutions: []policy.ResolutionRecord{rec(core.ResolutionDisputed)}}, 0, policy.PassConverged},
		{"an all-skipped pass converges",
			policy.ResolveMarker{Resolutions: []policy.ResolutionRecord{rec(core.ResolutionSkipped)}}, 0, policy.PassConverged},
		{"a deferral tracked elsewhere converges",
			policy.ResolveMarker{Resolutions: []policy.ResolutionRecord{deferredTracked}}, 0, policy.PassConverged},
		{"a mix of the three settles",
			policy.ResolveMarker{Resolutions: []policy.ResolutionRecord{rec(core.ResolutionDisputed), rec(core.ResolutionSkipped), deferredTracked}}, 0, policy.PassConverged},
		{"a legacy deferral without the tracking field reads as settled",
			policy.ResolveMarker{Resolutions: []policy.ResolutionRecord{rec(core.ResolutionDeferred)}}, 0, policy.PassConverged},
		{"a pass that pushed a fix hands back to the reviewer",
			policy.ResolveMarker{CommitSHA: "d81a3f2abc", Resolutions: []policy.ResolutionRecord{rec(core.ResolutionFixed), rec(core.ResolutionDisputed)}}, 0, policy.PassAwaitingReview},
		{"a deferral committed to the repository backlog moved the head",
			policy.ResolveMarker{CommitSHA: "d81a3f2abc", Resolutions: []policy.ResolutionRecord{
				{Resolution: core.ResolutionDeferred, Tracked: core.NewTracked(".crossrev/backlog#1")}}}, 0, policy.PassAwaitingReview},
		{"a dispute beside an escalation halts — the escalation wins",
			policy.ResolveMarker{Resolutions: []policy.ResolutionRecord{rec(core.ResolutionDisputed), rec(core.ResolutionEscalated)}}, 0, policy.PassHalted},
		{"an escalation an earlier pass left standing halts the settle",
			policy.ResolveMarker{Resolutions: []policy.ResolutionRecord{rec(core.ResolutionDisputed)}}, 1, policy.PassHalted},
		{"a blocked pass halts",
			policy.ResolveMarker{Blocked: true, Resolutions: []policy.ResolutionRecord{rec(core.ResolutionDisputed)}}, 0, policy.PassHalted},
		{"a deferral whose record never landed halts",
			policy.ResolveMarker{Resolutions: []policy.ResolutionRecord{deferredUnfiled}}, 0, policy.PassHalted},
		{"a fix the resolver claimed and never committed halts",
			policy.ResolveMarker{Resolutions: []policy.ResolutionRecord{rec(core.ResolutionFixed)}}, 0, policy.PassHalted},
		{"and it outranks the disputes beside it",
			policy.ResolveMarker{Resolutions: []policy.ResolutionRecord{rec(core.ResolutionFixed), rec(core.ResolutionDisputed)}}, 0, policy.PassHalted},
		{"a pass that recorded no resolutions halts",
			policy.ResolveMarker{}, 0, policy.PassHalted},
		{"the same legacy marker with a commit hands back to the reviewer",
			policy.ResolveMarker{CommitSHA: "d81a3f2abc"}, 0, policy.PassAwaitingReview},
	}
	for _, tc := range cases {
		if got := policy.ResolvePassLabel(tc.marker, tc.otherEscalated); got != tc.want {
			t.Errorf("%s: ResolvePassLabel = %q, want %q", tc.desc, got, tc.want)
		}
	}
}

// TestLabelsPassLabelStateNames pins the four words run_pass_labels
// (lib/run.sh:445-472) removes and re-adds by name, and the label each forms.
func TestLabelsPassLabelStateNames(t *testing.T) {
	cases := []struct {
		state policy.PassLabelState
		label string
	}{
		{policy.PassAwaitingReview, policy.LabelAwaitingReview},
		{policy.PassAwaitingResolution, policy.LabelAwaitingResolution},
		{policy.PassConverged, policy.LabelConverged},
		{policy.PassHalted, policy.LabelHalted},
	}
	for _, tc := range cases {
		if got := tc.state.Label(); got != tc.label {
			t.Errorf("%q.Label() = %q, want %q", tc.state, got, tc.label)
		}
	}
}
