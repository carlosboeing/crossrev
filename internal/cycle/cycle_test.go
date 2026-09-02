package cycle

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/prstate"
	"github.com/carlosboeing/crossrev/internal/ui"
)

// The marker shapes the driver's decisions turn on. Each is a whole marker as
// state_markers hands one over, so nothing here is a partial object the real
// reader would never see.
const (
	reviewIssues    = `{"v":1,"leg":"review","pass":%d,"state":"complete","verdict":"issues-remain","findings":[{"severity":"high"}]}`
	reviewEmpty     = `{"v":1,"leg":"review","pass":%d,"state":"complete","verdict":"issues-remain","findings":[]}`
	reviewConverged = `{"v":1,"leg":"review","pass":%d,"state":"complete","verdict":"converged","findings":[]}`
	reviewBlocked   = `{"v":1,"leg":"review","pass":%d,"state":"complete","verdict":"blocked","findings":[]}`

	// A pass that pushed hands back to the reviewer, so the loop goes round.
	resolvePushed = `{"v":1,"leg":"resolve","pass":%d,"state":"complete","blocked":false,"commit_sha":"abc123","resolutions":[{"resolution":"fixed"}]}`
	// The resolver reported blocked.
	resolveBlocked = `{"v":1,"leg":"resolve","pass":%d,"state":"complete","blocked":true,"resolutions":[{"resolution":"fixed"}],"commit_sha":"abc123"}`
	// Every finding answered and nothing pushed: legs_resolve_pass_label reads
	// converged, because the next review would decline an unchanged head.
	resolveSettled = `{"v":1,"leg":"resolve","pass":%d,"state":"complete","blocked":false,"resolutions":[{"resolution":"disputed"}]}`
	// A deferral nobody filed: halted, and no crossrev/stop, because nobody
	// pulled the brake.
	resolveUnfiled = `{"v":1,"leg":"resolve","pass":%d,"state":"complete","blocked":false,"resolutions":[{"resolution":"deferred","crossrev_tracked":""}]}`
	// An escalation standing from an earlier pass.
	resolveEscalated = `{"v":1,"leg":"resolve","pass":%d,"state":"complete","blocked":false,"commit_sha":"abc123","resolutions":[{"resolution":"escalated"}]}`
)

// The lines cmd_cycle prints, measured rather than transcribed. ui_say is
// `printf '  %s\n'` and ui_end is `printf '%s└%s  %s\n\n'` with the dim and
// reset escapes empty under NO_COLOR (lib/ui.sh:81 and :96):
//
//	$ NO_COLOR=1 bash -c 'source lib/ui.sh; ui_say hello; ui_end world' | od -c
//	0000000       h   e   l   l   o  \n   └          w   o   r   l   d  \n  \n
const sayCycling = "  Cycling acme/widget#42, up to 3 passes. " +
	"Ctrl-C is safe — each leg finishes the write in flight.\n"

func endLine(text string) string { return "└  " + text + "\n\n" }

// --- fakes ------------------------------------------------------------------

type legCall struct {
	leg core.Leg
	req LegRequest
}

// recorder is the one call log every collaborator writes to, so a test asserts
// the ORDER across the loader, the pairing check, the two legs and the nudge
// rather than each in isolation.
type recorder struct {
	order []string
	legs  []legCall
	loads []LoadRequest
}

type fakeLeg struct {
	rec  *recorder
	leg  core.Leg
	fail bool
}

func (f *fakeLeg) Run(_ context.Context, req LegRequest) LegResult {
	note := string(f.leg)
	if req.Continuation {
		note += "+continuation"
	}
	f.rec.order = append(f.rec.order, note)
	f.rec.legs = append(f.rec.legs, legCall{leg: f.leg, req: req})
	return LegResult{Failed: f.fail}
}

type loadStep struct {
	state State
	err   error
}

// fakeLoader answers a scripted sequence and fails the test when the driver
// asks for one more than the script holds. It does not repeat its last answer:
// a loader that did would let a driver with an extra ctx_load pass unnoticed,
// and the load count is part of what this port has to reproduce.
type fakeLoader struct {
	t     *testing.T
	rec   *recorder
	steps []loadStep
}

func (f *fakeLoader) Load(_ context.Context, req LoadRequest) (State, error) {
	f.t.Helper()
	f.rec.order = append(f.rec.order, "load")
	f.rec.loads = append(f.rec.loads, req)
	if len(f.rec.loads) > len(f.steps) {
		f.t.Fatalf("the driver asked for load %d and the test scripted %d", len(f.rec.loads), len(f.steps))
		return State{}, nil
	}
	step := f.steps[len(f.rec.loads)-1]
	return step.state, step.err
}

func slugOf(t *testing.T, s string) core.Slug {
	t.Helper()
	slug, err := core.ParseSlug(s)
	if err != nil {
		t.Fatalf("ParseSlug(%q): %v", s, err)
	}
	return slug
}

