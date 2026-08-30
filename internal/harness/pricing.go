// pricing.go — billing mode, table costs and the two presentation helpers, the
// way lib/usage.sh does them.
//
// Rates are per-token dollars upstream. The arithmetic scales each rate to
// nano-dollars so the sum stays in integers well below float precision and one
// division rounds once (lib/usage.sh:420-425).
//
// Three rules refuse to price rather than guess, and all three are reproduced
// below: a bucket with tokens in it whose rate the extract does not list, an
// unresolvable cache-write TTL whose rates differ, and a per-request
// long-context break a cumulative total cannot rule out.

package harness

import (
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
)

// The two values `cost_source` may hold. A record priced from neither carries
// null, which is a third state rather than a third name.
const (
	costSourceHarness = "harness"
	costSourceTable   = "table"
)

// The billing modes usage_billing_for answers (lib/usage.sh:363-374). The empty
// string is a fourth answer and means no claim: a credential whose form
// CrossRev cannot tell apart deserves none.
const (
	BillingEndpoint     = "endpoint"
	BillingAPI          = "api"
	BillingSubscription = "subscription"
)

// anthropicAPIKeyName is the variable whose presence in a harness's `env_keep`
// makes an API key the thing a run was charged to (lib/usage.sh:331).
const anthropicAPIKeyName = "ANTHROPIC_API_KEY"

// Prices is lib/prices.json: an ordered list of model keys with their rates.
//
// Order is kept because two of the three price-key rungs depend on it —
// `limit(1; …)` takes the first match in document order, and `sort_by | last`
// takes the last of a tie.
type Prices struct {
	version string
	entries []priceEntry
	index   map[string]int
}

type priceEntry struct {
	key string
	// rates is the entry's value as written. It is a node rather than a
	// map[string]float64 because not every top-level key of the extract holds
	// an object: `version` holds a string. Key reads this kind to skip such a
	// key on all three rungs, which is what the shell now does too.
	rates node
}

// LoadPrices parses a price extract.
func LoadPrices(raw []byte) (Prices, error) {
	root, err := decodeOrdered(raw)
	if err != nil {
		return Prices{}, err
	}
	if root.kind != kindObject {
		return Prices{}, errUnexpectedToken
	}
	table := Prices{
		entries: make([]priceEntry, 0, len(root.members)),
		index:   make(map[string]int, len(root.members)),
	}
	for _, entry := range root.members {
		table.index[entry.key] = len(table.entries)
		table.entries = append(table.entries, priceEntry{key: entry.key, rates: entry.value})
	}
	table.version, _ = root.member("version").asString()
	return table, nil
}

var (
	embeddedPrices     Prices
	embeddedPricesErr  error
	embeddedPricesOnce sync.Once
)

// PriceTable is the compiled-in extract, parsed once.
func PriceTable() (Prices, error) {
	embeddedPricesOnce.Do(func() {
		embeddedPrices, embeddedPricesErr = LoadPrices(priceExtract)
	})
	return embeddedPrices, embeddedPricesErr
}

// Version is `.version` of the extract, which a priced record records so a row
// says which table produced the figure.
func (p Prices) Version() string { return p.version }

// Key is usage_price_key (lib/usage.sh:381-418): the listed key for a reported
// model id.
//
// Lowercased, any `[...]` suffix stripped, then three rungs: an exact match
// against a listed key, a match against a listed key's bare id without its
// `provider/` prefix, else the longest bare id the report contains — so
// `grok-4.6-build` prices as `xai/grok-4.6`. Empty means unlisted, which prices
// as a refusal rather than a guess.
//
// Every rung requires the value to be an object, because not every top-level
// key of the extract is a model. `version` holds a string, and matching it
// returned a key whose rates cannot be read, which cost the caller the whole
// usage record ([#170]). The shape is checked rather than the name, so a later
// non-model key behaves correctly without anyone remembering this one.
//
// [#170]: https://github.com/carlosboeing/crossrev/issues/170
func (p Prices) Key(reported string) string {
	reported = strings.ToLower(reported)
	if at := strings.Index(reported, "["); at >= 0 {
		// `${reported%%\[*}` cuts at the FIRST bracket, so `[only-a-suffix]`
		// becomes the empty string and answers nothing.
		reported = reported[:at]
	}
	if reported == "" {
		return ""
	}
	if at, listed := p.index[reported]; listed && p.entries[at].priceable() {
		return reported
	}
	for _, entry := range p.entries {
		if entry.priceable() && bareID(entry.key) == reported {
			return entry.key
		}
	}
	best, bestLength := "", -1
	for _, entry := range p.entries {
		if !entry.priceable() {
			continue
		}
		bare := bareID(entry.key)
		// `sort_by(.bare | length) | last` is a stable ascending sort, so the
		// last of an equal-length tie wins — hence >= rather than >.
		if strings.Contains(reported, bare) && len(bare) >= bestLength {
			best, bestLength = entry.key, len(bare)
		}
	}
	return best
}

