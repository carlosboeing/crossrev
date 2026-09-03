package cycle

import (
	"context"
	"errors"
	"testing"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/ui"
)

// sayLine is ui_say's shape (lib/ui.sh:96), the same one sayCycling in
// cycle_test.go is built from.
func sayLine(text string) string { return "  " + text + "\n" }

// --- the two the bound exists to get right ----------------------------------

// TestBoundStartsNoFurtherPassAtTheCap pins that a cap of three runs no fourth
// pass. The pull request sits on a complete review pass 3 with an actionable
// finding and no resolve marker, so the pass is owed its resolve leg and
// nothing else (lib/run.sh:2859-2860, :2887). Measured:
//
//	Pass 3 is unfinished, and max_passes_per_cycle (3) starts no further pass. Its resolve leg is still owed.
//	LEG resolve --pr 42 --no-tips
//	LOAD 42 acme/widget
//	└  Reached max_passes_per_cycle (3) on acme/widget#42 — pass 3 is resolved, and no further pass starts.
func TestBoundStartsNoFurtherPassAtTheCap(t *testing.T) {
	r := newRig(t, []loadStep{
		{state: loaded(t, marker(reviewIssues, 3))},
		{state: loaded(t, marker(reviewIssues, 3), marker(resolvePushed, 3))},
	})

	got := r.driver.Run(context.Background(), request())

	if got.ExitCode != 0 {
		t.Errorf("exit code = %d, want 0", got.ExitCode)
	}
	r.wantOrder(t, "load", "resolve", "load")
	r.wantOut(t, sayCycling+
		sayLine("Pass 3 is unfinished, and max_passes_per_cycle (3) starts no further pass. Its resolve leg is still owed.")+
		endLine("Reached max_passes_per_cycle (3) on acme/widget#42 — pass 3 is resolved, and no further pass starts."))
}

// TestBoundRunsNoResolveWhenTheReviewRaisedNothingActionable pins the arm at
// lib/run.sh:2843-2852: a complete review pass at the bound that raised nothing
// at or above min_fix_severity is a convergence, and the resolve leg is never
// billed for it.
func TestBoundRunsNoResolveWhenTheReviewRaisedNothingActionable(t *testing.T) {
	r := newRig(t, []loadStep{{state: loaded(t, marker(reviewEmpty, 3))}})

	got := r.driver.Run(context.Background(), request())

	if got.ExitCode != 0 {
		t.Errorf("exit code = %d, want 0", got.ExitCode)
	}
	r.wantOrder(t, "load")
	r.wantOut(t, sayCycling+
		endLine("Converged after pass 3 — nothing at or above min_fix_severity (medium) remains."))
}

// --- fixtures the loop's own tests do not need -------------------------------

const (
	// An open claim: the review leg wrote its marker and has not come back.
	reviewStarted = `{"v":1,"leg":"review","pass":%d,"state":"started"}`
	// A decline over a pass that was mid-flight — the shape lib/run.sh:2818
	// warns the continuation flag would write.
	reviewDeclined = `{"v":1,"leg":"review","pass":%d,"state":"declined"}`
	// The resolve leg claimed the pass and has not finished it.
	resolveStarted = `{"v":1,"leg":"resolve","pass":%d,"state":"started"}`
)

// --- resuming the pass already under way -------------------------------------

// TestBoundResumesAnOpenClaimWithoutTheContinuationFlag pins lib/run.sh:2823-2824.
// `started` is an open claim, and the review leg resumes it at its existing pass
// number. The bound must not be applied to that: the continuation flag would
// refuse the resume, write a declined marker over a pass that is mid-flight, and
// exit clean with the review unfinished.
func TestBoundResumesAnOpenClaimWithoutTheContinuationFlag(t *testing.T) {
	r := newRig(t, []loadStep{
		{state: loaded(t, marker(reviewStarted, 3))},
		{state: loaded(t, marker(reviewIssues, 3))},
		{state: loaded(t, marker(reviewIssues, 3), marker(resolvePushed, 3))},
	})

	if got := r.driver.Run(context.Background(), request()); got.ExitCode != 0 {
		t.Errorf("exit code = %d, want 0", got.ExitCode)
	}
	r.wantOrder(t, "load", "review", "load", "resolve", "load")
	if len(r.rec.legs) != 2 {
		t.Fatalf("legs = %d, want 2", len(r.rec.legs))
	}
	if r.rec.legs[0].req.Continuation {
		t.Error("the resumed review leg was told --continuation")
	}
	if !r.rec.legs[0].req.NoTips {
		t.Error("the resumed review leg was not told --no-tips")
	}
}

