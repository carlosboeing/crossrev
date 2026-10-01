package preflight_test

import (
	"context"
	"strings"
	"testing"
)

const gatePairing = defaultPairing + "verification:\n  required_checks: [build, test@my-app]\n  wait_minutes: 5\n"

func gateAnswers(r *recorder, total string, code int) *recorder {
	r.answer("gh repo view --json nameWithOwner,defaultBranchRef",
		"{\"nameWithOwner\":\"acme/widget\",\"defaultBranchRef\":{\"name\":\"main\"}}\n", 0)
	r.answer("gh api repos/acme/widget/commits/main --jq .sha",
		"2c4a46cb321db01826d116b5ef2add6b0284d68c\n", 0)
	// gh writes a refusal to stderr; the recorder answers stdout only, and
	// the probe classifies on both streams the way the forge client does.
	r.answer("gh api repos/acme/widget/commits/2c4a46cb321db01826d116b5ef2add6b0284d68c/check-runs -F per_page=1 --jq .total_count",
		total, code)
	return r
}

// Without a configured gate doctor says nothing about required checks: no
// section, no probe, and the report reads exactly as it always has.
func TestDoctorIsSilentWithoutAConfiguredGate(t *testing.T) {
	r := coreVersions(newRecorder())
	c, buf := doctorChecker(t, r, onPath("git", "gh", "claude", "codex"), defaultPairing)
	if !c.ReportVerification(context.Background()) {
		t.Fatal("an unconfigured gate failed the report")
	}
	if strings.Contains(buf.String(), "Required checks") {
		t.Errorf("unconfigured report names required checks:\n%s", buf.String())
	}
	for _, argv := range r.argvs() {
		if strings.Contains(argv, "check-runs") {
			t.Errorf("unconfigured gate probed %q", argv)
		}
	}
}

// With a gate doctor names the checks and the wait, and a token that can
// read check runs passes.
func TestDoctorReportsTheGateAndAHeldPermission(t *testing.T) {
	r := gateAnswers(coreVersions(newRecorder()), "3\n", 0)
	c, buf := doctorChecker(t, r, onPath("git", "gh", "claude", "codex"), gatePairing)
	if !c.ReportVerification(context.Background()) {
		t.Fatal("a held permission failed the report")
	}
	out := buf.String()
	for _, want := range []string{
		"Required checks",
		"required build, test@my-app (from verification.required_checks)",
		"wait 5 minutes (from verification.wait_minutes)",
		"token can read check runs (checks: read)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("report lacks %q:\n%s", want, out)
		}
	}
}

// A token the endpoint refuses fails the report with the missing permission
// and the fix.
func TestDoctorFailsOnARefusedPermission(t *testing.T) {
	r := gateAnswers(coreVersions(newRecorder()), "gh: Resource not accessible by integration (HTTP 403)\n", 1)
	c, buf := doctorChecker(t, r, onPath("git", "gh", "claude", "codex"), gatePairing)
	if c.ReportVerification(context.Background()) {
		t.Fatal("a refused permission passed the report")
	}
	out := buf.String()
	for _, want := range []string{
		"token cannot read check runs (checks: read is missing)",
		"approve the new permission on the installation",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("report lacks %q:\n%s", want, out)
		}
	}
}

// An unanswered probe is unprobed rather than refused: gh may be missing,
// or this may be no checkout.
func TestDoctorIsUnprobedWithoutGh(t *testing.T) {
	r := coreVersions(newRecorder())
	c, buf := doctorChecker(t, r, onPath("git", "claude", "codex"), gatePairing)
	if !c.ReportVerification(context.Background()) {
		t.Fatal("an unprobed permission failed the report")
	}
	if out := buf.String(); !strings.Contains(out, "checks permission unprobed — gh is not installed") {
		t.Errorf("report lacks the unprobed line:\n%s", out)
	}
}
