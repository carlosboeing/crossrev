package review

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/harness"
	"github.com/carlosboeing/crossrev/internal/prompt"
	"github.com/carlosboeing/crossrev/internal/prstate"
	"github.com/carlosboeing/crossrev/internal/ui"
	"github.com/carlosboeing/crossrev/internal/validate"
)

// checkFindings parses the cases' finding JSON the way publish does.
func checkFindings(t *testing.T, raw string) []Finding {
	t.Helper()
	findings := parseFindings(json.RawMessage(raw))
	if findings == nil {
		t.Fatal("the findings do not parse")
	}
	return findings
}

func checkOrdered(t *testing.T, raw string) []checkCandidate {
	t.Helper()
	return orderCandidates(checkFindings(t, raw))
}

func TestApplyCheckDecisionsStampsAndCorrects(t *testing.T) {
	raw := `[
		{"id":"aaaaaaaaaaaaaaaa","path":"app.go","line":2,"side":"RIGHT","severity":"medium","category":"correctness","pre_existing":false,"title":"Unchecked fetch","why":"A failed request looks like a success","fix":"Check it","concerns":["correctness"]},
		{"id":"bbbbbbbbbbbbbbbb","path":"app.go","line":2,"side":"RIGHT","severity":"low","category":"maintainability","pre_existing":false,"title":"Missing return type","why":"The inferred type is wider","fix":"Annotate it","concerns":["consistency"]},
		{"id":"cccccccccccccccc","path":"old.go","line":9,"side":"RIGHT","severity":"high","category":"correctness","pre_existing":true,"title":"Stale guard","why":"The flag is gone","fix":"Drop it"}
	]`
	ordered := checkOrdered(t, raw)
	decisions := []validate.CheckDecision{
		{Position: 1, Decision: "confirmed", Reason: "the dereference is real", Severity: "high", HasSeverity: true},
		{Position: 2, Decision: "duplicate", DuplicateOf: 1, Reason: "the same unchecked fetch"},
		{Position: 3, Decision: "rejected", Reason: "the flag still exists"},
	}
	applied, records, checkedOut, applyErr := applyCheckDecisions(json.RawMessage(raw), ordered, decisions)
	if applyErr != nil {
		t.Fatalf("apply: %v", applyErr)
	}
	if len(records) != 3 || len(checkedOut) != 2 {
		t.Fatalf("records=%d checked_out=%d, want 3 and 2", len(records), len(checkedOut))
	}
	back := checkFindings(t, string(applied))

	// The survivor carries the group's highest severity with its
	// original recorded, and the union of the group's concerns.
	if back[0].Severity != "high" {
		t.Errorf("survivor severity = %q, want high", back[0].Severity)
	}
	if back[0].OriginalSeverity == nil || *back[0].OriginalSeverity != "medium" {
		t.Errorf("survivor original_severity = %v, want medium", back[0].OriginalSeverity)
	}
	if strings.Join(back[0].Concerns, ",") != "correctness,consistency" {
		t.Errorf("survivor concerns = %v, want both", back[0].Concerns)
	}
	if back[0].IsPosted() != true {
		t.Error("the survivor does not post")
	}
	// The record carries the survivor's original concerns for a
	// discard, and the corrections the decisions named.
	if string(records[0].OriginalConcerns) != `["correctness"]` {
		t.Errorf("record original_concerns = %s, want [correctness]", records[0].OriginalConcerns)
	}
	if records[0].Severity != "high" {
		t.Errorf("record severity = %q, want the high correction", records[0].Severity)
	}
	// The duplicate and the rejection never post, and stay on the
	// checked-out record with position, id, path, line, title and
	// reason.
	if back[1].IsPosted() || back[2].IsPosted() {
		t.Error("a checked-out candidate still posts")
	}
	if checkedOut[0].ID != "bbbbbbbbbbbbbbbb" || checkedOut[0].Decision != "duplicate" || checkedOut[0].Position != 2 {
		t.Errorf("checked_out[0] = %+v", checkedOut[0])
	}
	if checkedOut[1].ID != "cccccccccccccccc" || checkedOut[1].Decision != "rejected" || checkedOut[1].Reason != "the flag still exists" {
		t.Errorf("checked_out[1] = %+v", checkedOut[1])
	}
	if checkedOut[1].Path != "old.go" || checkedOut[1].Line != 9 || checkedOut[1].Title != "Stale guard" {
		t.Errorf("checked_out[1] anchors as %+v", checkedOut[1])
	}
	// An untouched finding encodes exactly as the enricher wrote it:
	// the rejection gained only its posted stamp.
	var nodes []map[string]json.RawMessage
	if err := json.Unmarshal(applied, &nodes); err != nil {
		t.Fatalf("applied decode: %v", err)
	}
	if len(nodes[2]) != 11 || string(nodes[2]["posted"]) != "false" {
		t.Errorf("rejected entry = %v", nodes[2])
	}
}

