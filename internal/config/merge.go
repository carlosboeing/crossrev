package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// deepMerge reproduces jq's `*` on two objects, which is what lib/config.sh:181
// composes the layers with.
//
// The left operand's key order is kept and the right operand's new keys are
// appended after it. Where both sides hold an object the merge recurses; every
// other value on the right replaces the left, null included.
func deepMerge(left, right *Object) *Object {
	out := left.Clone()
	if out == nil {
		out = NewObject()
	}
	if right == nil {
		return out
	}
	for _, key := range right.Keys() {
		rightValue := right.Value(key)
		rightObject, rightIsObject := rightValue.(*Object)
		leftObject, leftIsObject := out.Value(key).(*Object)
		if rightIsObject && leftIsObject {
			out.Set(key, deepMerge(leftObject, rightObject))
			continue
		}
		out.Set(key, cloneValue(rightValue))
	}
	return out
}

// lookup walks a dotted path such as `.policy.min_fix_severity`, which is the
// only shape cfg_get is ever called with.
func lookup(root *Object, path string) any {
	trimmed := strings.TrimPrefix(path, ".")
	if trimmed == "" {
		return root
	}
	var current any = root
	for _, segment := range strings.Split(trimmed, ".") {
		object, ok := current.(*Object)
		if !ok {
			return nil
		}
		current = object.Value(segment)
	}
	return current
}

// alternative reproduces jq's `// empty`, which treats null AND false as empty.
// The distinction matters: `logs.keep_transcripts: false` would read as unset
// through it, which is why lib/config.sh:231 reads that one key another way.
func alternative(value any) string {
	if value == nil {
		return ""
	}
	if boolean, ok := value.(bool); ok && !boolean {
		return ""
	}
	return renderScalar(value)
}

// alternativeString reproduces `// "<fallback>"`, used for
// `.backlog.destination` at lib/config.sh:202 and `.backlog.repository.layout`
// at lib/config.sh:209.
func alternativeString(value any, fallback string) string {
	if rendered := alternative(value); rendered != "" {
		return rendered
	}
	if value == nil {
		return fallback
	}
	if boolean, ok := value.(bool); ok && !boolean {
		return fallback
	}
	return ""
}

// notNull reproduces `if . == null then empty else tostring end`, which reads
// `false` as the value it is (lib/config.sh:231).
func notNull(value any) string {
	if value == nil {
		return ""
	}
	return renderCompact(value)
}

// renderScalar is what `jq -r` writes for one value: a string bare, everything
// else as its JSON.
//
// `jq -r` is not `jq -rc`, so a list or a mapping arrives pretty-printed with a
// two-space indent and a space after each object key. Every refusal that quotes
// a value reads it through this, so `min_fix_severity: [a, b]` is named across
// four lines in Bash. encoding/json's Indent writes the same shape, verified
// against `jq -r` over nested lists, nested objects and the empty forms of
// both, which stay compact on both sides.
func renderScalar(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	var buf bytes.Buffer
	if err := encodeJSON(&buf, value); err != nil {
		return ""
	}
	var indented bytes.Buffer
	if err := json.Indent(&indented, buf.Bytes(), "", "  "); err != nil {
		return buf.String()
	}
	return indented.String()
}

// renderCompact is what `tostring` writes for one value: a string bare, and
// everything else as its JSON on a single line.
//
// jq pretty-prints a container it writes as output and does not pretty-print
// one it converts with `tostring`, and both readings are live here. Five of the
// six refused keys are read through `// empty`, which is output, so
// `min_fix_severity: [a, b]` is named across four lines. The sixth is
// logs.keep_transcripts, read through `tostring` at lib/config.sh:231 because
// `//` would report the legitimate default of false as unset — and a list there
// is named on one line.
func renderCompact(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	var buf bytes.Buffer
	if err := encodeJSON(&buf, value); err != nil {
		return ""
	}
	return buf.String()
}

