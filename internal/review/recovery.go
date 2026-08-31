package review

import (
	"encoding/json"
	"fmt"

	"github.com/carlosboeing/crossrev/internal/prstate"
)

func hasRecordedFindings(m prstate.Marker) bool {
	if !m.Verdict.Present() || m.Verdict.IsNull() || m.Verdict.Value() == "" {
		return false
	}
	if len(m.Findings) == 0 || string(m.Findings) == "null" || string(m.Findings) == "[]" {
		return false
	}
	return true
}

func resumeMessage(pass int, findings json.RawMessage) string {
	return fmt.Sprintf("Resuming pass %d — the previous attempt recorded %d finding(s).", pass, findingCount(findings))
}

func findingCount(raw json.RawMessage) int {
	if len(raw) == 0 || string(raw) == "null" {
		return 0
	}
	var arr []json.RawMessage
	if err := json.Unmarshal(raw, &arr); err != nil {
		return 0
	}
	return len(arr)
}