// TestBoundResumesAnythingElseWithTheContinuationFlag pins lib/run.sh:2825-2826.
// A declined marker leaves the leg to compute a pass number of its own, and the
// bound is the only thing stopping it starting one, so that case keeps the flag.
//
// The fixture carries the open claim as well as the decline over it, because
// state_current_review_pass ignores declined markers (lib/state.sh:268, :295)
// and the driver would otherwise never read the pull request as being at the
// bound.
func TestBoundResumesAnythingElseWithTheContinuationFlag(t *testing.T) {
	r := newRig(t, []loadStep{
		{state: loaded(t, marker(reviewStarted, 3), marker(reviewDeclined, 3))},
		{state: loaded(t, marker(reviewIssues, 3))},
		{state: loaded(t, marker(reviewIssues, 3), marker(resolvePushed, 3))},
	})

	r.driver.Run(context.Background(), request())

	r.wantOrder(t, "load", "review+continuation", "load", "resolve", "load")
	if len(r.rec.legs) != 2 || !r.rec.legs[0].req.Continuation {
		t.Fatalf("the resumed review leg was not told --continuation: %+v", r.rec.legs)
	}
}

// TestBoundResumesAPassWithNoMarkerAtAllWithTheContinuationFlag pins the other
// half of the same else arm: state_marker_for answers `// empty` for a pass it
// holds no marker for, and jq reads `.state // ""` off that as the empty string
// (measured: `jq -r '.state // ""' <<<""` prints nothing and exits 0).
//
// Run cannot reach this — it derives the pass from the markers, so a pass at the
// bound always has a review marker — so the seam is called directly.
func TestBoundResumesAPassWithNoMarkerAtAllWithTheContinuationFlag(t *testing.T) {
	r := newRig(t, []loadStep{
		{state: loaded(t, marker(reviewIssues, 3))},
		{state: loaded(t, marker(reviewIssues, 3), marker(resolvePushed, 3))},
	})
	in := BoundInput{Pass: 3, Max: 3, State: loaded(t), Leg: LegRequest{PR: 42, NoTips: true}}

	if got := finishAtBound(context.Background(), r.driver, in); got.Failed {
		t.Errorf("Failed = true, want false")
	}
	r.wantOrder(t, "review+continuation", "load", "resolve", "load")
}

// TestBoundStopsWhenTheResumedReviewDidNotFinish pins lib/run.sh:2831-2834.
// Measured against the shell with a review leg that left its claim open:
//
//	└  Stopped on pass 3 of acme/widget#42 — the review leg did not finish, so no resolve leg follows it.
func TestBoundStopsWhenTheResumedReviewDidNotFinish(t *testing.T) {
	r := newRig(t, []loadStep{
		{state: loaded(t, marker(reviewStarted, 3))},
		{state: loaded(t, marker(reviewStarted, 3))},
	})

	if got := r.driver.Run(context.Background(), request()); got.ExitCode != 0 {
		t.Errorf("exit code = %d, want 0", got.ExitCode)
	}
	r.wantOrder(t, "load", "review", "load")
	r.wantOut(t, sayCycling+
		sayLine("Pass 3 is unfinished, and max_passes_per_cycle (3) starts no further pass. Resuming its review.")+
		endLine("Stopped on pass 3 of acme/widget#42 — the review leg did not finish, so no resolve leg follows it."))
}

// TestBoundRereadsThePassAfterTheResume pins lib/run.sh:2829: the pass is
// recomputed off the reload, so a review leg that resumed at a number of its own
// is reported at that number rather than at the one the bound was called with.
func TestBoundRereadsThePassAfterTheResume(t *testing.T) {
	r := newRig(t, []loadStep{
		{state: loaded(t, marker(reviewStarted, 3))},
		{state: loaded(t, marker(reviewIssues, 4))},
		{state: loaded(t, marker(reviewIssues, 4), marker(resolvePushed, 4))},
	})

	r.driver.Run(context.Background(), request())

	r.wantOut(t, sayCycling+
		sayLine("Pass 3 is unfinished, and max_passes_per_cycle (3) starts no further pass. Resuming its review.")+
		sayLine("Pass 4 is unfinished, and max_passes_per_cycle (3) starts no further pass. Its resolve leg is still owed.")+
		endLine("Reached max_passes_per_cycle (3) on acme/widget#42 — pass 4 is resolved, and no further pass starts."))
}

