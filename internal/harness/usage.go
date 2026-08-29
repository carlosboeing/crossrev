// usage.go — normalized token telemetry, the way lib/usage.sh normalizes it.
//
// Every adapter answers one shape of usage record, whatever its vendor calls
// the fields. `total` is a defined identity rather than a vendor number, so it
// means the same thing on every row: input_fresh + cache_read + the three
// cache-write counts + output. `reasoning` is persisted beside that total and
// never added to it, because every harness that reports reasoning nests it
// inside output already (lib/usage.sh:1-9).
//
// Two sides write the record. An adapter fills buckets and, where its harness
// supplies one, the harness's own cost. The orchestrator attaches billing mode
// and, when no harness cost survived, a table-priced estimate — pricing.go.
// Nothing here reads a credential or talks to a network.
//
// A miss is nil, never an error. None of these functions may fail a leg: the
// payload has already been read by the time telemetry is parsed, so a vendor
// that renamed a field costs a dash in a table and never an answer.
//
// # Why the JSON is decoded into an ordered tree rather than a map
//
// Two ports depend on object order and would be silently wrong without it.
// Claude's models list is `sort_by(-.total)` over `to_entries`, and jq's sort is
// stable, so two models with equal totals come back in the order the harness
// wrote them; a Go map would order them differently on every run. And
// CodexSessionID is `.. | objects | (.thread_id? // .session_id?) | first`,
// which is a document-order search. encoding/json into `map[string]any` throws
// both away, so decodeOrdered keeps them.

package harness

import (
	"bytes"
	"encoding/json"
	"io"
	"math"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
)

// Model is one entry of a usage record's `models` list: which model answered,
// and how many tokens it accounted for where the harness says.
type Model struct {
	ID string `json:"id"`
	// Total is nil where the harness reports per-model call counts rather than
	// token totals — grok's modelUsage is one (lib/usage.sh:146-148).
	Total *int64 `json:"total"`
}

// Usage is the normalized record every adapter answers.
//
// The field order is the order lib/usage.sh:31-35 writes the object in, because
// the marshalled form is compared against tests/fixtures/parity/usage.json key
// by key and byte by byte.
type Usage struct {
	InputFresh        int64    `json:"input_fresh"`
	CacheRead         int64    `json:"cache_read"`
	CacheWrite5m      int64    `json:"cache_write_5m"`
	CacheWrite1h      int64    `json:"cache_write_1h"`
	CacheWriteUnsplit int64    `json:"cache_write_unsplit"`
	Output            int64    `json:"output"`
	Reasoning         *int64   `json:"reasoning"`
	CostUSD           *float64 `json:"cost_usd"`
	CostSource        *string  `json:"cost_source"`
	PriceTable        *string  `json:"price_table"`
	Billing           *string  `json:"billing"`
	Models            []Model  `json:"models"`
	// Derived names the buckets that are a subtraction rather than a reported
	// figure. Codex is the only harness with one (lib/usage.sh:117-119).
	Derived []string `json:"derived"`
	// Total is the identity, absent until WithTotal has run. usage_zero prints
	// no `total` key and usage_with_total adds one, and the two are frozen
	// separately in the oracle as `zero` and `zero_with_total`.
	Total *int64 `json:"total,omitempty"`
}

// Zero is usage_zero (lib/usage.sh:31-36).
func Zero() Usage { return Usage{Derived: []string{}} }

// WithTotal is usage_with_total (lib/usage.sh:38-42): the six buckets summed
// into `total`, with reasoning deliberately left out.
func (u Usage) WithTotal() Usage {
	total := u.InputFresh + u.CacheRead + u.CacheWrite5m + u.CacheWrite1h +
		u.CacheWriteUnsplit + u.Output
	u.Total = &total
	return u
}

// Cached is usage_cached (lib/usage.sh:520-523): every token that came from or
// went into a cache.
func (u Usage) Cached() int64 {
	return u.CacheRead + u.CacheWrite5m + u.CacheWrite1h + u.CacheWriteUnsplit
}

