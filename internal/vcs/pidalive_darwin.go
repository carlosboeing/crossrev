//go:build darwin

package vcs

import (
	"encoding/binary"
	"syscall"
	"time"
)

// The kinfo_proc layout this reads is the kernel's stable process table
// entry on 64-bit Darwin (<sys/proc.h>, <sys/sysctl.h>): each entry is
// 648 bytes, the process state is the char at offset 36 (extern_proc.p_stat,
// after the start time, the two pointers and the flags), the pid is the
// int32 at offset 40, and SZOMB is 5. SIDL, SRUN, SSLEEP and SSTOP are all
// live states and answer not zombie. The numbers were measured against
// `ps -o stat=` on this platform rather than taken from a header this build
// cannot include: a live process reads 2 there and a killed-but-unreaped
// one reads 5.
//
// The start time is the entry's first field (extern_proc.p_un.__p_starttime,
// a timeval), so its seconds are the int64 at offset 0. That offset is read
// from the header layout the measured ones agree with — sixteen bytes of
// timeval, two pointers and the flags put p_stat at 36 — and was not
// measured separately.
const (
	kinfoProcSize    = 648
	kinfoStartOff    = 0
	kinfoStartLength = 8
	kinfoPStatOff    = 36
	kinfoPIDOff      = 40
	kinfoStatZomb    = 5
	kinfoPIDLength   = 4
)

// kinfoOffset is where pid's entry starts in the process table, or -1 when
// the table holds no such pid.
//
// `syscall.Sysctl("kern.proc.all")` is the standard library's route to the
// table: it returns every entry concatenated, shortened by one byte because
// Sysctl drops a trailing NUL, so an entry is readable while its pid field
// fits rather than while a whole entry does.
func kinfoOffset(raw string, pid int) int {
	for off := 0; off+kinfoPIDOff+kinfoPIDLength <= len(raw); off += kinfoProcSize {
		got := int(binary.LittleEndian.Uint32([]byte(raw[off+kinfoPIDOff : off+kinfoPIDOff+kinfoPIDLength])))
		if got == pid {
			return off
		}
	}
	return -1
}

// isZombie scans the process table for pid and reads its state.
//
// A pid the table no longer holds answers zombie — it vanished between the
// signal probe and this read, which is dead by either name. A table that
// cannot be read at all answers not zombie, keeping the signal probe's
// answer rather than taking a lock over on no evidence.
func isZombie(pid int) bool {
	raw, err := syscall.Sysctl("kern.proc.all")
	if err != nil {
		return false
	}
	off := kinfoOffset(raw, pid)
	if off < 0 {
		return true
	}
	return raw[off+kinfoPStatOff] == kinfoStatZomb
}

// processStartTime is when pid's process started, to the second, as the
// process table records it. It answers false when the table cannot be read,
// holds no such pid, or carries no start time for it.
func processStartTime(pid int) (time.Time, bool) {
	raw, err := syscall.Sysctl("kern.proc.all")
	if err != nil {
		return time.Time{}, false
	}
	off := kinfoOffset(raw, pid)
	if off < 0 {
		return time.Time{}, false
	}
	seconds := int64(binary.LittleEndian.Uint64([]byte(raw[off+kinfoStartOff : off+kinfoStartOff+kinfoStartLength])))
	if seconds <= 0 {
		return time.Time{}, false
	}
	return time.Unix(seconds, 0), true
}
