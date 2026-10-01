package review

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/carlosboeing/crossrev/internal/intel"
	"github.com/carlosboeing/crossrev/internal/prompt"
	"github.com/carlosboeing/crossrev/internal/runlog"
	"github.com/carlosboeing/crossrev/internal/ui"
	"github.com/carlosboeing/crossrev/internal/validate"
)

// longestConcern reserves the largest configured focus block during packing.
func longestConcern(concerns []string) string {
	longest := ""
	for _, concern := range concerns {
		if len(prompt.ConcernBlock(concern)) > len(prompt.ConcernBlock(longest)) {
			longest = concern
		}
	}
	return longest
}

// invokeConcerns accepts every concern before returning one merged input.
// Usage and reads are charged per invocation; only the returned union can
// enter a generation, so interruption leaves no partially accepted input.
func (l *Leg) invokeConcerns(ctx context.Context, req Request, loaded Context, settings legSettings, expected validate.ReviewExpectations, render func(string) []byte, suppliedBytes, call, total, part int, scope intel.Scope, outcome *batchOutcome, out *Result) (json.RawMessage, error) {
	var answers []json.RawMessage
	for i, concern := range settings.concerns {
		invocation := (call-1)*len(settings.concerns) + i + 1
		promptBytes := render(concern)
		start := l.now()
		readsMark := len(l.readsNotes)
		answer, envelope, msgs, err := l.invokePrompt(ctx, req, loaded, settings, expected, promptBytes, invocation)
		out.Messages = append(out.Messages, msgs...)
		if err != nil {
			return nil, err
		}
		l.logAcceptedCall(invocation, promptBytes, suppliedBytes, l.callReadsSince(readsMark), envelope, l.now().Sub(start).Milliseconds(), runlog.CallIdentity{Kind: "review", Concern: concern, Part: part})
		out.Messages = append(out.Messages, outcome.addEnvelope(envelope)...)
		answers = append(answers, answer)
		covered := len(outcome.verdicts)
		partName := "-"
		if part > 0 {
			partName = fmt.Sprint(part)
		}
		line := ui.Say(fmt.Sprintf("Review input %d of %d, concern %s, part %s — covered %d of %d required files.", call, total, concern, partName, covered, len(scope.Required)))
		if l.Progress != nil {
			l.Progress(line)
		} else {
			out.Messages = append(out.Messages, line)
		}
	}
	merged, err := mergeConcernPayloads(answers, settings.concerns)
	return merged, err
}

type concernCoverage struct {
	UnitNumber     int               `json:"unit_number"`
	Verdict        string            `json:"verdict"`
	FindingNumbers []int             `json:"finding_numbers"`
	Evidence       []json.RawMessage `json:"evidence"`
	Reason         *string           `json:"reason"`
}