// ParseClaude is usage_parse_claude (lib/usage.sh:49-98).
//
// Claude splits four buckets across every modelUsage entry and holds the
// cache-write TTL split plus the thinking count only on top-level `.usage`.
// Where the modelUsage write sum disagrees with the split, the split wins: it is
// the only source that says which rate applies, and an excess lands in
// cache_write_unsplit instead of being dropped.
func ParseClaude(raw []byte) *Usage {
	root, err := decodeOrdered(raw)
	if err != nil || root.kind != kindObject {
		return nil
	}

	entries := root.member("modelUsage").members
	split := root.member("usage").member("cache_creation")
	hasSplit := split.truthy()
	cost, hasCost := root.member("total_cost_usd").asFloat()

	// The one shape that answers null: no per-model usage, no `.usage` at all,
	// and no numeric cost to report on its own.
	if len(entries) == 0 && !root.member("usage").present() && !hasCost {
		return nil
	}

	record := Zero()
	var writeSum int64
	for _, entry := range entries {
		record.InputFresh += entry.value.number("inputTokens")
		record.CacheRead += entry.value.number("cacheReadInputTokens")
		record.Output += entry.value.number("outputTokens")
		writeSum += entry.value.number("cacheCreationInputTokens")
	}

	if hasSplit {
		record.CacheWrite5m = split.number("ephemeral_5m_input_tokens")
		record.CacheWrite1h = split.number("ephemeral_1h_input_tokens")
		if declared := record.CacheWrite5m + record.CacheWrite1h; writeSum > declared {
			record.CacheWriteUnsplit = writeSum - declared
		}
	} else {
		record.CacheWriteUnsplit = writeSum
	}

	if thinking, ok := root.member("usage").member("output_tokens_details").
		member("thinking_tokens").asInt(); ok {
		record.Reasoning = &thinking
	}
	if hasCost {
		source := costSourceHarness
		record.CostUSD, record.CostSource = &cost, &source
	}
	record.Models = claudeModels(entries)

	total := record.WithTotal()
	return &total
}

// ModelsClaude is usage_models_claude (lib/usage.sh:100-111), which answers the
// same list ParseClaude embeds. It is kept because the Bash file exports it.
func ModelsClaude(raw []byte) []Model {
	root, err := decodeOrdered(raw)
	if err != nil {
		return nil
	}
	return claudeModels(root.member("modelUsage").members)
}

// claudeModels ranks the session's models by token share.
//
// The answering model is the canonicalModel of the key holding the largest
// share. `keys | .[0]` would sort lexically, and a session where Haiku helped an
// Opus run would then name Haiku (lib/adapters/claude.sh:138-142).
func claudeModels(entries []member) []Model {
	if len(entries) == 0 {
		return nil
	}
	models := make([]Model, 0, len(entries))
	for _, entry := range entries {
		// `//` in jq falls through on null and false alone, so a
		// canonicalModel of "" is used as "" rather than replaced by the key.
		id, ok := entry.value.member("canonicalModel").asString()
		if !ok {
			id = stripBracketSuffix(entry.key)
		}
		total := entry.value.number("inputTokens") + entry.value.number("outputTokens") +
			entry.value.number("cacheReadInputTokens") + entry.value.number("cacheCreationInputTokens")
		models = append(models, Model{ID: id, Total: &total})
	}
	// jq's sort_by is stable, so equal totals keep the harness's own order.
	sort.SliceStable(models, func(i, j int) bool { return *models[i].Total > *models[j].Total })
	return models
}

// stripBracketSuffix is jq's `sub("\\[.*\\]$"; "")`: greedy, anchored at the
// end, so `claude-opus-5[1m]` is `claude-opus-5`.
func stripBracketSuffix(key string) string {
	if !strings.HasSuffix(key, "]") {
		return key
	}
	if at := strings.Index(key, "["); at >= 0 {
		return key[:at]
	}
	return key
}

