package review_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/exec"
	"github.com/carlosboeing/crossrev/internal/harness"
	"github.com/carlosboeing/crossrev/internal/prstate"
	"github.com/carlosboeing/crossrev/internal/prstate/storetest"
	"github.com/carlosboeing/crossrev/internal/ui"
)

// A broken served tool degrades where the policy says degrade: the pass
// still runs on the supplied prompt, the reason travels in the pass
// comment, and the marker carries the envelope.
func TestReadsDegradeOnABrokenTool(t *testing.T) {
	e := newEnv(t)
	writeRequiredHead(e, "a.go", "package a\n")
	e.runner.serveErr = errors.New("connection refused")
	e.runner.script = []exec.Result{
		{ExitCode: 0, Stdout: claudeStdout(batchAnswerFor(t, []string{"a.go"}))},
	}

	got := runLeg(t, e, e.request(t))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	// The reason reaches the posted summary, not only the terminal
	// warning: a reader of the pull request sees the same sentence.
	posted := strings.Join(append(append([]string{}, e.forge.created...), e.forge.edits...), "\n")
	if !strings.Contains(posted, "Served reads degraded (self_test_failed): this review judged the supplied content alone.") {
		t.Errorf("no degrade line in the posted pass comment")
	}
	if len(got.Marker.Reads) == 0 {
		t.Fatal("the marker carries no reads envelope")
	}
	envelope, err := prstate.DecodeReadsEnvelope(got.Marker.Reads)
	if err != nil {
		t.Fatalf("decoding the marker envelope: %v", err)
	}
	if envelope.DeclaredMode != "served" || envelope.EffectiveMode != "supplied" || envelope.Reason != "self_test_failed" {
		t.Errorf("envelope = %+v, want served-to-supplied with self_test_failed", envelope)
	}
}

// A call that fails its self-test and degrades to supplied is sent the
// supplied reads block: the prompt renders before the self-test runs, so
// the fallback rewrites the block rather than telling the reviewer it has
// a tool the child was not granted.
func TestDegradedCallIsSentTheSuppliedReadsBlock(t *testing.T) {
	e := newEnv(t)
	writeRequiredHead(e, "a.go", "package a\n")
	e.runner.serveErr = errors.New("connection refused")
	var prompts []string
	e.runner.onSpec = func(spec exec.Spec) {
		prompts = append(prompts, specPrompt(spec))
	}
	e.runner.script = []exec.Result{
		{ExitCode: 0, Stdout: claudeStdout(batchAnswerFor(t, []string{"a.go"}))},
	}

	got := runLeg(t, e, e.request(t))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if len(prompts) == 0 {
		t.Fatal("the harness received no prompt")
	}
	for _, p := range prompts {
		if strings.Contains(p, "You have one file-reading tool") {
			t.Errorf("a degraded prompt names the served tool")
		}
		if !strings.Contains(p, "You have no file-reading tool") {
			t.Errorf("a degraded prompt carries no supplied reads block")
		}
	}
}

// A healthy served call is sent the served reads block: the prompt names
// the tool the child is granted, so the reviewer reads through it rather
// than judging the supplied content alone.
func TestServedCallIsSentTheServedReadsBlock(t *testing.T) {
	e := newEnv(t)
	writeRequiredHead(e, "a.go", "package a\n")
	var prompts []string
	e.runner.onSpec = func(spec exec.Spec) {
		prompts = append(prompts, specPrompt(spec))
		// A healthy served child reaches the server, so the call the
		// prompt names stays healthy at the post-call check.
		serveChildSession(t, spec,
			`{"event":"start"}`,
			`{"event":"initialize"}`,
			`{"event":"tools_list"}`,
			`{"event":"end"}`,
		)
	}
	e.runner.script = []exec.Result{
		{ExitCode: 0, Stdout: claudeStdout(batchAnswerFor(t, []string{"a.go"}))},
	}

	got := runLeg(t, e, e.request(t))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if len(prompts) == 0 {
		t.Fatal("the harness received no prompt")
	}
	for _, p := range prompts {
		if !strings.Contains(p, "You have one file-reading tool") {
			t.Errorf("a served prompt names no served tool")
		}
	}
}