// A byte-identical pair raised by both concerns keeps both on the
// survivor: the duplicate folds, its concerns union.
func TestApplyCheckDecisionsUnionsIdenticalPairConcerns(t *testing.T) {
	raw := `[
		{"id":"aaaaaaaaaaaaaaaa","path":"app.go","line":2,"side":"RIGHT","severity":"high","category":"correctness","pre_existing":false,"title":"Unchecked fetch","why":"w","fix":"f","concerns":["correctness"]},
		{"id":"aaaaaaaaaaaaaaaa","path":"app.go","line":2,"side":"RIGHT","severity":"high","category":"correctness","pre_existing":false,"title":"Unchecked fetch","why":"w","fix":"f","concerns":["consistency"]}
	]`
	ordered := checkOrdered(t, raw)
	decisions := []validate.CheckDecision{
		{Position: 1, Decision: "confirmed", Reason: "real"},
		{Position: 2, Decision: "duplicate", DuplicateOf: 1, Reason: "byte-identical"},
	}
	applied, _, checkedOut, applyErr := applyCheckDecisions(json.RawMessage(raw), ordered, decisions)
	if applyErr != nil {
		t.Fatalf("apply: %v", applyErr)
	}
	back := checkFindings(t, string(applied))
	if strings.Join(back[0].Concerns, ",") != "correctness,consistency" {
		t.Errorf("survivor concerns = %v, want both", back[0].Concerns)
	}
	if len(checkedOut) != 1 || checkedOut[0].Decision != "duplicate" {
		t.Errorf("checked_out = %+v, want the folded duplicate", checkedOut)
	}
	if back[1].IsPosted() {
		t.Error("the duplicate still posts")
	}
}

// A duplicate chain through another duplicate folds every member onto
// the confirmed root, with the highest severity of the whole chain.
func TestApplyCheckDecisionsFoldsChainsOntoTheRoot(t *testing.T) {
	raw := `[
		{"id":"aaaaaaaaaaaaaaaa","path":"a.go","line":1,"side":"RIGHT","severity":"low","category":"correctness","pre_existing":false,"title":"one","why":"w","fix":"f"},
		{"id":"bbbbbbbbbbbbbbbb","path":"a.go","line":2,"side":"RIGHT","severity":"medium","category":"correctness","pre_existing":false,"title":"two","why":"w","fix":"f"},
		{"id":"cccccccccccccccc","path":"a.go","line":3,"side":"RIGHT","severity":"low","category":"correctness","pre_existing":false,"title":"three","why":"w","fix":"f"}
	]`
	ordered := checkOrdered(t, raw)
	decisions := []validate.CheckDecision{
		{Position: 1, Decision: "confirmed", Reason: "real"},
		{Position: 2, Decision: "duplicate", DuplicateOf: 3, Reason: "same as three"},
		{Position: 3, Decision: "duplicate", DuplicateOf: 1, Reason: "same as one"},
	}
	applied, _, checkedOut, applyErr := applyCheckDecisions(json.RawMessage(raw), ordered, decisions)
	if applyErr != nil {
		t.Fatalf("apply: %v", applyErr)
	}
	back := checkFindings(t, string(applied))
	if back[0].Severity != "medium" {
		t.Errorf("root severity = %q, want the chain's highest", back[0].Severity)
	}
	if back[0].OriginalSeverity == nil || *back[0].OriginalSeverity != "low" {
		t.Errorf("root original_severity = %v, want low", back[0].OriginalSeverity)
	}
	if len(checkedOut) != 2 {
		t.Fatalf("checked_out = %d, want 2", len(checkedOut))
	}
	if back[1].IsPosted() || back[2].IsPosted() {
		t.Error("a folded member still posts")
	}
}

