package preflight_test

import (
	"bytes"
	"context"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/carlosboeing/crossrev/internal/exec"
	"github.com/carlosboeing/crossrev/internal/harness"
	"github.com/carlosboeing/crossrev/internal/preflight"
	"github.com/carlosboeing/crossrev/internal/ui"
)

// reply is one canned answer for one program invocation.
type reply struct {
	stdout string
	code   int
}

// recorder is a Runner that answers from a table keyed by the argv it is given,
// and remembers every Spec it saw.
//
// Keyed by argv rather than by call order on purpose: preflight probes the five
// tools in a fixed order and then asks gh up to three questions, and a table
// keyed by order would have to be rewritten whenever a case changes which of
// the three answer.
type recorder struct {
	mu      sync.Mutex
	answers map[string]reply
	specs   []exec.Spec
}

func newRecorder() *recorder {
	return &recorder{answers: map[string]reply{}}
}

// answer registers what the program with this argv writes and exits with.
func (r *recorder) answer(key string, out string, code int) *recorder {
	r.answers[key] = reply{stdout: out, code: code}
	return r
}

func (r *recorder) Run(_ context.Context, spec exec.Spec) exec.Result {
	r.mu.Lock()
	r.specs = append(r.specs, spec)
	r.mu.Unlock()
	key := strings.Join(append([]string{spec.Path}, spec.Args...), " ")
	answer, found := r.answers[key]
	if !found {
		return exec.Result{ExitCode: 127, Stdout: []byte{}, Stderr: []byte{}}
	}
	return exec.Result{ExitCode: answer.code, Stdout: []byte(answer.stdout), Stderr: []byte{}}
}

// argvs is every invocation, in order, as one string each.
func (r *recorder) argvs() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.specs))
	for _, spec := range r.specs {
		out = append(out, strings.Join(append([]string{spec.Path}, spec.Args...), " "))
	}
	return out
}

// specFor is the first Spec whose argv matches, and whether there was one.
func (r *recorder) specFor(key string) (exec.Spec, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, spec := range r.specs {
		if strings.Join(append([]string{spec.Path}, spec.Args...), " ") == key {
			return spec, true
		}
	}
	return exec.Spec{}, false
}

// onPath is a LookPath over a fixed set of names, which is what lets a case
// describe the machine it wants rather than inherit the one the suite runs on.
func onPath(names ...string) func(string) (string, error) {
	installed := map[string]bool{}
	for _, name := range names {
		installed[name] = true
	}
	return func(name string) (string, error) {
		if installed[name] {
			return "/stub/" + name, nil
		}
		return "", os.ErrNotExist
	}
}

// document is the compiled-in harness descriptor, which is the same document
// lib/harnesses.sh reads.
func document(t *testing.T) harness.Document {
	t.Helper()
	doc, err := harness.Descriptors()
	if err != nil {
		t.Fatalf("harness.Descriptors: %v", err)
	}
	return doc
}

// capture is an IO writing into one buffer, with the plain palette every
// assertion here is written against (NO_COLOR=1 in the shell).
func capture() (*ui.IO, *bytes.Buffer) {
	var buf bytes.Buffer
	return &ui.IO{Out: &buf, Err: &buf, Palette: ui.Plain()}, &buf
}

// versions is the answer every core tool gives when a case does not care which
// version came back, keyed the way the recorder keys an invocation.
func coreVersions(r *recorder) *recorder {
	r.answer("git --version", "git version 2.50.1 (Apple Git-155)\n", 0)
	r.answer("gh --version", "gh version 2.97.0 (2026-01-01)\n", 0)
	r.answer("jq --version", "jq-1.8.1\n", 0)
	r.answer("yq --version", "yq (mikefarah/yq) version v4.53.3\n", 0)
	r.answer("openssl version", "OpenSSL 3.6.3 30 Sep 2026\n", 0)
	return r
}

// checker is a Checker wired to a recorder, a stub PATH and a captured IO.
//
// LookPath is always injected here, so no case built through this helper runs
// the production PATH search. TestLookPathTakesOnlyAnExecutableFile is the one
// that leaves the field nil and drives it.
func checker(t *testing.T, r *recorder, look func(string) (string, error)) (*preflight.Checker, *bytes.Buffer) {
	t.Helper()
	io, buf := capture()
	return &preflight.Checker{
		IO:       io,
		Runner:   r,
		Env:      []string{},
		LookPath: look,
		Harness:  document(t),
		OS:       "Darwin",
	}, buf
}
