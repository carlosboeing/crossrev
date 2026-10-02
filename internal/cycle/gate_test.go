package cycle

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/prstate"
	"github.com/carlosboeing/crossrev/internal/ui"
)

// gateSettleState is one pass whose review raised a finding and whose
// resolve leg disputed it without pushing, judged under gate state.
func gateSettleState(state string) State {
	resolve := prstate.Marker{
		Leg:         core.LegResolve,
		Pass:        1,
		State:       core.PassComplete,
		Resolutions: json.RawMessage(`[{"resolution":"disputed"}]`),
	}
	if state != "" {
		resolve.Verification = prstate.Some(prstate.MarkerVerification{State: state})
	}
	review := prstate.Marker{Leg: core.LegReview, Pass: 1, State: core.PassComplete, Verdict: prstate.Some("issues_remain")}
	return State{Markers: []prstate.Marker{review, resolve}, MinFixSeverity: core.Severity("medium")}
}

// The cycle reads the resolve pass by the rule the resolve leg labelled it
// with, so a settle the required checks held halted ends the cycle halted
// rather than reading as a clean settle.
func TestCycleReadsAGateHeldSettleAsHalted(t *testing.T) {
	for _, state := range []string{"failed", "pending", "missing", "unreadable"} {
		var buf bytes.Buffer
		if got := readResolve(&ui.IO{Out: &buf}, gateSettleState(state), 1); got != resolveHalted {
			t.Errorf("gate %s: reading = %v, want halted\n%s", state, got, buf.String())
		}
		if !strings.Contains(buf.String(), "required checks block convergence") {
			t.Errorf("gate %s: output does not name the gate:\n%s", state, buf.String())
		}
	}
}

// A passed or unconfigured gate leaves the settle as it read before.
func TestCycleReadsAPassedGateSettleAsBefore(t *testing.T) {
	var base, passed bytes.Buffer
	want := readResolve(&ui.IO{Out: &base}, gateSettleState(""), 1)
	if got := readResolve(&ui.IO{Out: &passed}, gateSettleState("passed"), 1); got != want || passed.String() != base.String() {
		t.Fatalf("passed gate = %v %q, want %v %q", got, passed.String(), want, base.String())
	}
}
