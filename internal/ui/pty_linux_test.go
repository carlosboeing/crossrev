//go:build linux

package ui_test

import (
	"os"
	"strconv"
	"syscall"
	"testing"
	"unsafe"
)

// openPTY hands back the slave side of a new pseudo-terminal. See the darwin
// file for why a test needs one; only the two ioctls that name the slave differ
// between the platforms.
//
// Nil means this platform would not give one out, and the caller skips.
func openPTY(t *testing.T) *os.File {
	t.Helper()
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		return nil
	}
	t.Cleanup(func() { master.Close() })

	unlock := int32(0)
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, master.Fd(),
		syscall.TIOCSPTLCK, uintptr(unsafe.Pointer(&unlock))); errno != 0 {
		return nil
	}
	var number uint32
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, master.Fd(),
		syscall.TIOCGPTN, uintptr(unsafe.Pointer(&number))); errno != 0 {
		return nil
	}
	// O_NOCTTY, so the test process never adopts this as its controlling
	// terminal: the suite must not acquire one it did not have.
	slave, err := os.OpenFile("/dev/pts/"+strconv.Itoa(int(number)), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		return nil
	}
	t.Cleanup(func() { slave.Close() })
	return slave
}
