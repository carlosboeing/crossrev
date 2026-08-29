package ui

import "os"

// IsTerminal reports whether f is a terminal, which is what `[[ -t 1 ]]` and
// `[[ -t 0 ]]` ask (lib/ui.sh:19 and :131).
//
// It asks the terminal driver rather than the file type, and the difference is
// not academic. The usual Go shortcut — a character device by
// os.FileMode — calls /dev/null a terminal, and a run whose stdin is
// redirected from /dev/null is exactly the CI case the input fallback exists
// to refuse. Under that shortcut the fallback would accept /dev/null, read the
// immediate end of it and answer no to a question nobody was asked, instead of
// saying that there is no terminal to ask on.
//
// There is no isatty in the standard library, so this is the ioctl one, and on
// a platform where it is not implemented the answer is no. That is the safe
// direction on both readers: no colour, and a question that refuses rather than
// one that answers itself.
func IsTerminal(f *os.File) bool {
	if f == nil {
		return false
	}
	return isTerminal(f.Fd())
}
