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
	if got := captured(c); !bytes.Equal(got, payload) {
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

	if got := string(captured(c)); got != "abcde" {
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
	if got := string(captured(c)); got != "y" {
		t.Errorf("captured %q, want one byte", got)
	}
}

// state must not hand back the buffer a later write would append to.
//
// Content alone cannot prove this: append writes past the length it was given,
// so the first three bytes read the same whether the slice is a copy or the
// buffer itself. Spare capacity is the property that separates them — a copy
// comes from make([]byte, n), whose capacity is exactly n.
func TestCaptureStateDoesNotAliasTheBuffer(t *testing.T) {
	c := &capture{}
	if _, err := c.Write([]byte("abc")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	got := captured(c)
	if cap(got) != len(got) {
		t.Errorf("state returned a slice with %d spare bytes of capacity, so it aliases the buffer", cap(got)-len(got))
	}

	if _, err := c.Write([]byte("defghijkl")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if string(got) != "abc" {
		t.Errorf("the returned bytes changed under a later write: %q", got)
	}
}

// F1. A write that exactly fills the cap discarded nothing.
func TestCaptureOfExactlyTheCapIsNotTruncated(t *testing.T) {
	for _, tt := range []struct {
		name   string
		writes []string
	}{
		{name: "one write", writes: []string{"abcde"}},
		{name: "two writes", writes: []string{"abc", "de"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c := &capture{limit: 5}
			for _, write := range tt.writes {
				if _, err := c.Write([]byte(write)); err != nil {
					t.Fatalf("Write: %v", err)
				}
			}

			data, total, truncated := c.state()
			if string(data) != "abcde" {
				t.Errorf("captured %q, want %q", data, "abcde")
			}
			if truncated {
				t.Error("a write that exactly filled the cap reported truncation")
			}
			if total != 5 {
				t.Errorf("total = %d, want 5", total)
			}
		})
	}
}

// F1. One byte past the cap did discard something, whichever write carried it.
func TestCaptureOfOneBytePastTheCapIsTruncated(t *testing.T) {
	for _, tt := range []struct {
		name   string
		writes []string
	}{
		{name: "one write", writes: []string{"abcdef"}},
		{name: "a whole write after an exact fit", writes: []string{"abcde", "f"}},
		{name: "a write straddling the cap", writes: []string{"abcd", "ef"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c := &capture{limit: 5}
			for _, write := range tt.writes {
				if _, err := c.Write([]byte(write)); err != nil {
					t.Fatalf("Write: %v", err)
				}
			}

			data, total, truncated := c.state()
			if string(data) != "abcde" {
				t.Errorf("captured %q, want %q", data, "abcde")
			}
			if !truncated {
				t.Error("a write past the cap did not report truncation")
			}
			if total != 6 {
				t.Errorf("total = %d, want 6", total)
			}
		})
	}
}

// captured is the retained bytes, dropping the counters the caller does not
// need. state is the only reader; there is no second accessor to drift from it.
func captured(c *capture) []byte {
	data, _, _ := c.state()
	return data
}

func TestCaptureOfNothingIsEmptyNotNil(t *testing.T) {
	c := &capture{}
	if got := captured(c); got == nil || len(got) != 0 {
		t.Errorf("snapshot of an unwritten capture = %v, want an empty slice", got)
	}
}