// --- the two `|| return 1` sites ---------------------------------------------

// TestBoundReturnsOneWhenTheReviewLegFails pins lib/run.sh:2824 and :2820.
// Measured: the shell prints the resuming line, runs the leg, and returns 1
// without loading again.
func TestBoundReturnsOneWhenTheReviewLegFails(t *testing.T) {
	r := newRig(t, []loadStep{{state: loaded(t, marker(reviewStarted, 3))}})
	r.driver.Review = &fakeLeg{rec: r.rec, leg: core.LegReview, fail: true}

	got := r.driver.Run(context.Background(), request())

	if got.ExitCode != 1 {
		t.Errorf("exit code = %d, want 1", got.ExitCode)
	}
	if got.Err != nil {
		t.Errorf("Err = %v, want nil: the leg already said why", got.Err)
	}
	r.wantOrder(t, "load", "review")
}

// TestBoundReturnsOneWhenTheResolveLegFails pins lib/run.sh:2860.
func TestBoundReturnsOneWhenTheResolveLegFails(t *testing.T) {
	r := newRig(t, []loadStep{{state: loaded(t, marker(reviewIssues, 3))}})
	r.driver.Resolve = &fakeLeg{rec: r.rec, leg: core.LegResolve, fail: true}

	got := r.driver.Run(context.Background(), request())

	if got.ExitCode != 1 {
		t.Errorf("exit code = %d, want 1", got.ExitCode)
	}
	r.wantOrder(t, "load", "resolve")
}

// TestBoundReturnsTheLoadRefusal pins that a refusal from the reload reaches the
// caller. The shell calls ctx_load unchecked here (lib/run.sh:2828, :2860) and
// every failure inside it is a ui_die that prints and exits the process, so the
// message is the operator's either way; carrying it on BoundResult is how the
// port keeps it.
func TestBoundReturnsTheLoadRefusal(t *testing.T) {
	refusal := &ui.FatalError{Reason: "could not read acme/widget#42", Action: "Check the number."}
	r := newRig(t, []loadStep{
		{state: loaded(t, marker(reviewStarted, 3))},
		{err: refusal},
	})

	got := r.driver.Run(context.Background(), request())

	if got.ExitCode != 1 {
		t.Errorf("exit code = %d, want 1", got.ExitCode)
	}
	if !errors.Is(got.Err, error(refusal)) {
		t.Errorf("Err = %v, want the loader's refusal", got.Err)
	}
}

// TestBoundReturnsTheLoadRefusalAfterTheResolveLeg pins the second reload
// (lib/run.sh:2864): a refusal there exits 1 with its message, the same as the
// first reload's, rather than reading as a clean stop.
func TestBoundReturnsTheLoadRefusalAfterTheResolveLeg(t *testing.T) {
	refusal := &ui.FatalError{Reason: "could not read acme/widget#42", Action: "Check the number."}
	r := newRig(t, []loadStep{
		{state: loaded(t, marker(reviewIssues, 3), marker(resolveStarted, 3))},
		{err: refusal},
	})

	got := r.driver.Run(context.Background(), request())

	r.wantOrder(t, "load", "resolve", "load")
	if got.ExitCode != 1 {
		t.Errorf("exit code = %d, want 1", got.ExitCode)
	}
	if !errors.Is(got.Err, error(refusal)) {
		t.Errorf("Err = %v, want the loader's refusal", got.Err)
	}
}

// --- the verdict reading, shared with the loop -------------------------------

// TestBoundHaltsOnABlockedReview pins lib/run.sh:2839-2842.
func TestBoundHaltsOnABlockedReview(t *testing.T) {
	r := newRig(t, []loadStep{{state: loaded(t, marker(reviewBlocked, 3))}})

	if got := r.driver.Run(context.Background(), request()); got.ExitCode != 0 {
		t.Errorf("exit code = %d, want 0", got.ExitCode)
	}
	r.wantOrder(t, "load")
	r.wantOut(t, sayCycling+endLine("Halted after pass 3 — the reviewer could not complete."))
}

