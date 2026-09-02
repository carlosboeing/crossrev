package review_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/exec"
)

// The invoke line carries how long the harness took (lib/run.sh:831):
//
//	log_event invoke "harness=$harness attempt=$attempt exit=$adapter_rc duration=$(( SECONDS - invoke_start ))s"
//
// SECONDS is whole seconds, so the unit is `s` and never a fraction.
func TestTheInvokeEventRecordsItsDuration(t *testing.T) {
	e := newEnv(t)
	writeAppGo(t, e.dir)
	e.runner.script = []exec.Result{{ExitCode: 0, Stdout: claudeStdout(issuesPayload(twoFindings))}}

	if got := runLeg(t, e, e.request(t)); got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}

	log := readRunLog(t, e)
	if !strings.Contains(log, "invoke harness=claude attempt=1 exit=0 duration=") {
		t.Fatalf("the invoke event carries no duration:\n%s", log)
	}
}

// The harness's two streams are the archived record when a stem exists
// (lib/adapters/claude.sh:95-106), and they are filtered in place AFTER the
// answer has been parsed out of them (lib/adapters/claude.sh:148-154) — the
// other order would rewrite the model's own answer.
func TestTheTranscriptsHoldWhatTheHarnessPrintedAndAreRedacted(t *testing.T) {
	e := newEnv(t)
	writeAppGo(t, e.dir)
	const leak = "sk-ant-api03-EXAMPLEONLYnotarealkey0123456789"
	findings := strings.Replace(twoFindings, "A failed request looks like a success",
		"A failed request looks like a success near "+leak, 1)
	e.keepTranscripts = true
	e.runner.script = []exec.Result{{
		ExitCode: 0,
		Stdout:   claudeStdout(issuesPayload(findings)),
		Stderr:   []byte("harness chatter\n"),
	}}

	got := runLeg(t, e, e.request(t))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	// The payload the leg acted on is unfiltered: the finding text survives.
	if !strings.Contains(string(got.Payload), leak) {
		t.Errorf("the parsed payload was filtered before the parse: %s", got.Payload)
	}

	dir := runDir(t, e)
	stdout := mustReadFile(t, filepath.Join(dir, "review.attempt-1.stdout"))
	if !strings.Contains(stdout, "Unchecked fetch response") {
		t.Errorf("the transcript does not hold what the harness printed:\n%s", stdout)
	}
	if strings.Contains(stdout, leak) {
		t.Error("the kept transcript holds the token body in the clear")
	}
	if !strings.Contains(stdout, "sk-ant-api03-…[redacted]") {
		t.Errorf("the token body is not masked:\n%s", stdout)
	}
	if stderr := mustReadFile(t, filepath.Join(dir, "review.attempt-1.stderr")); !strings.Contains(stderr, "harness chatter") {
		t.Errorf("the stderr transcript is empty: %q", stderr)
	}
	if mode := fileMode(t, filepath.Join(dir, "review.attempt-1.stdout")); mode != 0o600 {
		t.Errorf("transcript mode = %o, want 600", mode)
	}
}

// A successful leg deletes them; that is log_transcripts_clear at the end of
// leg_review (lib/run.sh:1326).
func TestASuccessfulReviewLegClearsItsTranscripts(t *testing.T) {
	e := newEnv(t)
	writeAppGo(t, e.dir)
	e.runner.script = []exec.Result{{ExitCode: 0, Stdout: claudeStdout(issuesPayload(twoFindings))}}

	if got := runLeg(t, e, e.request(t)); got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}

	left, err := filepath.Glob(filepath.Join(runDir(t, e), "review.attempt-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 0 {
		t.Fatalf("transcripts left behind after a clean pass: %v", left)
	}
}

// A failed leg keeps them: they are the reason the files exist.
func TestAFailedReviewLegKeepsItsTranscripts(t *testing.T) {
	e := newEnv(t)
	writeAppGo(t, e.dir)
	e.runner.script = []exec.Result{{ExitCode: 1, Stderr: []byte("no canned payload\n")}}

	if got := runLeg(t, e, e.request(t)); got.Err == nil {
		t.Fatal("a harness that exits 1 did not fail the leg")
	}

	stderr := mustReadFile(t, filepath.Join(runDir(t, e), "review.attempt-1.stderr"))
	if !strings.Contains(stderr, "no canned payload") {
		t.Fatalf("the failed attempt's transcript does not hold the harness error: %q", stderr)
	}
}

func runDir(t *testing.T, e *env) string {
	t.Helper()
	return filepath.Join(e.dir, "run")
}

func readRunLog(t *testing.T, e *env) string {
	t.Helper()
	return mustReadFile(t, filepath.Join(runDir(t, e), "run.log"))
}

func mustReadFile(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(body)
}

func fileMode(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return info.Mode().Perm()
}