// A broken served tool halts where the policy says halt: the call
// publishes nothing and the failure names reads_unavailable.
func TestReadsHaltOnABrokenTool(t *testing.T) {
	e := newEnv(t)
	writeRequiredHead(e, "a.go", "package a\n")
	e.cfg = mustConfig(t, "version: 2\npolicy:\n  on_reads_unavailable: halt\n")
	e.runner.serveErr = errors.New("connection refused")
	e.runner.script = []exec.Result{
		{ExitCode: 0, Stdout: claudeStdout(batchAnswerFor(t, []string{"a.go"}))},
	}

	got := runLeg(t, e, e.request(t))
	if got.Err == nil {
		t.Fatal("Run: want the halt to stop the leg")
	}
	if !strings.Contains(got.Err.Error(), "reads_unavailable") {
		t.Errorf("err = %v, want the reads_unavailable name", got.Err)
	}
	if e.runner.calls != 0 {
		t.Errorf("harness calls = %d, want 0 (halted before any child)", e.runner.calls)
	}
}

// With nothing to byte-check against, the self-test is skipped rather than
// failed: a pass over an empty file listing runs clean, with no warning
// and no envelope on the marker.
func TestReadsSkipWithNothingToByteCheck(t *testing.T) {
	e := newEnv(t)
	writeAppGo(t, e.dir)
	// The child reaches the server but reads nothing: the handshake lands
	// in the log with no calls behind it, so the skipped self-test stays
	// clean.
	e.runner.onSpec = func(spec exec.Spec) {
		serveChildSession(t, spec,
			`{"event":"start"}`,
			`{"event":"initialize"}`,
			`{"event":"tools_list"}`,
			`{"event":"end"}`,
		)
	}
	e.runner.script = []exec.Result{{ExitCode: 0, Stdout: claudeStdout(`{"verdict":"converged","findings":[]}`)}}

	got := runLeg(t, e, e.request(t))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	for _, line := range ui.Texts(got.Messages) {
		if strings.Contains(line, "Served reads degraded") {
			t.Errorf("a skipped self-test degrades: %q", line)
		}
	}
	if len(got.Marker.Reads) != 0 {
		t.Errorf("the marker carries a reads envelope with no served reads: %s", got.Marker.Reads)
	}
}

// A required file past the server's line cap does not fail the self-test:
// the server would cut the read at DefaultMaxResultLines, so there is
// nothing whole to byte-check against. The pass runs clean, with no warning
// and no envelope on the marker.
func TestReadsSkipWhenTheOnlyCandidateIsPastTheLineCap(t *testing.T) {
	e := newEnv(t)
	long := strings.Repeat("x\n", 500)
	if len(long) >= 4096 {
		t.Fatalf("the fixture is %d bytes, want it under the probe byte cap", len(long))
	}
	writeRequiredHead(e, "long.go", long)
	// The child reaches the server but reads nothing: the handshake lands
	// in the log with no calls behind it, so the skipped self-test stays
	// clean.
	e.runner.onSpec = func(spec exec.Spec) {
		serveChildSession(t, spec,
			`{"event":"start"}`,
			`{"event":"initialize"}`,
			`{"event":"tools_list"}`,
			`{"event":"end"}`,
		)
	}
	e.runner.script = []exec.Result{{ExitCode: 0, Stdout: claudeStdout(batchAnswerFor(t, []string{"long.go"}))}}

	got := runLeg(t, e, e.request(t))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	for _, line := range ui.Texts(got.Messages) {
		if strings.Contains(line, "Served reads degraded") {
			t.Errorf("a skipped long-file self-test degrades: %q", line)
		}
	}
	if len(got.Marker.Reads) != 0 {
		t.Errorf("the marker carries a reads envelope with no served reads: %s", got.Marker.Reads)
	}
}

// A grok review leg at a moved pin is refused with
// review_isolation_unverified before any child starts: the tripwire block
// is verified on the pinned version, or the pin moves.
func TestUnverifiedGrokReviewIsRefused(t *testing.T) {
	e := newEnv(t)
	writeRequiredHead(e, "a.go", "package a\n")
	raw := harness.DescriptorJSON()
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("decoding the descriptor: %v", err)
	}
	for _, entry := range document["harnesses"].([]any) {
		if entry.(map[string]any)["name"] == "grok" {
			install := entry.(map[string]any)["install"].(map[string]any)
			install["pinned_version"] = "9.9.9"
			install["command"] = "install 9.9.9"
		}
	}
	mutated, _ := json.Marshal(document)
	doc, err := harness.Load(mutated)
	if err != nil {
		t.Fatalf("loading the moved pin: %v", err)
	}
	e.doc = doc
	req := e.request(t)
	req.HarnessOverride = "grok"
	e.runner.script = []exec.Result{
		{ExitCode: 0, Stdout: claudeStdout(batchAnswerFor(t, []string{"a.go"}))},
	}

	got := runLeg(t, e, req)
	if got.Err == nil {
		t.Fatal("Run: want the unverified grok review refused")
	}
	if !strings.Contains(got.Err.Error(), "review_isolation_unverified") {
		t.Errorf("err = %v, want the review_isolation_unverified name", got.Err)
	}
	if e.runner.calls != 0 {
		t.Errorf("harness calls = %d, want 0 (refused before any child)", e.runner.calls)
	}
}

