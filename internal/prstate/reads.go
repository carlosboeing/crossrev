// reads.go — the reads envelope every generation and pass marker carries.
//
// A review leg reads in a declared mode and an effective one: the two agree
// while the served tool serves, and differ when the leg degrades — file_tool
// resolving to supplied, or a failed self-test falling back to supplied.
// The envelope records both, the reason, and what the calls cost: how many
// read calls ran, how many reads served, how many bytes they returned, how
// many were refused, and whether any hit budget exhaustion.
//
// reads.json beside a ref-store generation carries the per-call detail with
// one flag per call: overlaps_supplied, whether the served read overlaps
// content the prompt already supplied. The marker carries the envelope even
// when the halted call publishes nothing: the envelope-only entry is the
// record the call happened.

package prstate

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ReadsEnvelope is the reads account of one pass: the modes, the reason,
// and the counts. Keys in the order the manifest and the marker write them.
type ReadsEnvelope struct {
	DeclaredMode    string `json:"declared_mode"`
	EffectiveMode   string `json:"effective_mode"`
	Reason          string `json:"reason"`
	Calls           int    `json:"calls"`
	Reads           int    `json:"reads"`
	Bytes           int64  `json:"bytes"`
	Refused         int    `json:"refused"`
	BudgetExhausted bool   `json:"budget_exhausted"`
}

// readsEnvelopeKeys is the exact key set a reads envelope carries.
var readsEnvelopeKeys = []string{
	"declared_mode", "effective_mode", "reason", "calls", "reads", "bytes", "refused", "budget_exhausted",
}

// NewReadsEnvelope builds the envelope the legs record.
func NewReadsEnvelope(declared, effective, reason string, calls, reads int, bytes int64, refused int, budgetExhausted bool) ReadsEnvelope {
	return ReadsEnvelope{
		DeclaredMode:    declared,
		EffectiveMode:   effective,
		Reason:          reason,
		Calls:           calls,
		Reads:           reads,
		Bytes:           bytes,
		Refused:         refused,
		BudgetExhausted: budgetExhausted,
	}
}

// DecodeReadsEnvelope reads an envelope back, refusing an unknown mode, a
// negative count, or a key set that is not exactly the envelope's.
func DecodeReadsEnvelope(raw json.RawMessage) (ReadsEnvelope, error) {
	obj, err := parseObject(raw)
	if err != nil {
		return ReadsEnvelope{}, coverageErrorf("malformed reads envelope: %v", err)
	}
	if !exactKeys(obj, readsEnvelopeKeys) {
		return ReadsEnvelope{}, coverageErrorf("reads envelope keys do not match the schema")
	}
	get := func(key string) json.RawMessage {
		v, _ := obj.get(key)
		return v
	}
	var envelope ReadsEnvelope
	if err := json.Unmarshal(get("declared_mode"), &envelope.DeclaredMode); err != nil {
		return ReadsEnvelope{}, coverageErrorf("invalid declared_mode")
	}
	if err := json.Unmarshal(get("effective_mode"), &envelope.EffectiveMode); err != nil {
		return ReadsEnvelope{}, coverageErrorf("invalid effective_mode")
	}
	if err := json.Unmarshal(get("reason"), &envelope.Reason); err != nil {
		return ReadsEnvelope{}, coverageErrorf("invalid reason")
	}
	for _, key := range []string{"calls", "reads", "refused"} {
		value, ok := decodeInt(get(key))
		if !ok {
			return ReadsEnvelope{}, coverageErrorf("invalid %s", key)
		}
		switch key {
		case "calls":
			envelope.Calls = value
		case "reads":
			envelope.Reads = value
		case "refused":
			envelope.Refused = value
		}
	}
	bytes, ok := decodeInt64(get("bytes"))
	if !ok || bytes < 0 {
		return ReadsEnvelope{}, coverageErrorf("invalid bytes")
	}
	envelope.Bytes = bytes
	if err := json.Unmarshal(get("budget_exhausted"), &envelope.BudgetExhausted); err != nil {
		return ReadsEnvelope{}, coverageErrorf("invalid budget_exhausted")
	}
	for _, mode := range []string{envelope.DeclaredMode, envelope.EffectiveMode} {
		switch mode {
		case "served", "file_tool", "supplied":
		default:
			return ReadsEnvelope{}, coverageErrorf("unknown read mode %q", mode)
		}
	}
	return envelope, nil
}

