package prstate_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/prstate"
)

// The check tail encodes after redriven, in writer order, and reads back.
func TestMarkerCarriesTheCheckTail(t *testing.T) {
	in := prstate.Marker{
		Version:      core.MarkerVersion,
		Leg:          core.LegReview,
		Pass:         1,
		State:        core.PassComplete,
		TS:           1700000000,
		Redriven:     prstate.Some(true),
		Check:        prstate.Some(prstate.CheckRan),
		CheckReason:  prstate.Some(prstate.CheckReasonSameModel),
		CheckRecord:  json.RawMessage(`{"digest":"abc","decisions":[]}`),
		CheckedOut:   json.RawMessage(`[{"position":2,"id":"aaaaaaaaaaaaaaaa","decision":"rejected"}]`),
		Findings:     json.RawMessage(`[]`),
	}
	raw, err := in.MarshalJSON()
	if err != nil {
		t.Fatalf("MarshalJSON: %v", err)
	}
	want := `"redriven":true,"check":"ran","check_reason":"same_model","check_record":{"digest":"abc","decisions":[]},"checked_out":[{"position":2,"id":"aaaaaaaaaaaaaaaa","decision":"rejected"}]`
	if !strings.Contains(string(raw), want) {
		t.Errorf("encoded marker = %s, want it to carry %s", raw, want)
	}
	var back prstate.Marker
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("UnmarshalJSON: %v", err)
	}
	if back.Check.Value() != prstate.CheckRan || back.CheckReason.Value() != prstate.CheckReasonSameModel {
		t.Errorf("check tail reads back as %q %q", back.Check.Value(), back.CheckReason.Value())
	}
	record, ok := back.DecodeCheckRecord()
	if !ok || record.Digest != "abc" {
		t.Errorf("check_record reads back as %+v, %v", record, ok)
	}
	if entries := back.DecodeCheckedOut(); len(entries) != 1 || entries[0].ID != "aaaaaaaaaaaaaaaa" {
		t.Errorf("checked_out reads back as %+v", entries)
	}
}

// A marker from before the check reads as no record and no entry, and its
// bytes are untouched by the new fields.
func TestMarkerWithoutCheckReadsEmpty(t *testing.T) {
	raw := json.RawMessage(`{"v":2,"leg":"review","pass":1,"state":"complete","ts":1700000000,"findings":[]}`)
	marker, err := prstate.ParseMarker(raw)
	if err != nil {
		t.Fatalf("ParseMarker: %v", err)
	}
	if marker.Check.Present() || marker.CheckReason.Present() {
		t.Error("a marker from before the check carries check state")
	}
	if _, ok := marker.DecodeCheckRecord(); ok {
		t.Error("a marker from before the check carries a check record")
	}
	if entries := marker.DecodeCheckedOut(); len(entries) != 0 {
		t.Errorf("a marker from before the check carries %d checked-out entries", len(entries))
	}
	if ids := prstate.CheckedOutIDs(marker.CheckedOut); len(ids) != 0 {
		t.Errorf("a marker from before the check checks out %d ids", len(ids))
	}
	back, err := marker.MarshalJSON()
	if err != nil {
		t.Fatalf("MarshalJSON: %v", err)
	}
	if strings.Contains(string(back), "check") {
		t.Errorf("re-encoded marker = %s, want no check keys", back)
	}
}

// CheckedOutIDs names every rejected and duplicate id, and nothing else.
func TestCheckedOutIDsNamesBothDecisions(t *testing.T) {
	raw := json.RawMessage(`[
		{"position":1,"id":"aaaaaaaaaaaaaaaa","decision":"rejected"},
		{"position":2,"id":"bbbbbbbbbbbbbbbb","decision":"duplicate"},
		{"position":3,"id":"","decision":"rejected"}
	]`)
	ids := prstate.CheckedOutIDs(raw)
	if len(ids) != 2 || !ids["aaaaaaaaaaaaaaaa"] || !ids["bbbbbbbbbbbbbbbb"] {
		t.Errorf("ids = %v", ids)
	}
}

// The summary block lists rejected candidates, at most ten with a count of
// the rest; duplicates stay on the marker and out of the summary.
func TestCheckSummaryBlockListsRejectedCandidates(t *testing.T) {
	entries := make([]string, 0, 12)
	for i := 1; i <= 12; i++ {
		entries = append(entries, `{"position":`+itoa(i)+`,"id":"id`+itoa(i)+`","path":"a.go","line":`+itoa(i)+`,"title":"defect `+itoa(i)+`","reason":"not a defect","decision":"rejected"}`)
	}
	entries = append(entries, `{"position":13,"id":"dup","path":"a.go","line":13,"title":"same defect","reason":"same point","decision":"duplicate"}`)
	raw := json.RawMessage("[" + strings.Join(entries, ",") + "]")
	got := prstate.CheckSummaryBlock(prstate.CheckRan, "", raw)
	if !strings.HasPrefix(got, "12 candidates rejected by the cross-model check:\n\n") {
		t.Errorf("block opens as:\n%s", got)
	}
	if strings.Count(got, "\n- `a.go:") != 10 {
		t.Errorf("block lists %d candidates, want 10:\n%s", strings.Count(got, "\n- `a.go:"), got)
	}
	if !strings.Contains(got, "- …and 2 more\n") {
		t.Errorf("block carries no count of the rest:\n%s", got)
	}
	if strings.Contains(got, "same defect") {
		t.Errorf("block lists the duplicate:\n%s", got)
	}
	single := json.RawMessage(`[{"position":1,"id":"a","path":"a.go","line":1,"title":"t","reason":"r","decision":"rejected"}]`)
	if got := prstate.CheckSummaryBlock(prstate.CheckRan, "", single); !strings.HasPrefix(got, "1 candidate rejected by the cross-model check:") {
		t.Errorf("singular block opens as:\n%s", got)
	}
}

