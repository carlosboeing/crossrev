package cycle

import (
	"context"
	"fmt"
	"io"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/policy"
	"github.com/carlosboeing/crossrev/internal/prstate"
	"github.com/carlosboeing/crossrev/internal/ui"
)

// Trigger is who asked for the cycle (lib/run.sh:2896, :2903).
type Trigger string

const (
	TriggerHuman     Trigger = "human"
	TriggerAutomatic Trigger = "automatic"
)

// Request is one `crossrev cycle` invocation, already parsed.
//
// The parser is the command's; what it produces is this. NoTips is the one flag
// that stays here rather than travelling on to a leg: the shell keeps
// `--no-tips` out of `args` (lib/run.sh:2904) and appends its own to every leg
// call, so the flag suppresses the cycle's single tip and nothing else.
type Request struct {
	PR              int
	Repo            core.Slug
	Trigger         Trigger
	HarnessOverride string
	KeepTranscripts bool
	NoTips          bool
}

// LegRequest is what one leg is invoked with: the arguments the shell collected
// into `args` (lib/run.sh:2900-2905), plus the two flags it appends at the call
// site (lib/run.sh:2944, :2946, :2972).
//
// Repo is the request's, not the loaded slug. The shell forwards only what it
// parsed, so a cycle started without `--repo` forwards none and each leg
// resolves the repository for itself.
type LegRequest struct {
	PR              int
	Repo            core.Slug
	Trigger         Trigger
	HarnessOverride string
	KeepTranscripts bool
	Continuation    bool
	NoTips          bool
}

// LegResult is how a leg stopped, as the driver reads it.
//
// The shell reads one bit: `leg_review … || return 1` (lib/run.sh:2944, :2946,
// :2972). A leg that fails has already said why, so nothing here carries a
// message.
type LegResult struct {
	Failed bool
}

// ReviewLeg and ResolveLeg are the two legs the driver drives.
//
// They are declared here rather than taken from internal/review and
// internal/resolve because the tier rule forbids a tier-3 package importing a
// peer. The narrow shape is also what the tests need: the loop's subject is the
// order of the calls and what each is told, not what a leg does with it.
type ReviewLeg interface {
	Run(ctx context.Context, req LegRequest) LegResult
}

// ResolveLeg is the resolve half of the pair.
type ResolveLeg interface {
	Run(ctx context.Context, req LegRequest) LegResult
}

// LoadRequest is one ctx_load call (lib/run.sh:232).
//
// The driver names the trigger on the first load only. Every later one is
// `ctx_load "$pr" "$repo"` (lib/run.sh:2938, :2950, :2974), where the third
// argument defaults to human — so the fork and draft refusals, which are the
// only trigger-gated ones, apply once and upfront rather than mid-loop.
type LoadRequest struct {
	PR      int
	Repo    core.Slug
	Trigger Trigger
}

// State is the part of the shared context the driver reads back after a leg.
//
// Draft is a value rather than an error because ctx_load answers it with return
// code 2 and the driver has a line of its own to print for it
// (lib/run.sh:259-263, consumed at :2921-2925).
type State struct {
	Repo              core.Slug
	PR                int
	Markers           []prstate.Marker
	Labels            []string
	MaxPassesPerCycle int
	MinFixSeverity    core.Severity
	Draft             bool
}

// Loader is ctx_load. It refuses the way ctx_load refuses, with a
// *ui.FatalError carrying the reason the shell prints.
type Loader interface {
	Load(ctx context.Context, req LoadRequest) (State, error)
}

// BoundInput is what _cycle_finish_at_bound is called with
// (lib/run.sh:2932): the pass the pull request is on, the cap that stopped it,
// and the leg arguments the driver would have forwarded.
type BoundInput struct {
	Pass  int
	Max   int
	State State
	Leg   LegRequest
}

// BoundResult is how it stopped. The shell writes
// `_cycle_finish_at_bound … || return 1`, so the failure is the whole of what
// it reports; Err carries a context-load refusal the caller has to print, the
// way Result.Err does.
type BoundResult struct {
	Failed bool
	Err    error
}

// Bound finishes a cycle whose newest review pass already sits at or past the
// cap (lib/run.sh:2795-2889).
//
// It is a seam rather than a method so the port can arrive in its own file and
// so a test can watch the driver hand over. A nil Bound is finishAtBound, which
// is what the shell calls.
type Bound func(ctx context.Context, d *Driver, in BoundInput) BoundResult