// ModelReportedFromModels is usage_model_reported_from_models
// (lib/usage.sh:113-115): the first entry's id, or the empty string.
func ModelReportedFromModels(models []Model) string {
	if len(models) == 0 {
		return ""
	}
	return models[0].ID
}

// ParseCodexEvents is usage_parse_codex_events (lib/usage.sh:121-143), reading
// the NDJSON `codex exec --json` writes to stdout.
//
// `cached_input_tokens` is deliberately NOT added: it is the cached subset of
// `input_tokens`, not a figure alongside it, so fresh input is the subtraction
// and the record's one derived field. Codex's writes carry no TTL, so they land
// unsplit. No vendor total is read.
func ParseCodexEvents(events []byte) *Usage {
	nodes, err := decodeStream(events)
	if err != nil {
		return nil
	}
	var usage node
	var found bool
	for _, event := range nodes {
		if kind, _ := event.member("type").asString(); kind != "turn.completed" {
			continue
		}
		// `.usage // empty` skips a null as well as an absent key, so the last
		// turn that CARRIES usage wins rather than the last turn.
		if candidate := event.member("usage"); candidate.truthy() {
			usage, found = candidate, true
		}
	}
	if !found {
		return nil
	}

	record := Zero()
	record.CacheRead = usage.number("cached_input_tokens")
	record.InputFresh = usage.number("input_tokens") - record.CacheRead
	record.CacheWriteUnsplit = usage.number("cache_write_input_tokens")
	record.Output = usage.number("output_tokens")
	if reasoning, ok := usage.member("reasoning_output_tokens").asInt(); ok {
		record.Reasoning = &reasoning
	}
	record.Derived = []string{"input_fresh"}

	total := record.WithTotal()
	return &total
}

// ParseGrok is usage_parse_grok (lib/usage.sh:149-175).
//
// Grok's own total_tokens reconciles with the parts, but reading it would trust
// a vendor field the identity replaces. Its modelUsage carries call counts
// rather than token totals, so per-model totals are unknown and left null.
func ParseGrok(raw []byte) *Usage {
	root, err := decodeOrdered(raw)
	if err != nil {
		return nil
	}
	usage := root.member("usage")
	if !usage.truthy() {
		return nil
	}

	record := Zero()
	record.InputFresh = usage.number("input_tokens")
	record.CacheRead = usage.number("cache_read_input_tokens")
	record.CacheWriteUnsplit = usage.number("cache_creation_input_tokens")
	record.Output = usage.number("output_tokens")
	if reasoning, ok := usage.member("reasoning_tokens").asInt(); ok {
		record.Reasoning = &reasoning
	}
	if cost, ok := root.member("total_cost_usd").asFloat(); ok {
		source := costSourceHarness
		record.CostUSD, record.CostSource = &cost, &source
	}
	// jq's `keys` sorts, so the model list is alphabetical rather than the
	// harness's own order — which is the opposite of the claude parser above,
	// and is what the oracle records.
	if names := root.member("modelUsage").keys(); len(names) > 0 {
		slices.Sort(names)
		record.Models = make([]Model, 0, len(names))
		for _, name := range names {
			record.Models = append(record.Models, Model{ID: name})
		}
	}

	total := record.WithTotal()
	return &total
}

// ParseAgy is usage_parse_agy (lib/usage.sh:179-200).
//
// Antigravity's vendor total_tokens excludes cache reads — on the measured run
// it reported 48,162 of the 133,830 the parts sum to — so the parts are summed
// and the vendor total ignored (lib/adapters/agy.sh:126-129).
func ParseAgy(raw []byte) *Usage {
	root, err := decodeOrdered(raw)
	if err != nil {
		return nil
	}
	usage := root.member("usage")
	if !usage.truthy() {
		return nil
	}

	record := Zero()
	record.InputFresh = usage.number("input_tokens")
	record.CacheRead = usage.number("cache_read_tokens")
	record.Output = usage.number("output_tokens")
	if reasoning, ok := usage.member("thinking_tokens").asInt(); ok {
		record.Reasoning = &reasoning
	}

	total := record.WithTotal()
	return &total
}

