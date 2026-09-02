package resolve

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/forge"
	"github.com/carlosboeing/crossrev/internal/harness"
)

// TestContext pins context loading: one pull-request read, policy from the
// base revision, threads and backlog candidates through forge, and 1-based
// finding numbers.
func TestContext(t *testing.T) {
	t.Run("policy is read from the base revision", func(t *testing.T) {
		e := setup(t)
		e.addReview(t, defaultFindings(), "issues-remain")
		e.git.show = map[string][]byte{
			e.base.SHA() + ":.github/crossrev.yml": []byte("version: 1\nresolver:\n  harness: claude\n"),
		}
		got := e.run(t)
		if got.Err != nil {
			t.Fatalf("Run: %v", got.Err)
		}
		var sawBase, sawHead bool
		for _, c := range e.git.showCalls {
			if c.Path == ".github/crossrev.yml" && c.Revision.SHA() == e.base.SHA() {
				sawBase = true
			}
			if c.Path == ".github/crossrev.yml" && c.Revision.SHA() == e.head.SHA() {
				sawHead = true
			}
		}
		if !sawBase {
			t.Fatal("config was not read at the base revision")
		}
		if sawHead {
			t.Fatal("config was read from the head revision")
		}
	})

	t.Run("threads load through forge and findings are numbered from 1", func(t *testing.T) {
		e := setup(t)
		e.addReview(t, defaultFindings(), "issues-remain")
		e.forge.threads = []forge.ReviewThread{{
			ID:            "thread-1",
			Path:          "app.ts",
			Line:          2,
			RootCommentID: 55,
			Comments: []forge.ThreadComment{{
				Author: "codex",
				Body:   "nil deref <!-- crossrev:f {\"id\":\"" + testFinding + "\",\"pass\":1,\"leg\":\"review\"} -->",
			}},
		}}
		got := e.run(t)
		if got.Err != nil {
			t.Fatalf("Run: %v", got.Err)
		}
		if !contains(e.forge.order, "ReviewThreads") {
			t.Fatalf("threads were not loaded through forge: %v", e.forge.order)
		}
		if !strings.Contains(string(got.Prompt), "### 1. `"+testFinding+"`") {
			t.Fatalf("prompt did not number the finding as 1:\n%s", got.Prompt)
		}
	})

	t.Run("github_issues candidates keep the current dedupe input shape", func(t *testing.T) {
		e := setup(t)
		e.addReview(t, defaultFindings(), "issues-remain")
		e.git.show = map[string][]byte{
			e.base.SHA() + ":.github/crossrev.yml": []byte("version: 1\nbacklog:\n  destination: github_issues\n"),
		}
		e.forge.candidates = []forge.IssueCandidate{{
			Number: 19,
			Title:  "same crash",
			State:  "open",
			Body:   "seen before",
		}}
		got := e.run(t)
		if got.Err != nil {
			t.Fatalf("Run: %v", got.Err)
		}
		if !containsPrefix(e.forge.order, "IssueCandidates:") {
			t.Fatalf("candidates were not loaded through forge: %v", e.forge.order)
		}
		if !strings.Contains(string(got.Prompt), "### candidates for finding 1 (`"+testFinding+"`)") {
			t.Fatalf("prompt is missing the per-finding candidate block:\n%s", got.Prompt)
		}
		if !strings.Contains(string(got.Prompt), "**#19** (open) same crash") {
			t.Fatalf("prompt is missing the candidate issue:\n%s", got.Prompt)
		}
	})

	t.Run("automatic trigger trusts CROSSREV_APP_SLUG not ViewerLogin", func(t *testing.T) {
		e := setup(t)
		e.addReview(t, defaultFindings(), "issues-remain")
		e.forge.comments[len(e.forge.comments)-1].AuthorLogin = "crossrev[bot]"
		t.Setenv("CROSSREV_APP_SLUG", "crossrev")
		got := e.runReq(t, Request{PR: 42, Repo: e.slug, Trigger: TriggerAutomatic})
		if got.Err != nil {
			t.Fatalf("Run: %v", got.Err)
		}
		if got.Outcome != OutcomeComplete {
			t.Fatalf("Outcome = %q, want complete; ViewerLogin=tester must not hide crossrev[bot] markers", got.Outcome)
		}
		if got.Pass != 1 {
			t.Errorf("Pass = %d, want 1", got.Pass)
		}
	})

	t.Run("empty trigger is human", func(t *testing.T) {
		e := setup(t)
		e.addReview(t, defaultFindings(), "issues-remain")
		got := e.runReq(t, Request{PR: 42, Repo: e.slug, Trigger: ""})
		if got.Err != nil {
			t.Fatalf("empty trigger: %v", got.Err)
		}
		if got.Outcome != OutcomeComplete {
			t.Fatalf("Outcome = %q, want complete (lib/run.sh:1731 trigger=human)", got.Outcome)
		}
	})

	t.Run("entry guards", func(t *testing.T) {
		tests := []struct {
			name        string
			setup       func(*testing.T, *testEnv, *Request)
			wantOutcome Outcome
			wantErr     string
		}{
			{
				name: "automatic fork is refused",
				setup: func(_ *testing.T, e *testEnv, req *Request) {
					req.Trigger = TriggerAutomatic
					e.forge.pr.IsCrossRepository = true
				},
				wantOutcome: OutcomeRefused,
				wantErr:     "comes from a fork",
			},
			{
				name: "a pull request that is not OPEN is refused",
				setup: func(_ *testing.T, e *testEnv, _ *Request) {
					e.forge.pr.State = "CLOSED"
				},
				wantOutcome: OutcomeRefused,
				wantErr:     "is not open",
			},
			{
				name: "automatic draft skips",
				setup: func(_ *testing.T, e *testEnv, req *Request) {
					req.Trigger = TriggerAutomatic
					e.forge.pr.IsDraft = true
				},
				wantOutcome: OutcomeSkipped,
			},
			{
				name: "unknown trigger is refused",
				setup: func(_ *testing.T, _ *testEnv, req *Request) {
					req.Trigger = "cron"
				},
				wantOutcome: OutcomeRefused,
				wantErr:     "unknown resolve trigger",
			},
			{
				name: "AssertPushTarget refuses a worktree at the wrong revision",
				setup: func(t *testing.T, e *testEnv, _ *Request) {
					e.addReview(t, defaultFindings(), "issues-remain")
					e.git.wrongHead = e.base
				},
				wantOutcome: OutcomeRefused,
				wantErr:     "the tree is at revision",
			},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				e := setup(t)
				req := Request{PR: 42, Repo: e.slug, Trigger: TriggerHuman}
				tt.setup(t, e, &req)
				got := e.runReq(t, req)
				if got.Outcome != tt.wantOutcome {
					t.Errorf("Outcome = %q, want %q (err=%v)", got.Outcome, tt.wantOutcome, got.Err)
				}
				if tt.wantErr != "" {
					if got.Err == nil || !strings.Contains(got.Err.Error(), tt.wantErr) {
						t.Errorf("Err = %v, want it to contain %q", got.Err, tt.wantErr)
					}
				}
				if e.runner.specs != nil {
					t.Errorf("harness started on a guard: %d specs", len(e.runner.specs))
				}
			})
		}
	})

	t.Run("stop label returns before a harness process", func(t *testing.T) {
		e := setup(t)
		e.addReview(t, defaultFindings(), "issues-remain")
		e.forge.pr.Labels = append(e.forge.pr.Labels, forge.Label{Name: "crossrev/stop"})
		got := e.run(t)
		if got.Err != nil {
			t.Fatalf("Run: %v", got.Err)
		}
		if got.Outcome != OutcomeStopped {
			t.Fatalf("Outcome = %q, want %q", got.Outcome, OutcomeStopped)
		}
		if e.runner.specs != nil {
			t.Fatal("harness ran under crossrev/stop")
		}
	})
}

