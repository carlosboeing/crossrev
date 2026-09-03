package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/cli"
	"github.com/carlosboeing/crossrev/internal/harness"
	"github.com/carlosboeing/crossrev/internal/sandbox"
	"github.com/carlosboeing/crossrev/internal/ui"
)

// doctorRun is one `crossrev doctor`, with the report captured.
func doctorRun(t *testing.T) (int, string) {
	t.Helper()
	doc, err := harness.Descriptors()
	if err != nil {
		t.Fatalf("reading the compiled-in descriptor: %v", err)
	}
	var out, errOut bytes.Buffer
	io := &ui.IO{Out: &out, Err: &errOut, Palette: ui.Plain()}
	status, err := doctor(context.Background(), io, doc)
	if err != nil {
		t.Fatalf("doctor: %v", err)
	}
	return status, out.String() + errOut.String()
}

// harnessStub is a harness CLI that answers a version, so preflight counts one
// as installed.
func harnessStub() stub {
	return stub{name: "claude", body: "#!/bin/sh\necho 'claude 1.2.3'\n"}
}

// doctor asks for a harness, not just the core five (bin/crossrev:165:
// `preflight_check harness || doctor_ok=1`).
//
// The requirement string is the whole of the difference. NeedCore probes git,
// gh, jq, yq and openssl; NeedHarness probes those and then every harness the
// descriptor drives, and refuses when none of them is installed. A machine with
// the five tools and no model CLI is set up for nothing, and asking for the
// core set alone tells it everything is fine.
func TestDoctorRequiresAHarnessAndNotJustTheCoreTools(t *testing.T) {
	sandboxPATH(t, coreToolStubs()) // the five, and no harness

	status, report := doctorRun(t)

	if !strings.Contains(report, "no harness CLI found") {
		t.Errorf("doctor did not probe for a harness at all (bin/crossrev:165 asks for `harness`):\n%s", report)
	}
	if status != cli.ExitFailure {
		t.Errorf("doctor answered status %d with no harness installed, want %d:\n%s",
			status, cli.ExitFailure, report)
	}
}

// A stranded quarantine fails doctor, and the two verdict lines are the
// shell's own (bin/crossrev:166, :176 and :178).
//
// The quarantine is the one probe whose effect on the exit status is invisible
// on a machine that is missing something else: every other check is already
// failing there, so `doctor_ok` was going to be 1 either way. This stands the
// process somewhere everything else passes, so the quarantine is the only thing
// deciding the answer.
func TestDoctorFailsOnAStrandedQuarantineAndSaysSo(t *testing.T) {
	work := sandboxPATH(t, append(coreToolStubs(), harnessStub()))

	// Everything installed: the verdict at bin/crossrev:176, word for word.
	status, report := doctorRun(t)
	if status != cli.ExitOK {
		t.Fatalf("doctor answered status %d on a complete machine, want %d:\n%s",
			status, cli.ExitOK, report)
	}
	if want := "Everything CrossRev needs is installed."; !strings.Contains(report, want) {
		t.Errorf("the success verdict is not %q (bin/crossrev:176):\n%s", want, report)
	}

	// The same machine, with a quarantine a killed run left behind.
	if err := os.MkdirAll(filepath.Join(work, sandbox.QuarantineDir, ".github"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	status, report = doctorRun(t)
	if !strings.Contains(report, "stranded quarantine found at "+sandbox.QuarantineDir) {
		t.Fatalf("doctor did not find the quarantine:\n%s", report)
	}
	if status != cli.ExitFailure {
		t.Errorf("a stranded quarantine answered status %d, want %d: bin/crossrev:166 sets doctor_ok on it:\n%s",
			status, cli.ExitFailure, report)
	}
	if want := "Fix what is marked ✗ above, then run this again."; !strings.Contains(report, want) {
		t.Errorf("the failure verdict is not %q (bin/crossrev:178):\n%s", want, report)
	}
}

// `config show` prints what `jq .` prints: two-space indentation and a trailing
// newline (bin/crossrev:158, `jq . <<<"$CFG_MERGED"`).
//
// Neither half is observable through the shell suite, which reads the output
// with `out="$($CROSSREV config show)"` and then parses it with jq — command
// substitution strips the trailing newline, and jq does not care how its input
// was laid out. Both are what a person reads, and both are what a `diff`
// against the shell's output compares.
func TestConfigShowIsJqsIndentationWithATrailingNewline(t *testing.T) {
	sandboxPATH(t, coreToolStubs())

	doc, err := harness.Descriptors()
	if err != nil {
		t.Fatalf("reading the compiled-in descriptor: %v", err)
	}
	var out, errOut bytes.Buffer
	io := &ui.IO{Out: &out, Err: &errOut, Palette: ui.Plain()}

	status, err := configShow(context.Background(), io, doc)
	if err != nil {
		t.Fatalf("config show: %v", err)
	}
	if status != cli.ExitOK {
		t.Fatalf("config show answered status %d:\n%s", status, errOut.String())
	}

	got := out.String()
	if !strings.HasSuffix(got, "}\n") || strings.HasSuffix(got, "}\n\n") {
		t.Errorf("config show does not end with one newline after the closing brace, and `jq .` does:\n%q",
			got[max(0, len(got)-16):])
	}

	// jq's own indentation is two spaces per level. The first key of the
	// object is at one level in, so it carries exactly two.
	lines := strings.Split(got, "\n")
	if len(lines) < 3 {
		t.Fatalf("config show printed %d lines, so it is not indented at all:\n%s", len(lines), got)
	}
	if lines[0] != "{" {
		t.Fatalf("config show does not open with a bare brace:\n%q", lines[0])
	}
	if !strings.HasPrefix(lines[1], `  "`) || strings.HasPrefix(lines[1], `   `) {
		t.Errorf("the first key is %q, want two spaces of indentation the way `jq .` writes it (bin/crossrev:158)",
			lines[1])
	}
}