// ParseOpencodeExport is usage_parse_opencode_export (lib/usage.sh:202-234),
// reading what `opencode export <sessionID>` prints.
//
// Reasoning is persisted beside the total and never added to it: every other
// harness that reports reasoning nests it inside output, and opencode publishes
// no vendor total to check against, so consistency decides
// (lib/adapters/opencode.sh:52-58).
func ParseOpencodeExport(raw []byte) *Usage {
	root, err := decodeOrdered(raw)
	if err != nil {
		return nil
	}
	tokens := root.member("info").member("tokens")
	if !tokens.truthy() {
		return nil
	}

	record := Zero()
	record.InputFresh = tokens.number("input")
	record.CacheRead = tokens.member("cache").number("read")
	record.CacheWriteUnsplit = tokens.member("cache").number("write")
	record.Output = tokens.number("output")
	if reasoning, ok := tokens.member("reasoning").asInt(); ok {
		record.Reasoning = &reasoning
	}
	if id, ok := root.member("info").member("model").member("id").asString(); ok {
		record.Models = []Model{{ID: id}}
	}

	total := record.WithTotal()
	return &total
}

// OpencodeModelID is `.info.model.id` of an export, which the adapter reports
// as the answering model (lib/adapters/opencode.sh:268).
func OpencodeModelID(exported []byte) string {
	root, err := decodeOrdered(exported)
	if err != nil {
		return ""
	}
	id, _ := root.member("info").member("model").member("id").asString()
	return id
}

// CodexSessionID is usage_codex_session_id (lib/usage.sh:246-253): the session
// this invocation ran, read from its own event stream.
//
// Both spellings are read, at any depth. Codex has renamed the field once
// already — `session_configured.session_id` became `thread.started.thread_id` —
// and a rename this function does not know about costs a dash in the model
// column, never a wrong model name.
func CodexSessionID(events []byte) string {
	nodes, err := decodeStream(events)
	if err != nil {
		return ""
	}
	for _, event := range nodes {
		// `.thread_id? // .session_id? // empty | select(type == "string")`:
		// the alternative falls through on null and false, and the select then
		// drops whatever survived if it is not a string — it does NOT retry the
		// second spelling. A numeric thread_id therefore skips this object.
		if found, ok := firstIdentifier(event, func(object node) (string, bool) {
			chosen := object.member("thread_id")
			if !chosen.truthy() {
				chosen = object.member("session_id")
			}
			return chosen.asString()
		}); ok {
			return found
		}
	}
	return ""
}

// rolloutScanCap is how many uncorrelated rollout files may be opened before
// the search gives up, so a sessions directory holding thousands cannot turn a
// miss into a long scan (lib/usage.sh:298).
const rolloutScanCap = 25