// loaded is one ctx_load answer for the repository every case here uses. The
// caller overrides a field it cares about rather than passing eight arguments;
// TestDriverPrintsTheBytesTheShellPrints builds its State as a literal instead,
// so no case in this file depends on this helper being right.
func loaded(t *testing.T, markers ...string) State {
	t.Helper()
	return State{
		Repo:              slugOf(t, "acme/widget"),
		PR:                42,
		Markers:           parseMarkers(t, "["+strings.Join(markers, ",")+"]"),
		MaxPassesPerCycle: 3,
		MinFixSeverity:    core.SeverityMedium,
	}
}

func marker(shape string, pass int) string { return fmt.Sprintf(shape, pass) }

// rig wires a driver over the recorder and returns both.
type rig struct {
	driver *Driver
	rec    *recorder
	out    *bytes.Buffer
}

func newRig(t *testing.T, steps []loadStep) *rig {
	t.Helper()
	rec := &recorder{}
	out := &bytes.Buffer{}
	return &rig{
		rec: rec,
		out: out,
		driver: &Driver{
			Review:  &fakeLeg{rec: rec, leg: core.LegReview},
			Resolve: &fakeLeg{rec: rec, leg: core.LegResolve},
			Loader:  &fakeLoader{t: t, rec: rec, steps: steps},
			Out:     out,
			Nudge:   func() { rec.order = append(rec.order, "nudge") },
		},
	}
}

func request() Request {
	return Request{PR: 42, Trigger: TriggerHuman}
}

func (r *rig) wantOrder(t *testing.T, want ...string) {
	t.Helper()
	if got := strings.Join(r.rec.order, " "); got != strings.Join(want, " ") {
		t.Errorf("call order\n got %s\nwant %s", got, strings.Join(want, " "))
	}
}

func (r *rig) wantOut(t *testing.T, want string) {
	t.Helper()
	if got := r.out.String(); got != want {
		t.Errorf("output\n got %q\nwant %q", got, want)
	}
}

// --- the two refusals cmd_cycle repeats after the parser ---------------------

// TestDriverRefusesAMissingPullRequestNumber pins lib/run.sh:2910. Measured:
//
//	$ crossrev cycle
//	error  crossrev cycle needs a pull request number
//	       Usage: crossrev cycle --pr 42
func TestDriverRefusesAMissingPullRequestNumber(t *testing.T) {
	r := newRig(t, nil)
	got := r.driver.Run(context.Background(), Request{Trigger: TriggerHuman})
	if got.ExitCode != 1 {
		t.Errorf("exit code = %d, want 1", got.ExitCode)
	}
	var fatal *ui.FatalError
	if !errors.As(got.Err, &fatal) {
		t.Fatalf("Err = %v, want a *ui.FatalError", got.Err)
	}
	if fatal.Reason != "crossrev cycle needs a pull request number" {
		t.Errorf("reason = %q", fatal.Reason)
	}
	if fatal.Action != "Usage: crossrev cycle --pr 42" {
		t.Errorf("action = %q", fatal.Action)
	}
	r.wantOrder(t)
	r.wantOut(t, "")
}

// TestDriverRefusesAnUnknownTrigger pins lib/run.sh:2911-2916, which cmd_cycle
// checks a second time because ctx_load reads the trigger first and treats
// anything it does not recognise as human. Measured:
//
//	$ crossrev cycle --pr 42 --trigger nope
//	error  unknown cycle trigger: nope
//	       Use --trigger human or --trigger automatic.
//
// The empty trigger is refused for the same reason `--trigger ""` is: the case
// statement matches human and automatic and nothing else. The parser defaults
// an absent flag to human, so nothing legitimate reaches here empty.
func TestDriverRefusesAnUnknownTrigger(t *testing.T) {
	for _, trigger := range []Trigger{"nope", ""} {
		t.Run(string(trigger), func(t *testing.T) {
			r := newRig(t, nil)
			got := r.driver.Run(context.Background(), Request{PR: 42, Trigger: trigger})
			if got.ExitCode != 1 {
				t.Errorf("exit code = %d, want 1", got.ExitCode)
			}
			var fatal *ui.FatalError
			if !errors.As(got.Err, &fatal) {
				t.Fatalf("Err = %v, want a *ui.FatalError", got.Err)
			}
			if want := "unknown cycle trigger: " + string(trigger); fatal.Reason != want {
				t.Errorf("reason = %q, want %q", fatal.Reason, want)
			}
			if fatal.Action != "Use --trigger human or --trigger automatic." {
				t.Errorf("action = %q", fatal.Action)
			}
			r.wantOrder(t)
		})
	}
}

// --- the draft decision, applied once and upfront ---------------------------

// TestDriverStopsOnADraftBeforeAnythingRuns pins the return-2 arm of ctx_load
// (lib/run.sh:259-263) as cmd_cycle consumes it (lib/run.sh:2918-2925): two
// lines, exit 0, and no pairing check and no leg, because the shell returns
// before run_assert_cycle_pairing.
//
// The rule is applied here rather than left to the first leg because a cycle
// that declined mid-loop would report the reviewer as unable to finish.
func TestDriverStopsOnADraftBeforeAnythingRuns(t *testing.T) {
	state := loaded(t)
	state.Draft = true
	r := newRig(t, []loadStep{{state: state}})
	r.driver.Pairing = func(string) error {
		r.rec.order = append(r.rec.order, "pairing")
		return nil
	}

	got := r.driver.Run(context.Background(), Request{PR: 42, Trigger: TriggerAutomatic})

	if got.ExitCode != 0 {
		t.Errorf("exit code = %d, want 0", got.ExitCode)
	}
	r.wantOrder(t, "load")
	r.wantOut(t,
		"  acme/widget#42 is a draft pull request, so an automatic invocation does not review it.\n"+
			"  Mark it ready for review, or ask for a review explicitly.\n")
}

