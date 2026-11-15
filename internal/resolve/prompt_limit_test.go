package resolve

import (
	"encoding/json"
	"fmt"
	"github.com/carlosboeing/crossrev/internal/harness"
	"strings"
	"testing"
)

// oversizedFindings is one finding whose text alone overflows the default
// resolver's hard input limit: the rendered prompt cannot fit H whatever
// the diff carries beside it.
func oversizedFindings() json.RawMessage {
	bulk := strings.Repeat("a failed request reads as success. ", 25000)
	return json.RawMessage(`[
  {"id":"` + testFinding + `","path":"app.ts","line":2,"side":"RIGHT","severity":"high","category":"correctness","pre_existing":false,"title":"nil deref","why":"` + bulk + `","fix":"check"}
]`)
}

// TestResolvePromptOverHardLimitIsRefusedBeforeAnyChildStarts pins the
// resolve size gate: a rendered prompt past the resolver harness's hard
// limit refuses with resolve_prompt_exceeds_limit before any child
// starts, halting with crossrev/halted and naming the two ways forward.
func TestResolvePromptOverHardLimitIsRefusedBeforeAnyChildStarts(t *testing.T) {
	e := setup(t)
	e.addReview(t, oversizedFindings(), "issues-remain")
	got := e.run(t)
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if got.Outcome != OutcomeHalted {
		t.Fatalf("Outcome = %q, want halted (the prompt fits no child)", got.Outcome)
	}
	if got.Message != "resolve_prompt_exceeds_limit" {
		t.Errorf("Message = %q, want resolve_prompt_exceeds_limit", got.Message)
	}
	if len(e.adapter.invs) != 0 {
		t.Fatalf("adapter invocations = %d, want 0 (refused before any child starts)", len(e.adapter.invs))
	}
	final := map[string]bool{}
	for _, label := range e.forge.addedLabels {
		final[label] = true
	}
	for _, label := range e.forge.removedLabels {
		delete(final, label)
	}
	if !final["crossrev/halted"] {
		t.Errorf("final labels = %v, want crossrev/halted standing", final)
	}
	var ways int
	for _, line := range got.Messages {
		if strings.Contains(line.Text, "Reduce findings or other prompt context") {
			ways++
		}
		if strings.Contains(line.Text, "larger input budget") {
			ways++
		}
	}
	if ways != 2 {
		t.Errorf("the refusal names %d of the 2 ways forward: %v", ways, got.Messages)
	}
}

// TestResolvePromptUnderHardLimitStartsTheChild is the gate's other side:
// an ordinary prompt measures under H and reaches the resolver.
func TestResolvePromptUnderHardLimitStartsTheChild(t *testing.T) {
	e := setup(t)
	e.addReview(t, defaultFindings(), "issues-remain")
	got := e.run(t)
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if len(e.adapter.invs) == 0 {
		t.Fatal("adapter was not invoked for a prompt under the hard limit")
	}
	if got.Message == "resolve_prompt_exceeds_limit" {
		t.Fatal("an ordinary prompt tripped the size gate")
	}
}

func TestResolveBudgetIncludesSchema(t *testing.T) {
	baseline := setup(t)
	baseline.addReview(t, defaultFindings(), "issues-remain")
	if got := baseline.run(t); got.Err != nil {
		t.Fatal(got.Err)
	}
	if len(baseline.adapter.invs) == 0 {
		t.Fatal("baseline never invoked")
	}
	bare := len(baseline.adapter.invs[0].Prompt.Text)
	e := setup(t)
	e.addReview(t, defaultFindings(), "issues-remain")
	window := int(float64(bare+100)/3.12) + 1
	raw := strings.Replace(string(harness.DescriptorJSON()), `"window_tokens": 200000`, fmt.Sprintf(`"window_tokens": %d`, window), 1)
	var err error
	e.doc, err = harness.Load([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	got := e.run(t)
	if got.Message != "resolve_prompt_exceeds_limit" || len(e.adapter.invs) != 0 {
		t.Fatalf("schema overflow reached child: outcome=%s message=%s calls=%d", got.Outcome, got.Message, len(e.adapter.invs))
	}
}

func TestResolveRefusesInheritedGrokMaxBeforeModelInvocation(t *testing.T) {
	e := setup(t)
	e.git.show = map[string][]byte{e.base.SHA() + ":.github/crossrev.yml": []byte(
		"version: 2\nresolver:\n  harness: opencode\n  model: opencode-go/deepseek-v4.1-flash\n  effort: max\n")}
	e.addReview(t, defaultFindings(), "issues-remain")
	got := e.runReq(t, Request{PR: 42, Repo: e.slug, Trigger: TriggerHuman, Harness: "grok"})
	if got.Err == nil || !strings.Contains(got.Err.Error(), `unsupported effort "max" for grok`) {
		t.Fatalf("wrong refusal: %v", got.Err)
	}
	if len(e.adapter.invs) != 0 {
		t.Fatal("invalid effort started a model")
	}
	if len(*e.git.worktrees) != 0 {
		t.Fatal("invalid effort created a resolver worktree")
	}
}