// Degraded, unavailable and same-model passes say so in the summary, with
// the reason beside the state.
func TestCheckSummaryBlockStatesTheCheckOutcome(t *testing.T) {
	for _, tc := range []struct {
		name   string
		check  string
		reason string
		want   string
	}{
		{"degraded", prstate.CheckDegraded, "quota", "check: degraded (quota): every candidate posted unchecked."},
		{"unavailable", prstate.CheckUnavailable, "review_isolation_unverified", "check: unavailable (review_isolation_unverified): every candidate posted unchecked."},
		{"same model", prstate.CheckRan, prstate.CheckReasonSameModel, "check: ran (same_model): the checker answered as the model that reviewed, so this pass had no second lineage."},
	} {
		if got := prstate.CheckSummaryBlock(tc.check, tc.reason, nil); !strings.Contains(got, tc.want) {
			t.Errorf("%s block = %q, want it to carry %q", tc.name, got, tc.want)
		}
	}
	// A same-model pass never calls its check cross-model.
	rejected := json.RawMessage(`[{"position":1,"id":"a","path":"a.go","line":1,"title":"t","reason":"r","decision":"rejected"}]`)
	got := prstate.CheckSummaryBlock(prstate.CheckRan, prstate.CheckReasonSameModel, rejected)
	if strings.Contains(got, "cross-model") {
		t.Errorf("same-model block calls the check cross-model:\n%s", got)
	}
	if !strings.Contains(got, "1 candidate rejected by the check:") {
		t.Errorf("same-model block = %q", got)
	}
	// A clean ran, an off and a no-candidates pass render nothing.
	for _, check := range []string{prstate.CheckRan, prstate.CheckOff, prstate.CheckNoCandidates, ""} {
		if got := prstate.CheckSummaryBlock(check, "", nil); got != "" {
			t.Errorf("check %q renders %q, want nothing", check, got)
		}
	}
}

// The retention ladder sheds coverage payloads only: an oversized
// marker whose bulk is check fields fails instead of dropping a
// decision to fit, and the fields read back byte for byte.
func TestShedToFitNeverShedsTheCheckTail(t *testing.T) {
	big := strings.Repeat("r", 300)
	var decisions, entries strings.Builder
	decisions.WriteString("[")
	entries.WriteString("[")
	for i := 1; i <= 150; i++ {
		if i > 1 {
			decisions.WriteString(",")
			entries.WriteString(",")
		}
		decisions.WriteString(`{"position":` + itoa(i) + `,"id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","decision":"rejected","reason":"` + big + `"}`)
		entries.WriteString(`{"position":` + itoa(i) + `,"id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","path":"app.go","line":2,"title":"defect","reason":"` + big + `","decision":"rejected"}`)
	}
	decisions.WriteString("]")
	entries.WriteString("]")
	marker := prstate.Marker{
		Version:     core.MarkerVersion,
		Leg:         core.LegReview,
		Pass:        1,
		State:       core.PassStarted,
		TS:          1700000000,
		Check:       prstate.Some(prstate.CheckRan),
		CheckRecord: json.RawMessage(`{"digest":"abc","decisions":` + decisions.String() + `}`),
		CheckedOut:  json.RawMessage(entries.String()),
		Findings:    json.RawMessage(`[]`),
	}
	recordBefore, checkedOutBefore := string(marker.CheckRecord), string(marker.CheckedOut)
	render := func(m prstate.Marker) (string, error) {
		encoded, err := m.Encode()
		if err != nil {
			return "", err
		}
		return "body" + encoded, nil
	}
	shed, err := prstate.ShedToFit(&marker, render, prstate.OverflowDegrade)
	if err == nil {
		t.Fatal("an unsheddable oversized marker fits")
	}
	var exhausted *prstate.LedgerExhausted
	if !errors.As(err, &exhausted) {
		t.Fatalf("err = %v, want the loud ladder failure", err)
	}
	if shed.DroppedPredecessor || shed.Degraded {
		t.Errorf("shed = %+v, want nothing shed", shed)
	}
	if string(marker.CheckRecord) != recordBefore || string(marker.CheckedOut) != checkedOutBefore {
		t.Error("the ladder touched the check fields")
	}
	if record, ok := marker.DecodeCheckRecord(); !ok || len(record.Decisions) != 150 {
		t.Errorf("the record keeps %d decisions, want 150", len(record.Decisions))
	}
	if entries := marker.DecodeCheckedOut(); len(entries) != 150 {
		t.Errorf("checked_out keeps %d entries, want 150", len(entries))
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
