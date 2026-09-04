package cycle

import (
	"context"
	"fmt"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/policy"
	"github.com/carlosboeing/crossrev/internal/prstate"
	"github.com/carlosboeing/crossrev/internal/ui"
)

// finishAtBound finishes the pass already under way, then stops
// (lib/run.sh:2788-2895).
//
// The bound stops another pass from *starting*; it must not strand one that has
// already begun. A cycle interrupted between the two legs of the last allowed
// pass would otherwise be unresumable: the restart reads the pass number, finds
// it at the bound and exits 0, leaving that pass's findings unresolved and its
// threads open — a halt that reads as a clean finish, which is the shape of
// failure this loop exists to avoid.
//
// It runs at most one leg of each kind. Nothing here starts a pass: the review
// leg is invoked only to resume an unfinished claim for the pass already at the
// bound.
func finishAtBound(ctx context.Context, d *Driver, in BoundInput) BoundResult {
	out := d.io()
	state, pass, max := in.State, in.Pass, in.Max

	// lib/run.sh:2805-2807
	marker, _ := prstate.MarkerFor(state.Markers, pass, core.LegReview)
	if marker.State != core.PassComplete {
		// lib/run.sh:2808
		out.Say(fmt.Sprintf(
			"Pass %d is unfinished, and max_passes_per_cycle (%d) starts no further pass. Resuming its review.",
			pass, max))
		// Whether the bound applies turns on which kind of unfinished this is,
		// and the marker's own state answers it (lib/run.sh:2809-2827).
		//
		// `started` is an open claim, and the review leg resumes an open claim
		// at its existing pass number rather than beginning another — the open
		// claim is read off the same marker this was, so the two cannot
		// disagree. The bound must not be applied to that: a person may
		// legitimately have started pass 4 under a bound of 3, which is exactly
		// what `crossrev review --pr N` typed by hand does, and the
		// continuation flag would refuse the resume, write a declined marker
		// over a pass that is mid-flight, and exit clean with the review
		// unfinished.
		//
		// Anything else — a declined marker, or none — leaves the leg to
		// compute a pass number of its own, and the bound is the only thing
		// stopping it starting one. That case keeps the flag.
		leg := in.Leg
		leg.Continuation = marker.State != core.PassStarted
		if d.Review.Run(ctx, leg).Failed {
			return BoundResult{Failed: true}
		}
		// lib/run.sh:2828-2830. The reload names the loaded repository rather
		// than the one the operator typed, which is the one place this argv
		// differs from the loop's (lib/run.sh:2944, :2950, :2974).
		var err error
		state, err = d.Loader.Load(ctx, reloadLoaded(state))
		if err != nil {
			return BoundResult{Failed: true, Err: err}
		}
		pass = prstate.CurrentReviewPass(state.Markers)
		marker, _ = prstate.MarkerFor(state.Markers, pass, core.LegReview)
		if marker.State != core.PassComplete {
			// lib/run.sh:2831-2834
			out.End(fmt.Sprintf(
				"Stopped on pass %d of %s#%d — the review leg did not finish, so no resolve leg follows it.",
				pass, state.Repo, state.PR))
			return BoundResult{}
		}
	}

	// The same reading the loop takes after a review leg (lib/run.sh:2837-2853).
	// No tip follows any of it: nothing in this function nudges.
	if readReview(out, state, pass) != reviewContinues {
		return BoundResult{}
	}
	// lib/run.sh:2854-2857
	if prstate.CurrentPassComplete(state.Markers, pass, core.LegResolve) {
		out.End(capWithoutStarting(max, state))
		return BoundResult{}
	}

	// lib/run.sh:2859
	out.Say(fmt.Sprintf(
		"Pass %d is unfinished, and max_passes_per_cycle (%d) starts no further pass. Its resolve leg is still owed.",
		pass, max))
	// lib/run.sh:2860
	if d.Resolve.Run(ctx, in.Leg).Failed {
		return BoundResult{Failed: true}
	}

	// The same outcomes the loop reports after a resolve leg, so the last line
	// an operator reads describes what actually happened rather than assuming
	// the bound was the reason it stopped (lib/run.sh:2862-2892).
	var err error
	state, err = d.Loader.Load(ctx, reloadLoaded(state))
	if err != nil {
		return BoundResult{Failed: true, Err: err}
	}
	if readResolve(out, state, pass) != resolveContinues {
		return BoundResult{}
	}
	// lib/run.sh:2893
	out.End(fmt.Sprintf(
		"Reached max_passes_per_cycle (%d) on %s#%d — pass %d is resolved, and no further pass starts.",
		max, state.Repo, state.PR, pass))
	return BoundResult{}
}

// reloadLoaded is `ctx_load "$CTX_PR" "$CTX_REPO"` (lib/run.sh:2828, :2860):
// the pull request and repository the last load resolved, and no trigger.
func reloadLoaded(state State) LoadRequest {
	return LoadRequest{PR: state.PR, Repo: state.Repo, Trigger: TriggerHuman}
}

// reviewReading is what the reading of a finished review pass came to.
type reviewReading int

