package verify

import (
	"strings"

	"github.com/carlosboeing/crossrev/internal/prstate"
)

// FromRecord reconstructs recorded evidence for summaries and recovery.
func FromRecord(record prstate.MarkerVerification) Evidence {
	ev := Evidence{State: State(record.State), Reason: record.Reason}
	for _, check := range record.Checks {
		ev.Checks = append(ev.Checks, Check{Name: check.Name, App: check.App, State: State(check.State), Conclusion: check.Conclusion, URL: check.URL, Note: check.Note})
	}
	return ev
}

// Debt names the halt word and the checks that block convergence.
func Debt(ev Evidence) string {
	word, _ := ev.HaltWord()
	switch ev.State {
	case Unreadable:
		return word + ": " + ev.Reason
	case Failed:
		var parts []string
		for _, check := range ev.Checks {
			if check.State != Failed {
				continue
			}
			part := check.Name + " (" + check.Conclusion + ")"
			if check.URL != "" {
				part += " " + check.URL
			}
			parts = append(parts, part)
		}
		return word + ": " + strings.Join(parts, ", ")
	default:
		var names []string
		for _, check := range ev.Checks {
			if check.State == Passed {
				continue
			}
			names = append(names, check.Name)
		}
		return word + ": " + strings.Join(names, ", ")
	}
}
