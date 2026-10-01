package review_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/exec"
	"github.com/carlosboeing/crossrev/internal/prstate"
	"github.com/carlosboeing/crossrev/internal/review"
	"github.com/carlosboeing/crossrev/internal/validate"
)

func TestConcernCallsUsageIdentityAndSingleConcern(t *testing.T) {
	for _, concerns := range []string{"", "correctness", "consistency"} {
		t.Run(concerns, func(t *testing.T) {
			e := newEnv(t)
			e.keepTranscripts = true
			writeRequiredHead(e, "a.go", "package a\n")
			answer := findingAnswer(t, "a.go", nil, "Same candidate")
			e.runner.script = []exec.Result{{ExitCode: 0, Stdout: claudeStdoutWithUsage(t, answer, "claude-test", 100, 10, 20, 30)}}
			prompts := capturePrompt(e)
			req := e.request(t)
			if concerns != "" {
				e.cfg = mustConfig(t, "version: 2\nreview:\n  concerns: ["+concerns+"]\n  check: off\n")
			}
			got := runLeg(t, e, req)
			if got.Err != nil {
				t.Fatal(got.Err)
			}
			count := 1
			if concerns == "" {
				count = 2
			}
			if e.runner.calls != count {
				t.Fatalf("calls=%d want %d", e.runner.calls, count)
			}
			if usageBuckets(t, got.Marker.Usage)["total"] != int64(count*160) {
				t.Fatalf("usage=%s", got.Marker.Usage)
			}
			if len(ledgerGenerations(t, e)) != 2 {
				t.Fatal("concern attempt published a generation")
			}
			findings := parseTestFindings(t, got.Marker.Findings)
			if len(findings) != 1 {
				t.Fatalf("findings=%d", len(findings))
			}
			var provenance []string
			_ = json.Unmarshal(findings[0]["concerns"], &provenance)
			if len(provenance) != count {
				t.Fatalf("provenance=%v", provenance)
			}
			log := readRunLog(t, e)
			for invocation := 1; invocation <= count; invocation++ {
				path := filepath.Join(runDir(t, e), "review.call-"+itoa2(invocation)+".attempt-1.stdout")
				if body, err := os.ReadFile(path); err != nil || len(body) == 0 {
					t.Fatalf("invocation %d transcript missing: %v", invocation, err)
				}
			}
			for i, p := range *prompts {
				concern := concerns
				if concern == "" {
					concern = []string{"correctness", "consistency"}[i]
				}
				if !strings.Contains(p, "## Review focus: "+concern) {
					t.Fatalf("wrong focus in prompt %d", i)
				}
				if !strings.Contains(log, "kind=review concern="+concern+" part=-") {
					t.Fatalf("missing identity in log:\n%s", log)
				}
			}
		})
	}
}

func TestConcernInterruptionRestartsWholeInputAndSplitFile(t *testing.T) {
	for _, split := range []bool{false, true} {
		for _, failAt := range []int{2, 4} {
			if !split && failAt == 4 {
				continue
			}
			t.Run(itoa2(failAt)+"/"+map[bool]string{false: "whole", true: "split"}[split], func(t *testing.T) {
				e := newEnv(t)
				body := "package a\n"
				if split {
					body += strings.Repeat("// filler line to exceed the prompt budget\n", 8000)
				}
				writeRequiredHead(e, "a.go", body)
				for i := 1; i < failAt; i++ {
					e.runner.script = append(e.runner.script, exec.Result{ExitCode: 0, Stdout: claudeStdout(partFindingAnswer(t, "a.go", "Partial finding"))})
				}
				e.runner.script = append(e.runner.script, exec.Result{ExitCode: 1, Stderr: []byte("harness died")})
				got := runLeg(t, e, e.request(t))
				if got.Err == nil {
					t.Fatal("want failure")
				}
				for _, gen := range ledgerGenerations(t, e) {
					for _, r := range gen.Records {
						if r.Type == prstate.CoverageRecordUnit && r.Verdict.Present() {
							t.Fatal("partial input published a verdict")
						}
					}
				}
				for _, body := range e.forge.edits {
					if strings.Contains(body, "Partial finding") {
						t.Fatal("partial input persisted finding")
					}
				}
				e.runner.script = []exec.Result{{ExitCode: 0, Stdout: claudeStdout(partAnswer(t, "a.go"))}}
				prompts := capturePrompt(e)
				resumed := runLeg(t, e, e.request(t))
				if resumed.Err != nil || resumed.Outcome != review.OutcomeInvoked {
					t.Fatalf("resume=%v", resumed.Err)
				}
				if !strings.Contains((*prompts)[0], "## Review focus: correctness") {
					t.Fatal("resume skipped first concern")
				}
				if split && !strings.Contains((*prompts)[0], "part 1 of") {
					t.Fatal("resume skipped first part")
				}
			})
		}
	}
}

