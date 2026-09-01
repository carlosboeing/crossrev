package review_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/exec"
	"github.com/carlosboeing/crossrev/internal/forge"
	"github.com/carlosboeing/crossrev/internal/review"
	"github.com/carlosboeing/crossrev/internal/validate"
)

func TestInvokeHostedRunnerWithoutTokenRefuses(t *testing.T) {
	e := newEnv(t)
	t.Setenv("RUNNER_ENVIRONMENT", "github-hosted")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")
	got := runLeg(t, e, e.request(t))
	if got.Err == nil {
		t.Fatal("wanted a missing-secret refusal on a github-hosted runner")
	}
	if !strings.Contains(got.Err.Error(), "CLAUDE_CODE_OAUTH_TOKEN") {
		t.Errorf("err = %q, want it to name CLAUDE_CODE_OAUTH_TOKEN", got.Err)
	}
	if len(e.runner.Specs()) != 0 {
		t.Fatalf("harness started after a missing hosted secret: %d specs", len(e.runner.Specs()))
	}
}

func TestInvokeWriteCapabilityIsFalse(t *testing.T) {
	e := newEnv(t)
	got := runLeg(t, e, e.request(t))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if len(e.runner.Specs()) != 1 {
		t.Fatalf("harness calls = %d, want 1", len(e.runner.Specs()))
	}
	spec := e.runner.Specs()[0]
	joined := strings.Join(spec.Args, " ")
	if strings.Contains(joined, "acceptEdits") {
		t.Errorf("review invocation granted writes: %v", spec.Args)
	}
	if _, found := forgeCredentialIn(spec.Env); found {
		t.Errorf("spec env carried a forge credential: %v", spec.Env)
	}
}

func TestInvokeStripsAForgeCredentialFromTheHarnessEnvironment(t *testing.T) {
	e := newEnv(t)
	leg := e.leg(t)
	leg.Env = []string{"PATH=/usr/bin:/bin", "GH_TOKEN=should-never-reach-the-model", "HOME=" + e.dir}
	req := e.request(t)
	got := leg.Run(context.Background(), req)
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	specs := e.runner.Specs()
	if len(specs) == 0 {
		t.Fatal("no harness spec")
	}
	if name, found := forgeCredentialIn(specs[0].Env); found {
		t.Fatalf("harness env carried %s", name)
	}
}

func TestInvokeSemanticRetryRunsTheHarnessOnceMore(t *testing.T) {
	e := newEnv(t)
	leg := e.leg(t)
	attempts := 0
	leg.Validate = func(payload []byte) error {
		attempts++
		if attempts == 1 {
			return &validate.SemanticError{Problem: "finding 9 was not in the numbered list"}
		}
		return validate.Findings(payload)
	}
	got := leg.Run(context.Background(), e.request(t))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if len(e.runner.Specs()) != 2 {
		t.Fatalf("harness calls = %d, want 2 (one semantic retry)", len(e.runner.Specs()))
	}
	if got.Outcome != review.OutcomeInvoked {
		t.Errorf("Outcome = %q, want invoked", got.Outcome)
	}
}

