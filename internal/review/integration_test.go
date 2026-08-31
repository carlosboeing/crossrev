package review_test

// The review half of the cross-leg integration proof. One review pass runs
// end to end against the package's fake collaborators, and the durable marker
// bytes it leaves on the claim comment are compared with the frozen fixture at
// testdata/review-pass-1.marker. internal/resolve/integration_test.go reads
// those exact bytes back as its input, so the marker on disk — not a Go value
// — is what crosses from one leg to the other.
//
// The behaviour pinned here is what the Bash suites prove for the shell
// implementation: the claim-then-record-then-publish order of
// tests/test-persist.sh and tests/test-recovery.sh, the comment shapes of
// tests/test-presentation.sh, and the label transitions of tests/test-policy.sh.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/exec"
	"github.com/carlosboeing/crossrev/internal/policy"
	"github.com/carlosboeing/crossrev/internal/prstate"
	"github.com/carlosboeing/crossrev/internal/review"
)

// handoffFixture is the serialised pass marker exactly as EncodeMarker emits
// it, frozen so the review leg's output and the resolve leg's input cannot
// drift apart. Regenerate by running the scenario below and capturing the
// marker envelope off the final claim edit.
const handoffFixture = "testdata/review-pass-1.marker"

func TestIntegrationReviewPassPublishesTheMarkerResolveReads(t *testing.T) {
	e := newEnv(t)
	writeAppGo(t, e.dir)
	// Repository-provided harness configuration: the sandbox must hold it out
	// of the reviewer's reach for the length of the invocation and put it back
	// afterwards (tests/test-permissions.sh).
	const harnessConfig = "# Repository instructions the reviewer must not read.\n"
	if err := os.WriteFile(filepath.Join(e.dir, "CLAUDE.md"), []byte(harnessConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	payload, err := os.ReadFile("testdata/review-pass-1-payload.json")
	if err != nil {
		t.Fatalf("read review payload fixture: %v", err)
	}
	quarantinedDuringRun := false
	e.runner.onSpec = func(exec.Spec) {
		if _, err := os.Stat(filepath.Join(e.dir, "CLAUDE.md")); os.IsNotExist(err) {
			quarantinedDuringRun = true
		}
	}
	e.runner.script = []exec.Result{{ExitCode: 0, Stdout: claudeStdout(strings.TrimSpace(string(payload)))}}

	got := runLeg(t, e, e.request(t))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if got.Outcome != review.OutcomeInvoked {
		t.Fatalf("Outcome = %q, want invoked", got.Outcome)
	}
	if got.Pass != 1 {
		t.Fatalf("Pass = %d, want 1", got.Pass)
	}

	// The sandbox quarantined the workdir's harness configuration while the
	// reviewer ran and restored it before the leg returned.
	if !quarantinedDuringRun {
		t.Error("CLAUDE.md was still in the workdir while the harness ran")
	}
	if body, err := os.ReadFile(filepath.Join(e.dir, "CLAUDE.md")); err != nil || string(body) != harnessConfig {
		t.Errorf("CLAUDE.md after the leg = %q, %v — the sandbox did not restore it", body, err)
	}
	if _, err := os.Stat(filepath.Join(e.dir, ".crossrev-quarantine")); !os.IsNotExist(err) {
		t.Errorf("quarantine residue left behind: %v", err)
	}

	// The claim lands before the reviewer runs, so a crash mid-review leaves a
	// marker behind (tests/test-recovery.sh).
	events := e.log.all()
	if len(events) != 2 || events[0] != "claim" || events[1] != "harness" {
		t.Fatalf("event order = %v, want [claim harness]", events)
	}

	// Forge call order: claim create, findings recorded on the claim, one
	// inline comment per finding, the summary edit, the completing edit, then
	// the label moves (tests/test-persist.sh, tests/test-policy.sh).
	wantOps := []string{
		"comment-create",
		"comment-edit",
		"review-comment", "review-comment",
		"comment-edit", "comment-edit",
		"label-remove", "label-remove", "label-remove", "label-remove",
		"label-add", "label-add",
	}
	if strings.Join(e.forge.ops, ",") != strings.Join(wantOps, ",") {
		t.Fatalf("forge ops = %v, want %v", e.forge.ops, wantOps)
	}

	// One claim, edited rather than reposted (tests/test-presentation.sh).
	if len(e.forge.created) != 1 {
		t.Fatalf("created comments = %d, want 1 (the claim)", len(e.forge.created))
	}
	if !strings.HasPrefix(e.forge.created[0], "**crossrev — reviewing, pass 1**") {
		t.Errorf("claim body = %q", e.forge.created[0])
	}
	for _, id := range e.forge.editIDs {
		if id != got.ClaimID {
			t.Errorf("edited comment %d, want the claim %d", id, got.ClaimID)
		}
	}

	// The findings are recorded on the claim before any comment is posted, so
	// a crash after posting cannot lose what the reviewer said
	// (tests/test-recovery.sh).
	if !strings.Contains(e.forge.edits[0], "Findings recorded; posting them now.") {
		t.Errorf("first edit = %q, want the findings-recorded edit", e.forge.edits[0])
	}
	recorded := decodeEditMarker(t, e.forge.edits[0])
	if recorded.State != core.PassStarted {
		t.Errorf("recorded marker state = %q, want started", recorded.State)
	}
	if n := len(decodeEditFindings(t, e.forge.edits[0])); n != 2 {
		t.Errorf("recorded findings = %d, want 2", n)
	}

	// The summary edit is the pass rendered for a reader: heading, verdict and
	// one table row per finding (tests/test-presentation.sh).
	summary := e.forge.edits[1]
	for _, want := range []string{
		"## crossrev review — pass 1",
		"Verdict: **issues-remain**.",
		"Unchecked fetch response",
		"Missing return type",
		"[`app.go:2`]",
	} {
		if !strings.Contains(summary, want) {
			t.Errorf("summary body is missing %q", want)
		}
	}

	// Each finding lands inline on its line, carrying its finding marker
	// (tests/test-presentation.sh).
	if len(e.forge.reviewPosted) != 2 {
		t.Fatalf("inline posts = %d, want 2", len(e.forge.reviewPosted))
	}
	for i, posted := range e.forge.reviewPosted {
		if posted.Path != "app.go" || posted.Line != 2 || posted.Side != core.SideRight {
			t.Errorf("post %d anchor = %s:%d %s, want app.go:2 RIGHT", i, posted.Path, posted.Line, posted.Side)
		}
		if !strings.Contains(posted.Body, prstate.FindingMarkerPrefix) {
			t.Errorf("post %d body has no finding marker: %q", i, posted.Body)
		}
	}

	// The pass pill and the loop-state label (tests/test-policy.sh).
	if !containsString(e.forge.labelsAdded, policy.LabelPassPrefix+"1") {
		t.Errorf("labels added = %v, want pass-1", e.forge.labelsAdded)
	}
	if !containsString(e.forge.labelsAdded, policy.LabelAwaitingResolution) {
		t.Errorf("labels added = %v, want awaiting-resolution", e.forge.labelsAdded)
	}
	for _, name := range []string{policy.LabelAwaitingReview, policy.LabelConverged, policy.LabelHalted} {
		if !containsString(e.forge.labelsRemoved, name) {
			t.Errorf("labels removed = %v, want %s gone", e.forge.labelsRemoved, name)
		}
	}

	// The final marker is the pass of record: complete, with the verdict, the
	// head it reviewed and the threads its findings landed on.
	final := decodeEditMarker(t, e.forge.edits[len(e.forge.edits)-1])
	if final.State != core.PassComplete {
		t.Errorf("final state = %q, want complete", final.State)
	}
	if final.Leg != core.LegReview || final.Pass != 1 {
		t.Errorf("final marker leg/pass = %s/%d, want review/1", final.Leg, final.Pass)
	}
	if v := final.Verdict.Value(); v != string(core.VerdictIssuesRemain) {
		t.Errorf("verdict = %q, want issues-remain", v)
	}
	if h := final.HeadSHA.Value(); h != headSHA {
		t.Errorf("head_sha = %q, want %s", h, headSHA)
	}
	if r := final.RunID.Value(); r != runID {
		t.Errorf("run_id = %q, want %s", r, runID)
	}
	if h := final.Harness.Value(); h != "claude" {
		t.Errorf("harness = %q, want claude", h)
	}
	findings := decodeEditFindings(t, e.forge.edits[len(e.forge.edits)-1])
	if len(findings) != 2 {
		t.Fatalf("final findings = %d, want 2", len(findings))
	}
	for i, f := range findings {
		for _, key := range []string{"id", "thread_id", "root_comment_id"} {
			v := string(f[key])
			if v == "" || v == "null" {
				t.Errorf("finding %d %s = %s, want a value the resolve leg can address", i, key, v)
			}
		}
	}

	// The handoff itself: the durable marker bytes on the claim comment are
	// the fixture the resolve leg reads, compared with nothing normalised
	// away. effort_reported is part of that comparison, because Bash assigns
	// the key unconditionally and a marker that omits it is the defect this
	// phase fixed.
	assertSameMarkerBytes(t, e.forge.edits[len(e.forge.edits)-1], readFixture(t, handoffFixture))
}

// readFixture loads a testdata file as the exact bytes a comment body carries.
func readFixture(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return strings.TrimRight(string(raw), "\n")
}

// decodeEditMarker pulls the pass marker out of one recorded comment-edit
// body, the way the resolve leg's comment scan will read it back.
func decodeEditMarker(t *testing.T, body string) prstate.Marker {
	t.Helper()
	raw, ok := prstate.DecodeMarker(body)
	if !ok {
		t.Fatalf("edit body carries no marker: %q", body)
	}
	marker, err := prstate.ParseMarker(raw)
	if err != nil {
		t.Fatalf("ParseMarker: %v", err)
	}
	return marker
}

func decodeEditFindings(t *testing.T, body string) []map[string]json.RawMessage {
	t.Helper()
	marker := decodeEditMarker(t, body)
	var findings []map[string]json.RawMessage
	if err := marker.DecodeFindings(&findings); err != nil {
		t.Fatalf("DecodeFindings: %v", err)
	}
	return findings
}

// assertSameMarkerBytes compares the serialised marker embedded in two comment
// bodies byte for byte. Key order is part of the comparison on purpose: the
// bytes are what every pull request carries, so the two legs must agree on
// them exactly.
func assertSameMarkerBytes(t *testing.T, gotBody, wantBody string) {
	t.Helper()
	// The raw marker text, not a decode-and-re-encode of it. Round-tripping
	// through DecodeMarker and EncodeMarker normalises away exactly what this
	// test exists to pin: a duplicated key, a difference in spacing, a number
	// spelled another way. A mutation making Node.Set append instead of
	// replace writes thread_id twice, and a round-tripping compare would not
	// see it.
	got, ok := prstate.DecodeMarker(gotBody)
	if !ok {
		t.Fatalf("no marker in body: %q", gotBody)
	}
	want, ok := prstate.DecodeMarker(wantBody)
	if !ok {
		t.Fatalf("no marker in fixture: %q", wantBody)
	}
	if string(got) != string(want) {
		t.Errorf("durable marker bytes differ from %s\ngot:  %s\nwant: %s", handoffFixture, got, want)
	}
}