func TestConcernReadAllowanceIsShared(t *testing.T) {
	e := newEnv(t)
	writeRequiredHead(e, "a.go", "package a\n")
	var granted [][]string
	e.runner.onSpec = func(spec exec.Spec) {
		granted = append(granted, servedArgs(t, spec))
		lines := []string{`{"event":"start"}`, `{"event":"initialize"}`, `{"event":"tools_list"}`}
		if len(granted) == 1 {
			lines = append(lines, `{"event":"read","payload":{"path":"a.go","revision":"head","start_line":1,"end_line":1,"bytes":11}}`)
		}
		lines = append(lines, `{"event":"end"}`)
		serveChildSession(t, spec, lines...)
	}
	e.runner.script = []exec.Result{{ExitCode: 0, Stdout: claudeStdout(batchAnswer(t, 1))}}
	got := runLeg(t, e, e.request(t))
	if got.Err != nil {
		t.Fatal(got.Err)
	}
	if len(granted) != 2 {
		t.Fatalf("sessions=%d", len(granted))
	}
	for key, want := range map[string]string{"--per-leg-reads": "199", "--per-leg-bytes": "1048565"} {
		if value, ok := servedFlag(granted[1], key); !ok || value != want {
			t.Fatalf("second concern %s=%s want %s", key, value, want)
		}
	}
}

func TestConcernPackingReservesLongestBlock(t *testing.T) {
	probe := newEnv(t)
	writeRequiredHead(probe, "a.go", "package a\n")
	probe.runner.script = []exec.Result{{ExitCode: 0, Stdout: claudeStdout(partAnswer(t, "a.go"))}}
	base := capturePrompt(probe)
	if got := runLeg(t, probe, probe.request(t)); got.Err != nil {
		t.Fatal(got.Err)
	}
	if len(*base) != 2 {
		t.Fatal("want both concerns")
	}
	short, long := len((*base)[0])+1, len((*base)[1])+1
	if short > long {
		short, long = long, short
	}
	if long <= short+4 {
		t.Fatal("fixture needs distinct focus lengths")
	}
	// The shorter focus could fit whole, but the longer focus must split.
	fill := (claudePackBytes() - short - 2) / 2
	e := newEnv(t)
	writeRequiredHead(e, "a.go", "package a\n"+strings.Repeat("x\n", fill))
	e.runner.script = []exec.Result{{ExitCode: 0, Stdout: claudeStdout(partAnswer(t, "a.go"))}}
	prompts := capturePrompt(e)
	if got := runLeg(t, e, e.request(t)); got.Err != nil {
		t.Fatal(got.Err)
	}
	if len(*prompts) < 4 {
		t.Fatalf("calls=%d want at least two parts per concern", len(*prompts))
	}
	for _, p := range *prompts {
		if len(p)+1 > claudePackBytes() {
			t.Fatalf("prompt=%d exceeds packing limit=%d", len(p)+1, claudePackBytes())
		}
	}
}

func TestConcernRetryChargesEachAttemptOnce(t *testing.T) {
	e := newEnv(t)
	writeRequiredHead(e, "a.go", "package a\n")
	validations := 0
	e.validate = func(_ []byte, _ validate.ReviewExpectations) error {
		validations++
		if validations == 1 {
			return semanticProblem("coverage omitted unit 1")
		}
		return nil
	}
	for i := int64(1); i <= 3; i++ {
		e.runner.script = append(e.runner.script, exec.Result{ExitCode: 0, Stdout: claudeStdoutWithUsage(t, batchAnswer(t, 1), "claude-test", 100*i, 0, 0, 10*i)})
	}
	got := runLeg(t, e, e.request(t))
	if got.Err != nil {
		t.Fatal(got.Err)
	}
	if e.runner.calls != 3 {
		t.Fatalf("calls=%d want first concern, retry, second concern", e.runner.calls)
	}
	if usageBuckets(t, got.Marker.Usage)["total"] != 660 {
		t.Fatalf("usage=%s want total 660", got.Marker.Usage)
	}
	lines := runLogLines(readRunLog(t, e), "call")
	if len(lines) != 2 {
		t.Fatalf("call lines=%v", lines)
	}
	if !strings.Contains(lines[0], "fresh=300 cached=0 output=30") || !strings.Contains(lines[1], "fresh=300 cached=0 output=30") {
		t.Fatalf("call usage=%v", lines)
	}
}