// ReadsCall is one served read inside reads.json.
type ReadsCall struct {
	Path             string `json:"path"`
	Revision         string `json:"revision"`
	StartLine        int    `json:"start_line"`
	EndLine          int    `json:"end_line"`
	Bytes            int64  `json:"bytes"`
	Served           bool   `json:"served"`
	OverlapsSupplied bool   `json:"overlaps_supplied"`
}

// ReadsStats sums one call log: whether the harness shook the server's
// hand, and what the reads cost. Handshake is the post-call check that the
// log carries initialize and tools_list: without both, no read in the log
// can be trusted to have come through the served tool.
type ReadsStats struct {
	Handshake       bool
	Calls           int
	Reads           int
	Bytes           int64
	Refused         int
	BudgetExhausted bool
}

// ParseReadLog reads one call's server log: the read calls with their
// ranges and byte counts, and the summed stats. Malformed lines are
// skipped, never trusted; an empty or missing log answers zero stats with
// no handshake.
func ParseReadLog(log []byte) ([]ReadsCall, ReadsStats) {
	var calls []ReadsCall
	var stats ReadsStats
	var initialize, toolsList bool
	for _, line := range strings.Split(string(log), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		var entry struct {
			Event   string          `json:"event"`
			Payload json.RawMessage `json:"payload"`
		}
		if err := json.Unmarshal([]byte(trimmed), &entry); err != nil {
			continue
		}
		switch entry.Event {
		case "initialize":
			initialize = true
		case "tools_list":
			toolsList = true
		case "read":
			var read struct {
				Path      string `json:"path"`
				Revision  string `json:"revision"`
				StartLine int    `json:"start_line"`
				EndLine   int    `json:"end_line"`
				Bytes     int64  `json:"bytes"`
			}
			if err := json.Unmarshal(entry.Payload, &read); err != nil {
				continue
			}
			if read.Bytes < 0 {
				continue
			}
			calls = append(calls, ReadsCall{
				Path:      read.Path,
				Revision:  read.Revision,
				StartLine: read.StartLine,
				EndLine:   read.EndLine,
				Bytes:     read.Bytes,
				Served:    true,
			})
			stats.Reads++
			stats.Bytes += read.Bytes
		case "refused":
			var refused struct {
				Reason string `json:"reason"`
			}
			if err := json.Unmarshal(entry.Payload, &refused); err != nil {
				continue
			}
			stats.Refused++
			if refused.Reason == "budget_exhausted" {
				stats.BudgetExhausted = true
			}
		}
	}
	stats.Handshake = initialize && toolsList
	stats.Calls = stats.Reads + stats.Refused
	return calls, stats
}

// BuildReadsJSON renders reads.json for one ref-store generation: the
// served calls with overlaps_supplied set for every read whose path the
// prompt already supplied.
func BuildReadsJSON(calls []ReadsCall, suppliedPaths []string) ([]byte, error) {
	supplied := make(map[string]bool, len(suppliedPaths))
	for _, path := range suppliedPaths {
		supplied[path] = true
	}
	out := make([]ReadsCall, 0, len(calls))
	for _, call := range calls {
		call.OverlapsSupplied = call.Served && supplied[call.Path]
		out = append(out, call)
	}
	document := map[string]any{"v": 1, "calls": out}
	raw, err := json.Marshal(document)
	if err != nil {
		return nil, fmt.Errorf("encoding reads.json: %w", err)
	}
	return raw, nil
}
