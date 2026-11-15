package harness

import (
	"encoding/json"
	"slices"
	"strings"
)

// MergeUsage combines independent invocations. Cumulative snapshots from the
// same session must be reduced by their adapter before reaching this boundary.
func MergeUsage(a, b Usage) Usage {
	out := a
	out.CountsUnavailable = a.CountsUnavailable && b.CountsUnavailable
	out.InputFresh += b.InputFresh
	out.CacheRead += b.CacheRead
	out.CacheWrite5m += b.CacheWrite5m
	out.CacheWrite1h += b.CacheWrite1h
	out.CacheWriteUnsplit += b.CacheWriteUnsplit
	out.Output += b.Output
	out.Reasoning = nil
	if a.Reasoning != nil && b.Reasoning != nil {
		n := *a.Reasoning + *b.Reasoning
		out.Reasoning = &n
	}
	out = clearCost(out)
	if a.CostUSD != nil && b.CostUSD != nil && stringValue(a.CostSource) == stringValue(b.CostSource) && stringValue(a.PriceTable) == stringValue(b.PriceTable) {
		cost := *a.CostUSD + *b.CostUSD
		out.CostUSD = &cost
		out.CostSource = a.CostSource
		out.PriceTable = a.PriceTable
	}
	out.Models = nil
	for _, m := range append(slices.Clone(a.Models), b.Models...) {
		at := slices.IndexFunc(out.Models, func(other Model) bool { return other.ID == m.ID })
		if at < 0 {
			copy := m
			if m.Total != nil {
				n := *m.Total
				copy.Total = &n
			}
			out.Models = append(out.Models, copy)
			continue
		}
		if out.Models[at].Total != nil && m.Total != nil {
			n := *out.Models[at].Total + *m.Total
			out.Models[at].Total = &n
		} else {
			out.Models[at].Total = nil
		}
	}
	out.Derived = mergeStrings(a.Derived, b.Derived)
	out.Omissions = mergeStrings(a.Omissions, b.Omissions)
	if b.Normalization > out.Normalization {
		out.Normalization = b.Normalization
	}
	if out.Source != b.Source {
		out.Source = "multiple"
	}
	out.CostUnavailable = ""
	return out.WithTotal()
}

// FoldUsage records missing telemetry as an omission rather than treating an
// unmeasured invocation as a free call.
func FoldUsage(sum, attempt *Usage) *Usage {
	if attempt == nil {
		attempt = &Usage{CountsUnavailable: true, Normalization: 1, Omissions: []string{"missing_usage"}}
	}
	if sum == nil {
		copy := *attempt
		return &copy
	}
	merged := MergeUsage(*sum, *attempt)
	return &merged
}

func mergeStrings(a, b []string) []string {
	out := slices.Clone(a)
	for _, v := range b {
		if !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	return out
}
func stringValue(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// TelemetryJSON is a numeric/identity-only log entry. Payloads, prompts,
// credentials, and reasoning text never enter the telemetry event.
func TelemetryJSON(envelope *Envelope, major int) string {
	if envelope.Usage != nil {
		u := envelope.Usage
		u.Normalization = 1
		switch envelope.Harness {
		case "codex":
			u.Source = "turn.completed.usage"
		case "claude":
			u.Source = "result.modelUsage"
		case "opencode":
			u.Source = "session_export.info.tokens"
		case "agy":
			u.Source = "result.usage"
		case "grok":
			u.Source = "end.usage"
			u.Omissions = mergeStrings(u.Omissions, []string{"side_model_calls"})
		}
		if len(u.Models) == 0 && envelope.ModelReported != nil && *envelope.ModelReported != "" {
			u.Models = []Model{{ID: *envelope.ModelReported}}
		}
	}
	raw, _ := json.Marshal(struct {
		Harness  string  `json:"harness"`
		CLIMajor int     `json:"cli_major,omitempty"`
		OK       bool    `json:"ok"`
		Model    *string `json:"model_reported"`
		Effort   *string `json:"effort_reported"`
		Usage    *Usage  `json:"usage"`
	}{envelope.Harness, major, envelope.OK, envelope.ModelReported, envelope.EffortReported, envelope.Usage})
	return string(raw)
}

// TelemetryNote explains the two columns and only the gaps present in this run.
// Legacy records keep their historical rendering.
func TelemetryNote(u *Usage) string {
	if u == nil || u.Normalization == 0 {
		return ""
	}
	notes := []string{"Tokens includes input, output, reasoning, and cache traffic once. Cached is the read/write portion already included in Tokens."}
	for _, omission := range u.Omissions {
		switch omission {
		case "side_model_calls":
			notes = append(notes, "Usage excludes compaction and other side-model calls that Grok does not report.")
		case "missing_usage":
			notes = append(notes, "Usage is partial because at least one invocation did not report token counts.")
		}
	}
	if u.CostUSD == nil {
		reason := "the reported usage cannot be priced reliably"
		switch u.CostUnavailable {
		case "model_unavailable":
			reason = "the model is unavailable"
		case "model_rates_unavailable":
			reason = "the model is absent from the bundled rate table"
		case "endpoint_rates_unavailable":
			reason = "rates for the configured endpoint are unavailable"
		case "multiple_models":
			reason = "a complete cost breakdown for all models is unavailable"
		case "partial_usage":
			reason = "token usage is incomplete"
		}
		notes = append(notes, "Cost unavailable: "+reason+".")
	}
	return strings.Join(notes, " ")
}