func TestInvokeWarnsWhenSandboxRestoreFails(t *testing.T) {
	e := newEnv(t)
	if err := os.WriteFile(e.dir+"/CLAUDE.md", []byte("quarantine me\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var workdir string
	defer func() {
		if workdir != "" {
			if err := os.Chmod(workdir, 0o700); err != nil {
				t.Errorf("restore workdir permissions: %v", err)
			}
		}
	}()
	e.runner.onSpec = func(spec exec.Spec) {
		workdir = spec.Dir
		if err := os.Chmod(workdir, 0o500); err != nil {
			t.Errorf("make sandbox restore fail: %v", err)
		}
	}
	got := runLeg(t, e, e.request(t))
	if got.Err == nil {
		t.Fatal("Run succeeded after the sandbox restore failed")
	}
	if !strings.Contains(got.Err.Error(), "could not be put back") {
		t.Errorf("error = %q, want the restore refusal", got.Err)
	}
	want := "the rejected attempt's edits could not be put back\n   They are still in the checkout, and a later run would capture them as its own baseline. Check `git status` before re-running the leg."
	if !strings.Contains(strings.Join(got.Messages, "\n"), want) {
		t.Errorf("messages = %q, want warning %q", got.Messages, want)
	}
}

func TestInvokeSchemaNativeShapeErrorDoesNotRetry(t *testing.T) {
	e := newEnv(t)
	leg := e.leg(t)
	leg.Validate = func([]byte) error {
		return &validate.ShapeError{Problem: "no verdict key"}
	}
	got := leg.Run(context.Background(), e.request(t))
	if got.Err == nil {
		t.Fatal("wanted a shape failure")
	}
	if len(e.runner.Specs()) != 1 {
		t.Fatalf("harness calls = %d, want 1 (schema-native shape does not retry)", len(e.runner.Specs()))
	}
}

func TestInvokeRecordsClaimThenHarness(t *testing.T) {
	e := newEnv(t)
	if got := runLeg(t, e, e.request(t)); got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	events := e.log.all()
	if len(events) < 2 || events[0] != "claim" || events[1] != "harness" {
		t.Fatalf("event order = %v, want claim then harness", events)
	}
}

func TestInvokeSemanticRetryIsExactlyOne(t *testing.T) {
	e := newEnv(t)
	leg := e.leg(t)
	leg.Validate = func([]byte) error {
		return &validate.SemanticError{Problem: "finding 9 was not in the numbered list"}
	}
	got := leg.Run(context.Background(), e.request(t))
	if got.Err == nil {
		t.Fatal("wanted a semantic failure after one retry")
	}
	if len(e.runner.Specs()) != 2 {
		t.Fatalf("harness calls = %d, want 2 (one semantic retry then stop)", len(e.runner.Specs()))
	}
}

func TestInvokeKeepsPathAndHomeAndOmitsGHToken(t *testing.T) {
	e := newEnv(t)
	leg := e.leg(t)
	leg.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + e.dir, "GH_TOKEN=should-never-reach-the-model"}
	got := leg.Run(context.Background(), e.request(t))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if len(e.runner.Specs()) == 0 {
		t.Fatal("no harness spec")
	}
	env := e.runner.Specs()[0].Env
	if _, found := forgeCredentialIn(env); found {
		t.Fatal("harness env carried a forge credential")
	}
	if !envHas(env, "PATH") {
		t.Errorf("spec.Env dropped PATH: %v", env)
	}
	if !envHas(env, "HOME") {
		t.Errorf("spec.Env dropped HOME: %v", env)
	}
}

func TestInvokeRefusesALeakedEndpointVariable(t *testing.T) {
	e := newEnv(t)
	leg := e.leg(t)
	leg.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + e.dir, "ANTHROPIC_BASE_URL=https://example.invalid"}
	got := leg.Run(context.Background(), e.request(t))
	if got.Err == nil {
		t.Fatal("wanted AssertEnvClean to refuse a leaked ANTHROPIC_BASE_URL")
	}
	if !strings.Contains(got.Err.Error(), "ANTHROPIC_BASE_URL") {
		t.Errorf("err = %q, want it to name ANTHROPIC_BASE_URL", got.Err)
	}
	if len(e.runner.Specs()) != 0 {
		t.Fatalf("harness started after an endpoint leak: %d specs", len(e.runner.Specs()))
	}
}

func TestInvokeOpencodeShapeErrorRetriesOnce(t *testing.T) {
	e := newEnv(t)
	e.runner.script = []exec.Result{{
		ExitCode: 0,
		Stdout:   []byte(`{"type":"text","part":{"text":"{\"verdict\":\"converged\",\"findings\":[]}"}}` + "\n"),
	}}
	leg := e.leg(t)
	req := e.request(t)
	req.HarnessOverride = "opencode"
	leg.Validate = func([]byte) error {
		return &validate.ShapeError{Problem: "no verdict key"}
	}
	got := leg.Run(context.Background(), req)
	if got.Err == nil {
		t.Fatal("wanted a shape failure")
	}
	if len(e.runner.Specs()) != 2 {
		t.Fatalf("harness calls = %d, want 2 (schema-native false retries once)", len(e.runner.Specs()))
	}
}