// priceable is `select((.value | type) == "object")`: an entry whose value can
// hold rates at all.
func (e priceEntry) priceable() bool { return e.rates.kind == kindObject }

// bareID is `.key | split("/") | last`.
func bareID(key string) string {
	if at := strings.LastIndex(key, "/"); at >= 0 {
		return key[at+1:]
	}
	return key
}

var longContextBreak = regexp.MustCompile(`^input_cost_per_token_above_([0-9]+)k_tokens$`)

// tieredRateName matches the price keys that describe a service tier rather than
// the standard one (lib/usage.sh:462).
var tieredRateName = regexp.MustCompile(`flex|priority|batches`)

// Price is usage_price (lib/usage.sh:426-492): the record with a table-priced
// cost attached, or with the cost triple cleared.
func (p Prices) Price(u Usage, model string) Usage {
	key := p.Key(model)
	if key == "" {
		return clearCost(u)
	}
	at, listed := p.index[key]
	if !listed {
		return clearCost(u)
	}
	// Key answers only a key whose value is an object, so rates always holds
	// rates by the time it is read here.
	rates := p.entries[at].rates

	buckets := []struct {
		tokens int64
		rate   node
	}{
		{u.InputFresh, rates.member("input_cost_per_token")},
		{u.Output, rates.member("output_cost_per_token")},
		{u.CacheRead, rates.member("cache_read_input_token_cost")},
		{u.CacheWrite5m, rates.member("cache_creation_input_token_cost")},
		{u.CacheWrite1h, cacheWrite1hRate(rates)},
		{u.CacheWriteUnsplit, rates.member("cache_creation_input_token_cost")},
	}

	// A bucket holding tokens whose rate the extract does not list is a
	// refusal, never a zero: an entry can omit a rate entirely — gpt-5.5 lists
	// no cache-write rate at all — and defaulting the missing one to zero
	// prices those tokens free and understates the leg without saying so. Only
	// a nonzero bucket counts, so an entry that omits a rate it never needs
	// still prices.
	for _, bucket := range buckets {
		if bucket.tokens > 0 && bucket.rate.kind != kindNumber {
			return clearCost(u)
		}
	}

	// An unsplit write cannot be priced where the two TTLs cost differently,
	// because nothing says which one it was. The test is `has(…)` and then a
	// value comparison, so an entry listing only the standard rate — gpt-5.6 is
	// one — prices its unsplit writes at that rate rather than refusing.
	if u.CacheWriteUnsplit > 0 {
		if above, declared := rates.lookup("cache_creation_input_token_cost_above_1hr"); declared {
			if !sameValue(above, rates.member("cache_creation_input_token_cost")) {
				return clearCost(u)
			}
		}
	}

	// A per-request long-context break a cumulative total cannot rule out.
	if threshold, found := p.longContextThreshold(rates); found {
		cumulative := u.InputFresh + u.CacheRead + u.CacheWrite5m + u.CacheWrite1h + u.CacheWriteUnsplit
		if cumulative >= threshold*1000 {
			return clearCost(u)
		}
	}

	// Every `// 0` below is reached only for a bucket that is already zero,
	// because a nonzero one with no listed rate refused above.
	var nanoDollars float64
	for _, bucket := range buckets {
		rate, _ := bucket.rate.asFloat()
		nanoDollars += float64(bucket.tokens) * roundNano(rate)
	}
	cost := nanoDollars / 1e9
	source, version := costSourceTable, p.version
	u.CostUSD, u.CostSource, u.PriceTable = &cost, &source, &version
	return u
}

// cacheWrite1hRate is `$r.cache_creation_input_token_cost_above_1hr //
// $r.cache_creation_input_token_cost`.
func cacheWrite1hRate(rates node) node {
	if above := rates.member("cache_creation_input_token_cost_above_1hr"); above.truthy() {
		return above
	}
	return rates.member("cache_creation_input_token_cost")
}

// longContextThreshold is the `$bn` of lib/usage.sh:460-465: the thousands
// figure of the first standard-tier long-context break the entry lists.
//
// jq's `keys` sorts, so "first" is the lowest-sorting name rather than the
// first written.
func (p Prices) longContextThreshold(rates node) (int64, bool) {
	names := rates.keys()
	slices.Sort(names)
	for _, name := range names {
		match := longContextBreak.FindStringSubmatch(name)
		if match == nil || tieredRateName.MatchString(name) {
			continue
		}
		threshold, err := strconv.ParseInt(match[1], 10, 64)
		if err != nil {
			continue
		}
		return threshold, true
	}
	return 0, false
}

// roundNano is jq's `((rate * 1e9) | round)`. jq's round is
// round-half-away-from-zero on a double, which is exactly what math.Round does.
func roundNano(rate float64) float64 { return math.Round(rate * 1e9) }

// sameValue is jq's `==` for the two rate values a refusal compares. An absent
// key reads as null there, so a listed rate never equals a missing one.
func sameValue(left, right node) bool {
	if left.kind != right.kind {
		return false
	}
	switch left.kind {
	case kindNumber:
		return left.number_ == right.number_
	case kindString:
		return left.text == right.text
	case kindBool:
		return left.boolean == right.boolean
	default:
		return true
	}
}

