//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package ui

import (
	"syscall"
	"unsafe"
)

// isTerminal asks the terminal driver for the file's line settings. Only a
// terminal has any, so the call succeeding is the answer — this is what every
// isatty is, including the one Bash's `-t` calls.
func isTerminal(fd uintptr) bool {
	var settings syscall.Termios
	_, _, errno := syscall.Syscall6(syscall.SYS_IOCTL, fd, ioctlReadTermios,
		uintptr(unsafe.Pointer(&settings)), 0, 0, 0)
	return errno == 0
}