// TestBoundReadsAnAbsentVerdictAsBlocked pins the `// "blocked"` default at
// lib/run.sh:2837.
func TestBoundReadsAnAbsentVerdictAsBlocked(t *testing.T) {
	r := newRig(t, []loadStep{
		{state: loaded(t, `{"v":1,"leg":"review","pass":3,"state":"complete","findings":[]}`)},
	})

	r.driver.Run(context.Background(), request())

	r.wantOrder(t, "load")
	r.wantOut(t, sayCycling+endLine("Halted after pass 3 — the reviewer could not complete."))
}

// TestBoundConvergesOnAConvergedReview pins the converged arm of
// lib/run.sh:2843-2851.
func TestBoundConvergesOnAConvergedReview(t *testing.T) {
	r := newRig(t, []loadStep{{state: loaded(t, marker(reviewConverged, 3))}})

	r.driver.Run(context.Background(), request())

	r.wantOrder(t, "load")
	r.wantOut(t, sayCycling+
		endLine("Converged after pass 3 — nothing at or above min_fix_severity (medium) remains."))
}

// TestBoundHaltsOnAnEmptyPassWhileAnEscalationStands pins lib/run.sh:2847-2848:
// the review leg already wrote the label this message reports, so an empty pass
// with an escalation standing is not read out as a convergence.
func TestBoundHaltsOnAnEmptyPassWhileAnEscalationStands(t *testing.T) {
	r := newRig(t, []loadStep{
		{state: loaded(t, marker(resolveEscalated, 2), marker(reviewEmpty, 3))},
	})

	r.driver.Run(context.Background(), request())

	r.wantOrder(t, "load")
	r.wantOut(t, sayCycling+
		endLine("Halted after pass 3 — the pass raised nothing new, and the escalated findings still need a human decision."))
}

// TestBoundConvergesOnAConvergedVerdictDespiteAnEscalation pins the
// `verdict != converged` half of the same condition: a converged verdict is
// exempt from the escalation halt.
func TestBoundConvergesOnAConvergedVerdictDespiteAnEscalation(t *testing.T) {
	r := newRig(t, []loadStep{
		{state: loaded(t, marker(resolveEscalated, 2), marker(reviewConverged, 3))},
	})

	r.driver.Run(context.Background(), request())

	r.wantOut(t, sayCycling+
		endLine("Converged after pass 3 — nothing at or above min_fix_severity (medium) remains."))
}

// TestBoundNeverNudges pins the absence of run_upgrade_nudge from
// lib/run.sh:2801-2895. The loop nudges after a convergence (lib/run.sh:2974);
// this function does not, and the tip is not suppressed by --no-tips here
// because it is never reached.
func TestBoundNeverNudges(t *testing.T) {
	for _, tc := range []struct {
		name    string
		markers []string
	}{
		{"converged review", []string{marker(reviewConverged, 3)}},
		{"escalation halt", []string{marker(resolveEscalated, 2), marker(reviewEmpty, 3)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t, []loadStep{{state: loaded(t, tc.markers...)}})
			req := request()
			req.NoTips = false

			r.driver.Run(context.Background(), req)

			for _, step := range r.rec.order {
				if step == "nudge" {
					t.Fatalf("the bound nudged: %v", r.rec.order)
				}
			}
		})
	}
}

// --- the pass already resolved ------------------------------------------------

// TestBoundReportsTheCapWhenThePassIsAlreadyResolved pins lib/run.sh:2854-2857.
// Both legs of the pass at the bound have finished, so nothing is owed and no
// leg runs.
func TestBoundReportsTheCapWhenThePassIsAlreadyResolved(t *testing.T) {
	r := newRig(t, []loadStep{
		{state: loaded(t, marker(reviewIssues, 3), marker(resolvePushed, 3))},
	})

	r.driver.Run(context.Background(), request())

	r.wantOrder(t, "load")
	r.wantOut(t, sayCycling+
		endLine("Reached max_passes_per_cycle (3) on acme/widget#42 without starting another pass."))
}