// TestDriverStopsWhenTheContextLoadRefuses pins the other arm: ctx_load's
// ui_die exits 1, so the driver hands the refusal up and runs nothing.
func TestDriverStopsWhenTheContextLoadRefuses(t *testing.T) {
	refusal := &ui.FatalError{Reason: "could not read acme/widget#42", Action: "Check the number."}
	r := newRig(t, []loadStep{{err: refusal}})

	got := r.driver.Run(context.Background(), request())

	if got.ExitCode != 1 {
		t.Errorf("exit code = %d, want 1", got.ExitCode)
	}
	if !errors.Is(got.Err, error(refusal)) {
		t.Errorf("Err = %v, want the loader's refusal", got.Err)
	}
	r.wantOrder(t, "load")
	r.wantOut(t, "")
}

// --- the pairing seam -------------------------------------------------------

// TestDriverChecksThePairingBetweenTheLoadAndTheFirstLeg pins where
// run_assert_cycle_pairing sits (lib/run.sh:2926): after the context load, so
// the config is available, and before the pass loop, so a harness that cannot
// resolve is refused without a billed review. The override the request carried
// is what it is asked about.
func TestDriverChecksThePairingBetweenTheLoadAndTheFirstLeg(t *testing.T) {
	r := newRig(t, []loadStep{
		{state: loaded(t)},
		{state: loaded(t, marker(reviewConverged, 1))},
	})
	var asked []string
	r.driver.Pairing = func(override string) error {
		asked = append(asked, override)
		r.rec.order = append(r.rec.order, "pairing")
		return nil
	}

	req := request()
	req.HarnessOverride = "opencode"
	got := r.driver.Run(context.Background(), req)

	if got.ExitCode != 0 {
		t.Errorf("exit code = %d, want 0", got.ExitCode)
	}
	r.wantOrder(t, "load", "pairing", "review", "load", "nudge")
	if len(asked) != 1 || asked[0] != "opencode" {
		t.Errorf("pairing asked about %v, want [opencode]", asked)
	}
}

// TestDriverStopsWhenThePairingCheckRefuses pins the `||` the shell gets from
// ui_die: a harness that does not serve one of the two legs ends the run
// before any leg is billed.
func TestDriverStopsWhenThePairingCheckRefuses(t *testing.T) {
	refusal := &ui.FatalError{Reason: "grok cannot resolve", Action: "Name a harness that serves both legs."}
	r := newRig(t, []loadStep{{state: loaded(t)}})
	r.driver.Pairing = func(string) error { return refusal }

	got := r.driver.Run(context.Background(), request())

	if got.ExitCode != 1 {
		t.Errorf("exit code = %d, want 1", got.ExitCode)
	}
	if !errors.Is(got.Err, error(refusal)) {
		t.Errorf("Err = %v, want the pairing refusal", got.Err)
	}
	r.wantOrder(t, "load")
}

// TestDriverRunsWithoutAPairingCheck pins the nil seam: step 2 fills Pairing in,
// and until it does a nil one is no check rather than a panic.
func TestDriverRunsWithoutAPairingCheck(t *testing.T) {
	r := newRig(t, []loadStep{
		{state: loaded(t)},
		{state: loaded(t, marker(reviewConverged, 1))},
	})
	if got := r.driver.Run(context.Background(), request()); got.ExitCode != 0 {
		t.Errorf("exit code = %d, want 0", got.ExitCode)
	}
	r.wantOrder(t, "load", "review", "load", "nudge")
}

// --- the loop ---------------------------------------------------------------