// mergeConcernPayloads retains raw candidate fields and maps local positions
// into the union. Only identical location, category, title and why collapse.
// The same conservative coverage precedence is used for concerns and parts.
func mergeConcernPayloads(answers []json.RawMessage, concerns []string) (json.RawMessage, error) {
	if len(answers) == 0 || len(answers) != len(concerns) {
		return nil, fmt.Errorf("no complete concern answers")
	}
	var docs []map[string]json.RawMessage
	findingsUnion, mappings, err := mergeCandidatePayloads(answers, concerns)
	if err != nil {
		return nil, err
	}
	cover := map[int][]concernCoverage{}
	var order []int
	var examined []string
	var limits []string
	verdict := "converged"
	var blocked json.RawMessage = json.RawMessage("null")
	for i, answer := range answers {
		var doc map[string]json.RawMessage
		if err := json.Unmarshal(answer, &doc); err != nil {
			return nil, err
		}
		docs = append(docs, doc)
		mapped := mappings[i]
		var entries []concernCoverage
		if raw := doc["coverage"]; len(raw) > 0 {
			if err := json.Unmarshal(raw, &entries); err != nil {
				return nil, err
			}
		}
		for _, entry := range entries {
			for j, n := range entry.FindingNumbers {
				if n < 1 || n > len(mapped) {
					return nil, fmt.Errorf("concern %s names unknown finding %d", concerns[i], n)
				}
				entry.FindingNumbers[j] = mapped[n-1]
			}
			if _, exists := cover[entry.UnitNumber]; !exists {
				order = append(order, entry.UnitNumber)
			}
			cover[entry.UnitNumber] = append(cover[entry.UnitNumber], entry)
		}
		var ex string
		_ = json.Unmarshal(doc["examined_scope"], &ex)
		if ex != "" {
			examined = append(examined, ex)
		}
		var li []string
		_ = json.Unmarshal(doc["known_limits"], &li)
		limits = append(limits, li...)
		var v string
		_ = json.Unmarshal(doc["verdict"], &v)
		if v == "blocked" {
			verdict = v
			blocked = doc["blocked_reason"]
		} else if v == "issues-remain" && verdict != "blocked" {
			verdict = v
		}

	}
	var coverage []concernCoverage
	for _, number := range order {
		entries := cover[number]
		if len(entries) != len(answers) {
			return nil, fmt.Errorf("unit %d has incomplete concern coverage", number)
		}
		var parts []intel.SplitVerdict
		var evidence []json.RawMessage
		var reasons []string
		for _, entry := range entries {
			parts = append(parts, intel.SplitVerdict{Verdict: entry.Verdict, FindingNumbers: entry.FindingNumbers})
			evidence = append(evidence, entry.Evidence...)
			if entry.Reason != nil {
				reasons = append(reasons, *entry.Reason)
			}
		}
		v, numbers := intel.MergeSplitVerdicts(parts)
		if numbers == nil {
			numbers = []int{}
		}
		var reason *string
		if len(reasons) > 0 {
			joined := strings.Join(reasons, "; ")
			reason = &joined
		}
		coverage = append(coverage, concernCoverage{number, v, numbers, evidence, reason})
	}
	if limits == nil {
		limits = []string{}
	}
	result := docs[0]
	result["findings"] = findingsUnion
	result["coverage"], _ = json.Marshal(coverage)
	result["examined_scope"], _ = json.Marshal(strings.Join(examined, "; "))
	result["known_limits"], _ = json.Marshal(limits)
	result["verdict"], _ = json.Marshal(verdict)
	result["blocked_reason"] = blocked
	return json.Marshal(result)
}

// mergeCandidatePayloads maps each input's local finding positions into one
// union and preserves its concern set. Severity and proposed fixes do not
// alter identity; distinct consequence text always remains a distinct claim.
func mergeCandidatePayloads(payloads []json.RawMessage, concerns []string) (json.RawMessage, [][]int, error) {
	type candidateKey struct {
		Path, Side           string
		Line                 int
		Category, Title, Why string
	}
	candidates := []map[string]json.RawMessage{}
	var origins [][]string
	seen := map[candidateKey]int{}
	mappings := make([][]int, len(payloads))
	for i, payload := range payloads {
		var doc struct{ Findings []map[string]json.RawMessage }
		if err := json.Unmarshal(payload, &doc); err != nil {
			return nil, nil, err
		}
		for _, finding := range doc.Findings {
			raw, err := json.Marshal(finding)
			if err != nil {
				return nil, nil, err
			}
			var key candidateKey
			if err := json.Unmarshal(raw, &key); err != nil {
				return nil, nil, err
			}
			n, exists := seen[key]
			if !exists {
				n = len(candidates)
				seen[key] = n
				candidates = append(candidates, finding)
				origins = append(origins, nil)
			}
			var raised []string
			if concerns != nil {
				raised = []string{concerns[i]}
			} else if raw := finding["concerns"]; len(raw) > 0 {
				if err := json.Unmarshal(raw, &raised); err != nil {
					return nil, nil, err
				}
			}
			for _, concern := range raised {
				present := false
				for _, old := range origins[n] {
					if old == concern {
						present = true
					}
				}
				if !present {
					origins[n] = append(origins[n], concern)
				}
			}
			mappings[i] = append(mappings[i], n+1)
		}
	}
	for i, finding := range candidates {
		if len(origins[i]) > 0 {
			// A set is encoded in the engine's fixed execution order.
			var ordered []string
			for _, concern := range []string{"correctness", "consistency"} {
				for _, raised := range origins[i] {
					if raised == concern {
						ordered = append(ordered, concern)
					}
				}
			}
			finding["concerns"], _ = json.Marshal(ordered)
		}
	}
	raw, err := json.Marshal(candidates)
	return raw, mappings, err
}
