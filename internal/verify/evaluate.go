package verify

import (
	"fmt"

	"github.com/carlosboeing/crossrev/internal/config"
	"github.com/carlosboeing/crossrev/internal/forge"
	"github.com/carlosboeing/crossrev/internal/policy"
	"github.com/carlosboeing/crossrev/internal/prstate"
)

// State is the required-check gate's verdict. It is the policy vocabulary,
// not a second spelling of it: the predicate and the evaluator judge one
// set of states.
type State = policy.VerificationState

const (
	// None means no checks are required, so the gate is open by default.
	None = policy.VerificationNone
	// Passed means every required check reported a passing run.
	Passed = policy.VerificationPassed
	// Pending means a required check's newest run is still going.
	Pending = policy.VerificationPending
	// Failed means a required check's newest run failed.
	Failed = policy.VerificationFailed
	// Missing means no run carries a required check's name and app.
	Missing = policy.VerificationMissing
	// Unreadable means the enumeration itself could not be trusted: a
	// refused read or a partial list. It fails closed, never open.
	Unreadable = policy.VerificationUnreadable
)

// Check is one required entry's evidence: the entry, its state, and the
// selected run's conclusion and URL.
type Check struct {
	Name string
	App  string
	// State is passed, pending, failed or missing — or unreadable when the
	// enumeration could not be trusted and no entry was evaluated.
	State State
	// Conclusion is the selected run's conclusion, empty for a missing or
	// unfinished run.
	Conclusion string
	// URL is the selected run's page, empty for a missing run.
	URL string
	// Note qualifies the state: `skipped` or `neutral` beside a pass, or
	// the raw value beside an unknown one.
	Note string
}

// Evidence is the evaluated gate: the overall state, the per-check detail,
// and the reason when the enumeration could not be trusted.
type Evidence struct {
	State  State
	Checks []Check
	Reason string
}

// Configured reports the gate judged required checks, rather than reading
// none_required for an empty list.
func (e Evidence) Configured() bool { return e.State != None && e.State != "" }

// MarkerRecord renders the evidence as the marker's verification record:
// the overall state and the per-check detail. Both legs record through
// this one mapping, so a review marker and a resolve marker spell the same
// evidence the same way.
func (e Evidence) MarkerRecord() prstate.MarkerVerification {
	out := prstate.MarkerVerification{State: string(e.State), Reason: e.Reason}
	for _, check := range e.Checks {
		out.Checks = append(out.Checks, prstate.MarkerVerificationCheck{
			Name:       check.Name,
			App:        check.App,
			State:      string(check.State),
			Conclusion: check.Conclusion,
			URL:        check.URL,
			Note:       check.Note,
		})
	}
	return out
}

// HaltWord names the halt a blocking state ends the pass with. Passed and
// none_required name none, because they do not block.
func (e Evidence) HaltWord() (string, bool) {
	switch e.State {
	case Pending:
		return "required_check_pending", true
	case Missing:
		return "required_check_missing", true
	case Failed:
		return "required_check_failed", true
	case Unreadable:
		return "required_checks_unreadable", true
	}
	return "", false
}

// Evaluate judges the required checks against the enumeration for the
// head: one state per entry, and the overall state.
//
// Selection matches name and app together, and the run with the greatest
// id wins, so a rerun supersedes an older result. Aggregation ranks a
// failure above a wait, a wait above an absence, and only all passed is
// passed: a running check may still report, so what is still moving
// outranks what never appeared.
//
// A refused read or a truncated enumeration is unreadable, whatever the
// runs it did list say — a gate that judged a partial list would converge
// on evidence it never saw.
func Evaluate(required []config.RequiredCheck, runs forge.CheckRuns, readErr error) Evidence {
	if len(required) == 0 {
		return Evidence{State: None}
	}
	if readErr != nil {
		return unreadable(required, readErr.Error())
	}
	if runs.Truncated {
		return unreadable(required, "the endpoint reported more check runs than it listed")
	}
	ev := Evidence{State: Passed}
	for _, entry := range required {
		check := Check{Name: entry.Name, App: entry.App, State: Missing}
		var selected *forge.CheckRun
		for i := range runs.Runs {
			candidate := &runs.Runs[i]
			if candidate.Name != entry.Name || candidate.App != entry.App {
				continue
			}
			if selected == nil || candidate.ID > selected.ID {
				selected = candidate
			}
		}
		if selected != nil {
			check.State, check.Note = mapRun(selected.Status, selected.Conclusion)
			check.Conclusion = selected.Conclusion
			check.URL = selected.URL
		}
		ev.Checks = append(ev.Checks, check)
		ev.State = combine(ev.State, check.State)
	}
	return ev
}

// unreadable is the evidence for an enumeration that could not be trusted.
// Every entry rides along unread, so the marker records what was required
// beside why it could not be judged.
func unreadable(required []config.RequiredCheck, reason string) Evidence {
	ev := Evidence{State: Unreadable, Reason: reason}
	for _, entry := range required {
		ev.Checks = append(ev.Checks, Check{Name: entry.Name, App: entry.App, State: Unreadable})
	}
	return ev
}

// combine folds one entry's state into the overall verdict: a failure above
// a wait, a wait above an absence.
func combine(overall, entry State) State {
	rank := func(s State) int {
		switch s {
		case Failed:
			return 3
		case Pending:
			return 2
		case Missing:
			return 1
		}
		return 0
	}
	if rank(entry) > rank(overall) {
		return entry
	}
	return overall
}

// mapRun maps one run's status and conclusion to its state. A completed run
// passes on success, neutral or skipped and fails on every other
// conclusion; an unfinished run is pending; anything else — a status or
// conclusion GitHub never documented, or a null where a value belongs —
// fails with the raw value named, because an unknown answer is not
// evidence for convergence.
func mapRun(status, conclusion string) (State, string) {
	switch status {
	case "completed":
		switch conclusion {
		case "success":
			return Passed, ""
		case "neutral", "skipped":
			return Passed, conclusion
		case "failure", "timed_out", "cancelled", "action_required", "stale", "startup_failure":
			return Failed, ""
		default:
			return Failed, fmt.Sprintf("unknown conclusion %q", conclusion)
		}
	case "queued", "in_progress", "waiting", "requested", "pending":
		return Pending, ""
	default:
		return Failed, fmt.Sprintf("unknown status %q", status)
	}
}
