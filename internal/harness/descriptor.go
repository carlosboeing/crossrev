// descriptor.go — lib/harnesses.json, validated the way lib/harnesses.sh
// validates it.
//
// The descriptor is a trusted input. It names install commands written into a
// generated workflow, environment variable names, credential destinations, and
// quarantine paths handed to a move (lib/harnesses.sh:2-8). A malformed entry
// therefore reaches a side effect, so validation runs at load and fails closed.
//
// `harnesses` and `not_driven` are ARRAYS of objects carrying a `name`, not
// objects keyed by name, and lib/harnesses.sh:10-15 records why: jq keeps the
// last of two duplicate object keys and discards the first silently, so a
// name-keyed map cannot express "names are unique" at all. encoding/json does
// the same thing, so the reason survives the port unchanged.
//
// # Why validation runs over an untyped decode
//
// Four of the twelve checks are about a value's SHAPE rather than its content:
// `harnesses` given as an object, `legs` given as a bare string, a `version`
// that is any JSON value at all, and a quarantine entry that is not a string.
// Decoding straight into the typed structs below would turn each of those into
// an unmarshal error whose text is encoding/json's rather than the descriptor's,
// so the check would still fail — with a different message, on a different
// line, for a reason the operator cannot act on. Validate therefore reads the
// same generic tree jq reads, in the same order, and Load decodes only after it
// has passed.
//
// # The one divergence: where jq raises a type error
//
// harness_validate ends with `2>/dev/null || printf 'the descriptor is not
// parseable JSON'`, so ANY type error inside the filter — not just a parse
// failure — comes back as that sentence. jq raises one whenever a builtin meets
// the wrong type, and the shipped filter has four that can:
//
//	$h[].name, $nd[].name        indexing a member that is not an object
//	test(…)                      matching a name that is not a string
//	contains(…)                  a pinned version that is not a string
//	(.quarantine_shared // [])[] iterating something that is not an array
//
// Go reports the fault it found instead. Seventeen cases were measured across a
// 71-case mutation sweep, and every one of them is one of those four:
//
//	harnesses: "review" | 3           bash: unparseable   go: must be arrays
//	not_driven: "x" | 1               bash: unparseable   go: must be arrays
//	harnesses: [1,2]                  bash: unparseable   go: name null twice
//	not_driven: [1,2]                 bash: unparseable   go: a not_driven name
//	harnesses: [… , "x"]              bash: unparseable   go: name null pattern
//	credential.secret: 5 | true|false bash: unparseable   go: env name pattern
//	credential.env_names: [5]         bash: unparseable   go: env name pattern
//	credential.staging.env: 2         bash: unparseable   go: env name pattern
//	credential: "x"                   bash: unparseable   go: out-of-range
//	install: "x"                      bash: unparseable   go: out-of-range
//	install.command: 1                bash: unparseable   go: installer
//	install.pinned_version: 1         bash: unparseable   go: installer
//	quarantine_shared: "x"            bash: unparseable   go: ACCEPTED
//
// Sixteen of the seventeen name the actual fault where the shell names none,
// and that is a deliberate improvement rather than a slip: the shell's sentence
// is an artefact of where jq's bindings sit, and it tells an operator with a
// well-formed JSON file that their JSON does not parse.
//
// The seventeenth is not an improvement. A `quarantine_shared` that is not an
// array is ACCEPTED here and refused there. It is left because inventing a
// message the shell never prints is a larger divergence than this one, and
// because Load fails immediately after: `[]string` cannot decode a string, so
// the document is refused with encoding/json's error rather than reaching a
// caller. Nothing gets a Document out of it either way.

package harness

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"

	"github.com/carlosboeing/crossrev/internal/cred"
)

// Version is the only descriptor version this build reads
// (lib/harnesses.sh:38-39).
const Version = 1

