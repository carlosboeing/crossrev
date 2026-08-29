//go:build darwin || dragonfly || freebsd || netbsd || openbsd

package ui

import "syscall"

// ioctlReadTermios is the request that reads a terminal's line settings. The
// BSDs and Linux spell the same question differently, which is the only thing
// that varies between the two files that define it.
const ioctlReadTermios = syscall.TIOCGETA