// Get reads one dotted path out of the merge and renders it the way `cfg_get`
// does at lib/config.sh:339, where an absent, null or false value is empty.
func (c *Config) Get(path string) string {
	return alternative(lookup(c.Merged, path))
}

// GetJSON reads one dotted path out of the merge as compact JSON, which is
// `cfg_get_json` at lib/config.sh:340. An absent value reads as `null`.
func (c *Config) GetJSON(path string) []byte {
	value := lookup(c.Merged, path)
	if boolean, ok := value.(bool); ok && !boolean {
		value = nil
	}
	var buf bytes.Buffer
	if err := encodeJSON(&buf, value); err != nil {
		return []byte("null")
	}
	return buf.Bytes()
}

// MergedJSON is the merge as compact JSON, which is what `crossrev config show`
// prints and what the parity vectors record.
func (c *Config) MergedJSON() ([]byte, error) {
	return c.Merged.MarshalJSON()
}

// errNotMapping reports a document that parsed and holds something other than a
// mapping. It is a sentinel rather than a refusal because the refusal text
// names the file, and only the caller knows which file it read and whether it
// read the working tree or a revision.
var errNotMapping = errors.New("the document is not a mapping")

// decodeDocument reads YAML into the document shape the merge works on.
//
// Parsing is not the same question as shape, and the two shapes that are not a
// mapping get two different answers because they are two different files
// (_cfg_as_mapping, lib/config.sh:48-81).
//
//	null   — a comment, some whitespace, or an existing empty file. It states
//	         no policy, which is exactly what an absent file states, so it
//	         becomes an empty object and the run carries on in silence.
//	object — configuration. Passed through.
//	other  — a sequence, a string, a number or a boolean. That is a malformed
//	         config, and errNotMapping asks the caller to refuse it by name.
//
// Reading a non-mapping leniently is what made this worth splitting out. yq
// exits 0 for every one of these, so both readers report success and the merge
// receives something jq's `*` cannot multiply an object by: the merge produced
// nothing, jq's own error text reached the terminal, and the next assertion
// blamed a key nobody had set (issue 143).
func decodeDocument(source []byte) (*Object, error) {
	documents, err := decodeDocuments(source)
	if err != nil {
		return nil, err
	}
	// A file holding more than one document is not a mapping, whatever the
	// documents are. `yq -o=json -I=0` writes one line per document, and the
	// shape test then reads `jq -r type` over the whole stream and compares a
	// two-line answer against the word `object` (lib/config.sh:71-76). So
	// `mode: ci` followed by `---` and a second mapping is refused there, and
	// taking the first document here would load a policy the other
	// implementation will not run at all.
	if len(documents) > 1 {
		return nil, errNotMapping
	}
	if len(documents) == 0 {
		return NewObject(), nil
	}
	switch shaped := documents[0].(type) {
	case *Object:
		return shaped, nil
	case nil:
		return NewObject(), nil
	default:
		return nil, errNotMapping
	}
}

// decodeDocuments reads every document in one source, after dropping the
// leading region yq drops.
func decodeDocuments(source []byte) ([]any, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(withoutLeadingContent(source)))
	var out []any
	for {
		var document yaml.Node
		err := decoder.Decode(&document)
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		value, err := decodeNode(&document, newDecodeState())
		if err != nil {
			return nil, err
		}
		out = append(out, value)
	}
}