// TestDriverAlternatesTheLegsUntilAReviewConverges is the ordinary run: a first
// review, its resolve leg, then a second review that converges. The continuation
// flag is what separates the two reviews — the shell forwards
// `--continuation --no-tips` from the second pass onward and `--no-tips` on the
// first (lib/run.sh:2944 and :2946) — and `--no-tips` goes on every leg whatever the
// cycle was asked for, because the cycle prints the tip itself at most once.
func TestDriverAlternatesTheLegsUntilAReviewConverges(t *testing.T) {
	r := newRig(t, []loadStep{
		{state: loaded(t)},
		{state: loaded(t, marker(reviewIssues, 1))},
		{state: loaded(t, marker(reviewIssues, 1), marker(resolvePushed, 1))},
		{state: loaded(t, marker(reviewIssues, 1), marker(resolvePushed, 1))},
		{state: loaded(t, marker(reviewIssues, 1), marker(resolvePushed, 1), marker(reviewConverged, 2))},
	})

	got := r.driver.Run(context.Background(), request())

	if got.ExitCode != 0 {
		t.Errorf("exit code = %d, want 0", got.ExitCode)
	}
	r.wantOrder(t, "load", "review", "load", "resolve", "load", "load", "review+continuation", "load", "nudge")
	r.wantOut(t, sayCycling+endLine("Converged after pass 2 — nothing at or above min_fix_severity (medium) remains."))

	want := []struct {
		leg          core.Leg
		continuation bool
	}{
		{core.LegReview, false},
		{core.LegResolve, false},
		{core.LegReview, true},
	}
	if len(r.rec.legs) != len(want) {
		t.Fatalf("leg calls = %d, want %d", len(r.rec.legs), len(want))
	}
	for i, w := range want {
		call := r.rec.legs[i]
		if call.leg != w.leg || call.req.Continuation != w.continuation {
			t.Errorf("leg %d = %s continuation=%v, want %s continuation=%v",
				i, call.leg, call.req.Continuation, w.leg, w.continuation)
		}
		if !call.req.NoTips {
			t.Errorf("leg %d was not asked for --no-tips", i)
		}
	}
}

// TestDriverRunsNoResolveAfterABlockedReview pins lib/run.sh:2956-2958. A
// reviewer that could not complete leaves nothing to resolve, and the run ends
// with no tip, because the loop did not reach a state worth nudging about.
func TestDriverRunsNoResolveAfterABlockedReview(t *testing.T) {
	r := newRig(t, []loadStep{
		{state: loaded(t)},
		{state: loaded(t, marker(reviewBlocked, 1))},
	})

	got := r.driver.Run(context.Background(), request())

	if got.ExitCode != 0 {
		t.Errorf("exit code = %d, want 0", got.ExitCode)
	}
	r.wantOrder(t, "load", "review", "load")
	r.wantOut(t, sayCycling+endLine("Halted after pass 1 — the reviewer could not complete."))
}

// TestDriverReadsAnAbsentVerdictAsBlocked pins `jq -r '.verdict // "blocked"'`
// (lib/run.sh:2953): a review marker carrying no verdict is the halt, never a
// convergence.
func TestDriverReadsAnAbsentVerdictAsBlocked(t *testing.T) {
	r := newRig(t, []loadStep{
		{state: loaded(t)},
		{state: loaded(t, `{"v":1,"leg":"review","pass":1,"state":"complete","findings":[]}`)},
	})

	r.driver.Run(context.Background(), request())

	r.wantOrder(t, "load", "review", "load")
	r.wantOut(t, sayCycling+endLine("Halted after pass 1 — the reviewer could not complete."))
}

// TestDriverRunsNoResolveAfterAConvergedReview pins the converged arm
// (lib/run.sh:2960-2969): nothing left to resolve, and the tip fires.
func TestDriverRunsNoResolveAfterAConvergedReview(t *testing.T) {
	r := newRig(t, []loadStep{
		{state: loaded(t)},
		{state: loaded(t, marker(reviewConverged, 1))},
	})

	got := r.driver.Run(context.Background(), request())

	if got.ExitCode != 0 {
		t.Errorf("exit code = %d, want 0", got.ExitCode)
	}
	r.wantOrder(t, "load", "review", "load", "nudge")
	r.wantOut(t, sayCycling+endLine("Converged after pass 1 — nothing at or above min_fix_severity (medium) remains."))
}

// TestDriverRunsNoResolveWhenAPassRaisesNothingActionable pins the second half
// of the same condition: a verdict of issues-remain with no finding at or above
// min_fix_severity is a convergence too, because the resolve leg would have
// nothing to change.
func TestDriverRunsNoResolveWhenAPassRaisesNothingActionable(t *testing.T) {
	r := newRig(t, []loadStep{
		{state: loaded(t)},
		{state: loaded(t, marker(reviewEmpty, 1))},
	})

	r.driver.Run(context.Background(), request())

	r.wantOrder(t, "load", "review", "load", "nudge")
	r.wantOut(t, sayCycling+endLine("Converged after pass 1 — nothing at or above min_fix_severity (medium) remains."))
}

// TestDriverCallsAnEmptyPassAHaltWhileAnEscalationStands pins the exception
// (lib/run.sh:2960-2967), which keeps the terminal line and the pass label the
// review leg wrote in agreement: an empty pass while a human still owes a
// decision is a halt, not a convergence. The converged verdict is exempt,
// because that is the settlement being verified.
//
// The tip still fires here. It sits after the inner if/else rather than inside
// the converged arm (lib/run.sh:2968), so this halt nudges and the four other
// halts do not.
func TestDriverCallsAnEmptyPassAHaltWhileAnEscalationStands(t *testing.T) {
	r := newRig(t, []loadStep{
		{state: loaded(t)},
		{state: loaded(t, marker(resolveEscalated, 1), marker(reviewEmpty, 2))},
	})

	r.driver.Run(context.Background(), request())

	r.wantOrder(t, "load", "review", "load", "nudge")
	r.wantOut(t, sayCycling+endLine(
		"Halted after pass 2 — the pass raised nothing new, and the escalated findings still need a human decision."))
}