// Install is one harness's `.install` block: how a generated workflow puts the
// CLI on a runner.
type Install struct {
	// Kind is script or npm, the set lib/harnesses.sh:58 validates against.
	Kind string `json:"kind"`
	// URL is the installer's home, and must be non-empty.
	URL string `json:"url"`
	// Command is the line a workflow runs, and must be non-empty.
	Command string `json:"command"`
	// PinnedVersion is the version Command pins, empty when it pins none.
	// When set, Command has to contain it (lib/harnesses.sh:78-79).
	PinnedVersion string `json:"pinned_version"`
	// NeedsCredential says the installer itself needs one.
	NeedsCredential bool `json:"needs_credential"`
	// Hint is the URL an operator is sent to when the CLI is absent. The codex
	// adapter reads it into its refusal (lib/adapters/codex.sh:24).
	Hint string `json:"hint"`
}

// Staging is `.credential.staging` — where a restored credential is written and
// which environment variable points the harness at it.
//
// It repeats internal/cred.Staging rather than embedding it, because that type
// keeps an unexported field separating `"path": null` from `"path": ""` and
// exports no way to build one. The two are checked against each other by
// TestStagingMatchesTheCredentialView.
type Staging struct {
	// Kind is one of none, file, home or env (lib/harnesses.sh:59).
	Kind string `json:"kind"`
	// Env is the variable a prepared credential is announced in, empty for a
	// harness that stages nothing.
	Env string `json:"env"`
	// Path is the file inside the scratch home: relative, non-empty, and never
	// containing a `..` segment.
	Path string `json:"path"`
}

// Credential is one harness's `.credential` block.
//
// Every key the descriptor carries is declared here, which is the opposite of
// internal/cred's rule and is right for the opposite reason: that package reads
// the credential half and leaves the rest out so a field nobody reads cannot go
// stale, while this package is the descriptor's reader. A key declared nowhere
// would be a key the validator below cannot police and the generated tables in
// README.md and docs/credentials.md read anyway.
type Credential struct {
	// Archetype is A, B or C. A pairing decision reads it
	// (lib/preflight.sh:222-228).
	Archetype string `json:"archetype"`
	// Provenance is measured, inferred or vendor-documented.
	Provenance string `json:"provenance"`
	// AccessTokenSeconds is the token's lifetime, or nil where the store holds
	// no expiry at all.
	AccessTokenSeconds *int64 `json:"access_token_seconds"`
	// Store is prose naming where the credential lives, rendered into the
	// generated documentation tables.
	Store string `json:"store"`
	// Billing is subscription, api or unknown (lib/harnesses.sh:62-63).
	Billing string `json:"billing"`
	// Secret is the environment variable a hosted runner delivers the
	// credential in, empty when the harness needs none.
	Secret string `json:"secret"`
	// EnvNames is every variable this harness's credential can arrive in.
	EnvNames []string `json:"env_names"`
	// EnvKeep is what this harness may still hold once it is running.
	EnvKeep []string `json:"env_keep"`
	// SeedCommand is what an operator runs once to mint the credential.
	SeedCommand string `json:"seed_command"`
	// SeedHint is the sentence a refusal ends with.
	SeedHint string `json:"seed_hint"`
	// Staging is where a restored credential goes.
	Staging Staging `json:"staging"`
	// AccessTokenPath is the path to the access token inside the stored
	// credential. The JSON key still says jq because the file is shared with
	// the Bash side; the Go name does not, for the reason
	// internal/cred/descriptor.go:121-125 gives.
	AccessTokenPath string `json:"access_token_jq"`
	// AssertFresh is whether an expiry can be read out of this store at all.
	AssertFresh bool `json:"assert_fresh"`
	// Refresher is whether this credential rotates.
	Refresher bool `json:"refresher"`
}

// Descriptor is one driven harness: everything lib/harnesses.json says about it.
type Descriptor struct {
	// Name is the descriptor's own name, and the name of its adapter.
	Name string `json:"name"`
	// Binary is the program the adapter runs.
	Binary string `json:"binary"`
	// ProductName is the human name. It is `opencode` for opencode, which is
	// the one product whose own name is lowercase.
	ProductName string `json:"product_name"`
	// Install is how a workflow puts the CLI on a runner.
	Install Install `json:"install"`
	// SchemaStyle is inline, path or prompt: how this harness is handed the
	// output schema. Claude Code takes it inline and Codex takes a file path
	// (lib/adapters/claude.sh:45-49), and opencode takes neither.
	SchemaStyle string `json:"schema_style"`
	// SchemaNative is whether the harness constrains its own output to the
	// schema. False for opencode alone, and it is what arms the extra
	// shape-retry (lib/adapters/opencode.sh:8-15).
	SchemaNative bool `json:"schema_native"`
	// SandboxArgs are the hardening arguments the adapter must pass.
	SandboxArgs []string `json:"sandbox_args"`
	// Quarantine are the paths this harness auto-loads configuration from.
	Quarantine []string `json:"quarantine"`
	// Credential is the credential block.
	Credential Credential `json:"credential"`

	// legs is `.legs`, nil when the descriptor carries no such key. An absent
	// legs field means both legs, which is why the four entries that predate
	// the field carry no edit (lib/harnesses.sh:150-153). Unexported so that
	// "absent" and "empty" cannot be confused by a caller; Legs answers the
	// resolved set and ServesLeg answers the question.
	legs []string
}

