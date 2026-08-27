package policy

import (
	"strconv"
	"strings"

	"github.com/carlosboeing/crossrev/internal/core"
)

// The label names the loop mints. The `crossrev/` namespace is matched literally
// by workflows and by every marker already on a live pull request, so it stays
// lowercase whatever the prose does with the product name.
const (
	LabelAwaitingReview     = "crossrev/awaiting-review"
	LabelAwaitingResolution = "crossrev/awaiting-resolution"
	LabelConverged          = "crossrev/converged"
	LabelHalted             = "crossrev/halted"
	LabelStop               = "crossrev/stop"
	LabelPassPrefix         = "crossrev/pass-"

	// LabelWatchdogRetried is not a loop state and not one of the six. It is
	// the watchdog's own bookkeeping, and it reads as a qualifier on whatever
	// state it sits beside (ADR 0008, lib/legs.sh:303-305).
	LabelWatchdogRetried = "crossrev/watchdog-retried"
)

// FixedLabels lists the loop-state labels `init` mints for every repository,
// in the order lib/init.sh:109 writes them. The sixth member of the contract is
// the pass label, which needs a number and comes from PassLabelName.
func FixedLabels() []string {
	return []string{LabelAwaitingResolution, LabelAwaitingReview, LabelConverged, LabelHalted, LabelStop}
}

// PassLabelName is the grey pass label for one pass number (lib/init.sh:106).
func PassLabelName(pass core.PassNumber) string {
	return LabelPassPrefix + strconv.Itoa(pass.Int())
}

// PassLabelState is the loop-state word a finished pass leaves behind, in the
// spelling lib/legs.sh prints and lib/run.sh:441 matches against.
type PassLabelState string

// The four words a pass label can carry. They are mutually exclusive: lib/run.sh
// removes the other three before adding one.
const (
	PassHalted             PassLabelState = "halted"
	PassConverged          PassLabelState = "converged"
	PassAwaitingReview     PassLabelState = "awaiting-review"
	PassAwaitingResolution PassLabelState = "awaiting-resolution"
)

// String renders the bare word.
func (p PassLabelState) String() string { return string(p) }

// Label is the label the word names, as lib/run.sh:464 forms it.
//
// The constants rather than `"crossrev/" + string(p)`: the four labels are
// declared ten lines above, and a second spelling of them here is how a rename
// lands in one place and not the other. An unknown word gets the concatenation,
// which is what lib/run.sh:464 would also produce.
func (p PassLabelState) Label() string {
	switch p {
	case PassAwaitingReview:
		return LabelAwaitingReview
	case PassAwaitingResolution:
		return LabelAwaitingResolution
	case PassConverged:
		return LabelConverged
	case PassHalted:
		return LabelHalted
	default:
		return "crossrev/" + string(p)
	}
}

// AwaitingLabel is the label a leg waits behind (lib/legs.sh:95-101).
//
// Not `crossrev/awaiting-<leg>`: the leg is named for the verb and the label for
// the noun, so `resolve` waits behind `crossrev/awaiting-resolution`. Derived in
// one place because the workflows key off these strings and a mismatch stalls
// the chain silently — the label sits on the pull request with nothing listening.
func AwaitingLabel(leg core.Leg) string {
	switch leg {
	case core.LegReview:
		return LabelAwaitingReview
	case core.LegResolve:
		return LabelAwaitingResolution
	default:
		return "crossrev/awaiting-" + string(leg)
	}
}

// PassLabel is the loop-state label a finished review pass leaves behind
// (lib/legs.sh:261-271).
//
// An empty pass after an escalation is not a convergence. The reviewer correctly
// declines to re-raise a settled finding, so nothing actionable means nothing
// NEW while the escalated thread still waits on a human, and halted is the
// honest label beside a marker that reads issues-remain. The converged verdict
// arm is exempt: a reviewer that says converged after the human settled the
// thread is the settlement being verified, which is the one way out of the halt
// that does not need a person again.
func PassLabel(verdict core.Verdict, actionable, escalated int) PassLabelState {
	if verdict == core.VerdictBlocked {
		return PassHalted
	}
	if actionable > 0 {
		return PassAwaitingResolution
	}
	if escalated > 0 && verdict != core.VerdictConverged {
		return PassHalted
	}
	return PassConverged
}