// A tripwire halt still records the envelope-only marker entry the
// Marker.Reads comment promises: the halted call publishes nothing, and
// the envelope is the record the call happened.
func TestTripwireHaltRecordsTheReadsEnvelope(t *testing.T) {
	e := newEnv(t)
	writeRequiredHead(e, "a.go", "package a\n")
	e.runner.script = []exec.Result{{
		ExitCode: 0,
		Stdout:   []byte("{\"type\":\"assistant\",\"message\":{\"content\":[{\"type\":\"tool_use\",\"name\":\"Bash\",\"input\":{\"command\":\"id\"}}]}}\n" + string(claudeStdout(batchAnswerFor(t, []string{"a.go"})))),
	}}

	got := runLeg(t, e, e.request(t))
	if got.Err == nil {
		t.Fatal("Run: want the tripwire to halt the leg")
	}
	if len(got.Marker.Reads) == 0 {
		t.Fatal("the halted marker carries no reads envelope")
	}
	envelope, err := prstate.DecodeReadsEnvelope(got.Marker.Reads)
	if err != nil {
		t.Fatalf("decoding the halted marker envelope: %v", err)
	}
	if envelope.Reason != "review_leg_ran_command" {
		t.Errorf("envelope reason = %q, want review_leg_ran_command", envelope.Reason)
	}
}

// A self-test halt still records the envelope-only marker entry: the leg
// stops before any child starts, and the envelope is the record the call
// was attempted.
func TestSelfTestHaltRecordsTheReadsEnvelope(t *testing.T) {
	e := newEnv(t)
	writeRequiredHead(e, "a.go", "package a\n")
	e.cfg = mustConfig(t, "version: 2\npolicy:\n  on_reads_unavailable: halt\n")
	e.runner.serveErr = errors.New("connection refused")

	got := runLeg(t, e, e.request(t))
	if got.Err == nil {
		t.Fatal("Run: want the halt to stop the leg")
	}
	if len(got.Marker.Reads) == 0 {
		t.Fatal("the halted marker carries no reads envelope")
	}
	envelope, err := prstate.DecodeReadsEnvelope(got.Marker.Reads)
	if err != nil {
		t.Fatalf("decoding the halted marker envelope: %v", err)
	}
	if envelope.DeclaredMode != "served" || envelope.EffectiveMode != "served" || envelope.Reason != "self_test_failed" {
		t.Errorf("envelope = %+v, want a served self_test_failed halt", envelope)
	}
}

