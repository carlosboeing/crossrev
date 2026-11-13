package runlog_test

import (
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/runlog"
)

// A check call writes the review call's line with a kind=check identity,
// so the checker's usage reads back separately; a review call's line keeps
// its bytes exactly.
func TestCheckCallNamesItsKind(t *testing.T) {
	l := openLog(t, runlog.Options{})
	l.Call(1, 100, 50, 0, 10, 5, 20, "reviewer-model", 11)
	l.CheckCall(2, 200, 60, 1, 30, 0, 40, "checker-model", 22)

	got := readLog(t, l)
	if !strings.Contains(got, "call 2 prompt_bytes=200 supplied_bytes=60 fresh=30 cached=0 output=40 reads=1 commands=0 model=checker-model ms=22 kind=check concern=- part=-\n") {
		t.Errorf("run log carries no kind=check line:\n%s", got)
	}
	if !strings.Contains(got, "call 1 prompt_bytes=100 supplied_bytes=50 fresh=10 cached=5 output=20 reads=0 commands=0 model=reviewer-model ms=11\n") {
		t.Errorf("the review line moved:\n%s", got)
	}
}