// ResolvePassLabel is the loop-state label a finished resolve pass leaves behind
// (lib/legs.sh:234-248), read off the marker rather than recomputed beside it so
// the label and the marker cannot disagree.
//
// A pass that pushed hands back to the reviewer, because the head moved and
// there is something new to see — and the push, not the resolution, is the
// signal: a deferral recorded into the repository backlog moves the head without
// fixing anything. A pass that settled every finding without pushing is over,
// and converged is the honest label, because the reviewer declines an unchanged
// head and awaiting-review would park the loop on a command that refuses.
//
// Five endings still halt, and each outranks a settle: blocked, an escalation
// (this pass's or one an earlier pass left standing), a deferral whose record
// never landed, a fix that reached no commit, and a pass that recorded no
// resolutions at all.
//
// otherEscalated counts escalations standing in other passes' markers. This
// pass's own are read off the marker, and the caller may hold a newer record of
// this pass than the marker list does.
func ResolvePassLabel(m ResolveMarker, otherEscalated int) PassLabelState {
	switch {
	case m.Blocked,
		countResolution(m, core.ResolutionEscalated) > 0,
		otherEscalated > 0,
		ResolveUnpushedFix(m),
		ResolveUnrecorded(m),
		UnfiledDeferrals(m) > 0:
		return PassHalted
	case m.CommitSHA == "":
		return PassConverged
	default:
		return PassAwaitingReview
	}
}

// LabelColour is the colour a loop label is minted with (lib/legs.sh:295-308).
//
// Six hues, no two adjacent on the wheel, so the label row on a pull request
// carries state at a glance rather than a row of identical purple pills. Red is
// reserved for crossrev/stop, the one label a human applies. Every colour is
// dark enough that GitHub renders the label text white and clears 4.5:1 in all
// three renderings GitHub uses.
//
// One map rather than a constant per call site: the watchdog mints
// crossrev/halted itself when it gives up, and a second hex there is how the
// same label ends up two colours depending on which code path created it.
func LabelColour(label string) string {
	switch label {
	case LabelAwaitingReview:
		return "0969da" // blue, in progress
	case LabelAwaitingResolution:
		return "8250df" // purple, a leg owes an answer
	case LabelConverged:
		return "1a7f37" // green, finished on its own
	case LabelHalted:
		return "bc4c00" // orange, a human is needed
	case LabelStop:
		return "cf222e" // red, a human applied it
	case LabelWatchdogRetried:
		return "fbca04"
	}
	if strings.HasPrefix(label, LabelPassPrefix) {
		return "57606a" // grey, informational
	}
	return "ededed"
}

// LabelDescription is what the label means, in the words the design uses for it
// (lib/legs.sh:322-333).
//
// Per label rather than one string for all of them, because a label description
// is the only place GitHub shows a reader what a label means without them going
// looking. "crossrev loop state" on all six answered nothing, and the colours
// are the only other signal a reader has.
func LabelDescription(label string) string {
	switch label {
	case LabelAwaitingReview:
		return "crossrev: a review is owed on this pull request"
	case LabelAwaitingResolution:
		return "crossrev: the review landed, the resolve leg is owed"
	case LabelConverged:
		return "crossrev: the loop finished on its own"
	case LabelHalted:
		return "crossrev: stopped short, a human is needed"
	case LabelStop:
		return "crossrev: apply this to stop the loop"
	case LabelWatchdogRetried:
		return "crossrev: the watchdog retried this leg once"
	}
	if strings.HasPrefix(label, LabelPassPrefix) {
		// `${1##*-}` is the text after the LAST hyphen. PassLabelName appends a
		// decimal number to the prefix, so no label it mints carries a second
		// hyphen and the two readings cannot disagree — this matches the Bash
		// character for character rather than claiming a distinction.
		return "crossrev: reached pass " + label[strings.LastIndex(label, "-")+1:]
	}
	return "crossrev loop state"
}
