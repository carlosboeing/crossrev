package exec

import "sync"

// capture is the io.Writer os/exec copies a child's stream into. It keeps the
// first limit bytes and counts the rest.
//
// The mutex is load-bearing rather than defensive. os/exec normally joins its
// copying goroutines inside Wait, but a non-zero Cmd.WaitDelay lets Wait return
// while a copier is still between its Read and its Write. Reading the buffer
// unguarded at that moment is a data race, and a race that only fires when a
// harness leaves an orphan behind is one nobody would reproduce.
type capture struct {
	// limit caps the retained bytes. Zero means uncapped, which is the parity
	// default: no adapter truncates its capture file.
	limit int

	mu        sync.Mutex
	buf       []byte
	total     int
	truncated bool
}

// Write retains what fits and discards the rest, always reporting the whole
// write as accepted. A short write would become io.ErrShortWrite inside
// os/exec's copier and fail the command, which would make a caller's output cap
// decide whether the child succeeded.
func (c *capture) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.total += len(p)
	if c.limit <= 0 {
		c.buf = append(c.buf, p...)
		return len(p), nil
	}

	room := c.limit - len(c.buf)
	if room <= 0 {
		c.truncated = true
		return len(p), nil
	}
	if len(p) > room {
		c.buf = append(c.buf, p[:room]...)
		c.truncated = true
		return len(p), nil
	}
	c.buf = append(c.buf, p...)
	return len(p), nil
}

// state returns a copy of the retained bytes with the counters that describe
// them, read under one lock so the three cannot disagree.
//
// A copy, not the buffer: the caller keeps it in a Result while an abandoned
// copier may still append to c.buf.
func (c *capture) state() (data []byte, total int, truncated bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	out := make([]byte, len(c.buf))
	copy(out, c.buf)
	return out, c.total, c.truncated
}
