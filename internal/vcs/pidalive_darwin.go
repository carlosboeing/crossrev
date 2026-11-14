//go:build darwin

package vcs

import (
	"encoding/binary"
	"syscall"
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
const (
	kinfoProcSize  = 648
	kinfoPStatOff  = 36
	kinfoPIDOff    = 40
	kinfoStatZomb  = 5
	kinfoPIDLength = 4
)

// isZombie scans the process table for pid and reads its state.
//
// `syscall.Sysctl("kern.proc.all")` is the standard library's route to the
// table: it returns every entry concatenated, shortened by one byte because
// Sysctl drops a trailing NUL, so an entry is readable while its pid field
// fits rather than while a whole entry does. A pid the table no longer holds
// answers zombie — it vanished between the signal probe and this read, which
// is dead by either name. A table that cannot be read at all answers not
// zombie, keeping the signal probe's answer rather than taking a lock over
// on no evidence.
func isZombie(pid int) bool {
	raw, err := syscall.Sysctl("kern.proc.all")
	if err != nil {
		return false
	}
	for off := 0; off+kinfoPIDOff+kinfoPIDLength <= len(raw); off += kinfoProcSize {
		got := int(binary.LittleEndian.Uint32([]byte(raw[off+kinfoPIDOff : off+kinfoPIDOff+kinfoPIDLength])))
		if got != pid {
			continue
		}
		return raw[off+kinfoPStatOff] == kinfoStatZomb
	}
	return true
}
