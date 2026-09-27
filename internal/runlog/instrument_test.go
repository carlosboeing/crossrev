package runlog_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/carlosboeing/crossrev/internal/runlog"
)

// TestRunStartCarriesTheBuildRevision pins that `run start` names the build
// revision the binary was built from.
func TestRunStartCarriesTheBuildRevision(t *testing.T) {
	l := openLog(t, runlog.Options{
		Repo:     "acme/widget",
		PR:       "7",
		Revision: "0913bf7b99dcecf746d0e6fcef5a9c1d64aaf3b0",
	})

	if got, want := readLog(t, l), "2026-08-29T01:02:03Z run start repo=acme/widget pr=7 revision=0913bf7b99dcecf746d0e6fcef5a9c1d64aaf3b0\n"; got != want {
		t.Errorf("run log = %q, want %q", got, want)
	}
}

// TestRunStartWithoutARevisionReadsADash: a build with no VCS stamp — go run,
// go test, an exported tarball — still writes the field, with the same
// missing-identity mark the call lines use for a harness that names no model.
func TestRunStartWithoutARevisionReadsADash(t *testing.T) {
	l := openLog(t, runlog.Options{Repo: "acme/widget", PR: "7"})

	if got, want := readLog(t, l), "2026-08-29T01:02:03Z run start repo=acme/widget pr=7 revision=-\n"; got != want {
		t.Errorf("run log = %q, want %q", got, want)
	}
}

// TestPhaseEventLines pins the preparation instrumentation: one line per
// step, with the term count on the terms step.
func TestPhaseEventLines(t *testing.T) {
	l := openLog(t, runlog.Options{Repo: "acme/widget", PR: "7", Leg: "review"})
	l.Phase("enumerate", 3)
	l.Phase("reads", 11)
	l.Phase("diff", 7)
	l.PhaseTerms(41, 2)
	l.Phase("search", 5)
	l.Phase("pack", 1)

	want := "2026-08-29T01:02:03Z run start repo=acme/widget pr=7 revision=-\n" +
		"2026-08-29T01:02:03Z phase enumerate ms=3\n" +
		"2026-08-29T01:02:03Z phase reads ms=11\n" +
		"2026-08-29T01:02:03Z phase diff ms=7\n" +
		"2026-08-29T01:02:03Z phase terms terms=41 ms=2\n" +
		"2026-08-29T01:02:03Z phase search ms=5\n" +
		"2026-08-29T01:02:03Z phase pack ms=1\n"
	if got := readLog(t, l); got != want {
		t.Errorf("run log = %q, want %q", got, want)
	}
}

// TestCallEventLine pins the per-accepted-call instrumentation, and that a
// harness naming no model reads as a dash.
func TestCallEventLine(t *testing.T) {
	l := openLog(t, runlog.Options{Repo: "acme/widget", PR: "7", Leg: "review"})
	l.Call(2, 184320, 409600, 1000, 2000, 300, "stub-model", 12)
	l.Call(3, 184320, 409600, 0, 0, 0, "", 9)

	want := "2026-08-29T01:02:03Z run start repo=acme/widget pr=7 revision=-\n" +
		"2026-08-29T01:02:03Z call 2 prompt_bytes=184320 supplied_bytes=409600 fresh=1000 cached=2000 output=300 reads=0 commands=0 model=stub-model ms=12\n" +
		"2026-08-29T01:02:03Z call 3 prompt_bytes=184320 supplied_bytes=409600 fresh=0 cached=0 output=0 reads=0 commands=0 model=- ms=9\n"
	if got := readLog(t, l); got != want {
		t.Errorf("run log = %q, want %q", got, want)
	}
}

// TestTranscriptBaseForCallNamesReviewCalls: one pass's calls must not share
// a stem, so the second batch cannot overwrite the first's evidence.
func TestTranscriptBaseForCallNamesReviewCalls(t *testing.T) {
	l := openLog(t, runlog.Options{Repo: "acme/widget", PR: "7", Leg: "review"})
	base, ok := l.TranscriptBaseForCall(2, 3)
	if !ok {
		t.Fatal("TranscriptBaseForCall reported no stem")
	}
	if want := filepath.Join(l.Dir(), "review.call-2.attempt-3"); base != want {
		t.Errorf("base = %q, want %q", base, want)
	}
	for _, stream := range []string{".stdout", ".stderr", ".payload"} {
		assertMode(t, base+stream, 0o600)
	}
}

// TestTranscriptBaseForCallNeedsALeg, like TranscriptBase: with no leg the
// adapters fall back to anonymous temporary files.
func TestTranscriptBaseForCallNeedsALeg(t *testing.T) {
	l := openLog(t, runlog.Options{Repo: "acme/widget", PR: "7"})
	if base, ok := l.TranscriptBaseForCall(1, 1); ok {
		t.Errorf("TranscriptBaseForCall reported %q with no leg set", base)
	}
}

// TestClearTranscriptsFindsCallStems: the whole-leg clear — and with it the
// retention sweep's expectation that a run directory holds only swept files —
// must find the per-call stems too, while leaving the other leg's alone.
func TestClearTranscriptsFindsCallStems(t *testing.T) {
	l := openLog(t, runlog.Options{Repo: "acme/widget", PR: "7", Leg: "review"})
	first, _ := l.TranscriptBaseForCall(1, 1)
	second, _ := l.TranscriptBaseForCall(2, 1)
	l.SetLeg("resolve")
	other, _ := l.TranscriptBase(1)
	l.SetLeg("review")

	l.ClearTranscripts("")

	for _, base := range []string{first, second} {
		if exists(base + ".stdout") {
			t.Errorf("%s survived a whole-leg clear", base)
		}
	}
	if !exists(other + ".stdout") {
		t.Error("the other leg's transcript was cleared")
	}
	if !exists(filepath.Join(l.Dir(), "run.log")) {
		t.Error("the run log was cleared")
	}
}

// TestClearTranscriptsOneCallAttempt removes the call stem it is given and
// nothing else.
func TestClearTranscriptsOneCallAttempt(t *testing.T) {
	l := openLog(t, runlog.Options{Repo: "acme/widget", PR: "7", Leg: "review"})
	first, _ := l.TranscriptBaseForCall(1, 1)
	second, _ := l.TranscriptBaseForCall(2, 1)

	l.ClearTranscripts(first)

	if exists(first + ".stdout") {
		t.Error("the cleared call survived")
	}
	if !exists(second + ".stdout") {
		t.Error("a different call was cleared too")
	}
}

// TestTranscriptBaseKeepsAttemptNaming: the resolve leg runs no calls, so
// its stems keep the attempt-only form.
func TestTranscriptBaseKeepsAttemptNaming(t *testing.T) {
	l := openLog(t, runlog.Options{Repo: "acme/widget", PR: "7", Leg: "resolve"})
	base, ok := l.TranscriptBase(1)
	if !ok {
		t.Fatal("TranscriptBase reported no stem")
	}
	if want := filepath.Join(l.Dir(), "resolve.attempt-1"); base != want {
		t.Errorf("base = %q, want %q", base, want)
	}
	if _, err := os.Stat(filepath.Join(l.Dir(), "resolve.call-1.attempt-1.stdout")); !os.IsNotExist(err) {
		t.Error("a call stem exists for a leg that runs no calls")
	}
}