// A pre_existing correction replaces the value and records the original;
// a correction that changes nothing records nothing.
func TestApplyCheckDecisionsCorrectsPreExisting(t *testing.T) {
	raw := `[
		{"id":"aaaaaaaaaaaaaaaa","path":"a.go","line":1,"side":"RIGHT","severity":"high","category":"correctness","pre_existing":false,"title":"one","why":"w","fix":"f"},
		{"id":"bbbbbbbbbbbbbbbb","path":"a.go","line":2,"side":"RIGHT","severity":"high","category":"correctness","pre_existing":false,"title":"two","why":"w","fix":"f"}
	]`
	ordered := checkOrdered(t, raw)
	decisions := []validate.CheckDecision{
		{Position: 1, Decision: "confirmed", Reason: "survives a revert", PreExisting: true, HasPre: true},
		{Position: 2, Decision: "confirmed", Reason: "as raised", Severity: "high"},
	}
	applied, records, _, applyErr := applyCheckDecisions(json.RawMessage(raw), ordered, decisions)
	if applyErr != nil {
		t.Fatalf("apply: %v", applyErr)
	}
	back := checkFindings(t, string(applied))
	if !back[0].PreExisting {
		t.Error("the correction did not replace pre_existing")
	}
	if back[0].OriginalPreExisting == nil || *back[0].OriginalPreExisting {
		t.Errorf("original_pre_existing = %v, want false", back[0].OriginalPreExisting)
	}
	if records[0].PreExisting == nil || !*records[0].PreExisting {
		t.Errorf("record pre_existing = %v, want the true correction", records[0].PreExisting)
	}
	if back[1].OriginalSeverity != nil || back[1].OriginalPreExisting != nil {
		t.Errorf("an unchanged finding records originals: %+v", back[1])
	}
	if len(records[1].OriginalConcerns) != 0 {
		t.Errorf("a lone confirmation records original concerns: %s", records[1].OriginalConcerns)
	}
}

// Reasons over 300 characters and titles over 120 truncate on the
// record; the transcript keeps the full text.
func TestApplyCheckDecisionsTruncatesReasonAndTitle(t *testing.T) {
	raw := `[{"id":"aaaaaaaaaaaaaaaa","path":"a.go","line":1,"side":"RIGHT","severity":"high","category":"correctness","pre_existing":false,"title":"` + strings.Repeat("t", 200) + `","why":"w","fix":"f"}]`
	ordered := checkOrdered(t, raw)
	decisions := []validate.CheckDecision{
		{Position: 1, Decision: "rejected", Reason: strings.Repeat("r", 400)},
	}
	_, records, checkedOut, applyErr := applyCheckDecisions(json.RawMessage(raw), ordered, decisions)
	if applyErr != nil {
		t.Fatalf("apply: %v", applyErr)
	}
	if len([]rune(records[0].Reason)) != 300 || len([]rune(checkedOut[0].Reason)) != 300 {
		t.Errorf("reason truncates to %d and %d, want 300", len([]rune(records[0].Reason)), len([]rune(checkedOut[0].Reason)))
	}
	if len([]rune(checkedOut[0].Title)) != 120 {
		t.Errorf("title truncates to %d, want 120", len([]rune(checkedOut[0].Title)))
	}
}

// Positions number the path-ordered candidates, not the marker
// entries: with the marker listing b.go before a.go, position 1 still
// judges a.go, and the decision lands on a.go's entry.
func TestApplyCheckDecisionsFollowsCandidateOrder(t *testing.T) {
	raw := `[
		{"id":"bbbbbbbbbbbbbbbb","path":"b.go","line":1,"side":"RIGHT","severity":"high","category":"correctness","pre_existing":false,"title":"bee","why":"w","fix":"f"},
		{"id":"aaaaaaaaaaaaaaaa","path":"a.go","line":1,"side":"RIGHT","severity":"high","category":"correctness","pre_existing":false,"title":"aye","why":"w","fix":"f"}
	]`
	ordered := checkOrdered(t, raw)
	if ordered[0].finding.ID != "aaaaaaaaaaaaaaaa" || ordered[0].index != 1 {
		t.Fatalf("position 1 = %+v, want a.go at index 1", ordered[0])
	}
	if ordered[1].finding.ID != "bbbbbbbbbbbbbbbb" || ordered[1].index != 0 {
		t.Fatalf("position 2 = %+v, want b.go at index 0", ordered[1])
	}
	decisions := []validate.CheckDecision{
		{Position: 1, Decision: "confirmed", Reason: "real"},
		{Position: 2, Decision: "rejected", Reason: "wrong"},
	}
	applied, records, checkedOut, err := applyCheckDecisions(json.RawMessage(raw), ordered, decisions)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if records[0].ID != "aaaaaaaaaaaaaaaa" || checkedOut[0].ID != "bbbbbbbbbbbbbbbb" {
		t.Errorf("records = %+v checked_out = %+v, want the decisions on their entries", records, checkedOut)
	}
	back := checkFindings(t, string(applied))
	// The stored order stands: b.go first, stamped; a.go second, posting.
	if back[0].ID != "bbbbbbbbbbbbbbbb" || back[0].IsPosted() {
		t.Errorf("stored first entry = %+v, want b.go stamped", back[0])
	}
	if back[1].ID != "aaaaaaaaaaaaaaaa" || !back[1].IsPosted() {
		t.Errorf("stored second entry = %+v, want a.go posting", back[1])
	}
}