// Legs is the legs this harness may serve, in descriptor order.
//
// An entry with no `legs` key answers both, which is the rule
// lib/harnesses.sh:156-158 encodes as `(… .legs) // ["review","resolve"]`.
func (d Descriptor) Legs() []string {
	if d.legs == nil {
		return []string{LegReview, LegResolve}
	}
	return slices.Clone(d.legs)
}

// DeclaresLegs reports that the descriptor carries a `legs` key for this
// harness, which is a different fact from which legs it serves.
func (d Descriptor) DeclaresLegs() bool { return d.legs != nil }

// The descriptor's own vocabulary for the two legs. run_leg_settings and
// preflight receive reviewer and resolver, so their callers normalise
// (lib/harnesses.sh:150-153).
const (
	LegReview  = "review"
	LegResolve = "resolve"
)

// NotDriven is one harness the descriptor names and does not drive.
type NotDriven struct {
	// Name is the harness's name.
	Name string `json:"name"`
	// Reason is the sentence explaining why there is no adapter behind it.
	Reason string `json:"reason"`
	// Archetype, Provenance and AccessTokenSeconds sit at the top level here
	// rather than under a credential block, because a not-driven harness has
	// no adapter to hold one. The generated tables read them
	// (scripts/render-harness-docs.sh).
	Archetype          string `json:"archetype"`
	Provenance         string `json:"provenance"`
	AccessTokenSeconds *int64 `json:"access_token_seconds"`
}

// Pairing is `.default_pairing`: which harness reviews and which resolves when
// no repository policy says.
type Pairing struct {
	Reviewer string `json:"reviewer"`
	Resolver string `json:"resolver"`
}

// Document is a whole parsed descriptor.
type Document struct {
	raw              []byte
	version          int
	endpointHost     string
	defaultPairing   Pairing
	quarantineShared []string
	notDriven        []NotDriven
	harnesses        []Descriptor
	index            map[string]int
	credentials      cred.Document
}

// ErrDescriptor is returned for a descriptor that cannot be read.
//
// The Bash loader prints the first problem and dies (lib/harnesses.sh:101-104).
// Every message below is that function's, word for word, because it is what an
// operator sees and what tests/test-harnesses.sh:105-175 asserts on.
var ErrDescriptor = fmt.Errorf("the harness descriptor is invalid")

// ErrNotJSON is the answer to a descriptor jq could not parse, which
// lib/harnesses.sh:81-82 reports as a validation problem rather than as a
// parse failure.
var ErrNotJSON = fmt.Errorf("the descriptor is not parseable JSON")

var (
	harnessNamePattern = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	envNamePattern     = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)
)

