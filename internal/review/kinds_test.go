package review_test

import (
	"testing"

	"github.com/carlosboeing/crossrev/internal/exec"
	"github.com/carlosboeing/crossrev/internal/ui"
)

// Three sites, each measured against the shell, each pinned to the helper the
// shell used at it.
//
//	lib/run.sh:1255  ui_ok   "posted $posted finding comment(s)"
//	lib/run.sh:1293  ui_ok   "posted a summary comment"
//	lib/run.sh:1305  ui_warn "the reviewer returned verdict …" "The actionable count outranks …"
//
// The kind is not decoration. A leg's lines reach a terminal through
// internal/ui's Print, and until this existed every one of them printed as a
// ui_say: a verified success lost its glyph, and a warning went to stdout with
// no consequence indent and no ⚠ — which is rule 3 and rule 5 of the voice, at
// the top of internal/ui, made unenforceable.
func TestPublishedLinesCarryTheHelperTheShellUsed(t *testing.T) {
	e := newEnv(t)
	writeAppGo(t, e.dir)
	e.runner.script = []exec.Result{{ExitCode: 0, Stdout: claudeStdout(issuesPayload(twoFindings))}}

	got := runLeg(t, e, e.request(t))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}

	kinds := map[string]ui.Kind{}
	for _, line := range got.Messages {
		kinds[line.Text] = line.Kind
	}

	for _, want := range []string{"posted a summary comment", "posted 2 finding comment(s)"} {
		kind, ok := kinds[want]
		if !ok {
			t.Fatalf("the leg never reported %q; it reported %q", want, ui.Texts(got.Messages))
		}
		if kind != ui.KindOK {
			t.Errorf("%q is kind %v, want KindOK: the shell says it with ui_ok", want, kind)
		}
	}

	// Nothing else in a clean pass is a ui_ok. A site that quietly became one
	// would claim a verified success the shell does not claim.
	for _, line := range got.Messages {
		if line.Kind != ui.KindOK {
			continue
		}
		if line.Text != "posted a summary comment" && line.Text != "posted 2 finding comment(s)" {
			t.Errorf("%q is reported as a verified success, and the shell says it with ui_say", line.Text)
		}
	}
}

// The third site, and the rule that makes a warning a warning: both halves are
// carried apart so ui.Warn does its own joining. The leg used to build the
// joined bytes itself, which meant a change to ui.Warn's three-space indent
// diverged this line silently — the comment on the old code said so.
func TestTheVerdictWarningKeepsItsTwoHalvesApart(t *testing.T) {
	e := newEnv(t)
	writeAppGo(t, e.dir)
	convergedPayload := `{"verdict":"converged","blocked_reason":null,"findings":` + twoFindings + `}`
	e.runner.script = []exec.Result{{ExitCode: 0, Stdout: claudeStdout(convergedPayload)}}

	got := runLeg(t, e, e.request(t))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}

	const wantCondition = "the reviewer returned verdict 'converged' alongside 1 actionable finding"
	const wantConsequence = "The actionable count outranks the verdict, so the pass is labelled 'awaiting-resolution' to run the resolve leg."

	var warning ui.Line
	for _, line := range got.Messages {
		if line.Text == wantCondition {
			warning = line
			break
		}
	}
	if warning.Text == "" {
		t.Fatalf("no verdict warning among %q", ui.Texts(got.Messages))
	}
	if warning.Kind != ui.KindWarn {
		t.Errorf("kind = %v, want KindWarn: lib/run.sh:1305 is ui_warn", warning.Kind)
	}
	if warning.Action != wantConsequence {
		t.Errorf("consequence = %q, want %q", warning.Action, wantConsequence)
	}
	// And the joined form is unchanged, so an assertion written before Line
	// existed still reads the same bytes.
	if got, want := warning.String(), wantCondition+"\n   "+wantConsequence; got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}
