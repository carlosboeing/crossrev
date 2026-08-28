package exec

import (
	"bytes"
	"strings"
	"testing"
)

func TestCaptureUncappedKeepsEverything(t *testing.T) {
	c := &capture{}
	payload := bytes.Repeat([]byte("x"), 1<<20)

	n, err := c.Write(payload)

	if err != nil {
		t.Fatalf("Write returned %v, want nil", err)
	}
	if n != len(payload) {
		t.Fatalf("Write reported %d bytes, want %d", n, len(payload))
	}
	if got := c.snapshot(); !bytes.Equal(got, payload) {
		t.Fatalf("captured %d bytes, want the whole %d", len(got), len(payload))
	}
	if c.truncated {
		t.Error("an uncapped capture reported truncation")
	}
	if c.total != len(payload) {
		t.Errorf("total = %d, want %d", c.total, len(payload))
	}
}

func TestCaptureKeepsTheHeadAndReportsTheWholeSize(t *testing.T) {
	c := &capture{limit: 5}

	if n, err := c.Write([]byte("abc")); n != 3 || err != nil {
		t.Fatalf("first Write = (%d, %v), want (3, nil)", n, err)
	}
	if n, err := c.Write([]byte("defgh")); n != 5 || err != nil {
		t.Fatalf("second Write = (%d, %v), want (5, nil)", n, err)
	}
	if n, err := c.Write([]byte("ijk")); n != 3 || err != nil {
		t.Fatalf("third Write = (%d, %v), want (3, nil)", n, err)
	}

	if got := string(c.snapshot()); got != "abcde" {
		t.Errorf("captured %q, want the first five bytes %q", got, "abcde")
	}
	if !c.truncated {
		t.Error("a capture past its cap did not report truncation")
	}
	if c.total != 11 {
		t.Errorf("total = %d, want the 11 bytes the writer produced", c.total)
	}
}

// A cap must never be reported to the child as a short write. io.Copy turns a
// short write into ErrShortWrite, which os/exec surfaces as a failed command —
// so a caller asking for less output would change whether the leg succeeded.
func TestCaptureNeverReportsAShortWrite(t *testing.T) {
	c := &capture{limit: 1}
	for i := 0; i < 4; i++ {
		n, err := c.Write([]byte(strings.Repeat("y", 100)))
		if n != 100 || err != nil {
			t.Fatalf("Write %d = (%d, %v), want (100, nil)", i, n, err)
		}
	}
	if got := string(c.snapshot()); got != "y" {
		t.Errorf("captured %q, want one byte", got)
	}
}

// The snapshot must not alias the buffer a later write would append to.
func TestCaptureSnapshotDoesNotAliasTheBuffer(t *testing.T) {
	c := &capture{}
	if _, err := c.Write([]byte("abc")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got := c.snapshot()
	if _, err := c.Write([]byte("def")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if string(got) != "abc" {
		t.Errorf("snapshot changed under a later write: %q", got)
	}
}

func TestCaptureOfNothingIsEmptyNotNil(t *testing.T) {
	c := &capture{}
	if got := c.snapshot(); got == nil || len(got) != 0 {
		t.Errorf("snapshot of an unwritten capture = %v, want an empty slice", got)
	}
}