// withoutLeadingContent drops the blank lines, comment lines and bare document
// markers a file opens with, which is what yq drops before it parses.
//
// yq holds that region aside so it can print it back out, and only what follows
// reaches the YAML parser. The difference is visible wherever a tab appears in
// it, because a tab cannot start a token: `yq '.'` reads a document of one tab
// as null and exits 0, so the Bash loads the defaults in silence, while
// go-yaml handed the same bytes reports `found character that cannot start any
// token` and the file is refused as unparsable.
//
// Measured against yq over eight sources. A tab-only document, a tab before a
// leading comment and a tab-only line above real content all parse there; a tab
// on a content line, a tab on a line below content and a tab under a comment
// that follows content are all errors there, and this drops none of them. For
// every file that opens with a key the region is empty and nothing changes.
//
// A bare `---` above the first content line goes with them, and that is the
// half that decides a shape rather than a message. yq swallows every empty
// document there and prints one JSON line, so `---`, `---` and then `mode: ci`
// loads. go-yaml handed the same bytes builds three documents, and the
// multi-document guard in decodeDocument then refuses a file whose own refusal
// hint tells the reader to check it with a command that parses it. Measured
// against yq over `---\n---\nmode: ci`, `---\n---`, three markers, a marker
// around a comment and a marker around a blank line, which all load there.
//
// Only a marker alone on its line is dropped, and only while the region lasts.
// A marker below content opens a second document on both sides and is still
// refused, and `---x` is not a marker on either side.
//
// A marker carrying content on the same line is a separate region of yq's
// behaviour and is left alone here. yq removes the `---` and reads the rest, so
// `--- mode: ci` is a mapping there and go-yaml refuses it — but the rule is
// not the same one: yq errors on `--- mode: ci` followed by `x: 1`, and reads
// `--- !!map` above `mode: ci` as the string `ci`. Nothing rewrites the line
// here until that is measured rather than guessed at.
func withoutLeadingContent(source []byte) []byte {
	rest := source
	for len(rest) > 0 {
		line := rest
		if end := bytes.IndexByte(rest, '\n'); end >= 0 {
			line = rest[:end+1]
		}
		trimmed := bytes.Trim(line, " \t\r\n")
		if len(trimmed) > 0 && trimmed[0] != '#' && !bytes.Equal(trimmed, documentMarker) {
			break
		}
		rest = rest[len(line):]
	}
	return rest
}

// documentMarker is the bare `---` that opens a YAML document.
var documentMarker = []byte("---")

func decodeNode(node *yaml.Node, state *decodeState) (any, error) {
	// A zero node is not a scalar, and reading it as one is what let a
	// comment-only file through as the empty string. An empty, comment-only or
	// whitespace-only source has no document in it at all, so yaml.Unmarshal
	// leaves the node zero rather than building a DocumentNode — and that is
	// the null yq prints for the same three inputs.
	if node == nil || node.Kind == 0 {
		return nil, nil
	}
	if err := state.enter(); err != nil {
		return nil, err
	}
	defer state.leave()
	switch node.Kind {
	case yaml.DocumentNode:
		if len(node.Content) == 0 {
			return nil, nil
		}
		return decodeNode(node.Content[0], state)
	case yaml.AliasNode:
		return decodeNode(node.Alias, state)
	case yaml.MappingNode:
		if err := state.open(node); err != nil {
			return nil, err
		}
		defer state.close(node)
		out := NewObject()
		if err := decodeMapping(out, node, state); err != nil {
			return nil, err
		}
		return out, nil
	case yaml.SequenceNode:
		if err := state.open(node); err != nil {
			return nil, err
		}
		defer state.close(node)
		out := make([]any, 0, len(node.Content))
		for _, item := range node.Content {
			value, err := decodeNode(item, state)
			if err != nil {
				return nil, err
			}
			out = append(out, value)
		}
		return out, nil
	default:
		return decodeScalar(node)
	}
}

// maxDecodeDepth bounds every recursive step taken through a document: a
// container entered, an alias followed to its anchor, and a merge key followed
// to its source all count against the same budget.
//
// One budget rather than one per path, because a bound carried by one function
// and reset by the next bounds nothing. The earlier bound counted merge keys
// only, and decodeMapping passed it to itself alone, so any hop back through
// decodeNode started again from zero.
//
// A Go stack overflow is a fatal error that no recover() catches, so a document
// that recursed without a bound would end the process with a runtime dump
// instead of a refusal — and Load reads the operator file on every invocation,
// whatever revision it was asked for, so no caller could route around it.
//
// The value is high enough that no configuration meets it: yq reads a thousand
// levels of nesting and is already unusable well below ten thousand, so a file
// that nests past this has no answer from the other implementation either.
const maxDecodeDepth = 1000