func contains(order []string, op string) bool {
	for _, s := range order {
		if s == op {
			return true
		}
	}
	return false
}

func containsPrefix(order []string, prefix string) bool {
	for _, s := range order {
		if strings.HasPrefix(s, prefix) {
			return true
		}
	}
	return false
}

// --- the two refusals run_leg_settings prints before anything is billed ------

// legsRewritten is the shipped lib/harnesses.json with the named harnesses'
// `legs` rewritten, which is how every refusal below was measured:
//
//	$ jq '(.harnesses[] | select(.name=="grok") | .legs) |= ["review"]' \
//	    lib/harnesses.json > /tmp/d.json
//	$ NO_COLOR=1 CROSSREV_HARNESS_FILE=/tmp/d.json bash -c 'ROOT=$PWD;
//	    source lib/ui.sh; source lib/harnesses.sh; source lib/run.sh;
//	    _run_assert_harness_serves_leg grok resolve'
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

func wantRefusal(t *testing.T, err error, message, hint string) {
	t.Helper()
	var refusal *Refusal
	if !errors.As(err, &refusal) {
		t.Fatalf("err = %v, want a *Refusal", err)
	}
	if refusal.Message != message {
		t.Errorf("message\n got %q\nwant %q", refusal.Message, message)
	}
	if refusal.Hint != hint {
		t.Errorf("hint\n got %q\nwant %q", refusal.Hint, hint)
	}
}