// A raw record that no longer parses beside its findings fails loudly
// rather than applying decisions onto entries they were not judged for.
func TestApplyCheckDecisionsRefusesAMisalignedRecord(t *testing.T) {
	raw := `[{"id":"a"}]`
	ordered := checkOrdered(t, raw)
	decisions := []validate.CheckDecision{{Position: 1, Decision: "confirmed", Reason: "x"}}
	if _, _, _, err := applyCheckDecisions(json.RawMessage(`[{"id":"a"},{"id":"b"}]`), ordered, decisions); err == nil {
		t.Error("a misaligned record applies")
	}
	if _, _, _, err := applyCheckDecisions(json.RawMessage(`{broken`), ordered, decisions); err == nil {
		t.Error("an unparseable record applies")
	}
}

// Stripping removes a check's application: the posted stamps go, and
// the corrections revert to their recorded originals. A posted:true
// entry is nobody's business here and stays.
func TestStripCheckApplicationRevertsToTheRaisedValues(t *testing.T) {
	raw := json.RawMessage(`[
		{"id":"a","severity":"high","original_severity":"medium","pre_existing":true,"original_pre_existing":false,"posted":false},
		{"id":"b","severity":"low","posted":true}
	]`)
	stripped := stripCheckApplication(raw)
	var back []map[string]json.RawMessage
	if err := json.Unmarshal(stripped, &back); err != nil {
		t.Fatalf("stripped decode: %v", err)
	}
	if string(back[0]["severity"]) != `"medium"` || string(back[0]["pre_existing"]) != "false" {
		t.Errorf("stripped entry = %v, want the raised values", back[0])
	}
	for _, key := range []string{"posted", "original_severity", "original_pre_existing"} {
		if _, ok := back[0][key]; ok {
			t.Errorf("stripped entry keeps %s", key)
		}
	}
	if string(back[1]["posted"]) != "true" {
		t.Errorf("posted:true entry = %v", back[1])
	}
	if out := stripCheckApplication(nil); out != nil {
		t.Errorf("stripping nothing answers %s", out)
	}
}

// The concerns revert restores each survivor's original concerns from
// the discarded record, reconciled by id; an empty original removes
// the key, and a record for a finding that moved lands nowhere.
func TestRevertCheckConcernsRestoresOriginalsByID(t *testing.T) {
	raw := json.RawMessage(`[
		{"id":"a","concerns":["correctness","consistency"]},
		{"id":"b","concerns":["correctness"]}
	]`)
	stored := []prstate.CheckDecision{
		{Position: 1, ID: "a", OriginalConcerns: json.RawMessage(`["correctness"]`)},
		{Position: 2, ID: "moved-away", OriginalConcerns: json.RawMessage(`["consistency"]`)},
	}
	reverted := revertCheckConcerns(raw, stored, orderCandidates(checkFindings(t, string(raw))))
	var back []map[string]json.RawMessage
	if err := json.Unmarshal(reverted, &back); err != nil {
		t.Fatalf("reverted decode: %v", err)
	}
	if string(back[0]["concerns"]) != `["correctness"]` {
		t.Errorf("survivor concerns = %s, want the original", back[0]["concerns"])
	}
	if string(back[1]["concerns"]) != `["correctness"]` {
		t.Errorf("moved finding concerns = %s, want them untouched", back[1]["concerns"])
	}
	emptyRaw := json.RawMessage(`[{"id":"a","concerns":["consistency"]}]`)
	empty := revertCheckConcerns(emptyRaw,
		[]prstate.CheckDecision{{Position: 1, ID: "a", OriginalConcerns: json.RawMessage(`[]`)}}, orderCandidates(checkFindings(t, string(emptyRaw))))
	var emptyBack []map[string]json.RawMessage
	if err := json.Unmarshal(empty, &emptyBack); err != nil {
		t.Fatalf("empty decode: %v", err)
	}
	if _, ok := emptyBack[0]["concerns"]; ok {
		t.Errorf("empty original keeps concerns: %v", emptyBack[0])
	}
}