// errAliasCycle reports an anchor that is named from inside its own value.
//
// Such a cycle is writable in YAML, contrary to what this file used to claim.
// An anchor is registered when its node opens rather than when it closes, so
// the `*a` in `m: &a` / `  b: *a` resolves to the mapping still being built.
//
// There is no value to reproduce. yq answers the family inconsistently because
// it rewrites the anchored node in place while expanding it. One self-reference
// prints an expansion two levels deep and then an empty container, and exits 0.
// Two of them in the same mapping overflow yq's own stack and exit 2, and so
// does a `<<: *a` written one level inside `&a` — while the same `<<: *a`
// written directly inside `&a` exits 0 and prints a literal `<<` key. So every
// cycle is refused here, under a message that says what is actually wrong
// rather than calling a file unparsable that yq parses.
var errAliasCycle = errors.New("an anchor refers to itself")

// errTooDeep reports a document nested past maxDecodeDepth.
var errTooDeep = errors.New("the document nests too deeply")

// decodeState is the budget carried through every recursive path.
//
// depth counts the steps open above the current node. active holds the
// container nodes on that path, which is what makes a second arrival at one a
// cycle rather than a second expansion — the depth bound alone would answer a
// cycle with the wrong refusal, and only after a thousand wasted steps.
type decodeState struct {
	depth  int
	active map[*yaml.Node]bool
}

func newDecodeState() *decodeState {
	return &decodeState{active: map[*yaml.Node]bool{}}
}

// enter takes one step down, or reports the bound.
func (s *decodeState) enter() error {
	if s.depth >= maxDecodeDepth {
		return errTooDeep
	}
	s.depth++
	return nil
}

// leave gives the step back.
func (s *decodeState) leave() { s.depth-- }

// open marks one container as being decoded, and reports a cycle when it
// already is.
func (s *decodeState) open(node *yaml.Node) error {
	if node == nil {
		return nil
	}
	if s.active[node] {
		return errAliasCycle
	}
	s.active[node] = true
	return nil
}

// close unmarks one container, so a second alias to it beside the first is an
// expansion rather than a cycle.
func (s *decodeState) close(node *yaml.Node) {
	if node != nil {
		delete(s.active, node)
	}
}