// Validate is harness_validate (lib/harnesses.sh:27-83): twelve checks in one
// pass, answering the first problem or the empty string.
//
// The order is the Bash function's `elif` chain and is load-bearing. A
// descriptor with two faults reports the earlier one, and
// TestTheEarlierOfTwoFaultsIsTheOneReported freezes which that is — against the
// live shell, over eight pairs whose two faults produce different sentences.
// Until that test existed this comment named a guarantee nothing checked: every
// fixture carried exactly one fault, so swapping two checks changed no answer.
func Validate(raw []byte) string {
	var root any
	if err := json.Unmarshal(raw, &root); err != nil {
		return ErrNotJSON.Error()
	}
	document, isObject := root.(map[string]any)
	if !isObject && root != nil {
		// Indexing a null answers a null in jq rather than erroring, so a
		// literal `null` document reaches the version check and is refused
		// there. Measured: `echo null | jq -r .version` prints null and exits
		// 0, where an array, a string, a number and a true each error. Only
		// those four reach the Bash function's `2>/dev/null || printf` and come
		// back as this sentence.
		return ErrNotJSON.Error()
	}

	harnessList, harnessesAreArray := jsonArray(document["harnesses"])
	notDrivenList, notDrivenIsArray := jsonArray(document["not_driven"])

	// `[ $h[].name ]` and `[ $nd[].name ]` run before jq's `elif` chain, so a
	// `harnesses` OR a `not_driven` that is not an array of objects makes jq
	// error before check two is reached. That is the head of the divergence
	// family this file's header sets out in full; the answers below are the
	// specific fault rather than the shell's unparseable-JSON sentence. No
	// shipped or tested descriptor reaches it — tests/test-harnesses.sh:123
	// uses an object, which both implementations answer identically.
	names := memberNames(harnessList)
	others := memberNames(notDrivenList)

	if version, isVersion := document["version"].(float64); !isVersion || int(version) != Version || version != float64(int(version)) {
		return fmt.Sprintf("the descriptor's version is %s, and this build reads version 1", toJSON(document["version"]))
	}
	if !harnessesAreArray || !notDrivenIsArray {
		return "harnesses and not_driven must be arrays of objects carrying a name"
	}
	if len(names) == 0 {
		return "the descriptor names no harnesses"
	}
	if duplicate, found := firstDuplicate(names); found {
		// jq interpolates the duplicate bare, without tojson, so a string name
		// prints unquoted here and the pattern message below prints quoted.
		return fmt.Sprintf("harness name %s appears more than once", jqText(duplicate))
	}
	for _, name := range names {
		// `select((type == "string") and test(…) | not)`: a name that is not a
		// string fails the conjunction and is reported, with its own JSON in
		// the message rather than an empty pair of quotes.
		text, isText := name.(string)
		if !isText || !harnessNamePattern.MatchString(text) {
			return fmt.Sprintf("harness name %s is not [a-z][a-z0-9-]*", toJSON(name))
		}
	}
	if _, found := firstDuplicate(others); found {
		return "a not_driven name is duplicated, or is also a driven harness"
	}
	if _, found := firstDuplicate(append(slices.Clone(names), others...)); found {
		return "a not_driven name is duplicated, or is also a driven harness"
	}
	for _, entry := range harnessList {
		for _, name := range declaredEnvNames(entry) {
			if !envNamePattern.MatchString(name) {
				return fmt.Sprintf("environment variable name %s is not [A-Z_][A-Z0-9_]*", name)
			}
		}
	}
	for _, entry := range harnessList {
		if !inRange(entry) {
			return fmt.Sprintf("harness %s carries an out-of-range archetype, provenance, schema_style, install kind or staging kind", entryName(entry))
		}
	}
	for _, entry := range harnessList {
		// `(.credential.billing? // "unknown")` at lib/harnesses.sh:62. jq's
		// `//` steps past a null and a false as well as an absent key, so all
		// three read as unknown and are accepted. Measured, because reading the
		// key as merely DECLARED refused two documents the shell loads:
		//
		//	{"credential":{"billing":null}}  -> unknown
		//	{"credential":{"billing":false}} -> unknown
		//	{"credential":{"billing":""}}    -> (empty, and refused)
		//
		// The empty string is truthy in jq, so it is not one of the three and
		// is refused by both. A non-string that is truthy is refused as well,
		// which is what leaving billing empty here does.
		billing := "unknown"
		if value, declared := valueAt(entry, "credential", "billing"); declared && truthyJSON(value) {
			billing, _ = value.(string)
		}
		if billing != "subscription" && billing != "api" && billing != "unknown" {
			return fmt.Sprintf("harness %s carries a credential billing that is not subscription, api or unknown", entryName(entry))
		}
	}
	for _, entry := range harnessList {
		raw, declared := valueAt(entry, "legs")
		if !declared {
			continue
		}
		legs, isArray := jsonArray(raw)
		if !isArray || len(legs) == 0 || !everyLeg(legs) {
			return fmt.Sprintf("harness %s carries a legs field that is not a non-empty array drawn from review and resolve", entryName(entry))
		}
	}
	for _, path := range declaredPaths(harnessList, document["quarantine_shared"]) {
		if !relativePath(path) {
			return fmt.Sprintf("quarantine or destination path %s is absolute, empty, or contains a .. segment", toJSON(path))
		}
	}
	for _, entry := range harnessList {
		if !installerComplete(entry) {
			return fmt.Sprintf("harness %s has an installer with no url, no command, or a pinned version its command does not carry", entryName(entry))
		}
	}
	return ""
}

