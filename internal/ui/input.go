package ui

import (
	"errors"
	"io"
	"os"
	"strings"
)

// DefaultTTYPath is the controlling terminal.
const DefaultTTYPath = "/dev/tty"

// ErrNoInput is returned by an Input that has nowhere to read from.
var ErrNoInput = errors.New("no terminal is attached")

// ErrNoAnswer is returned when the source ended before the reader answered.
// Bash's `read` reports the same thing the same way: a source that ends without
// a newline is a failed read even when it handed over some bytes, so a "y" with
// no newline behind it is not a yes.
var ErrNoAnswer = errors.New("the answer ended before it was given")

// Input is where an answer comes from.
//
// Open is called once per question rather than once per session, which is what
// `read -r reply <"$src"` does: the redirection opens the source for that read
// alone (lib/ui.sh:152 and :161).
type Input interface {
	Open() (io.ReadCloser, error)
}

// Terminal resolves an answer source the way _ui_input_source does
// (lib/ui.sh:129-134).
//
// The controlling terminal first, so prompting still works with the tool on the
// right-hand side of a pipe — `curl … | sh` is a supported install path and its
// stdin is the script. But the controlling terminal is not always there: cron,
// a CI step, a container without one, and some editor-embedded shells all fail
// to open it. Falling back to stdin covers those, and having neither is worth a
// real message rather than the operating system's.
//
// The two arms are not the same path, which is the whole reason both fields are
// here rather than resolved inside. A session at a terminal takes the first and
// a runner cannot, so a test that wants to exercise either says which: TTYPath
// points at a file the test made, and Stdin is whatever the test opened.
type Terminal struct {
	// TTYPath is the controlling terminal. Empty means DefaultTTYPath.
	TTYPath string
	// Stdin is the second arm, used only when it is itself a terminal. Nil
	// means there is no second arm.
	Stdin *os.File
}

func (t Terminal) Open() (io.ReadCloser, error) {
	path := t.TTYPath
	if path == "" {
		path = DefaultTTYPath
	}
	if f, err := os.Open(path); err == nil {
		return f, nil
	}
	if t.Stdin != nil && IsTerminal(t.Stdin) {
		// The caller's stdin is not this package's to close.
		return io.NopCloser(t.Stdin), nil
	}
	return nil, ErrNoInput
}

// open resolves the source for one question, or returns the error that becomes
// the no-terminal refusal.
func (o *IO) open() (io.ReadCloser, error) {
	if o == nil || o.Input == nil {
		return nil, ErrNoInput
	}
	return o.Input.Open()
}

// noInput is the refusal when there is nowhere to read an answer from
// (_ui_no_input, lib/ui.sh:136-139).
func (o *IO) noInput() error {
	return o.Die("CrossRev needs to ask you something, but no terminal is attached",
		"Run this in a terminal directly. Editor-embedded and captured shells often have no controlling terminal, which is what this is.")
}

// readAnswer reads one line, and reports whether the line was finished.
//
// One byte at a time, because the source may be a terminal shared with whatever
// runs next and a buffered read would swallow what it did not need — which is
// what `read` avoids on a seekable source by seeking back, and on a terminal by
// reading a byte at a time.
//
// Leading and trailing spaces and tabs are dropped, because `read -r reply`
// with one variable splits on IFS and hands over what is left.
func readAnswer(r io.Reader) (string, error) {
	var line []byte
	buf := make([]byte, 1)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			if buf[0] == '\n' {
				return strings.Trim(string(line), " \t"), nil
			}
			line = append(line, buf[0])
			continue
		}
		if err != nil {
			// A source that ends without a newline is a failed read, whatever
			// it handed over on the way.
			return "", ErrNoAnswer
		}
	}
}