// decodeMapping writes one mapping's pairs into out, resolving the merge key.
//
// go-yaml resolves an alias and leaves `<<` alone, so `git:\n  <<: *defaults`
// decoded to a literal `<<` key holding the anchor and the defaults underneath
// it survived untouched. yq resolves it, so a repository sharing a policy block
// through an anchor got one effective policy from Bash and another from Go,
// with both exiting 0 and nothing to read.
//
// The model below is measured against yq over sixteen documents, and it is
// positional: the source's pairs are written where the `<<` sits, so a key
// written after the merge wins and a key written before it does not. A sequence
// of sources is applied last to first, which is what makes the earliest entry
// the one whose keys survive — `<<: [*a, *b, *c]` with a `k` in each answers
// with a's `k` and b's and c's other keys in that order.
//
// What the merge key answers is the half that is not settled forever. yq warns
// on every file holding one that its `--yaml-fix-merge-anchor-to-spec` default
// is going to flip, and running it with the flag set changes three things
// rather than the one this used to name. A key written beside the merge wins
// wherever it sits, which reverses the two middle rows above. A sequence of
// sources applies first to last rather than last to first, so `<<: [*x, *y]`
// answers with x's keys before y's. And a mapping written out inside a `<<`
// sequence is applied rather than dropped, as is one written out on its own.
//
// Nothing CrossRev's own config shapes depend on any of it. What is the same
// under either setting is that the key resolves at all, and that no `<<` a
// merge loads survives as a literal key: yq drops the key wherever it will not
// follow it. The one file where a literal `<<` does reach yq's output is a
// self-referential one, `m: &a` / `  <<: *a`, and refuseAliasCycle refuses that
// whole family rather than reproducing it.
//
// This is written down in a code comment and nowhere a reader looks. After the
// cutover the durable record for the coming flip belongs in the public
// documentation, and is still owed.
//
// yq follows a chain, so a source that itself merges resolves too, and the
// recursion here does the same. What yq will not follow it drops in silence:
// `<<` holding a mapping written out rather than an alias, holding null, or
// holding a scalar leaves the key out and merges nothing. An alias naming
// something that is not a mapping is an error there, so the file is refused.
//
// Where a key is repeated, the merge key also decides which of its two
// positions the key keeps. yq rebuilds a mapping that holds a `<<` and
// rewrites nothing else, so a repeated key in that mapping moves to the end
// while the same repetition anywhere else keeps its first position — which is
// what jq does with the literal duplicate yq prints for it. The two rules are
// measured against yq over twelve shapes and are picked between per mapping,
// on whether that mapping holds a `<<` at all: `<<: 1`, `<<:` null and an
// inline `<<: {x: 1}` all merge nothing and all still move the repeat. Keys a
// merge source contributes are written in the source's own order, so its
// repeats follow the source's rule and not this mapping's.
func decodeMapping(out *Object, node *yaml.Node, state *decodeState) error {
	if err := state.enter(); err != nil {
		return err
	}
	defer state.leave()
	set := out.Set
	if holdsMergeKey(node) {
		set = out.SetLast
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		key, value := node.Content[i], node.Content[i+1]
		if keyName(key) == "<<" {
			sources, err := mergeSources(value)
			if err != nil {
				return err
			}
			for _, source := range sources {
				if err := state.open(source); err != nil {
					return err
				}
				err := decodeMapping(out, source, state)
				state.close(source)
				if err != nil {
					return err
				}
			}
			continue
		}
		decoded, err := decodeNode(value, state)
		if err != nil {
			return err
		}
		set(keyName(key), decoded)
	}
	return nil
}

// holdsMergeKey reports whether one mapping writes a `<<` of its own, which is
// what decides how it positions a repeated key.
func holdsMergeKey(node *yaml.Node) bool {
	for i := 0; i+1 < len(node.Content); i += 2 {
		if keyName(node.Content[i]) == "<<" {
			return true
		}
	}
	return false
}

// mergeSources lists the mappings one `<<` names, in the order yq applies them.
func mergeSources(node *yaml.Node) ([]*yaml.Node, error) {
	var named []*yaml.Node
	switch node.Kind {
	case yaml.AliasNode:
		named = []*yaml.Node{node}
	case yaml.SequenceNode:
		for i := len(node.Content) - 1; i >= 0; i-- {
			named = append(named, node.Content[i])
		}
	default:
		return nil, nil
	}
	var sources []*yaml.Node
	for _, candidate := range named {
		if candidate.Kind != yaml.AliasNode {
			continue
		}
		target := candidate.Alias
		if target == nil || target.Kind != yaml.MappingNode {
			return nil, fmt.Errorf("a merge key can only name a mapping")
		}
		sources = append(sources, target)
	}
	return sources, nil
}

// keyName is the text yq writes for one mapping key.
//
// It is the scalar's source text and nothing else: yq resolves a key no further
// than reading it, so `~` is the key `~` rather than `null`, `0x10` is the key
// `0x10` rather than `16`, and a key that is itself a list or a mapping — the
// `? [a, b]` form — is the empty string. An alias key is the text its anchor
// holds.
func keyName(node *yaml.Node) string {
	for depth := 0; node != nil && node.Kind == yaml.AliasNode; depth++ {
		if depth > maxDecodeDepth {
			return ""
		}
		node = node.Alias
	}
	if node == nil {
		return ""
	}
	return node.Value
}

