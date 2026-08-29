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
	switch HarnessName(s) {
	case HarnessClaude:
		return HarnessClaude, nil
	case HarnessCodex:
		return HarnessCodex, nil
	case HarnessAgy:
		return HarnessAgy, nil
	case HarnessGrok:
		return HarnessGrok, nil
	case HarnessOpencode:
		return HarnessOpencode, nil
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

// ErrWriteCapability is returned for a capability the adapters never receive.
var ErrWriteCapability = errors.New("a write capability is either no or yes")

// WriteCapabilities lists both capabilities, least capable first.
func WriteCapabilities() []WriteCapability { return []WriteCapability{WriteNo, WriteYes} }

// ParseWriteCapability accepts only the two words lib/run.sh:488-489 writes.
//
// The empty string is refused rather than read as `no`. The Bash default is the
// word `no`, so an unset capability is a value that went missing on the way
// here, and guessing at it is how a reviewer leg ends up able to write.
func ParseWriteCapability(s string) (WriteCapability, error) {
	switch WriteCapability(s) {
	case WriteNo:
		return WriteNo, nil
	case WriteYes:
		return WriteYes, nil
	}
	return "", fmt.Errorf("%w: %q", ErrWriteCapability, s)
}

// String renders the capability as the adapters receive it.
func (w WriteCapability) String() string { return string(w) }

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
// GH_TOKEN, GITHUB_TOKEN, GH_ENTERPRISE_TOKEN and GITHUB_ENTERPRISE_TOKEN
// before starting the model-facing process.
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

// ErrInvocationRequest is returned when an invocation is missing something an
// adapter cannot run without.
var ErrInvocationRequest = errors.New("an invocation is incomplete")

// NewInvocationRequest validates an invocation before an adapter is started.
//
// The arguments are run_invoke's, in its order (lib/run.sh:774). The prompt,
// the schema and the workdir are required because adapter_claude takes all
// three positionally and defaults none of them (lib/adapters/claude.sh:16). The
// model, the effort and the endpoint are optional for the same reason: the
// adapter defaults each to the empty string (lib/adapters/claude.sh:17) and the
// harness picks its own.
func NewInvocationRequest(harness HarnessName, promptPath, schemaPath, workdir, model, effort, endpoint string, write WriteCapability) (InvocationRequest, error) {
	if _, err := ParseHarnessName(string(harness)); err != nil {
		return InvocationRequest{}, fmt.Errorf("%w: %w", ErrInvocationRequest, err)
	}
	for _, required := range []struct {
		name  string
		value string
	}{
		{name: "prompt path", value: promptPath},
		{name: "schema path", value: schemaPath},
		{name: "workdir", value: workdir},
	} {
		if required.value == "" {
			return InvocationRequest{}, fmt.Errorf("%w: no %s", ErrInvocationRequest, required.name)
		}
	}
	if _, err := ParseWriteCapability(string(write)); err != nil {
		return InvocationRequest{}, fmt.Errorf("%w: %w", ErrInvocationRequest, err)
	}
	return InvocationRequest{
		Harness:    harness,
		PromptPath: promptPath,
		SchemaPath: schemaPath,
		Workdir:    workdir,
		Model:      model,
		Effort:     effort,
		Endpoint:   endpoint,
		Write:      write,
	}, nil
}

// Envelope is what an adapter returns: nine keys, in this order, identical
// across lib/adapters/claude.sh:120-121 and :154-158,
// lib/adapters/codex.sh:96-97, and lib/adapters/agy.sh:105-106 and :137-139.
//
// The adapter writes all nine itself, the reported model, the reported effort,
// the token total and the usage record included. usage_attach_envelope at
// lib/usage.sh:503 does not add them: it returns early when `.usage` is already
// null (lib/usage.sh:507), and otherwise merges billing and cost into a record
// the adapter already wrote, then refreshes `tokens` from it.
//
// No field carries omitempty. Every adapter writes all nine keys explicitly,
// null included — lib/run.sh:838 writes `{"ok":false,"payload":null,...}` when
// an adapter produces nothing at all — so a key dropped for being empty is a
// shape the shipped tool never emits. Each optional field is a pointer for the
// same reason: a harness that reported nothing and one that reported an empty
// string or a zero are three different facts about the run.
type Envelope struct {
	OK             bool            `json:"ok"`
	Payload        json.RawMessage `json:"payload"`
	Harness        HarnessName     `json:"harness"`
	Endpoint       *string         `json:"endpoint"`
	ModelReported  *string         `json:"model_reported"`
	EffortReported *string         `json:"effort_reported"`
	Tokens         *int64          `json:"tokens"`
	Usage          *Usage          `json:"usage"`
	Error          *string         `json:"error"`
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

// ErrBilling is returned for a billing mode lib/usage.sh never writes.
var ErrBilling = errors.New("a billing mode is not one lib/usage.sh names")

// ParseBilling accepts the three named modes and the empty string.
//
// The empty string is a value rather than an absence: usage_billing_for falls
// through to it where the credential descriptor names no mode, and
// lib/usage.sh:492 writes that to the marker as null, which every reader takes
// back off with `// ""`.
func ParseBilling(s string) (Billing, error) {
	switch Billing(s) {
	case BillingNone:
		return BillingNone, nil
	case BillingEndpoint:
		return BillingEndpoint, nil
	case BillingAPI:
		return BillingAPI, nil
	case BillingSubscription:
		return BillingSubscription, nil
	}
	return "", fmt.Errorf("%w: %q", ErrBilling, s)
}

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

// ErrCostSource is returned for a cost source lib/usage.sh never writes.
var ErrCostSource = errors.New("a cost source is not one lib/usage.sh names")

// ParseCostSource accepts the two named sources and the empty string, which is
// what _usage_clear_cost leaves behind (lib/usage.sh:27).
func ParseCostSource(s string) (CostSource, error) {
	switch CostSource(s) {
	case CostSourceNone:
		return CostSourceNone, nil
	case CostSourceHarness:
		return CostSourceHarness, nil
	case CostSourceTable:
		return CostSourceTable, nil
	}
	return "", fmt.Errorf("%w: %q", ErrCostSource, s)
}

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
// The field order is the marker's byte order and is load-bearing. A usage
// record is embedded verbatim in a pull-request comment body (lib/state.sh:159),
// so a key in the wrong place is a diff on every pull request from then on.
// encoding/json emits struct fields in declaration order, and jq appends a new
// key at the end — which is why Total is declared last, after Derived: it is
// absent from usage_zero and added afterwards by usage_with_total
// (lib/usage.sh:38).
//
// Reasoning, CostUSD, PriceTable, CostSource and Billing are pointers because
// the shipped record writes them as null when nothing reported them, and a
// reported zero — or a reported empty string — is a different fact from nothing
// reported. _usage_clear_cost at lib/usage.sh:27 sets cost_source back to null,
// and lib/usage.sh:492 deliberately writes null rather than "" for billing.
//
// Models is a plain slice because the shell writes models:null. Derived is not,
// because lib/usage.sh:81 and lib/usage.sh:93 write derived:[] unconditionally;
// MarshalJSON below is what makes a nil Derived render as the empty array.
type Usage struct {
	InputFresh        int64        `json:"input_fresh"`
	CacheRead         int64        `json:"cache_read"`
	CacheWrite5m      int64        `json:"cache_write_5m"`
	CacheWrite1h      int64        `json:"cache_write_1h"`
	CacheWriteUnsplit int64        `json:"cache_write_unsplit"`
	Output            int64        `json:"output"`
	Reasoning         *int64       `json:"reasoning"`
	CostUSD           *float64     `json:"cost_usd"`
	CostSource        *CostSource  `json:"cost_source"`
	PriceTable        *string      `json:"price_table"`
	Billing           *Billing     `json:"billing"`
	Models            []ModelUsage `json:"models"`
	Derived           []string     `json:"derived"`
	Total             *int64       `json:"total,omitempty"`
}

// MarshalJSON renders the record the way the shipped jq pipeline writes it.
//
// The only thing it changes is Derived: a nil slice would marshal to null, and
// no writer in lib/usage.sh ever produces null there.
func (u Usage) MarshalJSON() ([]byte, error) {
	if u.Derived == nil {
		u.Derived = []string{}
	}
	type usage Usage
	return json.Marshal(usage(u))
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