// The failure classes: unverified isolation, an unknown or missing
// harness, a harness failure and a rejected answer degrade to posting
// unchecked; the tripwire, a restore failure, a credential or endpoint
// refusal, a reads halt and cancellation propagate.
func TestCheckDegradeMapsTheFailureClasses(t *testing.T) {
	fatal := func(kind error, reason string) error {
		return &ui.FatalError{Reason: reason, Kind: kind}
	}
	for _, tc := range []struct {
		name   string
		err    error
		state  string
		reason string
	}{
		{"isolation", fatal(harness.ErrIsolationUnverified, "the grok review leg cannot be verified at pin 1.0.5 (review_isolation_unverified)"), prstate.CheckUnavailable, checkReasonIsolationUnverified},
		{"quota", fatal(harness.ErrHarnessFailed, "the claude harness failed: quota exceeded"), prstate.CheckDegraded, checkReasonQuota},
		{"transient", fatal(harness.ErrHarnessFailed, "the claude harness failed: 503 server error"), prstate.CheckDegraded, checkReasonTransient},
		{"harness failed", fatal(harness.ErrHarnessFailed, "the claude harness failed: 401 unauthorized"), prstate.CheckDegraded, checkReasonHarnessFailed},
		{"answer rejected", fatal(harness.ErrAnswerRejected, "claude twice returned an answer that contradicts what it was given"), prstate.CheckDegraded, checkReasonAnswerRejected},
		{"not installed", &harness.Refusal{Reason: "no claude binary", Kind: harness.ErrNotInstalled}, prstate.CheckUnavailable, checkReasonNotInstalled},
		{"version refused", &harness.Refusal{Reason: "unsupported CLI", Kind: harness.ErrVersionUnsupported}, prstate.CheckUnavailable, checkReasonVersionRefused},
		{"other refusal", &harness.Refusal{Reason: "no scratch", Kind: harness.ErrScratch}, prstate.CheckUnavailable, checkReasonUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state, reason, ok := checkDegrade(tc.err)
			if !ok {
				t.Fatalf("the failure propagates, want %s %s", tc.state, tc.reason)
			}
			if state != tc.state || reason != tc.reason {
				t.Errorf("degrade = %s %s, want %s %s", state, reason, tc.state, tc.reason)
			}
		})
	}
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"tripwire", fatal(nil, "the review leg ran a command (review_leg_ran_command)")},
		{"restore", newSandboxRestoreFailure("claude", "the answer failed", "permission denied")},
		{"endpoint", &harness.Refusal{Reason: "no endpoint", Kind: harness.ErrEndpointUnsupported}},
		{"endpoint token", &harness.Refusal{Reason: "no token", Kind: harness.ErrEndpointToken}},
		{"reads halt", fatal(nil, "reads unavailable and the policy says halt (reads_unavailable)")},
		{"cancelled", errors.Join(context.Canceled, &ui.FatalError{Reason: "interrupted"})},
		{"unknown", errors.New("boom")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if state, reason, ok := checkDegrade(tc.err); ok {
				t.Errorf("the failure degrades to %s %s, want it propagated", state, reason)
			}
		})
	}
}

// A malformed group — a duplicate chain the validator would have
// refused — folds nothing and loops never.
func TestCheckGroupsStopsAMalformedChain(t *testing.T) {
	decisions := []validate.CheckDecision{
		{Position: 1, Decision: "duplicate", DuplicateOf: 1},
		{Position: 2, Decision: "duplicate", DuplicateOf: 3},
		{Position: 3, Decision: "duplicate", DuplicateOf: 2},
		{Position: 4, Decision: "confirmed"},
	}
	groups := checkGroups(decisions)
	if len(groups) != 0 {
		t.Errorf("groups = %v, want none", groups)
	}
}