// decodeScalar turns one YAML scalar into the value yq would have printed.
//
// The tag is go-yaml's and the rendering is yq's and then jq's, because the
// Bash reads YAML through `yq -o=json` and reads values back out through
// `jq -r`: a number reaches a comparison as the text those two wrote, which is
// why `logs.retention_days: 5.0` is refused at lib/config.sh:225. The two
// resolutions differ on more than formatting. go-yaml reads a leading zero as
// octal where yq reads it as decimal, so `0777` is 511 to one and 777 to the
// other; go-yaml accepts `0b101` where yq errors and the Bash then refuses the
// whole file; and go-yaml tags `08` a float whose literal is not JSON at all,
// which would put `crossrev config show` outside its own format.
//
// yq's text is not the last word on a float, because jq rewrites the exponent
// forms it reads. scientificString carries that half.
func decodeScalar(node *yaml.Node) (any, error) {
	switch node.Tag {
	case "!!null":
		return nil, nil
	case "!!bool":
		var boolean bool
		if err := node.Decode(&boolean); err != nil {
			return nil, err
		}
		return boolean, nil
	case "!!int":
		return decodeInteger(node.Value)
	case "!!float":
		return decodeFloat(node.Value)
	default:
		return node.Value, nil
	}
}

// decodeInteger renders an integer the way yq renders one: underscores dropped,
// `0x` and `0o` read as the bases they name, and everything else read as
// decimal, so `0777` is 777 and `007` is 7.
//
// A literal yq cannot read that way — `0b101`, `0O17`, or a value wider than
// int64 — is an error here, and the caller refuses the file as unparsable. That
// is what the Bash does with it too: yq exits non-zero, _cfg_yaml_to_json
// returns 1, and the caller refuses (lib/config.sh:36-38, 43-46).
func decodeInteger(literal string) (any, error) {
	// yq resolves the exact literal `-0` as a float and prints it `-0.0`, where
	// go-yaml tags it an integer and would print `0`. It is the literal and not
	// the value: `-00`, `-000` and `-0_0` are integers to both and print `0`.
	if literal == "-0" {
		return Number("-0.0"), nil
	}
	text := strings.ReplaceAll(literal, "_", "")
	base, digits := 10, text
	switch {
	case strings.HasPrefix(text, "0x"), strings.HasPrefix(text, "0X"):
		base, digits = 16, text[2:]
	case strings.HasPrefix(text, "0o"):
		base, digits = 8, text[2:]
	}
	whole, err := strconv.ParseInt(digits, base, 64)
	if err != nil {
		return nil, fmt.Errorf("cannot read %q as an integer: %w", literal, err)
	}
	return Number(strconv.FormatInt(whole, 10)), nil
}

// decodeFloat renders a float the way yq writes it and jq then reads it back.
//
// yq keeps the literal where the literal is already a JSON number carrying a
// decimal point or an exponent, so `5.00` stays `5.00` and `1e3` stays `1e3`.
// Anything else it parses and writes back with a decimal point, which is where
// `08` becomes `8.0` and `.5` becomes `0.5`. Keeping the literal is the half
// that matters: a refusal quotes it, and reformatting every float would turn
// `5.0` into `5` and accept a value lib/config.sh:225 refuses.
//
// The test is the literal as written, not the literal with its underscores
// removed. `1_0e3` is not a JSON number, so yq writes it back as `10000.0`
// rather than keeping `10e3`.
//
// The parse is the literal as written too, and for the same reason on the other
// side of the question. yq hands the raw literal to strconv.ParseFloat, which
// takes an underscore only between two digits, so `1_0e3` and `1_000.5` are
// numbers there and `1e_3`, `1_e3`, `1.5_`, `1.0_` and `1e3_` are all errors
// and the whole file is refused. Removing the underscores first accepted every
// one of them, and `retention_days: 1.0_` then reached its assertion as the
// number 1.0 and was refused for not being whole — a second wrong answer under
// a message that names the wrong fault. The integer path already reads the
// literal yq reads, over eighteen underscore forms that agree.
func decodeFloat(literal string) (any, error) {
	rendered := literal
	if !(strings.ContainsAny(literal, ".eE") && json.Valid([]byte(literal))) {
		value, err := strconv.ParseFloat(literal, 64)
		if err != nil {
			return nil, fmt.Errorf("cannot read %q as a number: %w", literal, err)
		}
		rendered = strconv.FormatFloat(value, 'f', -1, 64)
		if !strings.ContainsAny(rendered, ".eE") {
			rendered += ".0"
		}
	}
	return Number(scientificString(rendered)), nil
}