func TestInvokeSubstitutesAMissingConfiguredHarnessBeforeTheClaim(t *testing.T) {
	e := newEnv(t)
	e.lookPath = func(name string) (string, error) {
		if name == "claude" {
			return "/usr/bin/claude", nil
		}
		return "", os.ErrNotExist
	}
	e.cfg = mustConfig(t, "version: 1\nreviewer:\n  harness: codex\n")
	req := e.request(t)
	req.HarnessOverride = ""
	leg := e.leg(t)
	got := leg.Run(context.Background(), req)
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if len(e.forge.created) == 0 {
		t.Fatal("no claim")
	}
	if !strings.Contains(e.forge.created[0], `"harness":"claude"`) {
		t.Errorf("claim named the missing harness: %s", e.forge.created[0])
	}
	found := false
	for _, msg := range got.Messages {
		if strings.Contains(msg, "is not installed") && strings.Contains(msg, "claude") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("messages = %v, want the substitute warning (lib/run.sh:542-543)", got.Messages)
	}
	if len(e.runner.Specs()) == 0 {
		t.Fatal("no harness spec")
	}
	if e.runner.Specs()[0].Path != "claude" {
		t.Errorf("Path = %q, want claude", e.runner.Specs()[0].Path)
	}
}

func TestInvokeSkipsTheHarnessWhenFindingsAreAlreadyRecorded(t *testing.T) {
	e := newEnv(t)
	raw := fmt.Sprintf(`{"v":1,"leg":"review","pass":1,"state":"started","ts":%d,"comment_id":9001,"run_id":"x","head_sha":%q,"verdict":"issues-remain","findings":[{"path":"app.go","title":"x","severity":"high"}]}`, frozenNow.Unix(), headSHA)
	e.forge.comments = []forge.IssueComment{commentWithMarker(t, 9001, parseMarker(t, raw))}
	got := runLeg(t, e, e.request(t))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if len(e.runner.Specs()) != 0 {
		t.Fatalf("harness calls = %d, want 0 when findings are already on the claim", len(e.runner.Specs()))
	}
}

func TestInvokeRedriveEditsTheClaim(t *testing.T) {
	e := newEnv(t)
	raw := fmt.Sprintf(`{"v":1,"leg":"review","pass":1,"state":"complete","ts":1699950000,"comment_id":9001,"run_id":"x","head_sha":%q,"verdict":"blocked","findings":[]}`, headSHA)
	e.forge.comments = []forge.IssueComment{commentWithMarker(t, 9001, parseMarker(t, raw))}
	got := runLeg(t, e, e.request(t))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	found := false
	for _, body := range e.forge.edits {
		if strings.Contains(body, "Driving the pass again") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("edits = %v, want one body carrying the driving-again text", e.forge.edits)
	}
}

func TestInvokeRecordsEffortReportedNullWhenAbsent(t *testing.T) {
	e := newEnv(t)
	got := runLeg(t, e, e.request(t))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	raw, err := got.Marker.MarshalJSON()
	if err != nil {
		t.Fatalf("MarshalJSON: %v", err)
	}
	if !strings.Contains(string(raw), `"effort_reported":null`) {
		t.Errorf("marker JSON = %s, want effort_reported:null", string(raw))
	}
	found := false
	for _, body := range e.forge.edits {
		if strings.Contains(body, `"effort_reported":null`) {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("claim edits = %v, want one containing \"effort_reported\":null", e.forge.edits)
	}
}

func envHas(env []string, name string) bool {
	prefix := name + "="
	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			return true
		}
	}
	return false
}

func forgeCredentialIn(env []string) (string, bool) {
	for _, name := range exec.ForgeCredentialNames() {
		prefix := name + "="
		for _, entry := range env {
			if strings.HasPrefix(entry, prefix) {
				return name, true
			}
		}
	}
	return "", false
}
