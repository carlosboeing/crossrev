//go:build !(darwin || dragonfly || freebsd || linux || netbsd || openbsd)

package ui

// isTerminal has no answer on a platform with no ioctl for it, and no is the
// safe one: the colour stays off and a question refuses rather than answering
// itself. CrossRev needs bash, gh and jq to run at all, so no supported host
// reaches this file.
func isTerminal(uintptr) bool { return false }
