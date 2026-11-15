package resolve

import (
	"context"
	"fmt"

	"github.com/carlosboeing/crossrev/internal/harness"
	"github.com/carlosboeing/crossrev/internal/policy"
	"github.com/carlosboeing/crossrev/internal/prstate"
	"github.com/carlosboeing/crossrev/internal/ui"
	"github.com/carlosboeing/crossrev/internal/validate"
)

// refuseOversizedPrompt measures the rendered resolve prompt against the
// resolver harness's hard input limit H before any child starts. A prompt
// past H refuses with resolve_prompt_exceeds_limit: no child could read
// it, so starting one would burn a run that cannot judge. The refusal
// halts the pass with crossrev/halted and names the two ways forward —
// less context, or a harness/model with a larger input budget.
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
	inputBytes := harness.InputBytes(s.settings.Harness, harness.Invocation{
		Prompt: harness.File{Text: string(promptBytes)},
		Schema: harness.File{Path: "schema", Text: string(validate.ResolveSchema())},
		Write:  true,
	})
	if inputBytes <= limit.HardBytes {
		return nil
	}
	if l.Log != nil {
		l.Log.Event("budget", fmt.Sprintf("resolve_prompt_exceeds_limit prompt=%d hard=%d harness=%s", inputBytes, limit.HardBytes, s.settings.Harness))
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
			ui.Say(fmt.Sprintf("resolve_prompt_exceeds_limit: the resolve prompt is %d bytes, over the %d-byte hard input limit for %s — no child started.", inputBytes, limit.HardBytes, s.settings.Harness)),
			ui.Say("Reduce findings or other prompt context, or select a harness/model with a larger input budget. An argv transport can impose its own cap."),
			ui.Blank(),
		},
	}
}
