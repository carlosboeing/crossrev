package prstate_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/prstate"
)

// The verification key carries the overall state and the per-check detail,
// in the writers' field order after redriven.
func TestVerificationMarkerBytes(t *testing.T) {
	marker := prstate.Marker{
		Version: 2,
		Pass:    1,
		Verification: prstate.Some(prstate.MarkerVerification{
			State: "failed",
			Checks: []prstate.MarkerVerificationCheck{
				{Name: "build", App: "github-actions", State: "failed",
					Conclusion: "failure", URL: "https://github.com/acme/widget/runs/11"},
			},
		}),
	}
	encoded, err := marker.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	want := `"verification":{"state":"failed","reason":"","checks":[` +
		`{"name":"build","app":"github-actions","state":"failed",` +
		`"conclusion":"failure","url":"https://github.com/acme/widget/runs/11","note":""}]}`
	if !strings.Contains(string(encoded), want) {
		t.Errorf("encoded marker =\n%s\nwant it to contain\n%s", encoded, want)
	}
	raw, ok := prstate.DecodeMarker("body" + encoded)
	if !ok {
		t.Fatalf("DecodeMarker found no marker in %s", encoded)
	}
	var back prstate.Marker
	if err := back.UnmarshalJSON(raw); err != nil {
		t.Fatalf("UnmarshalJSON: %v", err)
	}
	got, ok := back.Verification.Get()
	if !ok || got.State != "failed" || len(got.Checks) != 1 || got.Checks[0].Name != "build" {
		t.Errorf("decoded verification = %+v,%v", got, ok)
	}
}

// An oversized verification detail fails the marker write loudly: the
// retention ladder sheds coverage, never evidence, so a marker that cannot
// fit it answers LedgerExhausted with the field intact.
func TestOversizedVerificationFailsTheWriteLoudly(t *testing.T) {
	marker := prstate.Marker{
		Version: 2,
		Pass:    1,
		Verification: prstate.Some(prstate.MarkerVerification{
			State:  "failed",
			Reason: strings.Repeat("x", prstate.CommentCap),
		}),
	}
	render := func(m prstate.Marker) (string, error) {
		encoded, err := m.Encode()
		if err != nil {
			return "", err
		}
		return "body" + string(encoded), nil
	}
	_, err := prstate.ShedToFit(&marker, render, prstate.OverflowDegrade)
	var exhausted *prstate.LedgerExhausted
	if !errors.As(err, &exhausted) {
		t.Fatalf("ShedToFit = %v, want a *LedgerExhausted", err)
	}
	if _, ok := marker.Verification.Get(); !ok {
		t.Error("the ladder shed the verification evidence it must never shed")
	}
}