// TestBoundOwesTheResolveLegOfAnUnfinishedResolvePass pins the other side of
// state_current_pass_complete: a resolve marker that is not complete leaves the
// leg owed.
func TestBoundOwesTheResolveLegOfAnUnfinishedResolvePass(t *testing.T) {
	r := newRig(t, []loadStep{
		{state: loaded(t, marker(reviewIssues, 3), marker(resolveStarted, 3))},
		{state: loaded(t, marker(reviewIssues, 3), marker(resolvePushed, 3))},
	})

	r.driver.Run(context.Background(), request())

	r.wantOrder(t, "load", "resolve", "load")
}

// --- the post-resolve reading, shared with the loop --------------------------

// TestBoundHaltsOnABlockedResolveMarker pins lib/run.sh:2868-2871.
func TestBoundHaltsOnABlockedResolveMarker(t *testing.T) {
	r := newRig(t, []loadStep{
		{state: loaded(t, marker(reviewIssues, 3))},
		{state: loaded(t, marker(reviewIssues, 3), marker(resolveBlocked, 3))},
	})

	r.driver.Run(context.Background(), request())

	r.wantOut(t, sayCycling+
		sayLine("Pass 3 is unfinished, and max_passes_per_cycle (3) starts no further pass. Its resolve leg is still owed.")+
		endLine("Halted after pass 3 — the resolver reported blocked."))
}

// TestBoundHaltsOnTheStopLabel pins lib/run.sh:2872-2875.
func TestBoundHaltsOnTheStopLabel(t *testing.T) {
	stopped := loaded(t, marker(reviewIssues, 3), marker(resolvePushed, 3))
	stopped.Labels = []string{"crossrev/stop"}
	r := newRig(t, []loadStep{
		{state: loaded(t, marker(reviewIssues, 3))},
		{state: stopped},
	})

	r.driver.Run(context.Background(), request())

	r.wantOut(t, sayCycling+
		sayLine("Pass 3 is unfinished, and max_passes_per_cycle (3) starts no further pass. Its resolve leg is still owed.")+
		endLine("Halted after pass 3 — a point needs a human decision, so crossrev/stop is applied."))
}

// TestBoundConvergesWhenTheResolvePassSettledWithoutPushing pins
// lib/run.sh:2885-2888. A pass that settled every finding without pushing is
// done: the next review would decline the unchanged head.
func TestBoundConvergesWhenTheResolvePassSettledWithoutPushing(t *testing.T) {
	r := newRig(t, []loadStep{
		{state: loaded(t, marker(reviewIssues, 3))},
		{state: loaded(t, marker(reviewIssues, 3), marker(resolveSettled, 3))},
	})

	r.driver.Run(context.Background(), request())

	r.wantOut(t, sayCycling+
		sayLine("Pass 3 is unfinished, and max_passes_per_cycle (3) starts no further pass. Its resolve leg is still owed.")+
		endLine("Converged after pass 3 — nothing at or above min_fix_severity (medium) remains."))
}

// TestBoundHaltsWhenTheResolvePassLeftSomethingForAPerson pins
// lib/run.sh:2889-2892, backticks and all.
func TestBoundHaltsWhenTheResolvePassLeftSomethingForAPerson(t *testing.T) {
	r := newRig(t, []loadStep{
		{state: loaded(t, marker(reviewIssues, 3))},
		{state: loaded(t, marker(reviewIssues, 3), marker(resolveUnfiled, 3))},
	})

	r.driver.Run(context.Background(), request())

	r.wantOut(t, sayCycling+
		sayLine("Pass 3 is unfinished, and max_passes_per_cycle (3) starts no further pass. Its resolve leg is still owed.")+
		endLine("Halted after pass 3 — the resolve leg left something a person has to settle. `crossrev status --pr 42` says what."))
}

// TestBoundIgnoresTheLabelOfAnUnfinishedResolvePass pins the
// `state == complete` guard on the label read (lib/run.sh:2882): a resolve leg
// that came back without settling its marker gets the cap line, not a label.
func TestBoundIgnoresTheLabelOfAnUnfinishedResolvePass(t *testing.T) {
	r := newRig(t, []loadStep{
		{state: loaded(t, marker(reviewIssues, 3))},
		{state: loaded(t, marker(reviewIssues, 3), marker(resolveStarted, 3))},
	})

	r.driver.Run(context.Background(), request())

	r.wantOut(t, sayCycling+
		sayLine("Pass 3 is unfinished, and max_passes_per_cycle (3) starts no further pass. Its resolve leg is still owed.")+
		endLine("Reached max_passes_per_cycle (3) on acme/widget#42 — pass 3 is resolved, and no further pass starts."))
}