// Load validates a descriptor and then decodes it.
//
// The credential view is parsed here too, from the same bytes, so that a caller
// holding a Document cannot end up with a credential document built from a
// different file.
func Load(raw []byte) (Document, error) {
	if problem := Validate(raw); problem != "" {
		return Document{}, fmt.Errorf("%w: %s\n   It drives sourced paths, install commands, environment names and quarantine paths, so CrossRev stops rather than acting on it. Fix lib/harnesses.json.", ErrDescriptor, problem)
	}

	var wire struct {
		Version          int         `json:"version"`
		EndpointHost     string      `json:"endpoint_host"`
		DefaultPairing   Pairing     `json:"default_pairing"`
		QuarantineShared []string    `json:"quarantine_shared"`
		NotDriven        []NotDriven `json:"not_driven"`
		Harnesses        []struct {
			Descriptor
			// DeclaredLegs is `.legs` as written, with nil meaning the key is
			// absent. Named apart from Descriptor.Legs so the embedded
			// method is not shadowed by a field.
			DeclaredLegs *[]string `json:"legs"`
		} `json:"harnesses"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		return Document{}, fmt.Errorf("%w: %w", ErrDescriptor, err)
	}

	credentials, err := cred.Load(raw)
	if err != nil {
		return Document{}, err
	}

	loaded := Document{
		raw:              slices.Clone(raw),
		version:          wire.Version,
		endpointHost:     wire.EndpointHost,
		defaultPairing:   wire.DefaultPairing,
		quarantineShared: wire.QuarantineShared,
		notDriven:        wire.NotDriven,
		harnesses:        make([]Descriptor, 0, len(wire.Harnesses)),
		index:            make(map[string]int, len(wire.Harnesses)),
		credentials:      credentials,
	}
	for _, entry := range wire.Harnesses {
		descriptor := entry.Descriptor
		if entry.DeclaredLegs != nil {
			descriptor.legs = *entry.DeclaredLegs
		}
		loaded.index[descriptor.Name] = len(loaded.harnesses)
		loaded.harnesses = append(loaded.harnesses, descriptor)
	}
	return loaded, nil
}

var (
	embedded     Document
	embeddedErr  error
	embeddedOnce sync.Once
)

// Descriptors is the compiled-in descriptor, parsed once.
//
// It answers an error rather than panicking, for the reason cred.Descriptors
// does: the bytes are generated, and a copy that went in malformed should stop
// the command that needed it rather than the process at init time.
func Descriptors() (Document, error) {
	embeddedOnce.Do(func() {
		embedded, embeddedErr = Load(harnessDescriptor)
	})
	return embedded, embeddedErr
}

// Raw is the bytes this document was loaded from.
func (d Document) Raw() []byte { return slices.Clone(d.raw) }

// Version is `.version`.
func (d Document) Version() int { return d.version }

// EndpointHost is `.endpoint_host`: the harness a named endpoint is reached
// through. The grok adapter reads it into its refusal
// (lib/adapters/grok.sh:29-31).
func (d Document) EndpointHost() string { return d.endpointHost }

// DefaultPairing is `.default_pairing`.
func (d Document) DefaultPairing() Pairing { return d.defaultPairing }

// Credentials is the same descriptor read as internal/cred reads it.
func (d Document) Credentials() cred.Document { return d.credentials }

// Names is harness_names (lib/harnesses.sh:125-129): every driven harness, in
// descriptor order.
func (d Document) Names() []string {
	names := make([]string, 0, len(d.harnesses))
	for _, entry := range d.harnesses {
		names = append(names, entry.Name)
	}
	return names
}

// Known is harness_known (lib/harnesses.sh:140).
func (d Document) Known(name string) bool {
	_, found := d.index[name]
	return found
}

// For answers one harness's descriptor, and whether the document carries it.
//
// The slices are cloned. Returning the struct copies it, but a struct copy
// shares its slices' backing arrays, so a caller writing to the quarantine list
// it got back would edit the document and change every later answer — including
// which paths a sandbox moves.
func (d Document) For(name string) (Descriptor, bool) {
	at, found := d.index[name]
	if !found {
		return Descriptor{}, false
	}
	entry := d.harnesses[at]
	entry.SandboxArgs = slices.Clone(entry.SandboxArgs)
	entry.Quarantine = slices.Clone(entry.Quarantine)
	entry.legs = slices.Clone(entry.legs)
	entry.Credential.EnvNames = slices.Clone(entry.Credential.EnvNames)
	entry.Credential.EnvKeep = slices.Clone(entry.Credential.EnvKeep)
	return entry, true
}

// ServesLeg is harness_serves_leg (lib/harnesses.sh:154-159).
//
// A name the descriptor does not carry serves EVERY leg, because that is jq's
// answer. The filter is
//
//	((.harnesses[] | select(.name == $n) | .legs) // ["review","resolve"])
//	  | index($l) != null
//
// and the parenthesised selection produces NO values for an unknown name, so
// `//` falls through to the default pair rather than to false. Measured on the
// shipped descriptor, `harness_serves_leg nosuch review` and `… nosuch resolve`
// both return 0. A zero Descriptor declares no legs, so Legs() answers the same
// default pair and the lookup's `found` is not read.
//
// The laxness is covered rather than accidental: an unknown name is refused by
// run_leg_settings' adapter test (lib/run.sh:506) with a message that names the
// fault, before this check runs at lib/run.sh:526.
func (d Document) ServesLeg(name, leg string) bool {
	entry, _ := d.For(name)
	return slices.Contains(entry.Legs(), leg)
}

// NamesForLeg is harness_names_for_leg (lib/harnesses.sh:163-168).
func (d Document) NamesForLeg(leg string) []string {
	var names []string
	for _, entry := range d.harnesses {
		if slices.Contains(entry.Legs(), leg) {
			names = append(names, entry.Name)
		}
	}
	return names
}

// NotDrivenReason is harness_not_driven (lib/harnesses.sh:142-148): the reason
// this name has no adapter, and whether the descriptor names it at all.
//
// An entry with an empty reason answers false, which is what the Bash `[[ -n
// "$reason" ]] || return 1` does — the reason IS the answer there.
func (d Document) NotDrivenReason(name string) (string, bool) {
	for _, entry := range d.notDriven {
		if entry.Name == name && entry.Reason != "" {
			return entry.Reason, true
		}
	}
	return "", false
}

// NotDriven is every not-driven entry, in descriptor order.
func (d Document) NotDriven() []NotDriven { return slices.Clone(d.notDriven) }

// SandboxArgs is sandbox_args_for (lib/sandbox.sh:104-107): the hardening
// arguments this harness must be run with.
//
// The Bash answer is one space-joined string, which the codex adapter then
// appends as a SINGLE argv entry (lib/adapters/codex.sh:56). That works only
// while the list holds one element, and it holds one. Go answers the list, and
// the codex adapter appends each element as its own argument — which is the
// same argv today and the correct one if a second argument is ever added.
func (d Document) SandboxArgs(name string) []string {
	entry, found := d.For(name)
	if !found {
		return nil
	}
	return entry.SandboxArgs
}

// QuarantinePaths is _sandbox_paths (lib/sandbox.sh:46-49): every path any
// harness auto-loads configuration from, plus the shared list, sorted and
// deduplicated the way jq's `unique` sorts.
func (d Document) QuarantinePaths() []string {
	var paths []string
	for _, entry := range d.harnesses {
		paths = append(paths, entry.Quarantine...)
	}
	paths = append(paths, d.quarantineShared...)
	slices.Sort(paths)
	return slices.Compact(paths)
}

// NamesHuman is harness_names_human (lib/harnesses.sh:171-182): the driven
// harnesses as a sentence fragment, for message text that has to name the set.
func (d Document) NamesHuman() string { return NamesHuman(d.Names()) }

// NamesHuman is _names_human (lib/harnesses.sh:171-178) over any list:
// "claude, codex, agy and opencode".
func NamesHuman(names []string) string {
	var out strings.Builder
	for at, name := range names {
		out.WriteString(name)
		switch {
		case at == len(names)-2:
			out.WriteString(" and ")
		case at < len(names)-1:
			out.WriteString(", ")
		}
	}
	return out.String()
}

// --- the pieces the validator reads -----------------------------------------

func jsonArray(value any) ([]any, bool) {
	list, ok := value.([]any)
	if !ok {
		// `// []` reads an absent key, a null and a false alike as the empty
		// array, so all three pass the array check.
		return nil, !truthyJSON(value)
	}
	return list, true
}

// truthyJSON is jq's own truth test, which is what every `//` alternative in
// the validator turns on: null and false are false and everything else is true,
// the empty string and the number zero included. An absent key reads as the
// null jq answers for it.
func truthyJSON(value any) bool {
	boolean, isBool := value.(bool)
	if isBool {
		return boolean
	}
	return value != nil
}

// memberNames is `[ $h[].name ]`, keeping each name AS WRITTEN so the checks
// above can report a null or a number as the document spells it.
//
// An entry that is not an object contributes a null. jq has no answer there —
// `1 | .name` is a type error — so this is one of the cases the divergence note
// in Validate covers.
func memberNames(entries []any) []any {
	names := make([]any, 0, len(entries))
	for _, entry := range entries {
		object, ok := entry.(map[string]any)
		if !ok {
			names = append(names, nil)
			continue
		}
		names = append(names, object["name"])
	}
	return names
}

func entryName(entry any) string {
	object, ok := entry.(map[string]any)
	if !ok {
		return ""
	}
	name, _ := object["name"].(string)
	return name
}

// firstDuplicate is jq's `dup`: `group_by(.)` sorts, so the answer is the
// lowest-sorting repeated value rather than the first one encountered.
//
// Values are compared and ordered by their JSON, which is exact for the strings
// every valid descriptor carries — `tojson` adds the same quote to every one,
// so the order is the order of the names themselves. It is an approximation of
// jq's cross-type ordering for a document whose names are not all strings, and
// such a document is already inside the divergence note in Validate.
func firstDuplicate(values []any) (any, bool) {
	type keyed struct {
		json  string
		value any
	}
	sorted := make([]keyed, 0, len(values))
	for _, value := range values {
		sorted = append(sorted, keyed{json: toJSON(value), value: value})
	}
	slices.SortFunc(sorted, func(a, b keyed) int { return strings.Compare(a.json, b.json) })
	for at := 1; at < len(sorted); at++ {
		if sorted[at].json == sorted[at-1].json {
			return sorted[at].value, true
		}
	}
	return nil, false
}

// declaredEnvNames is the three sources lib/harnesses.sh:50-53 reads, in its
// order: every env_names entry, then secret, then staging.env. A null is
// skipped there and here.
func declaredEnvNames(entry any) []string {
	var names []string
	if list, ok := valueAtList(entry, "credential", "env_names"); ok {
		for _, name := range list {
			// A non-string element is kept rather than dropped. Dropping it
			// accepted `env_names: [5]`, which the shell refuses — a silent
			// pass on a descriptor that names an environment variable the
			// staging code will later interpolate.
			if name != nil {
				names = append(names, jqText(name))
			}
		}
	}
	if value, declared := valueAt(entry, "credential", "secret"); declared && value != nil {
		names = append(names, jqText(value))
	}
	if value, declared := valueAt(entry, "credential", "staging", "env"); declared && value != nil {
		names = append(names, jqText(value))
	}
	return names
}

func inRange(entry any) bool {
	return slices.Contains([]string{"A", "B", "C"}, stringAt(entry, "credential", "archetype")) &&
		slices.Contains([]string{"measured", "inferred", "vendor-documented"}, stringAt(entry, "credential", "provenance")) &&
		slices.Contains([]string{"inline", "path", "prompt"}, stringAt(entry, "schema_style")) &&
		slices.Contains([]string{"script", "npm"}, stringAt(entry, "install", "kind")) &&
		slices.Contains([]string{"none", "file", "home", "env"}, stringAt(entry, "credential", "staging", "kind"))
}

func everyLeg(legs []any) bool {
	for _, leg := range legs {
		text, ok := leg.(string)
		if !ok || (text != LegReview && text != LegResolve) {
			return false
		}
	}
	return true
}

// declaredPaths is the order lib/harnesses.sh:71-72 reads: per harness, the
// quarantine entries then the staging path, and the shared list last.
// The per-harness entries pass through `select(. != null)` and the shared list
// does not, so a null in quarantine_shared is a violation and a null in a
// harness's own quarantine list is skipped. That asymmetry is the shell's, and
// it is reproduced rather than tidied.
func declaredPaths(entries []any, shared any) []any {
	var paths []any
	for _, entry := range entries {
		if list, ok := valueAtList(entry, "quarantine"); ok {
			for _, path := range list {
				if path != nil {
					paths = append(paths, path)
				}
			}
		}
		if value, declared := valueAt(entry, "credential", "staging", "path"); declared && value != nil {
			paths = append(paths, value)
		}
	}
	if list, ok := shared.([]any); ok {
		paths = append(paths, list...)
	}
	return paths
}

// relativePath is the `relative` filter (lib/harnesses.sh:31-33): a non-empty
// STRING that does not start at the root and carries no `..` segment.
//
// The string test is the filter's first clause and is load-bearing rather than
// defensive. Stringifying the value first accepted `quarantine: [123]`, which
// the shell refuses — and every one of these paths is handed to a move or a
// remove (lib/harnesses.sh:2-8), so a value that is not a path at all must be
// refused here rather than at the point of use.
func relativePath(value any) bool {
	path, isText := value.(string)
	if !isText || path == "" || strings.HasPrefix(path, "/") {
		return false
	}
	return !slices.Contains(strings.Split(path, "/"), "..")
}

func installerComplete(entry any) bool {
	install, _ := valueAt(entry, "install")
	url, _ := valueAt(install, "url")
	command, _ := valueAt(install, "command")
	if emptyOrAbsent(url) || emptyOrAbsent(command) {
		return false
	}
	pinned, declared := valueAt(install, "pinned_version")
	if !declared || pinned == nil {
		return true
	}
	// `$i.command | contains($i.pinned_version)` needs both to be strings; jq
	// errors otherwise. A pinned version that is not a string is therefore not
	// carried by any command, which refuses rather than stringifying it — the
	// stringified form accepted `pinned_version: 1` against a command that
	// happens to hold a 1 somewhere.
	text, isText := pinned.(string)
	line, lineIsText := command.(string)
	if !isText || !lineIsText {
		return false
	}
	return strings.Contains(line, text)
}

// emptyOrAbsent is jq's `(x // "") == ""`, which is true for a null, a false
// and the empty string alike.
func emptyOrAbsent(value any) bool {
	switch typed := value.(type) {
	case nil:
		return true
	case bool:
		return !typed
	case string:
		return typed == ""
	default:
		return false
	}
}

// valueAt walks a chain of object keys, reporting whether the last one was
// declared. Indexing a null yields a null in jq rather than an error, so a
// missing intermediate is not a failure here either.
func valueAt(value any, keys ...string) (any, bool) {
	for at, key := range keys {
		object, ok := value.(map[string]any)
		if !ok {
			return nil, false
		}
		next, declared := object[key]
		if !declared {
			return nil, false
		}
		if at == len(keys)-1 {
			return next, true
		}
		value = next
	}
	return value, true
}

func valueAtList(value any, keys ...string) ([]any, bool) {
	found, declared := valueAt(value, keys...)
	if !declared {
		return nil, false
	}
	list, ok := found.([]any)
	return list, ok
}

func stringAt(value any, keys ...string) string {
	found, declared := valueAt(value, keys...)
	if !declared {
		return ""
	}
	text, _ := found.(string)
	return text
}

// jqText is jq's `\(…)` interpolation, and its `-r` output: a string prints as
// itself and every other value prints as its JSON. Measured against jq for
// null, a number, a bool, an array and an object.
func jqText(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	return toJSON(value)
}

// toJSON is jq's `tojson` for the values the messages interpolate.
func toJSON(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "null"
	}
	return string(encoded)
}
