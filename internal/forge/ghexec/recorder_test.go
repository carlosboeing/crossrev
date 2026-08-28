package ghexec_test

import (
	"context"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/exec"
	"github.com/carlosboeing/crossrev/internal/forge"
)

// recorder is a Runner that starts nothing and remembers everything it was
// handed, so a test asserts on the argument array, the environment and the
// audience without a process.
type recorder struct {
	specs []exec.Spec
	// results answers the calls in order. A call past the end answers as a
	// success with no output, which is what the stub does for a route nobody
	// declared.
	results []exec.Result
	calls   int
}

func (r *recorder) Run(_ context.Context, spec exec.Spec) exec.Result {
	r.specs = append(r.specs, spec)
	i := r.calls
	r.calls++
	if i < len(r.results) {
		return r.results[i]
	}
	return exec.Result{Stdout: []byte("{}\n")}
}

// out is a successful invocation printing s.
func out(s string) exec.Result { return exec.Result{Stdout: []byte(s)} }

// bad is an invocation that exited non-zero, which is how the stub reports a
// route declared as `!fail`.
func bad() exec.Result { return exec.Result{ExitCode: 1} }

// only returns the single Spec the recorder saw, failing if there was not
// exactly one.
func (r *recorder) only(t *testing.T) exec.Spec {
	t.Helper()
	if len(r.specs) != 1 {
		t.Fatalf("gh was invoked %d times, want once: %v", len(r.specs), r.argvs())
	}
	return r.specs[0]
}

// argvs is every recorded invocation as the stub would log it: one joined line
// per call.
func (r *recorder) argvs() []string {
	lines := make([]string, 0, len(r.specs))
	for _, s := range r.specs {
		lines = append(lines, strings.Join(s.Args, " "))
	}
	return lines
}

// wantArgs asserts the exact argument array of the nth invocation.
func (r *recorder) wantArgs(t *testing.T, n int, want ...string) {
	t.Helper()
	if n >= len(r.specs) {
		t.Fatalf("gh was invoked %d times, want at least %d", len(r.specs), n+1)
	}
	got := r.specs[n].Args
	if len(got) != len(want) {
		t.Fatalf("argv = %q\nwant   %q", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("argv = %q\nwant   %q", got, want)
		}
	}
}

// passthrough is a Publisher that changes nothing, which is what
// log_redact_publish does to a body carrying no credential shape.
type passthrough struct{}

func (passthrough) Filter(body string) (string, error) { return body, nil }
func (passthrough) Mask(text string) string            { return text }

// withheld is a Publisher that fails, returning the notice that stands in for
// the body (lib/log.sh:153-157).
type withheld struct{}

const withheldNotice = "CrossRev could not filter this text for credential shapes, so it withheld it rather than publishing it."

func (withheld) Filter(string) (string, error) {
	return withheldNotice, errFilter
}
func (withheld) Mask(text string) string { return text }

type filterError struct{}

func (filterError) Error() string { return "the publish filter failed" }

var errFilter = filterError{}

// masking is a Publisher that changes the body, so a test can prove the
// filtered text is what reaches gh.
type masking struct{}

func (masking) Filter(string) (string, error) { return "masked", nil }
func (masking) Mask(string) string            { return "masked-title" }

var (
	_ forge.Publisher = passthrough{}
	_ forge.Publisher = withheld{}
	_ forge.Publisher = masking{}
	_ exec.Runner     = (*recorder)(nil)
)
