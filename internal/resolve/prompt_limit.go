package resolve

import (
	"context"
	"fmt"

	"github.com/carlosboeing/crossrev/internal/policy"
	"github.com/carlosboeing/crossrev/internal/prstate"
	"github.com/carlosboeing/crossrev/internal/ui"
)

// refuseOversizedPrompt measures the rendered resolve prompt against the
// resolver harness's hard input limit H before any child starts. A prompt
// past H refuses with resolve_prompt_exceeds_limit: no child could read
// it, so starting one would burn a run that cannot judge. The refusal
// halts the pass with crossrev/halted and names the two ways forward —
// fewer findings in the pass, or a harness with a larger input window.
// A missing budget fails closed: with no window to measure against, the
// leg refuses rather than guessing the prompt fits.
func (l *Leg) refuseOversizedPrompt(ctx context.Context, s *session, marker prstate.Marker, promptBytes []byte) *Result {
	doc, err := l.document()
	if err != nil {
		refused := wrapErr(err)
		return &refused
	}
	limit, ok := doc.InputBudget(s.settings.Harness, s.settings.Model)
	if !ok {
		refused := wrapErr(fmt.Errorf("no input budget for harness %q", s.settings.Harness))
		return &refused
	}
	if len(promptBytes) <= limit.HardBytes {
		return nil
	}
	if l.Log != nil {
		l.Log.Event("budget", fmt.Sprintf("resolve_prompt_exceeds_limit prompt=%d hard=%d harness=%s", len(promptBytes), limit.HardBytes, s.settings.Harness))
	}
	// The halt lands here, beside the refusal, because the claim is
	// already open and invoke's result otherwise reaches no label path:
	// finishEmpty only labels the early outcomes from before the claim.
	_ = l.applyPassLabels(ctx, s, s.pass, policy.PassHalted)
	return &Result{
		Outcome: OutcomeHalted,
		Pass:    s.pass,
		Marker:  marker,
		Message: "resolve_prompt_exceeds_limit",
		Messages: []ui.Line{
			ui.Say(fmt.Sprintf("resolve_prompt_exceeds_limit: the resolve prompt is %d bytes, over the %d-byte hard input limit for %s — no child started.", len(promptBytes), limit.HardBytes, s.settings.Harness)),
			ui.Say("Either resolve fewer findings in this pass, or move the resolver to a harness with a larger input window."),
			ui.Blank(),
		},
	}
}

// hardInputBytes is the resolver harness's hard input limit, or false when it
// cannot be read. The size gate reports that case itself.
func (l *Leg) hardInputBytes(s *session) (int, bool) {
	doc, err := l.document()
	if err != nil {
		return 0, false
	}
	limit, ok := doc.InputBudget(s.settings.Harness, s.settings.Model)
	if !ok {
		return 0, false
	}
	return limit.HardBytes, true
}