// TestSettingsRefusesAHarnessThatCannotResolve pins
// _run_assert_harness_serves_leg (lib/run.sh:553-558), reached from
// run_leg_settings at lib/run.sh:520. The hint is built from the descriptor
// rather than written into the sentence: it names the harnesses that can take
// the leg and reads the refused harness's product name and declared legs back
// off its entry. Measured with grok rewritten to legs ["review"]:
//
//	error  the harness 'grok' cannot serve the resolve leg
//	       CrossRev runs the resolve leg on claude, codex, agy and opencode. Grok is limited to the review leg.
func TestSettingsRefusesAHarnessThatCannotResolve(t *testing.T) {
	e := setup(t)
	e.doc = legsRewritten(t, map[string][]string{"grok": {"review"}})
	e.addReview(t, defaultFindings(), "issues-remain")

	got := e.runReq(t, Request{PR: 42, Repo: e.slug, Trigger: TriggerHuman, Harness: "grok"})

	if got.Outcome != OutcomeRefused {
		t.Errorf("Outcome = %q, want %q", got.Outcome, OutcomeRefused)
	}
	wantRefusal(t, got.Err,
		"the harness 'grok' cannot serve the resolve leg",
		"CrossRev runs the resolve leg on claude, codex, agy and opencode. Grok is limited to the review leg.")
	if e.runner.specs != nil {
		t.Errorf("harness started on a refusal: %d specs", len(e.runner.specs))
	}
}

// TestSettingsRefusesWhenMostOfTheDescriptorCannotResolve pins that the list of
// harnesses that can take the leg shrinks with the descriptor rather than being
// a constant. Measured with claude, codex and agy rewritten to legs ["review"]:
//
//	error  the harness 'agy' cannot serve the resolve leg
//	       CrossRev runs the resolve leg on grok and opencode. Antigravity is limited to the review leg.
//
// Two names here and four above is what pins _names_human's "a and b" against
// its "a, b, c and d" (lib/harnesses.sh:171-178), and Antigravity against agy
// is what pins the product name as a descriptor read.
func TestSettingsRefusesWhenMostOfTheDescriptorCannotResolve(t *testing.T) {
	e := setup(t)
	e.doc = legsRewritten(t, map[string][]string{
		"claude": {"review"}, "codex": {"review"}, "agy": {"review"},
	})
	e.addReview(t, defaultFindings(), "issues-remain")

	got := e.runReq(t, Request{PR: 42, Repo: e.slug, Trigger: TriggerHuman, Harness: "agy"})

	wantRefusal(t, got.Err,
		"the harness 'agy' cannot serve the resolve leg",
		"CrossRev runs the resolve leg on grok and opencode. Antigravity is limited to the review leg.")
}

// TestSettingsSendsANotDrivenHarnessToEndpoints pins the branch at
// lib/run.sh:502-504: a name the descriptor lists under not_driven is refused
// with the reason it carries and the key that would work instead. The leg word
// in "resolver.endpoint" is the config key, not the descriptor's review/resolve
// vocabulary. Measured:
//
//	$ bash -c 'ROOT=$PWD; source lib/ui.sh; source lib/harnesses.sh;
//	    source lib/config.sh; source lib/run.sh; harness_source_adapters;
//	    CFG_MERGED="{}"; run_leg_settings resolver kimi'
//	error  there is no adapter for the harness 'kimi'
//	       CrossRev drives claude, codex, agy, grok and opencode directly. Kimi is reached through the claude adapter as a named endpoint, so there is no adapter_kimi behind the name: define it under endpoints: and set resolver.endpoint, not resolver.harness.
func TestSettingsSendsANotDrivenHarnessToEndpoints(t *testing.T) {
	e := setup(t)
	e.addReview(t, defaultFindings(), "issues-remain")

	got := e.runReq(t, Request{PR: 42, Repo: e.slug, Trigger: TriggerHuman, Harness: "kimi"})

	if got.Outcome != OutcomeRefused {
		t.Errorf("Outcome = %q, want %q", got.Outcome, OutcomeRefused)
	}
	wantRefusal(t, got.Err,
		"there is no adapter for the harness 'kimi'",
		"CrossRev drives claude, codex, agy, grok and opencode directly. Kimi is reached through the claude adapter as a named endpoint, so there is no adapter_kimi behind the name: define it under endpoints: and set resolver.endpoint, not resolver.harness.")
	if e.runner.specs != nil {
		t.Errorf("harness started on a refusal: %d specs", len(e.runner.specs))
	}
}

