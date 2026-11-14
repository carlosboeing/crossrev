//go:build linux

package vcs

import (
	"os"
	"strconv"
	"strings"
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