const (
	// reviewContinues is a pass with actionable findings left: the caller runs
	// the resolve leg.
	reviewContinues reviewReading = iota
	// reviewHalted is the blocked verdict. The loop prints no tip after it.
	reviewHalted
	// reviewFinished is the converged-or-empty arm, whichever of its two lines
	// was printed. The loop nudges after both, because the shell's tip sits
	// after the inner if/else rather than inside its converged branch
	// (lib/run.sh:2974).
	reviewFinished
)

// readReview is the decision the loop (lib/run.sh:2958-2973) and the bound
// (lib/run.sh:2837-2851) both take off the review marker for the current pass.
//
// It is one function so a byte can only be wrong in one place. It prints the
// terminal line when there is one; whether a tip follows is the caller's, and
// the two callers disagree.
func readReview(out *ui.IO, state State, pass int) reviewReading {
	marker, _ := prstate.MarkerFor(state.Markers, pass, core.LegReview)
	verdict := core.Verdict(marker.Verdict.Value())
	if verdict == "" {
		verdict = core.VerdictBlocked
	}
	actionable := actionableCount(marker.Findings, state.MinFixSeverity)

	if verdict == core.VerdictBlocked {
		out.End(fmt.Sprintf("Halted after pass %d — the reviewer could not complete.", pass))
		return reviewHalted
	}
	if verdict != core.VerdictConverged && actionable != 0 {
		return reviewContinues
	}
	// The review leg already wrote the label this message reports; the two have
	// to agree, so an empty pass while an escalation stands is not read out as
	// a convergence.
	if verdict != core.VerdictConverged && escalatedCount(state.Markers) > 0 {
		out.End(fmt.Sprintf(
			"Halted after pass %d — the pass raised nothing new, and the escalated findings still need a human decision.",
			pass))
	} else {
		out.End(fmt.Sprintf("Converged after pass %d — nothing at or above min_fix_severity (%s) remains.",
			pass, state.MinFixSeverity))
	}
	return reviewFinished
}

// resolveReading is what the reading of a just-run resolve leg came to.
type resolveReading int

const (
	// resolveContinues is a pass that handed back to the reviewer. The loop
	// goes round; the bound reports the cap.
	resolveContinues resolveReading = iota
	// resolveHalted is any of the three halts, whichever line was printed.
	resolveHalted
	// resolveConverged is the settle. The loop nudges after it.
	resolveConverged
)

// readResolve is the decision the loop (lib/run.sh:2981-3016) and the bound
// (lib/run.sh:2867-2892) both take after the resolve leg.
//
// How the resolve pass ended outranks the bound, because the bound is a
// statement about passes that will not start and this pass has a terminal state
// of its own. Reported as the cap instead, a settle reads as a failure to
// converge, and a deferral nobody filed or a fix that reached no commit reads
// as a pass that finished cleanly.
func readResolve(out *ui.IO, state State, pass int) resolveReading {
	marker, found := prstate.MarkerFor(state.Markers, pass, core.LegResolve)
	if blocked, _ := marker.Blocked.Get(); blocked {
		out.End(fmt.Sprintf("Halted after pass %d — the resolver reported blocked.", pass))
		return resolveHalted
	}
	if hasStop(state.Labels) {
		out.End(fmt.Sprintf("Halted after pass %d — a point needs a human decision, so crossrev/stop is applied.", pass))
		return resolveHalted
	}
	// The label the resolve leg just applied, read here by the same rule it
	// wrote it with, so the terminal and the pull request cannot disagree about
	// how the pass ended. Read once: two calls are two chances to answer
	// differently.
	label := policy.PassLabelState("")
	if found && marker.State == core.PassComplete {
		label = policy.ResolvePassLabel(asPolicyResolve(marker), escalatedCount(state.Markers))
	}
	// A pass that settled every finding without pushing is done: the next
	// review would decline the unchanged head, so the cycle stops here rather
	// than spinning declines until the cap and reporting a convergence as a
	// failure to converge.
	if label == policy.PassConverged {
		out.End(fmt.Sprintf("Converged after pass %d — nothing at or above min_fix_severity (%s) remains.",
			pass, state.MinFixSeverity))
		return resolveConverged
	}
	// And a halt ends the cycle too. Blocked and escalated are caught above,
	// each by the thing that records them; the halts left here — a deferral
	// nobody filed, a fix that reached no commit — apply no crossrev/stop,
	// because nobody pulled the brake.
	if label == policy.PassHalted {
		out.End(fmt.Sprintf(
			"Halted after pass %d — the resolve leg left something a person has to settle. `crossrev status --pr %d` says what.",
			pass, state.PR))
		return resolveHalted
	}
	return resolveContinues
}

// capWithoutStarting is the line the loop prints when a reload finds the pass
// already at the cap (lib/run.sh:2947) and the bound prints when the pass at
// the cap is already resolved (lib/run.sh:2855).
func capWithoutStarting(max int, state State) string {
	return fmt.Sprintf("Reached max_passes_per_cycle (%d) on %s#%d without starting another pass.",
		max, state.Repo, state.PR)
}