// Driver is cmd_cycle: the local-mode loop that runs the two legs in sequence
// until the pass bound or a terminal state (lib/run.sh:2895-3016).
//
// State lives on the pull request rather than in memory, so every decision here
// is taken off a context load the driver asks for again after each leg. That is
// what makes the loop a thin driver over the same legs the workflows invoke,
// and what lets the two modes be compared on real pull requests.
type Driver struct {
	Review  ReviewLeg
	Resolve ResolveLeg
	Loader  Loader
	// Out is where ui_say and ui_end write.
	Out io.Writer
	// Nudge is run_upgrade_nudge (lib/run.sh:3777-3789). The environment read,
	// the config read and the workflow directory probe belong to the command
	// that wires the driver; what belongs here is which endings call it.
	Nudge func()
	// Pairing is run_assert_cycle_pairing (lib/run.sh:575-587), asked about the
	// harness override the request carried. Nil is no check.
	Pairing func(override string) error
	// Bound is _cycle_finish_at_bound.
	Bound Bound
}

// Result is how the cycle stopped.
//
// ExitCode is the shell's return: 0 for every terminal state the loop reports
// itself, 1 for a leg that failed and for a refusal. Err carries a refusal the
// caller has to print; a failed leg leaves it nil, because the leg said why.
type Result struct {
	ExitCode int
	Err      error
}

func (d *Driver) io() *ui.IO { return &ui.IO{Out: d.Out} }

func (d *Driver) nudge() {
	if d.Nudge != nil {
		d.Nudge()
	}
}

// Run drives the cycle.
func (d *Driver) Run(ctx context.Context, req Request) Result {
	if req.PR == 0 {
		// lib/run.sh:2910
		return Result{ExitCode: 1, Err: &ui.FatalError{
			Reason: "crossrev cycle needs a pull request number",
			Action: "Usage: crossrev cycle --pr 42",
		}}
	}
	// Checked here as well as in the legs, because the context load below reads
	// the trigger first and treats anything it does not recognise as human
	// (lib/run.sh:2911-2916).
	switch req.Trigger {
	case TriggerHuman, TriggerAutomatic:
	default:
		return Result{ExitCode: 1, Err: &ui.FatalError{
			Reason: "unknown cycle trigger: " + string(req.Trigger),
			Action: "Use --trigger human or --trigger automatic.",
		}}
	}

	out := d.io()

	// The draft rule is applied once, here, rather than left to the first leg:
	// a cycle that declined mid-loop would report the reviewer as unable to
	// finish (lib/run.sh:2918-2925).
	state, err := d.Loader.Load(ctx, LoadRequest{PR: req.PR, Repo: req.Repo, Trigger: req.Trigger})
	if err != nil {
		return Result{ExitCode: 1, Err: err}
	}
	if state.Draft {
		// lib/run.sh:260-261
		out.Say(fmt.Sprintf("%s#%d is a draft pull request, so an automatic invocation does not review it.", state.Repo, state.PR))
		out.Say("Mark it ready for review, or ask for a review explicitly.")
		return Result{ExitCode: 0}
	}

	// lib/run.sh:2926
	if d.Pairing != nil {
		if err := d.Pairing(req.HarnessOverride); err != nil {
			return Result{ExitCode: 1, Err: err}
		}
	}

	max := state.MaxPassesPerCycle
	// lib/run.sh:2928
	out.Say(fmt.Sprintf("Cycling %s#%d, up to %d passes. Ctrl-C is safe — each leg finishes the write in flight.",
		state.Repo, state.PR, max))

	pass := prstate.CurrentReviewPass(state.Markers)
	if pass >= max {
		// lib/run.sh:2931-2934
		bound := d.Bound
		if bound == nil {
			bound = finishAtBound
		}
		if res := bound(ctx, d, BoundInput{
			Pass:  pass,
			Max:   max,
			State: state,
			Leg:   d.legRequest(req, false),
		}); res.Failed {
			return Result{ExitCode: 1, Err: res.Err}
		}
		return Result{ExitCode: 0}
	}

	// lib/run.sh:2936
	for i := 1; i <= max; i++ {
		if i > 1 {
			// lib/run.sh:2938-2943
			state, err = d.Loader.Load(ctx, d.reload(req))
			if err != nil {
				return Result{ExitCode: 1, Err: err}
			}
			pass = prstate.CurrentReviewPass(state.Markers)
			if pass >= max {
				out.End(capWithoutStarting(max, state))
				return Result{ExitCode: 0}
			}
		}
		// lib/run.sh:2944, :2946. The continuation flag is what tells the review
		// leg the pass was generated rather than individually requested, and the
		// pass bound is applied to a generated pass only.
		if d.Review.Run(ctx, d.legRequest(req, i > 1)).Failed {
			return Result{ExitCode: 1}
		}

		// Re-read: the review leg wrote the state this decision reads
		// (lib/run.sh:2949-2954).
		state, err = d.Loader.Load(ctx, d.reload(req))
		if err != nil {
			return Result{ExitCode: 1, Err: err}
		}
		pass = prstate.CurrentReviewPass(state.Markers)
		// lib/run.sh:2952-2967, shared with the bound so a byte can only be
		// wrong in one place.
		switch readReview(out, state, pass) {
		case reviewHalted:
			return Result{ExitCode: 0}
		case reviewFinished:
			// The tip sits after the inner if/else, not inside the converged
			// arm (lib/run.sh:2968), so the escalation halt nudges too.
			if !req.NoTips {
				d.nudge()
			}
			return Result{ExitCode: 0}
		}

		// lib/run.sh:2972
		if d.Resolve.Run(ctx, d.legRequest(req, false)).Failed {
			return Result{ExitCode: 1}
		}

		// lib/run.sh:2974-2975
		state, err = d.Loader.Load(ctx, d.reload(req))
		if err != nil {
			return Result{ExitCode: 1, Err: err}
		}
		// lib/run.sh:2975-3010, shared with the bound. Without the halt arm the
		// driver would read a pass the resolve leg labelled halted, start
		// another one anyway, and re-drive the resolver over work that is
		// waiting on a person.
		switch readResolve(out, state, pass) {
		case resolveHalted:
			return Result{ExitCode: 0}
		case resolveConverged:
			if !req.NoTips {
				d.nudge()
			}
			return Result{ExitCode: 0}
		}
	}

	// lib/run.sh:3013-3015
	out.End(fmt.Sprintf("Reached max_passes_per_cycle (%d) on %s#%d without converging. Every finding and reply is on the pull request.",
		max, state.Repo, state.PR))
	if !req.NoTips {
		d.nudge()
	}
	return Result{ExitCode: 0}
}