// --- what the reloads are told ------------------------------------------------

// TestBoundReloadsWithTheLoadedRepository pins lib/run.sh:2828 and :2860, which
// call ctx_load with "$CTX_PR" "$CTX_REPO" rather than with the pull request and
// repository the operator typed. The loop's own reloads use the typed pair
// (lib/run.sh:2944, :2950, :2974), so a cycle started without --repo forwards no
// repository there and the resolved slug here. Measured:
//
//	LOAD 42 acme/widget
func TestBoundReloadsWithTheLoadedRepository(t *testing.T) {
	r := newRig(t, []loadStep{
		{state: loaded(t, marker(reviewStarted, 3))},
		{state: loaded(t, marker(reviewIssues, 3))},
		{state: loaded(t, marker(reviewIssues, 3), marker(resolvePushed, 3))},
	})

	r.driver.Run(context.Background(), request())

	if len(r.rec.loads) != 3 {
		t.Fatalf("loads = %d, want 3", len(r.rec.loads))
	}
	var none core.Slug
	if r.rec.loads[0].Repo != none {
		t.Errorf("first load repo = %q, want the empty slug the request carried", r.rec.loads[0].Repo)
	}
	want := slugOf(t, "acme/widget")
	for i, load := range r.rec.loads[1:] {
		if load.Repo != want || load.PR != 42 {
			t.Errorf("reload %d = %s#%d, want acme/widget#42", i+1, load.Repo, load.PR)
		}
		if load.Trigger != TriggerHuman {
			t.Errorf("reload %d trigger = %q, want human", i+1, load.Trigger)
		}
	}
}

// --- the whole thing, as bytes ------------------------------------------------

// TestBoundPrintsTheBytesTheShellPrints spells the longest path out as one
// literal, against a State built here rather than by loaded(). Measured against
// the shell with a started review marker at the bound:
//
//	  Pass 3 is unfinished, and max_passes_per_cycle (3) starts no further pass. Resuming its review.
//	LEG review --pr 42 --no-tips
//	LOAD 42 acme/widget
//	  Pass 3 is unfinished, and max_passes_per_cycle (3) starts no further pass. Its resolve leg is still owed.
//	LEG resolve --pr 42 --no-tips
//	LOAD 42 acme/widget
//	└  Reached max_passes_per_cycle (3) on acme/widget#42 — pass 3 is resolved, and no further pass starts.
func TestBoundPrintsTheBytesTheShellPrints(t *testing.T) {
	repo := slugOf(t, "acme/widget")
	at := func(markers string) State {
		return State{
			Repo:              repo,
			PR:                42,
			Markers:           parseMarkers(t, markers),
			MaxPassesPerCycle: 3,
			MinFixSeverity:    core.SeverityMedium,
		}
	}
	r := newRig(t, []loadStep{
		{state: at(`[{"v":1,"leg":"review","pass":3,"state":"started"}]`)},
		{state: at(`[{"v":1,"leg":"review","pass":3,"state":"complete","verdict":"issues-remain","findings":[{"severity":"high"}]}]`)},
		{state: at(`[{"v":1,"leg":"review","pass":3,"state":"complete","verdict":"issues-remain","findings":[{"severity":"high"}]},` +
			`{"v":1,"leg":"resolve","pass":3,"state":"complete","blocked":false,"commit_sha":"abc123","resolutions":[{"resolution":"fixed"}]}]`)},
	})

	got := r.driver.Run(context.Background(), request())

	if got.ExitCode != 0 {
		t.Errorf("exit code = %d, want 0", got.ExitCode)
	}
	r.wantOrder(t, "load", "review", "load", "resolve", "load")
	r.wantOut(t, "  Cycling acme/widget#42, up to 3 passes. Ctrl-C is safe — each leg finishes the write in flight.\n"+
		"  Pass 3 is unfinished, and max_passes_per_cycle (3) starts no further pass. Resuming its review.\n"+
		"  Pass 3 is unfinished, and max_passes_per_cycle (3) starts no further pass. Its resolve leg is still owed.\n"+
		"└  Reached max_passes_per_cycle (3) on acme/widget#42 — pass 3 is resolved, and no further pass starts.\n\n")
}
