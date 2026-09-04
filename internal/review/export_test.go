package review_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/exec"
)

// The answering model and the whole-run usage record come from a SECOND child:
// `opencode export <sessionID>` reads the local session database and costs no
// model call (lib/adapters/opencode.sh:261-273).
//
// internal/harness had SessionID, ExportSpec and MergeExport and nothing called
// any of them, so every opencode leg recorded model_reported null, no token
// total and no usage record — and the run-details cell named no model.
func TestTheOpencodeSessionExportRuns(t *testing.T) {
	e := newEnv(t)
	writeAppGo(t, e.dir)
	e.runner.script = []exec.Result{
		{ExitCode: 0, Stdout: opencodeStream(issuesPayload(twoFindings))},
		{ExitCode: 0, Stdout: []byte(opencodeExport)},
	}

	req := e.request(t)
	req.HarnessOverride = "opencode"
	leg := e.leg(t)
	got := leg.Run(t.Context(), req)
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if e.runner.calls != 2 {
		t.Fatalf("children started = %d, want 2: the run and its export", e.runner.calls)
	}
	if argv := strings.Join(e.runner.specs[1].Args, " "); !strings.Contains(argv, "export stub-session") {
		t.Fatalf("the second child is not the export: %q", argv)
	}
	if got.Envelope == nil || got.Envelope.ModelReported == nil || *got.Envelope.ModelReported != "stub-model" {
		t.Fatalf("model_reported = %v, want stub-model", got.Envelope)
	}
	// 10 in, 4 cache reads, 2 out. Reasoning is stored beside the total and
	// never added to it (lib/usage.sh).
	if got.Envelope.Tokens == nil || *got.Envelope.Tokens != 16 {
		t.Errorf("tokens = %v, want 16", got.Envelope.Tokens)
	}
	if raw := string(got.Marker.Usage); !strings.Contains(raw, `"reasoning":1`) {
		t.Errorf("the marker does not store reasoning beside the total: %s", raw)
	}
	if raw, _ := got.Marker.MarshalJSON(); !strings.Contains(string(raw), `"model_reported":"stub-model"`) {
		t.Errorf("the marker does not record the answering model: %s", raw)
	}
}

// The export is telemetry: a failure leaves both fields unset and the review
// stands (lib/adapters/opencode.sh:266-273).
func TestAFailedOpencodeExportCostsTheLegNothing(t *testing.T) {
	e := newEnv(t)
	writeAppGo(t, e.dir)
	e.runner.script = []exec.Result{
		{ExitCode: 0, Stdout: opencodeStream(issuesPayload(twoFindings))},
		{ExitCode: 1, Stderr: []byte("no session\n")},
	}

	req := e.request(t)
	req.HarnessOverride = "opencode"
	leg := e.leg(t)
	got := leg.Run(t.Context(), req)
	if got.Err != nil {
		t.Fatalf("a failed export failed the leg: %v", got.Err)
	}
	if got.Envelope == nil || got.Envelope.ModelReported != nil {
		t.Errorf("a failed export set a model anyway: %v", got.Envelope)
	}
}

// opencodeStream is the event stream the opencode CLI prints: one JSON object
// per line, each carrying the session id, with the answer in text events.
func opencodeStream(payload string) []byte {
	var b strings.Builder
	b.WriteString(`{"type":"start","sessionID":"stub-session"}` + "\n")
	quoted, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	b.WriteString(`{"type":"text","sessionID":"stub-session","part":{"text":` + string(quoted) + "}}\n")
	return []byte(b.String())
}

const opencodeExport = `{"info":{"model":{"id":"stub-model"},"tokens":{"input":10,"output":2,"reasoning":1,"cache":{"read":4,"write":0}}}}`

// Prose with no object anywhere is a handoff, not an adapter failure: the
// adapter answers a null payload and run_invoke spends the extra attempt a
// harness that does not constrain its output is granted
// (lib/adapters/opencode.sh:253-258).
//
// The port answered a Go nil, which validate.Findings accepts, so a review that
// contained no review at all was published as an empty one.
func TestOpencodeProseIsRejectedAndRetried(t *testing.T) {
	e := newEnv(t)
	writeAppGo(t, e.dir)
	prose := []byte(`{"type":"text","sessionID":"stub-session","part":{"text":"I cannot produce JSON for this."}}` + "\n")
	e.runner.script = []exec.Result{{ExitCode: 0, Stdout: prose}}

	req := e.request(t)
	req.HarnessOverride = "opencode"
	leg := e.leg(t)
	got := leg.Run(t.Context(), req)
	if got.Err == nil {
		t.Fatal("a review with no object in it was published as an empty review")
	}
	// Two runs and their two exports.
	runs := 0
	for _, spec := range e.runner.specs {
		if !strings.Contains(strings.Join(spec.Args, " "), "export ") {
			runs++
		}
	}
	if runs != 2 {
		t.Errorf("the harness was asked %d time(s), want 2", runs)
	}
	if !strings.Contains(got.Err.Error(), "does not match the schema") {
		t.Errorf("refusal = %q", got.Err)
	}
}