// reload is every ctx_load after the first: the pull request and the repository
// the operator named, and no trigger (lib/run.sh:2938, :2950, :2974).
func (d *Driver) reload(req Request) LoadRequest {
	return LoadRequest{PR: req.PR, Repo: req.Repo, Trigger: TriggerHuman}
}

func (d *Driver) legRequest(req Request, continuation bool) LegRequest {
	return LegRequest{
		PR:              req.PR,
		Repo:            req.Repo,
		Trigger:         req.Trigger,
		HarnessOverride: req.HarnessOverride,
		KeepTranscripts: req.KeepTranscripts,
		Continuation:    continuation,
		NoTips:          true,
	}
}

// hasStop reports whether the loop's stop label is on the pull request.
//
// The shell greps the space-joined label list for the name as a word
// (lib/run.sh:2980), which also matches a label that merely ends in it, such as
// `team-crossrev/stop`. The name is matched whole here, which is the rule the
// rest of the Go tree already reads this label by
// (internal/review/context.go:169).
func hasStop(labels []string) bool {
	for _, label := range labels {
		if label == policy.LabelStop {
			return true
		}
	}
	return false
}

// asPolicyResolve is the marker view legs_resolve_pass_label reads
// (lib/legs.sh:234-248).
func asPolicyResolve(m prstate.Marker) policy.ResolveMarker {
	out := policy.ResolveMarker{CommitSHA: m.CommitSHA.Value()}
	if blocked, ok := m.Blocked.Get(); ok {
		out.Blocked = blocked
	}
	var records []struct {
		Resolution string  `json:"resolution"`
		Tracked    *string `json:"crossrev_tracked"`
	}
	if err := m.DecodeResolutions(&records); err != nil {
		return out
	}
	for _, record := range records {
		entry := policy.ResolutionRecord{Resolution: core.Resolution(record.Resolution)}
		if record.Tracked != nil {
			entry.Tracked = core.NewTracked(*record.Tracked)
		}
		out.Resolutions = append(out.Resolutions, entry)
	}
	return out
}