// Packing keeps path order, holds a shared excerpt in one call, and
// fits each call's rendered bytes to the limit; a group past the
// limit alone splits, and a singleton past the limit still goes, in a
// call of its own.
func TestPackCheckCallsBoundsAndDedupes(t *testing.T) {
	meta := prompt.Meta{Repo: prompt.Str("acme/widget"), PR: prompt.Num(42), Pass: prompt.Num(1)}
	mk := func(position int, path string, excerpt string) checkCandidate {
		return checkCandidate{
			position:     position,
			finding:      Finding{ID: "id", Path: path, Line: 1, Side: "RIGHT", Severity: "high", Category: "correctness", Title: "t", Why: "w"},
			excerptLabel: "excerpt",
			excerpt:      []byte(excerpt),
		}
	}
	// Two candidates on one hunk share it; a third elsewhere stands
	// alone. The limit fits the shared group plus nothing else.
	candidates := []checkCandidate{
		mk(1, "a.go", "shared hunk"),
		mk(2, "a.go", "shared hunk"),
		mk(3, "b.go", "other hunk"),
	}
	overhead := len(prompt.Check{Meta: meta}.Render())
	shared := len(renderCheckCall(meta, "", candidates[:2], 3)) - overhead
	solo := len(renderCheckCall(meta, "", candidates[2:], 3)) - overhead
	calls := packCheckCalls(meta, "", candidates, overhead, overhead+shared)
	if len(calls) != 2 {
		t.Fatalf("calls = %d, want 2", len(calls))
	}
	if len(calls[0]) != 2 || calls[0][0].position != 1 || calls[0][1].position != 2 {
		t.Errorf("first call holds positions %v, want 1 and 2", positionsOf(calls[0]))
	}
	if len(calls[1]) != 1 || calls[1][0].position != 3 {
		t.Errorf("second call holds positions %v, want 3", positionsOf(calls[1]))
	}
	rendered := string(renderCheckCall(meta, "", calls[0], 3))
	if strings.Count(rendered, "shared hunk") != 1 {
		t.Errorf("the shared excerpt renders %d times, want once:\n%s", strings.Count(rendered, "shared hunk"), rendered)
	}
	if !strings.Contains(rendered, "Candidates 1, 2 share the excerpt below.") || !strings.Contains(rendered, "Excerpt: as candidate 1 above.") {
		t.Errorf("the shared rendering names no sharing:\n%s", rendered)
	}
	// A limit fitting everything packs one call; a limit fitting
	// nothing splits the shared group and still sends each candidate
	// alone rather than dropping one.
	if calls := packCheckCalls(meta, "", candidates, overhead, overhead+shared+solo); len(calls) != 1 {
		t.Errorf("roomy packing makes %d calls, want 1", len(calls))
	}
	if calls := packCheckCalls(meta, "", candidates, overhead, overhead); len(calls) != 3 {
		t.Errorf("tight packing makes %d calls, want 3", len(calls))
	}
}

// A group past the limit alone splits across calls instead of going
// unbounded: each candidate renders the shared excerpt in its own
// call. A singleton past the limit still goes in a call of its own,
// for the hard-limit preflight to judge.
func TestPackCheckCallsSplitsAnOverflowingGroup(t *testing.T) {
	meta := prompt.Meta{Repo: prompt.Str("acme/widget"), PR: prompt.Num(42), Pass: prompt.Num(1)}
	mk := func(position int, excerpt string) checkCandidate {
		return checkCandidate{
			position:     position,
			finding:      Finding{ID: "id", Path: "a.go", Line: 1, Side: "RIGHT", Severity: "high", Category: "correctness", Title: "t", Why: "w"},
			excerptLabel: "excerpt",
			excerpt:      []byte(excerpt),
		}
	}
	shared := "a hunk long enough to matter, " + strings.Repeat("x", 2000)
	candidates := []checkCandidate{mk(1, shared), mk(2, shared)}
	overhead := len(prompt.Check{Meta: meta}.Render())
	solo := len(renderCheckCall(meta, "", candidates[:1], 2)) - overhead
	calls := packCheckCalls(meta, "", candidates, overhead, overhead+solo)
	if len(calls) != 2 {
		t.Fatalf("calls = %d, want 2 (the overflowing group split)", len(calls))
	}
	if got := positionsOf(calls[0]); len(got) != 1 || got[0] != 1 {
		t.Errorf("first call holds positions %v, want 1", got)
	}
	if got := positionsOf(calls[1]); len(got) != 1 || got[0] != 2 {
		t.Errorf("second call holds positions %v, want 2", got)
	}
	for i, call := range calls {
		rendered := string(renderCheckCall(meta, "", call, 2))
		if strings.Count(rendered, shared) != 1 {
			t.Errorf("call %d renders the excerpt %d times, want once", i+1, strings.Count(rendered, shared))
		}
	}
	if calls := packCheckCalls(meta, "", candidates[:1], overhead, overhead); len(calls) != 1 {
		t.Errorf("an overflowing singleton makes %d calls, want 1", len(calls))
	}
}

