//go:build darwin

package ui_test

import (
	"os"
	"syscall"
	"testing"
	"unsafe"
)

// openPTY hands back the slave side of a new pseudo-terminal.
//
// It exists because the second arm of Terminal.Open is guarded by IsTerminal,
// and under `go test` the process's own stdin is never a terminal — so a test
// that hands over os.Stdin exercises one arm and says nothing about the order
// of the two. A pty slave answers the same ioctl a real terminal does, which
// makes both arms available at once.
//
// Nil means this platform would not give one out, and the caller skips.
func openPTY(t *testing.T) *os.File {
	t.Helper()
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		return nil
	}
	t.Cleanup(func() { master.Close() })

	for _, request := range []uintptr{syscall.TIOCPTYGRANT, syscall.TIOCPTYUNLK} {
		if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, master.Fd(), request, 0); errno != 0 {
			return nil
		}
	}
	var name [128]byte
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, master.Fd(),
		syscall.TIOCPTYGNAME, uintptr(unsafe.Pointer(&name[0]))); errno != 0 {
		return nil
	}
	end := 0
	for end < len(name) && name[end] != 0 {
		end++
	}
	// O_NOCTTY, so the test process never adopts this as its controlling
	// terminal: the suite must not acquire one it did not have.
	slave, err := os.OpenFile(string(name[:end]), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		return nil
	}
	t.Cleanup(func() { slave.Close() })
	return slave
}
