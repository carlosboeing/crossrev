//go:build linux

package vcs

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// isZombie reads the process state from /proc, where a terminated but
// unreaped process carries `Z` (and a dead one `X`). The state is the first
// field after the closing parenthesis of the command name, which may itself
// contain spaces and parentheses, so the split is from the last `)`.
//
// A read that fails because the entry is already gone answers zombie: the
// process vanished between the signal probe and this read, which is dead by
// either name. Any other failure answers not zombie, keeping the signal
// probe's answer rather than taking a lock over on no evidence.
func isZombie(pid int) bool {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return os.IsNotExist(err)
	}
	text := string(raw)
	close := strings.LastIndex(text, ")")
	if close < 0 {
		return false
	}
	fields := strings.Fields(text[close+1:])
	if len(fields) == 0 {
		return false
	}
	return fields[0] == "Z" || fields[0] == "X"
}

// The start time in /proc/<pid>/stat is field 22 of proc(5), which is the
// twentieth field after the command name's closing parenthesis. It counts
// clock ticks since boot, and the tick /proc reports in is USER_HZ, which
// is 100 on every Linux architecture Go supports.
const (
	statStartField = 19
	statUserHZ     = 100
)

// processStartTime is when pid's process started, to the second: the boot
// time /proc/stat carries as `btime`, plus the ticks after boot at which
// the process started. It answers false when either cannot be read.
func processStartTime(pid int) (time.Time, bool) {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return time.Time{}, false
	}
	text := string(raw)
	close := strings.LastIndex(text, ")")
	if close < 0 {
		return time.Time{}, false
	}
	fields := strings.Fields(text[close+1:])
	if len(fields) <= statStartField {
		return time.Time{}, false
	}
	ticks, err := strconv.ParseInt(fields[statStartField], 10, 64)
	if err != nil {
		return time.Time{}, false
	}
	boot, ok := bootTime()
	if !ok {
		return time.Time{}, false
	}
	return time.Unix(boot+ticks/statUserHZ, 0), true
}

// bootTime is the `btime` line of /proc/stat: when the machine booted, in
// seconds since the epoch.
func bootTime() (int64, bool) {
	raw, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0, false
	}
	for _, line := range strings.Split(string(raw), "\n") {
		rest, found := strings.CutPrefix(line, "btime ")
		if !found {
			continue
		}
		boot, err := strconv.ParseInt(strings.TrimSpace(rest), 10, 64)
		return boot, err == nil
	}
	return 0, false
}