func positionsOf(candidates []checkCandidate) []int {
	out := make([]int, 0, len(candidates))
	for _, candidate := range candidates {
		out = append(out, candidate.position)
	}
	return out
}

// A record matches only on the same digest, revision pair and check
// mode; anything else re-checks.
func TestCheckRecordMatchesNeedsDigestRevisionAndMode(t *testing.T) {
	base, err := core.NewRevision("0913bf7b99dcecf746d0e6fcef5a9c1d64aaf3b0")
	if err != nil {
		t.Fatal(err)
	}
	head, err := core.NewRevision("2c4a46cb321db01826d116b5ef2add6b0284d68c")
	if err != nil {
		t.Fatal(err)
	}
	loaded := Context{}
	loaded.PR.BaseRefOid = base
	loaded.PR.HeadRefOid = head
	record := prstate.CheckRecord{
		Digest: "abc",
		Base:   "0913bf7b99dcecf746d0e6fcef5a9c1d64aaf3b0",
		Head:   "2c4a46cb321db01826d116b5ef2add6b0284d68c",
		Check:  "resolver",
	}
	if !checkRecordMatches(record, "abc", loaded, "resolver") {
		t.Error("the matching record does not match")
	}
	moved := record
	moved.Digest = "def"
	if checkRecordMatches(moved, "abc", loaded, "resolver") {
		t.Error("a moved digest matches")
	}
	moved = record
	moved.Head = "3333333333333333333333333333333333333333"
	if checkRecordMatches(moved, "abc", loaded, "resolver") {
		t.Error("a moved head matches")
	}
	if checkRecordMatches(record, "abc", loaded, "off") {
		t.Error("a changed check mode matches")
	}
}

// The digest is stable over the same candidates and moves with any
// judged input: words, values, concerns, excerpts and the changed
// fact alike.
func TestCheckDigestMovesWithAnyJudgedInput(t *testing.T) {
	base := []checkCandidate{{
		position: 1,
		finding: Finding{ID: "a", Path: "app.go", Line: 2, Side: "RIGHT", Severity: "high", Category: "correctness", Title: "t", Why: "w", Concerns: []string{"correctness"}},
		changed:  true,
		excerpt:  []byte("hunk"),
	}}
	want := checkDigest(base)
	if want == "" {
		t.Fatal("the digest is empty")
	}
	if again := checkDigest(base); again != want {
		t.Fatal("the digest is unstable")
	}
	mutations := map[string]func(*checkCandidate){
		"title":       func(c *checkCandidate) { c.finding.Title = "other" },
		"why":         func(c *checkCandidate) { c.finding.Why = "other" },
		"severity":    func(c *checkCandidate) { c.finding.Severity = "low" },
		"category":    func(c *checkCandidate) { c.finding.Category = "style" },
		"pre_existing": func(c *checkCandidate) { c.finding.PreExisting = true },
		"concerns":    func(c *checkCandidate) { c.finding.Concerns = []string{"consistency"} },
		"line":        func(c *checkCandidate) { c.finding.Line = 3 },
		"changed":     func(c *checkCandidate) { c.changed = false },
		"excerpt":     func(c *checkCandidate) { c.excerpt = []byte("other hunk") },
	}
	for name, mutate := range mutations {
		candidates := []checkCandidate{base[0]}
		mutate(&candidates[0])
		if got := checkDigest(candidates); got == want {
			t.Errorf("the digest survives a changed %s", name)
		}
	}
}
