package readserve

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/exec"
)

// TestServeChild re-executes this test binary as the read server itself, so
// the self-test speaks to a real server over a real pipe without building
// the crossrev binary. The parent sets CROSSREV_READERVE_CHILD=1 and hands
// the server argv after the -- separator.
func TestServeChild(t *testing.T) {
	if os.Getenv("CROSSREV_READERVE_CHILD") != "1" {
		t.Skip("the self-test parent runs this as the server child")
	}
	argv := strings.Split(os.Getenv("CROSSREV_READERVE_ARGV"), "\n")
	os.Exit(Run(context.Background(), argv, os.Stdin, os.Stdout, os.Stderr))
}

// fixtureRepo commits one file and answers its base and head revisions.
func probeFixture(t *testing.T) (dir, base, head string) {
	t.Helper()
	dir = t.TempDir()
	mustGit(t, dir, "init")
	mustGit(t, dir, "config", "user.email", "test@example.invalid")
	mustGit(t, dir, "config", "user.name", "test")
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n\nfunc A() {}\n"), 0o644); err != nil {
		t.Fatalf("writing the fixture: %v", err)
	}
	mustGit(t, dir, "add", "a.go")
	mustGit(t, dir, "commit", "-m", "base")
	base = gitOutput(t, dir, "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n\nfunc A() {}\n\nfunc B() {}\n"), 0o644); err != nil {
		t.Fatalf("writing the head: %v", err)
	}
	mustGit(t, dir, "add", "a.go")
	mustGit(t, dir, "commit", "-m", "head")
	head = gitOutput(t, dir, "rev-parse", "HEAD")
	return dir, base, head
}

func gitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	res := exec.NewOSRunner().Run(context.Background(), exec.Spec{Path: "git", Args: args, Dir: dir})
	if res.Err != nil || res.ExitCode != 0 {
		t.Fatalf("git %v exited %d: %s %s", args, res.ExitCode, res.Stderr, res.Err)
	}
	return strings.TrimRight(string(res.Stdout), "\n ")
}

// serveChildSession runs the server as a child process of this test
// binary: the child runs only TestServeChild, which re-executes Run with
// the server argv carried in the environment.
func serveChildSession(t *testing.T, dir, base, head string) (command string, args []string, env []string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("reading the test binary: %v", err)
	}
	argv := []string{"--repo", dir, "--base", base, "--head", head,
		"--log", filepath.Join(t.TempDir(), "reads.jsonl"), "--call", "selftest"}
	return exe, []string{"-test.run=TestServeChild"}, []string{
		"CROSSREV_READERVE_CHILD=1",
		"CROSSREV_READERVE_ARGV=" + strings.Join(argv, "\n"),
		"PATH=" + os.Getenv("PATH"),
	}
}

// The leg-start self-test speaks the handshake and byte-checks one read
// against git before any harness child starts.
func TestSelfTestPassesAgainstAHealthyServer(t *testing.T) {
	dir, base, head := probeFixture(t)
	command, argv, env := serveChildSession(t, dir, base, head)
	probe := Probe{Path: "a.go", Revision: "base", Want: []byte("package a\n\nfunc A() {}\n")}
	if err := SelfTest(context.Background(), exec.NewOSRunner(), command, argv, env, probe); err != nil {
		t.Errorf("self-test against a healthy server: %v", err)
	}
}

// A read that does not match git fails the self-test: the leg degrades
// rather than trusting the tool.
func TestSelfTestFailsOnAMismatch(t *testing.T) {
	dir, base, head := probeFixture(t)
	command, argv, env := serveChildSession(t, dir, base, head)
	probe := Probe{Path: "a.go", Revision: "base", Want: []byte("package b\n")}
	if err := SelfTest(context.Background(), exec.NewOSRunner(), command, argv, env, probe); err == nil {
		t.Error("a self-test byte mismatch passes")
	}
}

// A probe naming a symlink or a submodule fails as unservable rather than
// as a broken tool: the refusals name what the path is, so the leg tries
// its next candidate instead of failing the self-test on a healthy server.
func TestSelfTestMarksUnservableProbes(t *testing.T) {
	dir, base, _ := probeFixture(t)
	if err := os.Symlink("a.go", filepath.Join(dir, "link.go")); err != nil {
		t.Fatalf("linking the fixture: %v", err)
	}
	subSrc := t.TempDir()
	mustGit(t, subSrc, "init")
	mustGit(t, subSrc, "config", "user.email", "test@example.invalid")
	mustGit(t, subSrc, "config", "user.name", "test")
	if err := os.WriteFile(filepath.Join(subSrc, "inner.txt"), []byte("inner\n"), 0o644); err != nil {
		t.Fatalf("writing the submodule fixture: %v", err)
	}
	mustGit(t, subSrc, "add", "inner.txt")
	mustGit(t, subSrc, "commit", "-m", "inner")
	mustGit(t, dir, "-c", "protocol.file.allow=always", "submodule", "add", subSrc, "submod")
	mustGit(t, dir, "add", "link.go", ".gitmodules", "submod")
	mustGit(t, dir, "commit", "-m", "unservable")
	head := gitOutput(t, dir, "rev-parse", "HEAD")
	command, argv, env := serveChildSession(t, dir, base, head)
	for _, path := range []string{"link.go", "submod"} {
		probe := Probe{Path: path, Revision: "head", Want: []byte("whatever\n")}
		if err := SelfTest(context.Background(), exec.NewOSRunner(), command, argv, env, probe); !errors.Is(err, ErrProbeUnservable) {
			t.Errorf("self-test on %s = %v, want ErrProbeUnservable", path, err)
		}
	}
}

// runSession drives the server in-process with one request per line.
func runSession(t *testing.T, args []string, requests []string) []string {
	t.Helper()
	in := strings.Join(requests, "\n") + "\n"
	var out bytes.Buffer
	if code := Run(context.Background(), args, strings.NewReader(in), &out, &bytes.Buffer{}); code != 0 {
		t.Fatalf("the server exited %d", code)
	}
	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) != len(requests) {
		t.Fatalf("got %d responses for %d requests:\n%s", len(lines), len(requests), out.String())
	}
	return lines
}

func isRefused(response string) bool {
	var decoded struct {
		Result *struct {
			IsError bool `json:"isError"`
		} `json:"result"`
		Error *struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(response), &decoded); err != nil {
		return false
	}
	return (decoded.Result != nil && decoded.Result.IsError) || decoded.Error != nil
}

// A probe call is rejected: tools/call outside read_file never serves.
func TestProbeCallIsRejected(t *testing.T) {
	dir, base, head := probeFixture(t)
	args := []string{"--repo", dir, "--base", base, "--head", head,
		"--log", filepath.Join(t.TempDir(), "reads.jsonl"), "--call", "1"}
	responses := runSession(t, args, []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"probe","arguments":{}}}`,
	})
	if !isRefused(responses[1]) {
		t.Errorf("a probe call serves: %s", responses[1])
	}
}