// serveChildSession appends one harness child's served session to the call
// log the child's MCP config names. The fake harness child never speaks to
// a read server, so without this the post-call check always assesses an
// empty log. Specs without an MCP config (supplied legs) are left alone.
func serveChildSession(t *testing.T, spec exec.Spec, lines ...string) {
	t.Helper()
	var mcpPath string
	for at := 0; at+1 < len(spec.Args); at++ {
		if spec.Args[at] == "--mcp-config" {
			mcpPath = spec.Args[at+1]
		}
	}
	if mcpPath == "" {
		return
	}
	raw, err := os.ReadFile(mcpPath)
	if err != nil {
		t.Fatalf("reading the child MCP config: %v", err)
	}
	var document struct {
		Servers map[string]struct {
			Args []string `json:"args"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("decoding the child MCP config: %v", err)
	}
	var logPath string
	args := document.Servers["crossrev"].Args
	for at := 0; at+1 < len(args); at++ {
		if args[at] == "--log" {
			logPath = args[at+1]
		}
	}
	if logPath == "" {
		t.Fatal("the child MCP config names no read-server log")
	}
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("opening the served call log: %v", err)
	}
	defer func() { _ = f.Close() }()
	for _, line := range lines {
		if _, err := f.WriteString(line + "\n"); err != nil {
			t.Fatalf("writing the served call log: %v", err)
		}
	}
}

// A harness child's served session reaches the post-call check: the
// handshake and reads in the call log land in the marker envelope, and the
// session is archived beside the transcripts. The archive used to drain the
// scratch log before the assessment read it, so a healthy pass recorded
// nothing.
func TestServedChildSessionReadsLandInTheEnvelope(t *testing.T) {
	e := newEnv(t)
	writeRequiredHead(e, "a.go", "package a\n")
	e.runner.onSpec = func(spec exec.Spec) {
		serveChildSession(t, spec,
			`{"event":"start"}`,
			`{"event":"initialize"}`,
			`{"event":"tools_list"}`,
			`{"event":"read","payload":{"path":"a.go","revision":"head","start_line":1,"end_line":2,"bytes":11}}`,
			`{"event":"end"}`,
		)
	}
	e.runner.script = []exec.Result{
		{ExitCode: 0, Stdout: claudeStdout(batchAnswerFor(t, []string{"a.go"}))},
	}

	got := runLeg(t, e, e.request(t))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	for _, line := range ui.Texts(got.Messages) {
		if strings.Contains(line, "Served reads degraded") {
			t.Errorf("a healthy served session degrades: %q", line)
		}
	}
	if len(got.Marker.Reads) == 0 {
		t.Fatal("the marker carries no reads envelope for a served session")
	}
	envelope, err := prstate.DecodeReadsEnvelope(got.Marker.Reads)
	if err != nil {
		t.Fatalf("decoding the marker envelope: %v", err)
	}
	if envelope.DeclaredMode != "served" || envelope.EffectiveMode != "served" || envelope.Reason != "" {
		t.Errorf("envelope = %+v, want a healthy served session", envelope)
	}
	if envelope.Calls != 1 || envelope.Reads != 1 || envelope.Bytes != 11 || envelope.Refused != 0 {
		t.Errorf("envelope = %+v, want one read of 11 bytes", envelope)
	}
	archived, err := os.ReadFile(filepath.Join(e.dir, "run", "reads.call-1.jsonl"))
	if err != nil {
		t.Fatalf("the run directory holds no reads.call-1.jsonl: %v", err)
	}
	if !strings.Contains(string(archived), `"event":"tools_list"`) {
		t.Errorf("reads.call-1.jsonl carries no child session: %q", archived)
	}
}

// A refused call in the child's served session degrades where the policy
// says degrade: the reason travels in the pass comment and the marker
// carries the envelope.
func TestServedChildSessionRefusalsDegrade(t *testing.T) {
	e := newEnv(t)
	writeRequiredHead(e, "a.go", "package a\n")
	e.runner.onSpec = func(spec exec.Spec) {
		serveChildSession(t, spec,
			`{"event":"start"}`,
			`{"event":"initialize"}`,
			`{"event":"tools_list"}`,
			`{"event":"read","payload":{"path":"a.go","revision":"head","start_line":1,"end_line":2,"bytes":11}}`,
			`{"event":"refused","payload":{"reason":"not_found"}}`,
			`{"event":"end"}`,
		)
	}
	e.runner.script = []exec.Result{
		{ExitCode: 0, Stdout: claudeStdout(batchAnswerFor(t, []string{"a.go"}))},
	}

	got := runLeg(t, e, e.request(t))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	joined := strings.Join(ui.Texts(got.Messages), "\n")
	if !strings.Contains(joined, "Served reads degraded (calls_refused:") {
		t.Errorf("no calls_refused warning in the pass comment: %q", ui.Texts(got.Messages))
	}
	// The reviewer also read through the tool, so the warning must not
	// claim the review judged the supplied content alone: it names the
	// refused and served counts instead.
	if !strings.Contains(joined, "1 refused, 1 served") {
		t.Errorf("the calls_refused warning names no refused and served counts: %q", ui.Texts(got.Messages))
	}
	if strings.Contains(joined, "judged the supplied content alone") {
		t.Errorf("the calls_refused warning claims a served review judged the supplied content alone: %q", ui.Texts(got.Messages))
	}
	if len(got.Marker.Reads) == 0 {
		t.Fatal("the marker carries no reads envelope")
	}
	envelope, err := prstate.DecodeReadsEnvelope(got.Marker.Reads)
	if err != nil {
		t.Fatalf("decoding the marker envelope: %v", err)
	}
	if envelope.Reason != "calls_refused" || envelope.Refused != 1 {
		t.Errorf("envelope = %+v, want one refused call", envelope)
	}
}

// A refused call in the child's served session halts where the policy says
// halt: the call publishes nothing and the failure names reads_unavailable,
// with the envelope-only entry as the record the call happened.
func TestServedChildSessionRefusalsHalt(t *testing.T) {
	e := newEnv(t)
	writeRequiredHead(e, "a.go", "package a\n")
	e.cfg = mustConfig(t, "version: 2\npolicy:\n  on_reads_unavailable: halt\n")
	e.runner.onSpec = func(spec exec.Spec) {
		serveChildSession(t, spec,
			`{"event":"start"}`,
			`{"event":"initialize"}`,
			`{"event":"tools_list"}`,
			`{"event":"refused","payload":{"reason":"not_found"}}`,
			`{"event":"end"}`,
		)
	}
	e.runner.script = []exec.Result{
		{ExitCode: 0, Stdout: claudeStdout(batchAnswerFor(t, []string{"a.go"}))},
	}

	got := runLeg(t, e, e.request(t))
	if got.Err == nil {
		t.Fatal("Run: want the halt to stop the leg")
	}
	if !strings.Contains(got.Err.Error(), "reads_unavailable") {
		t.Errorf("err = %v, want the reads_unavailable name", got.Err)
	}
	if len(got.Marker.Reads) == 0 {
		t.Fatal("the halted marker carries no reads envelope")
	}
	envelope, err := prstate.DecodeReadsEnvelope(got.Marker.Reads)
	if err != nil {
		t.Fatalf("decoding the halted marker envelope: %v", err)
	}
	if envelope.Reason != "calls_refused" {
		t.Errorf("envelope reason = %q, want calls_refused", envelope.Reason)
	}
}

// A served child that never reaches the server leaves an empty call log,
// and the post-call handshake catches it: the call degrades where the
// policy says degrade, with the reason in the pass comment and the marker
// envelope.
func TestServedChildWithNoSessionDegrades(t *testing.T) {
	e := newEnv(t)
	writeRequiredHead(e, "a.go", "package a\n")
	// No serveChildSession: the fake child never speaks to the read
	// server, so the call log stays empty the way a failed MCP startup
	// leaves it.
	e.runner.script = []exec.Result{
		{ExitCode: 0, Stdout: claudeStdout(batchAnswerFor(t, []string{"a.go"}))},
	}

	got := runLeg(t, e, e.request(t))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	// The reason reaches the posted summary, not only the terminal
	// warning: a reader of the pull request sees the same sentence.
	posted := strings.Join(append(append([]string{}, e.forge.created...), e.forge.edits...), "\n")
	if !strings.Contains(posted, "Served reads degraded (missing_handshake): this review judged the supplied content alone.") {
		t.Errorf("no missing_handshake line in the posted pass comment")
	}
	if len(got.Marker.Reads) == 0 {
		t.Fatal("the marker carries no reads envelope")
	}
	envelope, err := prstate.DecodeReadsEnvelope(got.Marker.Reads)
	if err != nil {
		t.Fatalf("decoding the marker envelope: %v", err)
	}
	if envelope.Reason != "missing_handshake" {
		t.Errorf("envelope reason = %q, want missing_handshake", envelope.Reason)
	}
}

// A served child that never reaches the server halts where the policy says
// halt: the call publishes nothing and the failure names
// reads_unavailable, with the envelope-only entry as the record the call
// happened.
func TestServedChildWithNoSessionHalts(t *testing.T) {
	e := newEnv(t)
	writeRequiredHead(e, "a.go", "package a\n")
	e.cfg = mustConfig(t, "version: 2\npolicy:\n  on_reads_unavailable: halt\n")
	e.runner.script = []exec.Result{
		{ExitCode: 0, Stdout: claudeStdout(batchAnswerFor(t, []string{"a.go"}))},
	}

	got := runLeg(t, e, e.request(t))
	if got.Err == nil {
		t.Fatal("Run: want the halt to stop the leg")
	}
	if !strings.Contains(got.Err.Error(), "reads_unavailable") {
		t.Errorf("err = %v, want the reads_unavailable name", got.Err)
	}
	if len(got.Marker.Reads) == 0 {
		t.Fatal("the halted marker carries no reads envelope")
	}
	envelope, err := prstate.DecodeReadsEnvelope(got.Marker.Reads)
	if err != nil {
		t.Fatalf("decoding the halted marker envelope: %v", err)
	}
	if envelope.Reason != "missing_handshake" {
		t.Errorf("envelope reason = %q, want missing_handshake", envelope.Reason)
	}
}

// A symlink first candidate does not fail the self-test: the server
// refuses mode 120000 for what the path is, so the leg byte-checks the
// next candidate and the served call runs clean.
func TestSelfTestSkipsASymlinkCandidate(t *testing.T) {
	e := newEnv(t)
	writeRequiredHead(e, "a-link.go", "b.go\n")
	writeRequiredHead(e, "b.go", "package b\n")
	if e.vcs.symlinks == nil {
		e.vcs.symlinks = map[string]bool{}
	}
	e.vcs.symlinks["a-link.go"] = true
	e.runner.onSpec = func(spec exec.Spec) {
		serveChildSession(t, spec,
			`{"event":"start"}`,
			`{"event":"initialize"}`,
			`{"event":"tools_list"}`,
			`{"event":"end"}`,
		)
	}
	e.runner.script = []exec.Result{
		{ExitCode: 0, Stdout: claudeStdout(batchAnswerFor(t, []string{"a-link.go", "b.go"}))},
	}

	got := runLeg(t, e, e.request(t))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	for _, line := range ui.Texts(got.Messages) {
		if strings.Contains(line, "Served reads degraded") {
			t.Errorf("a skipped symlink candidate degrades: %q", line)
		}
	}
	if len(got.Marker.Reads) != 0 {
		t.Errorf("the marker carries a reads envelope after a clean served call: %s", got.Marker.Reads)
	}
}

// A pass whose only candidate is a symlink skips the self-test rather than
// failing it: there is nothing to byte-check against, the served call runs,
// and the marker stays clean.
func TestSelfTestSkipsWhenEveryCandidateIsASymlink(t *testing.T) {
	e := newEnv(t)
	writeRequiredHead(e, "a-link.go", "b.go\n")
	if e.vcs.symlinks == nil {
		e.vcs.symlinks = map[string]bool{}
	}
	e.vcs.symlinks["a-link.go"] = true
	e.runner.onSpec = func(spec exec.Spec) {
		serveChildSession(t, spec,
			`{"event":"start"}`,
			`{"event":"initialize"}`,
			`{"event":"tools_list"}`,
			`{"event":"end"}`,
		)
	}
	e.runner.script = []exec.Result{
		{ExitCode: 0, Stdout: claudeStdout(batchAnswerFor(t, []string{"a-link.go"}))},
	}

	got := runLeg(t, e, e.request(t))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	for _, line := range ui.Texts(got.Messages) {
		if strings.Contains(line, "Served reads degraded") {
			t.Errorf("an all-symlink pass degrades: %q", line)
		}
	}
	if len(got.Marker.Reads) != 0 {
		t.Errorf("the marker carries a reads envelope after a clean served call: %s", got.Marker.Reads)
	}
}

// A command event publishes nothing: no findings reach the pull request,
// no generation reaches the ledger, and the command text reaches the run
// log only — named there, absent from the error, the marker, the pass
// comment and every comment the leg wrote. The frozen path publishes no
// generation before the call, so an empty ledger proves the halted call
// published nothing.
func TestReviewTripwirePublishesNothing(t *testing.T) {
	const sentinel = "touch /tmp/crossrev-tripwire-sentinel-9f31aa-probe"
	for _, policy := range []string{"", "version: 2\npolicy:\n  on_reads_unavailable: halt\n"} {
		e := newEnv(t)
		writeAppGo(t, e.dir)
		if policy != "" {
			e.cfg = mustConfig(t, policy)
		}
		store := storetest.NewFakeStore()
		e.forge.store = store
		e.runner.script = []exec.Result{{
			ExitCode: 0,
			Stdout:   []byte(`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":` + strconv.Quote(sentinel) + `}}]}}` + "\n" + string(claudeStdout(`{"verdict":"converged","findings":[]}`))),
		}}

		got := runLeg(t, e, e.request(t))
		if got.Err == nil {
			t.Fatalf("policy %q: want the tripwire to halt the leg", policy)
		}
		if !strings.Contains(got.Err.Error(), "review_leg_ran_command") {
			t.Fatalf("policy %q: err = %v, want the review_leg_ran_command name", policy, got.Err)
		}
		if strings.Contains(got.Err.Error(), sentinel) {
			t.Errorf("policy %q: the error carries the command text: %v", policy, got.Err)
		}
		encoded, err := got.Marker.Encode()
		if err != nil {
			t.Fatalf("policy %q: encoding the marker: %v", policy, err)
		}
		if strings.Contains(encoded, sentinel) {
			t.Errorf("policy %q: the marker carries the command text", policy)
		}
		for _, line := range ui.Texts(got.Messages) {
			if strings.Contains(line, sentinel) {
				t.Errorf("policy %q: the pass comment carries the command text: %q", policy, line)
			}
		}
		for _, body := range append(append([]string{}, e.forge.created...), e.forge.edits...) {
			if strings.Contains(body, sentinel) {
				t.Errorf("policy %q: a pull request comment carries the command text", policy)
			}
		}
		log := readRunLog(t, e)
		if !strings.Contains(log, "review command:") || !strings.Contains(log, sentinel) {
			t.Errorf("policy %q: run.log carries no redacted tripwire record:\n%s", policy, log)
		}
		if len(e.forge.reviewPosted) != 0 || len(e.forge.filePosted) != 0 {
			t.Errorf("policy %q: the halted call posted findings: %+v %+v", policy, e.forge.reviewPosted, e.forge.filePosted)
		}
		if len(store.Published()) != 0 {
			t.Errorf("policy %q: the halted call published %d generations", policy, len(store.Published()))
		}
	}
}

