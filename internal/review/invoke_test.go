package review_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/exec"
	"github.com/carlosboeing/crossrev/internal/forge"
	"github.com/carlosboeing/crossrev/internal/harness"
	"github.com/carlosboeing/crossrev/internal/review"
	"github.com/carlosboeing/crossrev/internal/ui"
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

// --- the two refusals run_leg_settings prints before anything is billed ------

// legsRewritten is the shipped lib/harnesses.json with the named harnesses'
// `legs` rewritten, which is how every refusal below was measured:
//
//	$ jq '(.harnesses[] | select(.name=="grok") | .legs) |= ["resolve"]' \
//	    lib/harnesses.json > /tmp/d.json
//	$ NO_COLOR=1 CROSSREV_HARNESS_FILE=/tmp/d.json bash -c 'ROOT=$PWD;
//	    source lib/ui.sh; source lib/harnesses.sh; source lib/run.sh;
//	    _run_assert_harness_serves_leg grok review'
//
// Rewriting the shipped file rather than writing a small one keeps the names,
// the product names and the descriptor order the operator's message is built
// from, and every shipped entry serves both legs, so nothing here could be
// measured against the file as it ships.
func legsRewritten(t *testing.T, legs map[string][]string) harness.Document {
	t.Helper()
	var tree map[string]any
	if err := json.Unmarshal(harness.DescriptorJSON(), &tree); err != nil {
		t.Fatalf("decode the shipped descriptor: %v", err)
	}
	entries, ok := tree["harnesses"].([]any)
	if !ok {
		t.Fatal("the shipped descriptor has no harnesses array")
	}
	for _, entry := range entries {
		object, ok := entry.(map[string]any)
		if !ok {
			t.Fatal("a harness entry is not an object")
		}
		name, _ := object["name"].(string)
		if serves, found := legs[name]; found {
			object["legs"] = serves
		}
	}
	raw, err := json.Marshal(tree)
	if err != nil {
		t.Fatalf("re-encode the descriptor: %v", err)
	}
	doc, err := harness.Load(raw)
	if err != nil {
		t.Fatalf("harness.Load: %v", err)
	}
	return doc
}

func wantFatal(t *testing.T, err error, reason, action string) {
	t.Helper()
	var fatal *ui.FatalError
	if !errors.As(err, &fatal) {
		t.Fatalf("err = %v, want a *ui.FatalError", err)
	}
	if fatal.Reason != reason {
		t.Errorf("reason\n got %q\nwant %q", fatal.Reason, reason)
	}
	if fatal.Action != action {
		t.Errorf("action\n got %q\nwant %q", fatal.Action, action)
	}
}

// TestSettingsRefusesAHarnessThatCannotReview pins
// _run_assert_harness_serves_leg (lib/run.sh:553-558), reached from
// run_leg_settings at lib/run.sh:520. The hint is built from the descriptor
// rather than written into the sentence: it names the harnesses that can take
// the leg and reads the refused harness's product name and declared legs back
// off its entry. Measured with grok rewritten to legs ["resolve"]:
//
//	error  the harness 'grok' cannot serve the review leg
//	       CrossRev runs the review leg on claude, codex, agy and opencode. Grok is limited to the resolve leg.
func TestSettingsRefusesAHarnessThatCannotReview(t *testing.T) {
	e := newEnv(t)
	e.doc = legsRewritten(t, map[string][]string{"grok": {"resolve"}})
	req := e.request(t)
	req.HarnessOverride = "grok"

	got := runLeg(t, e, req)

	wantFatal(t, got.Err,
		"the harness 'grok' cannot serve the review leg",
		"CrossRev runs the review leg on claude, codex, agy and opencode. Grok is limited to the resolve leg.")
	if len(e.runner.Specs()) != 0 {
		t.Errorf("harness started on a refusal: %d specs", len(e.runner.Specs()))
	}
}

// TestSettingsRefusesAReviewOnlyListThatOmitsReview pins that the list of
// harnesses that can take the leg shrinks with the descriptor rather than being
// a constant. Measured with claude, codex and agy rewritten to legs
// ["resolve"]:
//
//	error  the harness 'codex' cannot serve the review leg
//	       CrossRev runs the review leg on grok and opencode. Codex is limited to the resolve leg.
//
// Two names here and four above is what pins _names_human's "a and b" against
// its "a, b, c and d" (lib/harnesses.sh:171-178).
func TestSettingsRefusesAReviewOnlyListThatOmitsReview(t *testing.T) {
	e := newEnv(t)
	e.doc = legsRewritten(t, map[string][]string{
		"claude": {"resolve"}, "codex": {"resolve"}, "agy": {"resolve"},
	})
	req := e.request(t)
	req.HarnessOverride = "codex"

	got := runLeg(t, e, req)

	wantFatal(t, got.Err,
		"the harness 'codex' cannot serve the review leg",
		"CrossRev runs the review leg on grok and opencode. Codex is limited to the resolve leg.")
}

