package core

import (
	"encoding/json"
	"errors"
	"fmt"
)

// HarnessName is a harness CrossRev drives directly.
type HarnessName string

// The five harnesses, in the order lib/harnesses.json declares them. A harness
// CrossRev does not drive is reached through `endpoints:` instead, and is never
// one of these.
const (
	HarnessClaude   HarnessName = "claude"
	HarnessCodex    HarnessName = "codex"
	HarnessAgy      HarnessName = "agy"
	HarnessGrok     HarnessName = "grok"
	HarnessOpencode HarnessName = "opencode"
)

// ErrHarnessName is returned for a name no descriptor carries.
var ErrHarnessName = errors.New("there is no adapter for that harness")

// HarnessNames lists the five harnesses in descriptor order.
func HarnessNames() []HarnessName {
	return []HarnessName{HarnessClaude, HarnessCodex, HarnessAgy, HarnessGrok, HarnessOpencode}
}

// ParseHarnessName accepts only the five declared names.
func ParseHarnessName(s string) (HarnessName, error) {
	for _, h := range HarnessNames() {
		if HarnessName(s) == h {
			return h, nil
		}
	}
	return "", fmt.Errorf("%w: %q", ErrHarnessName, s)
}

// String renders the harness as the descriptor names it.
func (h HarnessName) String() string { return string(h) }

// WriteCapability is whether the harness process may change files.
type WriteCapability string

// The two capabilities, spelled as the adapters receive them from
// lib/run.sh:488.
const (
	WriteNo  WriteCapability = "no"
	WriteYes WriteCapability = "yes"
)

// WriteCapabilityFor derives the capability from the leg role.
//
// Deliberately not configurable. Only the resolver changes files; granting the
// reviewer write access widens the blast radius of a prompt injection carried
// in a diff for nothing in return (lib/run.sh:484-489). Anything that is not
// the resolver reads `no`, which is the Bash default.
func WriteCapabilityFor(role LegRole) WriteCapability {
	if role == RoleResolver {
		return WriteYes
	}
	return WriteNo
}

// EndpointVendor is the endpoint name that means "no named endpoint".
// lib/usage.sh:363 treats it the same as an empty endpoint when it decides how
// a leg was paid for.
const EndpointVendor = "vendor"

// InvocationRequest is what the orchestrator hands an adapter: the arguments
// `run_invoke` passes through at lib/run.sh:774.
//
// The GitHub credential is not among them, and never is. The adapters strip
// GH_TOKEN, GITHUB_TOKEN and GH_ENTERPRISE_TOKEN before starting the
// model-facing process.
type InvocationRequest struct {
	Harness    HarnessName
	PromptPath string
	SchemaPath string
	Workdir    string
	Model      string
	Effort     string
	Endpoint   string
	Write      WriteCapability
}

// Envelope is what an adapter returns.
//
// `ok`, `payload` and `error` come from the adapter itself; the reported model,
// the reported effort, the token total and the usage record are attached by the
// orchestrator afterwards (lib/usage.sh:503). Each optional field is a pointer,
// because a harness that reported nothing and one that reported an empty string
// or a zero are three different facts about the run.
type Envelope struct {
	OK             bool            `json:"ok"`
	Payload        json.RawMessage `json:"payload,omitempty"`
	Error          string          `json:"error,omitempty"`
	ModelReported  *string         `json:"model_reported,omitempty"`
	EffortReported *string         `json:"effort_reported,omitempty"`
	Tokens         *int64          `json:"tokens,omitempty"`
	Usage          *Usage          `json:"usage,omitempty"`
}

// Billing is how a leg was paid for.
type Billing string

// The billing modes lib/usage.sh:363 produces. BillingNone is the empty string
// it falls through to, where the credential descriptor cannot name one: naming
// the wrong mode is worse than naming none.
const (
	BillingNone         Billing = ""
	BillingEndpoint     Billing = "endpoint"
	BillingAPI          Billing = "api"
	BillingSubscription Billing = "subscription"
)

// BillingModes lists the three named modes, in the order lib/usage.sh decides
// between them.
func BillingModes() []Billing { return []Billing{BillingEndpoint, BillingAPI, BillingSubscription} }

// String renders the billing mode as the marker holds it.
func (b Billing) String() string { return string(b) }

// CostSource is where a cost figure came from.
type CostSource string

// The two named sources: the harness's own estimate (lib/usage.sh:493) and the
// published rate table (lib/usage.sh:471). CostSourceNone is the empty state,
// which is also what a named endpoint rewrites the triple to.
const (
	CostSourceNone    CostSource = ""
	CostSourceHarness CostSource = "harness"
	CostSourceTable   CostSource = "table"
)

// CostSources lists the two named sources.
func CostSources() []CostSource { return []CostSource{CostSourceHarness, CostSourceTable} }

// String renders the cost source as the marker holds it.
func (c CostSource) String() string { return string(c) }

// ModelUsage is one answering model's share of a run, as usage_models_claude
// builds it at lib/usage.sh:100: an id and a token total, sorted by total
// descending.
type ModelUsage struct {
	ID    string `json:"id"`
	Total int64  `json:"total"`
}

// Usage is the telemetry record a leg carries, with the fields usage_zero
// declares at lib/usage.sh:31.
//
// Reasoning, CostUSD and PriceTable are pointers because the shipped record
// writes them as null when nothing reported them, and a reported zero is a
// different fact from nothing reported.
type Usage struct {
	InputFresh        int64        `json:"input_fresh"`
	CacheRead         int64        `json:"cache_read"`
	CacheWrite5m      int64        `json:"cache_write_5m"`
	CacheWrite1h      int64        `json:"cache_write_1h"`
	CacheWriteUnsplit int64        `json:"cache_write_unsplit"`
	Output            int64        `json:"output"`
	Reasoning         *int64       `json:"reasoning"`
	Total             *int64       `json:"total,omitempty"`
	CostUSD           *float64     `json:"cost_usd"`
	CostSource        CostSource   `json:"cost_source"`
	PriceTable        *string      `json:"price_table"`
	Billing           Billing      `json:"billing"`
	Models            []ModelUsage `json:"models"`
	Derived           []string     `json:"derived"`
}

// TokenTotal sums the six token buckets usage_with_total sums at
// lib/usage.sh:38. Reasoning is reported beside the buckets and is not one of
// them, so it is not added here.
func (u Usage) TokenTotal() int64 {
	return u.InputFresh + u.CacheRead + u.CacheWrite5m + u.CacheWrite1h + u.CacheWriteUnsplit + u.Output
}

// Cached sums the cache buckets, as usage_cached does at lib/usage.sh:520.
func (u Usage) Cached() int64 {
	return u.CacheRead + u.CacheWrite5m + u.CacheWrite1h + u.CacheWriteUnsplit
}