// TestDriverConvergesOnAConvergedVerdictDespiteAnEscalation is the exempt arm
// of the same rule.
func TestDriverConvergesOnAConvergedVerdictDespiteAnEscalation(t *testing.T) {
	r := newRig(t, []loadStep{
		{state: loaded(t)},
		{state: loaded(t, marker(resolveEscalated, 1), marker(reviewConverged, 2))},
	})

	r.driver.Run(context.Background(), request())

	r.wantOut(t, sayCycling+endLine("Converged after pass 2 — nothing at or above min_fix_severity (medium) remains."))
}

// --- what ends a pass after the resolve leg ---------------------------------

// TestDriverHaltsOnABlockedResolveMarker pins lib/run.sh:2976-2978.
func TestDriverHaltsOnABlockedResolveMarker(t *testing.T) {
	r := newRig(t, []loadStep{
		{state: loaded(t)},
		{state: loaded(t, marker(reviewIssues, 1))},
		{state: loaded(t, marker(reviewIssues, 1), marker(resolveBlocked, 1))},
	})

	got := r.driver.Run(context.Background(), request())

	if got.ExitCode != 0 {
		t.Errorf("exit code = %d, want 0", got.ExitCode)
	}
	r.wantOrder(t, "load", "review", "load", "resolve", "load")
	r.wantOut(t, sayCycling+endLine("Halted after pass 1 — the resolver reported blocked."))
}

// TestDriverHaltsOnTheStopLabel pins lib/run.sh:2980-2982. The label is the one
// a human applies, so the loop stops whatever the marker says.
func TestDriverHaltsOnTheStopLabel(t *testing.T) {
	third := loaded(t, marker(reviewIssues, 1), marker(resolvePushed, 1))
	third.Labels = []string{"crossrev/awaiting-review", "crossrev/stop"}
	r := newRig(t, []loadStep{
		{state: loaded(t)},
		{state: loaded(t, marker(reviewIssues, 1))},
		{state: third},
	})

	r.driver.Run(context.Background(), request())

	r.wantOrder(t, "load", "review", "load", "resolve", "load")
	r.wantOut(t, sayCycling+endLine("Halted after pass 1 — a point needs a human decision, so crossrev/stop is applied."))
}

// TestDriverDoesNotReadAStopLabelOutOfANeighbour pins that the label is matched
// whole. The shell greps for it as a word inside a space-joined list, which also
// matches a label ENDING in crossrev/stop; the Go tree matches the name exactly
// wherever it reads this label (internal/review/context.go:169), and this test
// holds the cycle driver to the same rule.
func TestDriverDoesNotReadAStopLabelOutOfANeighbour(t *testing.T) {
	third := loaded(t, marker(reviewIssues, 1), marker(resolveSettled, 1))
	third.Labels = []string{"crossrev/stopped", "team-crossrev/stop"}
	r := newRig(t, []loadStep{
		{state: loaded(t)},
		{state: loaded(t, marker(reviewIssues, 1))},
		{state: third},
	})

	r.driver.Run(context.Background(), request())

	r.wantOut(t, sayCycling+endLine("Converged after pass 1 — nothing at or above min_fix_severity (medium) remains."))
}

// TestDriverConvergesWhenAResolvePassSettledWithoutPushing pins
// lib/run.sh:2993-2999. Every finding answered and no commit means the head
// never moved, so the next review would decline it; reporting the cap there
// would read a convergence out as a failure to converge. The tip fires.
func TestDriverConvergesWhenAResolvePassSettledWithoutPushing(t *testing.T) {
	r := newRig(t, []loadStep{
		{state: loaded(t)},
		{state: loaded(t, marker(reviewIssues, 1))},
		{state: loaded(t, marker(reviewIssues, 1), marker(resolveSettled, 1))},
	})

	r.driver.Run(context.Background(), request())

	r.wantOrder(t, "load", "review", "load", "resolve", "load", "nudge")
	r.wantOut(t, sayCycling+endLine("Converged after pass 1 — nothing at or above min_fix_severity (medium) remains."))
}

// TestDriverHaltsWhenAResolvePassLeftSomethingForAPerson pins
// lib/run.sh:3001-3009, including the backticked command in the middle of the
// sentence. Blocked and escalated are caught by the two checks above; the halts
// left here — a deferral nobody filed, a fix that reached no commit — apply no
// crossrev/stop, and without this the driver would re-drive the resolver over
// work that is waiting on a person. No tip: the run stopped for a human.
func TestDriverHaltsWhenAResolvePassLeftSomethingForAPerson(t *testing.T) {
	r := newRig(t, []loadStep{
		{state: loaded(t)},
		{state: loaded(t, marker(reviewIssues, 1))},
		{state: loaded(t, marker(reviewIssues, 1), marker(resolveUnfiled, 1))},
	})

	r.driver.Run(context.Background(), request())

	r.wantOrder(t, "load", "review", "load", "resolve", "load")
	r.wantOut(t, sayCycling+endLine(
		"Halted after pass 1 — the resolve leg left something a person has to settle. "+
			"`crossrev status --pr 42` says what."))
}