// scientificString rewrites a JSON number as the text jq writes back out.
//
// yq is only half the pipeline. `_cfg_yaml_to_json` writes JSON and every
// reader then goes through jq — the merge at lib/config.sh:181, and `jq -r`
// for each value a refusal quotes at lib/config.sh:225 and 262 — so a number
// reaches a comparison as jq's text and not as yq's. The two differ on the
// exponent forms: yq writes `1e3`, and what a person sees is `1E+3`.
//
// jq keeps the decimal it read rather than a double, and prints it in the
// to-scientific-string form of the General Decimal Arithmetic specification,
// which is what decNumber implements and what jq has used since 1.7. Plain
// notation while the exponent is not positive and the adjusted exponent is -6
// or more; exponential notation otherwise. That single rule answers for every
// literal measured against `yq -o=json | jq -c` — `1e3` to `1E+3`, `1e-3` to
// `0.001`, `1e-7` to `1E-7`, `0.5e1` to `5`, `1.0e3` to `1.0E+3`, `-0e0` to
// `-0` — and leaves `2.50`, `5.0`, `0.0001`, `3.14` and `1000000.0` exactly as
// they are.
//
// It is a text transformation on purpose. Reading the literal into a float64
// first would lose the digits past the seventeenth, and both sides keep them:
// `1.23456789012345678e3` prints `1234.56789012345678`.
//
// A literal this cannot take apart is returned unchanged rather than guessed
// at, which is the same answer as leaving it alone.
//
// Two cases are left divergent and cannot be closed from here.
//
// jq can be built without decNumber, and such a build reads every number into a
// double and prints it back through that. The rule below follows the default
// build, which is what every published binary is.
//
// decNumber also clamps an exponent it cannot hold, at roughly -1.1e9, and this
// does not. No configuration value carries an exponent within nine orders of
// magnitude of that, so the divergence is recorded rather than reproduced.
//
// Both statements are true of the Bash implementation, and this comment is the
// only place either is written down. After the cutover nobody reads this file
// to find out how CrossRev reads a number, so the durable record for them
// belongs in the public documentation and is still owed.
func scientificString(literal string) string {
	sign, rest := "", literal
	if strings.HasPrefix(rest, "-") {
		sign, rest = "-", rest[1:]
	}
	mantissa, exponent := rest, 0
	if cut := strings.IndexAny(rest, "eE"); cut >= 0 {
		parsed, err := strconv.Atoi(rest[cut+1:])
		if err != nil {
			return literal
		}
		mantissa, exponent = rest[:cut], parsed
	}
	digits := mantissa
	if point := strings.IndexByte(mantissa, '.'); point >= 0 {
		digits = mantissa[:point] + mantissa[point+1:]
		exponent -= len(mantissa) - point - 1
	}
	if digits == "" {
		return literal
	}
	for i := 0; i < len(digits); i++ {
		if digits[i] < '0' || digits[i] > '9' {
			return literal
		}
	}
	// The coefficient's leading zeros are not digits of it, and dropping them
	// does not move the exponent. A zero keeps one digit.
	if digits = strings.TrimLeft(digits, "0"); digits == "" {
		digits = "0"
	}
	adjusted := exponent + len(digits) - 1
	if exponent > 0 || adjusted < -6 {
		out := digits[:1]
		if len(digits) > 1 {
			out += "." + digits[1:]
		}
		if adjusted < 0 {
			return sign + out + "E-" + strconv.Itoa(-adjusted)
		}
		return sign + out + "E+" + strconv.Itoa(adjusted)
	}
	if exponent == 0 {
		return sign + digits
	}
	if point := len(digits) + exponent; point > 0 {
		return sign + digits[:point] + "." + digits[point:]
	} else {
		return sign + "0." + strings.Repeat("0", -point) + digits
	}
}