// ReadCodexRollout is usage_read_codex_rollout (lib/usage.sh:288-316): the
// model and effort this session's rollout carries.
//
// Codex's event stream names neither. Two rules keep reading the rollout safe:
// treat any failure as a miss, and never fail a leg on rollout trouble — the
// payload has been read by the time this runs.
//
// The rollout is chosen by the session id this run's own events carry, not by
// being the newest file on disk. `~/.codex/sessions` is one directory shared by
// every Codex process on the machine, so the newest file belongs to whichever
// session wrote last. Reading that one names another process's model, prices
// the leg at its rates, and fires the substitution warning on a run that never
// substituted anything.
//
// A rollout line is an envelope — {timestamp, type, payload} — and the fields
// live inside `payload`, on the `turn_context` record, so `.payload // .`
// unwraps first and falls through for a line carrying no envelope.
func ReadCodexRollout(home, sessionID string) (model, effort string) {
	if home == "" || sessionID == "" {
		return "", ""
	}
	sessions := filepath.Join(home, "sessions")
	if info, err := os.Stat(sessions); err != nil || !info.IsDir() {
		return "", ""
	}

	var candidates []string
	err := filepath.WalkDir(sessions, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			// `find … 2>/dev/null` walks past an unreadable directory.
			return nil //nolint:nilerr // a miss, never a failed leg
		}
		if !entry.IsDir() {
			candidates = append(candidates, path)
		}
		return nil
	})
	if err != nil {
		return "", ""
	}
	// `find … | sort -r`: newest-looking name first, because a rollout filename
	// opens with its timestamp.
	sort.Sort(sort.Reverse(sort.StringSlice(candidates)))

	rollout := ""
	opened := 0
	for _, candidate := range candidates {
		if strings.Contains(filepath.Base(candidate), sessionID) {
			rollout = candidate
			break
		}
		if opened >= rolloutScanCap {
			continue
		}
		opened++
		lines, err := readRollout(candidate)
		if err != nil {
			continue
		}
		if id, ok := firstRolloutField(lines, "id"); ok && id == sessionID {
			rollout = candidate
			break
		}
	}
	if rollout == "" {
		return "", ""
	}

	lines, err := readRollout(rollout)
	if err != nil {
		return "", ""
	}
	model, _ = firstRolloutField(lines, "model")
	// Effort is spelled `effort` on the turn_context record; `reasoning_effort`
	// is read too, because that is the name the same value carries elsewhere in
	// Codex's own output (lib/usage.sh:285-287).
	effort, _ = firstRolloutField(lines, "effort", "reasoning_effort")
	return model, effort
}

func readRollout(path string) ([]node, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // the path came from the walk above
	if err != nil {
		return nil, err
	}
	return decodeStream(raw)
}

// firstRolloutField is `[ .[]? | objects | (.payload // .) | objects | .<field>?
// | select(type == "string") ] | first`.
func firstRolloutField(lines []node, fields ...string) (string, bool) {
	for _, line := range lines {
		if line.kind != kindObject {
			continue
		}
		unwrapped := line
		if payload := line.member("payload"); payload.truthy() {
			unwrapped = payload
		}
		if unwrapped.kind != kindObject {
			continue
		}
		// `(.effort? // .reasoning_effort?) | select(type == "string")` picks
		// the alternative first and selects afterwards, so a non-string
		// `effort` moves to the next LINE rather than to the second spelling.
		chosen := node{}
		for _, field := range fields {
			if chosen = unwrapped.member(field); chosen.truthy() {
				break
			}
		}
		if value, ok := chosen.asString(); ok {
			return value, true
		}
	}
	return "", false
}

// firstIdentifier walks one value the way jq's `..` does — the value itself,
// then its members in order, depth first — and answers the first object the
// pick function accepts.
func firstIdentifier(value node, pick func(node) (string, bool)) (string, bool) {
	if value.kind == kindObject {
		if found, ok := pick(value); ok {
			return found, true
		}
	}
	switch value.kind {
	case kindObject:
		for _, entry := range value.members {
			if found, ok := firstIdentifier(entry.value, pick); ok {
				return found, true
			}
		}
	case kindArray:
		for _, item := range value.items {
			if found, ok := firstIdentifier(item, pick); ok {
				return found, true
			}
		}
	}
	return "", false
}

// --- an order-preserving JSON tree ------------------------------------------

type nodeKind byte

const (
	kindAbsent nodeKind = iota
	kindNull
	kindBool
	kindNumber
	kindString
	kindArray
	kindObject
)

type member struct {
	key   string
	value node
}

type node struct {
	kind    nodeKind
	members []member
	items   []node
	text    string
	number_ float64
	boolean bool
}

func (n node) present() bool { return n.kind != kindAbsent && n.kind != kindNull }

// truthy is jq's own test: everything except null and false, with an absent key
// standing in for the null jq answers when a document does not carry it. It is
// what decides every `//` alternative ported below.
func (n node) truthy() bool {
	if !n.present() {
		return false
	}
	return n.kind != kindBool || n.boolean
}