// One Leg drives every pass the cycle asks it for, so a pass must not fold
// an earlier pass into its own ledger: a halted first pass followed by a
// healthy second pass on the same Leg records only the second pass's
// counts, with no reason.
func TestSecondRunOnOneLegCarriesOnlyItsOwnReads(t *testing.T) {
	e := newEnv(t)
	writeRequiredHead(e, "a.go", "package a\n")
	e.cfg = mustConfig(t, "version: 2\npolicy:\n  on_reads_unavailable: halt\n")
	e.runner.serveErr = errors.New("connection refused")
	e.runner.script = []exec.Result{
		{ExitCode: 0, Stdout: claudeStdout(batchAnswerFor(t, []string{"a.go"}))},
	}
	leg := e.leg(t)
	req := e.request(t)
	first := leg.Run(context.Background(), req)
	if first.Err == nil {
		t.Fatal("first Run: want the self-test halt to stop the leg")
	}
	if len(first.Marker.Reads) == 0 {
		t.Fatal("the halted marker carries no reads envelope")
	}

	// The tool recovers; the same leg drives the next pass.
	e.runner.serveErr = nil
	e.runner.onSpec = func(spec exec.Spec) {
		serveChildSession(t, spec,
			`{"event":"start"}`,
			`{"event":"initialize"}`,
			`{"event":"tools_list"}`,
			`{"event":"read","payload":{"path":"a.go","revision":"head","start_line":1,"end_line":2,"bytes":11}}`,
			`{"event":"end"}`,
		)
	}
	second := leg.Run(context.Background(), req)
	if second.Err != nil {
		t.Fatalf("second Run: %v", second.Err)
	}
	if len(second.Marker.Reads) == 0 {
		t.Fatal("the second marker carries no reads envelope")
	}
	envelope, err := prstate.DecodeReadsEnvelope(second.Marker.Reads)
	if err != nil {
		t.Fatalf("decoding the second marker envelope: %v", err)
	}
	if envelope.Reason != "" {
		t.Errorf("envelope reason = %q, want no reason on a healthy second pass", envelope.Reason)
	}
	if envelope.Calls != 1 || envelope.Reads != 1 || envelope.Bytes != 11 || envelope.Refused != 0 {
		t.Errorf("envelope = %+v, want the second pass's single read of 11 bytes", envelope)
	}
}