// TestDriverIgnoresTheLabelOfAnUnfinishedResolvePass pins the guard on the
// label read (lib/run.sh:2984-2991): a resolve marker that is not complete has
// no pass label, so the loop goes round rather than reading a half-written
// marker as an ending.
func TestDriverIgnoresTheLabelOfAnUnfinishedResolvePass(t *testing.T) {
	started := `{"v":1,"leg":"resolve","pass":1,"state":"started"}`
	r := newRig(t, []loadStep{
		{state: loaded(t)},
		{state: loaded(t, marker(reviewIssues, 1))},
		{state: loaded(t, marker(reviewIssues, 1), started)},
		{state: loaded(t, marker(reviewIssues, 1), started)},
		{state: loaded(t, marker(reviewIssues, 1), started, marker(reviewConverged, 2))},
	})

	r.driver.Run(context.Background(), request())

	r.wantOrder(t, "load", "review", "load", "resolve", "load", "load", "review+continuation", "load", "nudge")
}

// --- the cap ----------------------------------------------------------------

// TestDriverReportsTheCapBetweenIterations pins lib/run.sh:2937-2943: the pass
// number is re-read at the top of every iteration after the first, and a pass
// already at the cap stops the run before another review starts.
func TestDriverReportsTheCapBetweenIterations(t *testing.T) {
	r := newRig(t, []loadStep{
		{state: loaded(t)},
		{state: loaded(t, marker(reviewIssues, 1))},
		{state: loaded(t, marker(reviewIssues, 1), marker(resolvePushed, 1))},
		{state: loaded(t, marker(reviewIssues, 1), marker(resolvePushed, 1), marker(reviewIssues, 3))},
	})

	got := r.driver.Run(context.Background(), request())

	if got.ExitCode != 0 {
		t.Errorf("exit code = %d, want 0", got.ExitCode)
	}
	r.wantOrder(t, "load", "review", "load", "resolve", "load", "load")
	r.wantOut(t, sayCycling+endLine(
		"Reached max_passes_per_cycle (3) on acme/widget#42 without starting another pass."))
}

// TestDriverReportsTheCapAfterTheLastPass pins the line the loop falls out to
// (lib/run.sh:3013-3015), which is a different sentence from the one above: the
// passes ran and none of them converged. The tip fires.
func TestDriverReportsTheCapAfterTheLastPass(t *testing.T) {
	one := func(markers ...string) State {
		state := loaded(t, markers...)
		state.MaxPassesPerCycle = 1
		return state
	}
	r := newRig(t, []loadStep{
		{state: one()},
		{state: one(marker(reviewIssues, 1))},
		{state: one(marker(reviewIssues, 1), marker(resolvePushed, 1))},
	})

	got := r.driver.Run(context.Background(), request())

	if got.ExitCode != 0 {
		t.Errorf("exit code = %d, want 0", got.ExitCode)
	}
	r.wantOrder(t, "load", "review", "load", "resolve", "load", "nudge")
	r.wantOut(t,
		"  Cycling acme/widget#42, up to 1 passes. Ctrl-C is safe — each leg finishes the write in flight.\n"+
			endLine("Reached max_passes_per_cycle (1) on acme/widget#42 without converging. "+
				"Every finding and reply is on the pull request."))
}

// TestDriverHandsAPassAlreadyAtTheCapToTheBoundSeam pins lib/run.sh:2930-2934:
// a cycle whose newest review pass already sits at or past the cap never enters
// the loop. What happens then is _cycle_finish_at_bound's, which arrives with
// bound.go; this pins that the driver calls it, with the pass and the cap it
// read and the leg arguments it would have forwarded, and starts no pass of its
// own. The Cycling line is printed first, because the shell prints it before the
// comparison.
func TestDriverHandsAPassAlreadyAtTheCapToTheBoundSeam(t *testing.T) {
	r := newRig(t, []loadStep{{state: loaded(t, marker(reviewIssues, 3))}})
	var seen []BoundInput
	r.driver.Bound = func(_ context.Context, _ *Driver, in BoundInput) BoundResult {
		r.rec.order = append(r.rec.order, "bound")
		seen = append(seen, in)
		return BoundResult{}
	}

	req := request()
	req.HarnessOverride = "opencode"
	req.KeepTranscripts = true
	got := r.driver.Run(context.Background(), req)

	if got.ExitCode != 0 {
		t.Errorf("exit code = %d, want 0", got.ExitCode)
	}
	r.wantOrder(t, "load", "bound")
	r.wantOut(t, sayCycling)
	if len(seen) != 1 {
		t.Fatalf("bound called %d times, want 1", len(seen))
	}
	if seen[0].Pass != 3 || seen[0].Max != 3 {
		t.Errorf("bound got pass=%d max=%d, want 3 and 3", seen[0].Pass, seen[0].Max)
	}
	want := LegRequest{PR: 42, Trigger: TriggerHuman, HarnessOverride: "opencode", KeepTranscripts: true, NoTips: true}
	if seen[0].Leg != want {
		t.Errorf("bound leg request = %+v, want %+v", seen[0].Leg, want)
	}
}