// lookup answers a member and whether the key was declared at all, which is the
// difference between `has("k")` and `.k == null`.
func (n node) lookup(key string) (node, bool) {
	if n.kind != kindObject {
		return node{}, false
	}
	for _, entry := range n.members {
		if entry.key == key {
			return entry.value, true
		}
	}
	return node{}, false
}

// member is `.k`, answering an absent node for anything that is not an object
// carrying the key — which is jq's own behaviour for indexing a null.
func (n node) member(key string) node {
	found, _ := n.lookup(key)
	return found
}

func (n node) keys() []string {
	keys := make([]string, 0, len(n.members))
	for _, entry := range n.members {
		keys = append(keys, entry.key)
	}
	return keys
}

func (n node) asString() (string, bool) {
	if n.kind != kindString {
		return "", false
	}
	return n.text, true
}

func (n node) asFloat() (float64, bool) {
	if n.kind != kindNumber {
		return 0, false
	}
	return n.number_, true
}

// asInt reads a number as a whole count.
//
// Token counts are integers in every harness's output and in every vector the
// oracle records, so the truncation below is unreachable there. jq would keep a
// fraction; this is the one place a made-up fractional count would read
// differently, and it reads LOW rather than high.
func (n node) asInt() (int64, bool) {
	value, ok := n.asFloat()
	if !ok {
		return 0, false
	}
	return int64(math.Trunc(value)), true
}

// number is `.k // 0`.
func (n node) number(key string) int64 {
	value, _ := n.member(key).asInt()
	return value
}

func decodeOrdered(raw []byte) (node, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	value, err := decodeNode(decoder)
	if err != nil {
		return node{}, err
	}
	// A second value on the same input is not one document. jq would read it as
	// a stream; the single-value parsers here are handed one object.
	if _, err := decoder.Token(); err != io.EOF {
		return node{}, errTrailingJSON
	}
	return value, nil
}

// decodeStream reads a whitespace-separated sequence of JSON values, which is
// what `jq -s` slurps and what every harness event stream is.
func decodeStream(raw []byte) ([]node, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var values []node
	for {
		value, err := decodeNode(decoder)
		if err == io.EOF {
			return values, nil
		}
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
}

var (
	errTrailingJSON    = &jsonError{"the document carries more than one JSON value"}
	errUnexpectedToken = &jsonError{"the document holds a token no JSON value begins with"}
)

type jsonError struct{ reason string }

func (e *jsonError) Error() string { return e.reason }

func decodeNode(decoder *json.Decoder) (node, error) {
	token, err := decoder.Token()
	if err != nil {
		return node{}, err
	}
	return decodeFrom(decoder, token)
}

func decodeFrom(decoder *json.Decoder, token json.Token) (node, error) {
	switch typed := token.(type) {
	case json.Delim:
		switch typed {
		case '{':
			object := node{kind: kindObject}
			for decoder.More() {
				key, err := decoder.Token()
				if err != nil {
					return node{}, err
				}
				name, ok := key.(string)
				if !ok {
					return node{}, errUnexpectedToken
				}
				value, err := decodeNode(decoder)
				if err != nil {
					return node{}, err
				}
				object.members = append(object.members, member{key: name, value: value})
			}
			if _, err := decoder.Token(); err != nil {
				return node{}, err
			}
			return object, nil
		case '[':
			array := node{kind: kindArray}
			for decoder.More() {
				value, err := decodeNode(decoder)
				if err != nil {
					return node{}, err
				}
				array.items = append(array.items, value)
			}
			if _, err := decoder.Token(); err != nil {
				return node{}, err
			}
			return array, nil
		}
		return node{}, errUnexpectedToken
	case string:
		return node{kind: kindString, text: typed}, nil
	case json.Number:
		value, err := typed.Float64()
		if err != nil {
			return node{}, err
		}
		return node{kind: kindNumber, number_: value}, nil
	case bool:
		return node{kind: kindBool, boolean: typed}, nil
	case nil:
		return node{kind: kindNull}, nil
	}
	return node{}, errUnexpectedToken
}