// clearCost is _usage_clear_cost (lib/usage.sh:27-29).
func clearCost(u Usage) Usage {
	u.CostUSD, u.CostSource, u.PriceTable = nil, nil, nil
	return u
}

// BillingFor is usage_billing_for (lib/usage.sh:363-374).
//
// Billing mode is derived from what the orchestrator already holds, never
// detected. A named endpoint wins first because it changes what a cost means; a
// descriptor that keeps the vendor API key wins over an oauth token because
// env_keep lets both survive into one run and the key is what the run was
// charged to.
//
// Everything else comes from the credential descriptor rather than from a
// subscription default. A harness whose stored credential can be either an
// oauth grant or a provider API key — opencode's `{type, key}` entry is one —
// records `unknown` and gets no billing claim at all, because naming the wrong
// one is worse than naming none.
//
// The API key arrives as a bool rather than being read here. The Bash reads
// `${ANTHROPIC_API_KEY:-}` out of its own environment; Go takes the answer from
// the caller so that the decision is testable without one, and so that this
// package does not join the list of environment readers ADR 0001 bounds.
func BillingFor(doc Document, harness, endpoint string, anthropicAPIKeySet bool) string {
	if endpoint != "" && endpoint != "null" && endpoint != "vendor" {
		return BillingEndpoint
	}
	if anthropicAPIKeySet && keepsAnthropicAPIKey(doc, harness) {
		return BillingAPI
	}
	// Only subscription and api are claims; unknown, a missing field and an
	// unknown harness all print nothing (lib/usage.sh:341-350).
	entry, found := doc.For(harness)
	if !found {
		return ""
	}
	switch entry.Credential.Billing {
	case BillingSubscription, BillingAPI:
		return entry.Credential.Billing
	}
	return ""
}

// keepsAnthropicAPIKey is _usage_keeps_api_key (lib/usage.sh:327-335): which
// harnesses keep a vendor API key alive is recorded per harness in env_keep,
// not known here by name.
func keepsAnthropicAPIKey(doc Document, harness string) bool {
	entry, found := doc.For(harness)
	if !found {
		return false
	}
	return slices.Contains(entry.Credential.EnvKeep, anthropicAPIKeyName)
}

// Attach is usage_attach (lib/usage.sh:498-517): the orchestrator-side merge.
//
// Billing always; the cost triple rewritten when the billing mode forbids one (a
// named endpoint discards whatever the adapter reported), kept and marked when
// the harness supplied it, table-priced when neither happened.
func (p Prices) Attach(u Usage, doc Document, harness, endpoint, model string, anthropicAPIKeySet bool) Usage {
	billing := BillingFor(doc, harness, endpoint, anthropicAPIKeySet)
	if billing == BillingEndpoint {
		u = clearCost(u)
		u.Billing = &billing
		return u
	}
	if u.CostUSD != nil {
		source := costSourceHarness
		u.CostSource, u.PriceTable = &source, nil
		u.Billing = optionalString(billing)
		return u
	}
	u = p.Price(u, model)
	u.Billing = optionalString(billing)
	return u
}

func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

// costPattern is the guard usage_format_cost applies before printing
// (lib/usage.sh:545).
var costPattern = regexp.MustCompile(`^-?[0-9]+(\.[0-9]+)?([eE][+-]?[0-9]+)?$`)

// FormatCost is usage_format_cost (lib/usage.sh:544-550).
//
// It takes the value as text rather than as a number, because that is what it
// is asked to render: the caller reads `.cost_usd` out of a record where the
// key can be a number, a null, or absent, and the em dash is the answer to
// every one of those that is not a number.
func FormatCost(value string) string {
	if !costPattern.MatchString(value) {
		return "—"
	}
	number, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return "—"
	}
	return fmt.Sprintf("~$%.2f", number)
}

// Footnote is usage_footnote (lib/usage.sh:556-578): the footnote's inner
// sentences, composed from three clauses because no one sentence is true of
// every combination of cost source and billing mode.
//
// When no cost clause applies nothing prints, and the caller still renders its
// own gap sentence.
func Footnote(costSource, billing string) string {
	if costSource == "" || costSource == "null" {
		return ""
	}
	if billing == BillingEndpoint {
		return ""
	}
	var opening string
	switch costSource {
	case costSourceHarness:
		opening = "Cost is the harness's own estimate, not an amount charged."
	case costSourceTable:
		opening = "Cost is an estimate, not an amount charged, calculated from published API rates for the nearest listed model, which may not be the exact variant that answered."
	default:
		return ""
	}
	out := opening + " " + "Cache reads bill at about a tenth of the input rate and cache writes above it, so the token columns alone do not indicate cost."
	switch billing {
	case BillingSubscription:
		out += " A leg on a subscription inside its included usage is invoiced nothing."
	case BillingAPI:
		out += " The provider's invoice remains authoritative."
	}
	return out
}