// TestSettingsSendsANotDrivenHarnessToEndpoints pins the branch at
// lib/run.sh:502-504: a name the descriptor lists under not_driven is refused
// with the reason it carries and the key that would work instead. The leg word
// in "reviewer.endpoint" is the config key, not the descriptor's review/resolve
// vocabulary. Measured:
//
//	$ bash -c 'ROOT=$PWD; source lib/ui.sh; source lib/harnesses.sh;
//	    source lib/config.sh; source lib/run.sh; harness_source_adapters;
//	    CFG_MERGED="{}"; run_leg_settings reviewer kimi'
//	error  there is no adapter for the harness 'kimi'
//	       CrossRev drives claude, codex, agy, grok and opencode directly. Kimi is reached through the claude adapter as a named endpoint, so there is no adapter_kimi behind the name: define it under endpoints: and set reviewer.endpoint, not reviewer.harness.
func TestSettingsSendsANotDrivenHarnessToEndpoints(t *testing.T) {
	e := newEnv(t)
	req := e.request(t)
	req.HarnessOverride = "kimi"

	got := runLeg(t, e, req)

	wantFatal(t, got.Err,
		"there is no adapter for the harness 'kimi'",
		"CrossRev drives claude, codex, agy, grok and opencode directly. Kimi is reached through the claude adapter as a named endpoint, so there is no adapter_kimi behind the name: define it under endpoints: and set reviewer.endpoint, not reviewer.harness.")
	if len(e.runner.Specs()) != 0 {
		t.Errorf("harness started on a refusal: %d specs", len(e.runner.Specs()))
	}
}

// TestSettingsNamesTheDrivenHarnessesForAnUnknownName pins the else arm at
// lib/run.sh:505-506: a name the descriptor does not carry at all gets the same
// sentence without the endpoints half, and the names come from the descriptor.
// Measured:
//
//	$ … run_leg_settings reviewer nosuch
//	error  there is no adapter for the harness 'nosuch'
//	       CrossRev drives claude, codex, agy, grok and opencode directly.
//
// This is also the case ServesLeg is deliberately lax about: the adapter test
// at lib/run.sh:500 refuses the name before the serves-leg gate at :520 ever
// reads it, so the refusal names the fault rather than printing a sentence
// built from an empty product name.
func TestSettingsNamesTheDrivenHarnessesForAnUnknownName(t *testing.T) {
	e := newEnv(t)
	req := e.request(t)
	req.HarnessOverride = "nosuch"

	got := runLeg(t, e, req)

	wantFatal(t, got.Err,
		"there is no adapter for the harness 'nosuch'",
		"CrossRev drives claude, codex, agy, grok and opencode directly.")
}

// TestSettingsRefusesWhenNothingThatCanReviewIsInstalled pins the last refusal
// in run_leg_settings (lib/run.sh:538-540), reached once the configured harness
// has no binary and the substitution loop at :531-537 finds no other harness
// that serves the leg. The hint names the harnesses that could take the leg,
// read off the descriptor. Measured on the shipped descriptor with a PATH that
// carries jq and yq but no harness binary:
//
//	$ NO_COLOR=1 env PATH=/usr/bin:/bin:/usr/sbin:/sbin:/tmp/tools bash -c 'ROOT=$PWD;
//	    source lib/ui.sh; source lib/harnesses.sh; source lib/config.sh;
//	    source lib/run.sh; harness_source_adapters; CFG_MERGED="{}";
//	    run_leg_settings reviewer claude'
//	error  the reviewer is configured to use 'claude', which is not installed, and no other harness that can serve the review leg is either
//	       Install one of claude, codex, agy, grok and opencode. CrossRev needs at least one, and two different ones is what makes the cross-model check mean anything.
//
// The refused harness is named in the list it is told to install from, because
// the list is every harness that serves the leg rather than every alternative.
func TestSettingsRefusesWhenNothingThatCanReviewIsInstalled(t *testing.T) {
	e := newEnv(t)
	e.lookPath = func(string) (string, error) { return "", os.ErrNotExist }
	req := e.request(t)
	req.HarnessOverride = "claude"

	got := runLeg(t, e, req)

	wantFatal(t, got.Err,
		"the reviewer is configured to use 'claude', which is not installed, and no other harness that can serve the review leg is either",
		"Install one of claude, codex, agy, grok and opencode. CrossRev needs at least one, and two different ones is what makes the cross-model check mean anything.")
	if len(e.runner.Specs()) != 0 {
		t.Errorf("harness started on a refusal: %d specs", len(e.runner.Specs()))
	}
}

// TestSettingsNamesOnlyTheHarnessesThatCanReview pins that the install list is
// harness_names_for_leg rather than every driven harness. Measured with codex,
// agy and grok rewritten to legs ["resolve"] and the same binary-free PATH:
//
//	error  the reviewer is configured to use 'claude', which is not installed, and no other harness that can serve the review leg is either
//	       Install one of claude and opencode. CrossRev needs at least one, and two different ones is what makes the cross-model check mean anything.
//
// Two names here against five above is what pins _names_human's "a and b"
// (lib/harnesses.sh:171-178), and claude is asked for because a harness that
// cannot serve the leg is refused at lib/run.sh:520 before this line.
func TestSettingsNamesOnlyTheHarnessesThatCanReview(t *testing.T) {
	e := newEnv(t)
	e.doc = legsRewritten(t, map[string][]string{
		"codex": {"resolve"}, "agy": {"resolve"}, "grok": {"resolve"},
	})
	e.lookPath = func(string) (string, error) { return "", os.ErrNotExist }
	req := e.request(t)
	req.HarnessOverride = "claude"

	got := runLeg(t, e, req)

	wantFatal(t, got.Err,
		"the reviewer is configured to use 'claude', which is not installed, and no other harness that can serve the review leg is either",
		"Install one of claude and opencode. CrossRev needs at least one, and two different ones is what makes the cross-model check mean anything.")
}