// A command event on the review leg halts with review_leg_ran_command and
// publishes nothing, under either policy.
// A harness failure before any answer is a harness failure, not unavailable
// reads: the post-call check runs only on a call that answered. A transient
// 503 on the first attempt followed by a healthy served retry ends served
// with no reason — the failed attempt's empty log never becomes
// missing_handshake, and never rides readsReason into the retry to degrade
// a healthy answer.
func TestTransientFailureThenHealthyRetryStaysServed(t *testing.T) {
	for _, policy := range []string{"", "version: 2\npolicy:\n  on_reads_unavailable: halt\n"} {
		e := newEnv(t)
		writeRequiredHead(e, "a.go", "package a\n")
		if policy != "" {
			e.cfg = mustConfig(t, policy)
		}
		var children int
		e.runner.onSpec = func(spec exec.Spec) {
			children++
			// The failed first attempt never reaches the server, so only
			// the retry logs a child session.
			if children == 2 {
				serveChildSession(t, spec,
					`{"event":"start"}`,
					`{"event":"initialize"}`,
					`{"event":"tools_list"}`,
					`{"event":"read","payload":{"path":"a.go","revision":"head","start_line":1,"end_line":2,"bytes":11}}`,
					`{"event":"end"}`,
				)
			}
		}
		e.runner.script = []exec.Result{
			{ExitCode: 1, Stderr: []byte("UNAVAILABLE (code 503): overloaded")},
			{ExitCode: 0, Stdout: claudeStdout(batchAnswerFor(t, []string{"a.go"}))},
		}

		got := runLeg(t, e, e.request(t))
		if got.Err != nil {
			t.Fatalf("policy %q: Run: %v", policy, got.Err)
		}
		if e.runner.calls != 2 {
			t.Fatalf("policy %q: the harness was invoked %d time(s), want the transient retry", policy, e.runner.calls)
		}
		for _, line := range ui.Texts(got.Messages) {
			if strings.Contains(line, "Served reads degraded") {
				t.Errorf("policy %q: a retried served session degrades: %q", policy, line)
			}
		}
		if len(got.Marker.Reads) == 0 {
			t.Fatalf("policy %q: the marker carries no reads envelope for a served session", policy)
		}
		envelope, err := prstate.DecodeReadsEnvelope(got.Marker.Reads)
		if err != nil {
			t.Fatalf("policy %q: decoding the marker envelope: %v", policy, err)
		}
		if envelope.EffectiveMode != "served" || envelope.Reason != "" {
			t.Errorf("policy %q: envelope = %+v, want a healthy served session with no reason", policy, envelope)
		}
	}
}