// TestDriverReturnsOneWhenTheBoundSeamFails pins the `|| return 1` on the same
// line (lib/run.sh:2932).
func TestDriverReturnsOneWhenTheBoundSeamFails(t *testing.T) {
	r := newRig(t, []loadStep{{state: loaded(t, marker(reviewIssues, 4))}})
	r.driver.Bound = func(context.Context, *Driver, BoundInput) BoundResult {
		return BoundResult{Failed: true}
	}

	if got := r.driver.Run(context.Background(), request()); got.ExitCode != 1 {
		t.Errorf("exit code = %d, want 1", got.ExitCode)
	}
}

// --- what the legs are told -------------------------------------------------

// TestDriverForwardsTheTriggerUnchanged pins that every leg is told what the
// cycle was told. The shell forwards the flag as it was typed, in `args`
// (lib/run.sh:2900-2905), so an automatic cycle drives automatic legs.
//
// The reloads are the other half. ctx_load takes the trigger as its third
// argument and defaults it to human (lib/run.sh:232); the shell passes it only
// on the first call (lib/run.sh:2921) and calls every later one as
// `ctx_load "$pr" "$repo"` (lib/run.sh:2938, :2950, :2974). So the fork and
// draft refusals apply once, upfront, and never again mid-loop.
func TestDriverForwardsTheTriggerUnchanged(t *testing.T) {
	r := newRig(t, []loadStep{
		{state: loaded(t)},
		{state: loaded(t, marker(reviewIssues, 1))},
		{state: loaded(t, marker(reviewIssues, 1), marker(resolvePushed, 1))},
		{state: loaded(t, marker(reviewIssues, 1), marker(resolvePushed, 1))},
		{state: loaded(t, marker(reviewIssues, 1), marker(resolvePushed, 1), marker(reviewConverged, 2))},
	})

	req := request()
	req.Trigger = TriggerAutomatic
	req.Repo = slugOf(t, "acme/widget")
	r.driver.Run(context.Background(), req)

	for i, call := range r.rec.legs {
		if call.req.Trigger != TriggerAutomatic {
			t.Errorf("leg %d trigger = %q, want automatic", i, call.req.Trigger)
		}
		if call.req.PR != 42 || call.req.Repo != req.Repo {
			t.Errorf("leg %d = %s#%d, want acme/widget#42", i, call.req.Repo, call.req.PR)
		}
	}
	if len(r.rec.loads) != 5 {
		t.Fatalf("loads = %d, want 5", len(r.rec.loads))
	}
	if r.rec.loads[0].Trigger != TriggerAutomatic {
		t.Errorf("first load trigger = %q, want automatic", r.rec.loads[0].Trigger)
	}
	for i, load := range r.rec.loads[1:] {
		if load.Trigger != TriggerHuman {
			t.Errorf("load %d trigger = %q, want human", i+2, load.Trigger)
		}
		if load.PR != 42 || load.Repo != req.Repo {
			t.Errorf("load %d = %s#%d, want acme/widget#42", i+2, load.Repo, load.PR)
		}
	}
}

// TestDriverForwardsTheRequestsRepositoryNotTheLoadedOne pins that the legs get
// the `--repo` the operator typed, or nothing when they typed nothing. The
// shell appends to `args` only what it parsed (lib/run.sh:2900-2905), so a cycle
// started with no --repo forwards none and each leg resolves the slug itself.
func TestDriverForwardsTheRequestsRepositoryNotTheLoadedOne(t *testing.T) {
	r := newRig(t, []loadStep{
		{state: loaded(t)},
		{state: loaded(t, marker(reviewConverged, 1))},
	})

	r.driver.Run(context.Background(), request())

	if len(r.rec.legs) != 1 {
		t.Fatalf("leg calls = %d, want 1", len(r.rec.legs))
	}
	if got := r.rec.legs[0].req.Repo; got != (core.Slug{}) {
		t.Errorf("leg repo = %q, want the zero slug", got)
	}
	if got := r.rec.loads[0].Repo; got != (core.Slug{}) {
		t.Errorf("load repo = %q, want the zero slug", got)
	}
}

// --- a leg that fails -------------------------------------------------------

// TestDriverReturnsOneWhenAReviewLegFails pins `leg_review … || return 1`
// (lib/run.sh:2946): the leg has already said why, so the driver adds no line
// and runs nothing after it.
func TestDriverReturnsOneWhenAReviewLegFails(t *testing.T) {
	r := newRig(t, []loadStep{{state: loaded(t)}})
	r.driver.Review = &fakeLeg{rec: r.rec, leg: core.LegReview, fail: true}

	got := r.driver.Run(context.Background(), request())

	if got.ExitCode != 1 {
		t.Errorf("exit code = %d, want 1", got.ExitCode)
	}
	if got.Err != nil {
		t.Errorf("Err = %v, want nil: the leg reported it", got.Err)
	}
	r.wantOrder(t, "load", "review")
	r.wantOut(t, sayCycling)
}