// TestSettingsNamesTheDrivenHarnessesForAnUnknownName pins the else arm at
// lib/run.sh:505-506: a name the descriptor does not carry at all gets the same
// sentence without the endpoints half, and the names come from the descriptor.
// Measured:
//
//	$ ... run_leg_settings resolver nosuch
//	error  there is no adapter for the harness 'nosuch'
//	       CrossRev drives claude, codex, agy, grok and opencode directly.
//
// This is also the case ServesLeg is deliberately lax about: the adapter test
// at lib/run.sh:500 refuses the name before the serves-leg gate at :520 ever
// reads it, so the refusal names the fault rather than printing a sentence
// built from an empty product name.
func TestSettingsNamesTheDrivenHarnessesForAnUnknownName(t *testing.T) {
	e := setup(t)
	e.addReview(t, defaultFindings(), "issues-remain")

	got := e.runReq(t, Request{PR: 42, Repo: e.slug, Trigger: TriggerHuman, Harness: "nosuch"})

	wantRefusal(t, got.Err,
		"there is no adapter for the harness 'nosuch'",
		"CrossRev drives claude, codex, agy, grok and opencode directly.")
}

// TestSettingsRefusesWhenNothingThatCanResolveIsInstalled pins the last refusal
// in run_leg_settings (lib/run.sh:538-540), reached once the configured harness
// has no binary and the substitution loop at :531-537 finds no other harness
// that serves the leg. The hint names the harnesses that could take the leg,
// read off the descriptor. Measured on the shipped descriptor with a PATH that
// carries jq and yq but no harness binary:
//
//	$ NO_COLOR=1 env PATH=/usr/bin:/bin:/usr/sbin:/sbin:/tmp/tools bash -c 'ROOT=$PWD;
//	    source lib/ui.sh; source lib/harnesses.sh; source lib/config.sh;
//	    source lib/run.sh; harness_source_adapters; CFG_MERGED="{}";
//	    run_leg_settings resolver claude'
//	error  the resolver is configured to use 'claude', which is not installed, and no other harness that can serve the resolve leg is either
//	       Install one of claude, codex, agy, grok and opencode. CrossRev needs at least one, and two different ones is what makes the cross-model check mean anything.
//
// The refused harness is named in the list it is told to install from, because
// the list is every harness that serves the leg rather than every alternative.
func TestSettingsRefusesWhenNothingThatCanResolveIsInstalled(t *testing.T) {
	e := setup(t)
	e.lookPath = func(string) (string, error) { return "", os.ErrNotExist }
	e.addReview(t, defaultFindings(), "issues-remain")

	got := e.runReq(t, Request{PR: 42, Repo: e.slug, Trigger: TriggerHuman, Harness: "claude"})

	if got.Outcome != OutcomeRefused {
		t.Errorf("Outcome = %q, want %q", got.Outcome, OutcomeRefused)
	}
	wantRefusal(t, got.Err,
		"the resolver is configured to use 'claude', which is not installed, and no other harness that can serve the resolve leg is either",
		"Install one of claude, codex, agy, grok and opencode. CrossRev needs at least one, and two different ones is what makes the cross-model check mean anything.")
	if e.runner.specs != nil {
		t.Errorf("harness started on a refusal: %d specs", len(e.runner.specs))
	}
}

// TestSettingsNamesOnlyTheHarnessesThatCanResolve pins that the install list is
// harness_names_for_leg rather than every driven harness. Measured with codex,
// agy and grok rewritten to legs ["review"] and the same binary-free PATH:
//
//	error  the resolver is configured to use 'claude', which is not installed, and no other harness that can serve the resolve leg is either
//	       Install one of claude and opencode. CrossRev needs at least one, and two different ones is what makes the cross-model check mean anything.
//
// Two names here against five above is what pins _names_human's "a and b"
// (lib/harnesses.sh:171-178), and claude is asked for because a harness that
// cannot serve the leg is refused at lib/run.sh:520 before this line.
func TestSettingsNamesOnlyTheHarnessesThatCanResolve(t *testing.T) {
	e := setup(t)
	e.doc = legsRewritten(t, map[string][]string{
		"codex": {"review"}, "agy": {"review"}, "grok": {"review"},
	})
	e.lookPath = func(string) (string, error) { return "", os.ErrNotExist }
	e.addReview(t, defaultFindings(), "issues-remain")

	got := e.runReq(t, Request{PR: 42, Repo: e.slug, Trigger: TriggerHuman, Harness: "claude"})

	wantRefusal(t, got.Err,
		"the resolver is configured to use 'claude', which is not installed, and no other harness that can serve the resolve leg is either",
		"Install one of claude and opencode. CrossRev needs at least one, and two different ones is what makes the cross-model check mean anything.")
}