// A failed harness under halt reports the harness failure, not
// reads_unavailable: an authentication failure never reaches the read
// server, so there is no handshake to miss and nothing to degrade or halt
// on.
func TestFailedHarnessUnderHaltReportsTheHarnessFailure(t *testing.T) {
	e := newEnv(t)
	writeRequiredHead(e, "a.go", "package a\n")
	e.cfg = mustConfig(t, "version: 2\npolicy:\n  on_reads_unavailable: halt\n")
	e.runner.script = []exec.Result{
		{ExitCode: 1, Stderr: []byte("Invalid API key")},
	}

	got := runLeg(t, e, e.request(t))
	if got.Err == nil {
		t.Fatal("Run: want the harness failure to stop the leg")
	}
	if !strings.Contains(got.Err.Error(), "harness failed") {
		t.Errorf("err = %v, want the harness failure", got.Err)
	}
	if strings.Contains(got.Err.Error(), "reads_unavailable") {
		t.Errorf("err = %v, a failed harness is not unavailable reads", got.Err)
	}
}

func TestReviewTripwireHaltsTheLeg(t *testing.T) {
	for _, policy := range []string{"", "version: 2\npolicy:\n  on_reads_unavailable: halt\n"} {
		e := newEnv(t)
		writeRequiredHead(e, "a.go", "package a\n")
		if policy != "" {
			e.cfg = mustConfig(t, policy)
		}
		e.runner.script = []exec.Result{{
			ExitCode: 0,
			Stdout:   []byte("{\"type\":\"assistant\",\"message\":{\"content\":[{\"type\":\"tool_use\",\"name\":\"Bash\",\"input\":{\"command\":\"id\"}}]}}\n" + string(claudeStdout(batchAnswerFor(t, []string{"a.go"})))),
		}}

		got := runLeg(t, e, e.request(t))
		if got.Err == nil {
			t.Fatalf("policy %q: want the tripwire to halt the leg", policy)
		}
		if !strings.Contains(got.Err.Error(), "review_leg_ran_command") {
			t.Errorf("policy %q: err = %v, want the review_leg_ran_command name", policy, got.Err)
		}
	}
}