// TestDriverReturnsOneWhenAResolveLegFails pins the same on lib/run.sh:2972.
func TestDriverReturnsOneWhenAResolveLegFails(t *testing.T) {
	r := newRig(t, []loadStep{
		{state: loaded(t)},
		{state: loaded(t, marker(reviewIssues, 1))},
	})
	r.driver.Resolve = &fakeLeg{rec: r.rec, leg: core.LegResolve, fail: true}

	got := r.driver.Run(context.Background(), request())

	if got.ExitCode != 1 {
		t.Errorf("exit code = %d, want 1", got.ExitCode)
	}
	r.wantOrder(t, "load", "review", "load", "resolve")
	r.wantOut(t, sayCycling)
}

// --- the tip ----------------------------------------------------------------

// TestDriverSuppressesTheTipWhenAskedTo pins `(( no_tips )) || run_upgrade_nudge`
// (lib/run.sh:2968, :2998 and :3014). --no-tips is the flag the shell keeps out of
// `args` (lib/run.sh:2904), so it suppresses the cycle's own tip and nothing else.
func TestDriverSuppressesTheTipWhenAskedTo(t *testing.T) {
	r := newRig(t, []loadStep{
		{state: loaded(t)},
		{state: loaded(t, marker(reviewConverged, 1))},
	})

	req := request()
	req.NoTips = true
	r.driver.Run(context.Background(), req)

	r.wantOrder(t, "load", "review", "load")
}

// TestDriverRunsWithoutANudge pins the nil seam. The body of the nudge — the
// environment read, the config read and the workflow directory probe — belongs
// to the command that wires the driver, and a nil one is silence rather than a
// panic.
func TestDriverRunsWithoutANudge(t *testing.T) {
	r := newRig(t, []loadStep{
		{state: loaded(t)},
		{state: loaded(t, marker(reviewConverged, 1))},
	})
	r.driver.Nudge = nil

	if got := r.driver.Run(context.Background(), request()); got.ExitCode != 0 {
		t.Errorf("exit code = %d, want 0", got.ExitCode)
	}
}

// --- the bytes --------------------------------------------------------------

// TestDriverPrintsTheBytesTheShellPrints is the one case that builds its State
// as a literal and spells its expected output as one, so nothing in it depends
// on a helper in this file being right.
//
// Measured against the shell, with ctx_load, the legs and the state reads
// replaced and ui_say and ui_end left alone:
//
//	$ NO_COLOR=1 bash probe.sh --pr 42 | sed 's/^/|/; s/$/|/'
//	|  Cycling acme/widget#42, up to 3 passes. Ctrl-C is safe — each leg finishes the write in flight.|
//	|└  Halted after pass 1 — the resolve leg left something a person has to settle. `crossrev status --pr 42` says what.|
//	||
func TestDriverPrintsTheBytesTheShellPrints(t *testing.T) {
	repo, err := core.ParseSlug("acme/widget")
	if err != nil {
		t.Fatalf("ParseSlug: %v", err)
	}
	afterReview, err := prstate.ParseMarkers([]byte(
		`[{"v":1,"leg":"review","pass":1,"state":"complete","verdict":"issues-remain",` +
			`"findings":[{"severity":"high"}]}]`))
	if err != nil {
		t.Fatalf("ParseMarkers: %v", err)
	}
	afterResolve, err := prstate.ParseMarkers([]byte(
		`[{"v":1,"leg":"review","pass":1,"state":"complete","verdict":"issues-remain",` +
			`"findings":[{"severity":"high"}]},` +
			`{"v":1,"leg":"resolve","pass":1,"state":"complete","blocked":false,` +
			`"resolutions":[{"resolution":"deferred","crossrev_tracked":""}]}]`))
	if err != nil {
		t.Fatalf("ParseMarkers: %v", err)
	}
	state := func(markers []prstate.Marker) State {
		return State{
			Repo:              repo,
			PR:                42,
			Markers:           markers,
			Labels:            []string{"crossrev/awaiting-review"},
			MaxPassesPerCycle: 3,
			MinFixSeverity:    core.SeverityMedium,
			Draft:             false,
		}
	}

	rec := &recorder{}
	out := &bytes.Buffer{}
	driver := &Driver{
		Review:  &fakeLeg{rec: rec, leg: core.LegReview},
		Resolve: &fakeLeg{rec: rec, leg: core.LegResolve},
		Loader: &fakeLoader{t: t, rec: rec, steps: []loadStep{
			{state: state(nil)},
			{state: state(afterReview)},
			{state: state(afterResolve)},
		}},
		Out: out,
	}

	got := driver.Run(context.Background(), Request{PR: 42, Trigger: TriggerHuman})

	if got.ExitCode != 0 {
		t.Errorf("exit code = %d, want 0", got.ExitCode)
	}
	const want = "  Cycling acme/widget#42, up to 3 passes." +
		" Ctrl-C is safe — each leg finishes the write in flight.\n" +
		"└  Halted after pass 1 — the resolve leg left something a person has to settle." +
		" \x60crossrev status --pr 42\x60 says what.\n\n"
	if out.String() != want {
		t.Errorf("output\n got %q\nwant %q", out.String(), want)
	}
}
