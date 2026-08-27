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
	return renderScalar(value)
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

// Get reads one dotted path out of the merge and renders it the way `cfg_get`
// does at lib/config.sh:303, where an absent, null or false value is empty.
func (c *Config) Get(path string) string {
	return alternative(lookup(c.Merged, path))
}

// GetJSON reads one dotted path out of the merge as compact JSON, which is
// `cfg_get_json` at lib/config.sh:304. An absent value reads as `null`.
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
		value, err := decodeNode(&document)
		if err != nil {
			return nil, err
		}
		out = append(out, value)
	}
}

// withoutLeadingContent drops the blank and comment lines a file opens with,
// which is what yq does before it parses.
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
func withoutLeadingContent(source []byte) []byte {
	rest := source
	for len(rest) > 0 {
		line := rest
		if end := bytes.IndexByte(rest, '\n'); end >= 0 {
			line = rest[:end+1]
		}
		trimmed := bytes.Trim(line, " \t\r\n")
		if len(trimmed) > 0 && trimmed[0] != '#' {
			break
		}
		rest = rest[len(line):]
	}
	return rest
}

func decodeNode(node *yaml.Node) (any, error) {
	// A zero node is not a scalar, and reading it as one is what let a
	// comment-only file through as the empty string. An empty, comment-only or
	// whitespace-only source has no document in it at all, so yaml.Unmarshal
	// leaves the node zero rather than building a DocumentNode — and that is
	// the null yq prints for the same three inputs.
	if node == nil || node.Kind == 0 {
		return nil, nil
	}
	switch node.Kind {
	case yaml.DocumentNode:
		if len(node.Content) == 0 {
			return nil, nil
		}
		return decodeNode(node.Content[0])
	case yaml.AliasNode:
		return decodeNode(node.Alias)
	case yaml.MappingNode:
		out := NewObject()
		if err := decodeMapping(out, node, 0); err != nil {
			return nil, err
		}
		return out, nil
	case yaml.SequenceNode:
		out := make([]any, 0, len(node.Content))
		for _, item := range node.Content {
			value, err := decodeNode(item)
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

// maxMergeDepth bounds how far a chain of merge keys is followed. An anchor has
// to be defined before it is named, so a cycle is not writable in YAML — but
// the bound is what makes that a fact of this function rather than a fact about
// somebody else's parser.
const maxMergeDepth = 64

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
// Which side wins is the half that is not settled forever. yq warns on every
// file holding a merge key that its `--yaml-fix-merge-anchor-to-spec` default
// is going to flip, and the flip reverses the two middle rows above: under the
// specification a key written beside the merge wins wherever it sits. Nothing
// CrossRev's own config shapes depend on it, and the rest of this — that the
// key resolves at all, and does not reach the merge as a literal `<<` — is the
// same under either setting.
//
// yq follows a chain, so a source that itself merges resolves too, and the
// recursion here does the same. What yq will not follow it drops in silence:
// `<<` holding a mapping written out rather than an alias, holding null, or
// holding a scalar leaves the key out and merges nothing. An alias naming
// something that is not a mapping is an error there, so the file is refused.
func decodeMapping(out *Object, node *yaml.Node, depth int) error {
	if depth > maxMergeDepth {
		return fmt.Errorf("merge keys nest more than %d deep", maxMergeDepth)
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		key, value := node.Content[i], node.Content[i+1]
		if keyName(key) == "<<" {
			sources, err := mergeSources(value)
			if err != nil {
				return err
			}
			for _, source := range sources {
				if err := decodeMapping(out, source, depth+1); err != nil {
					return err
				}
			}
			continue
		}
		decoded, err := decodeNode(value)
		if err != nil {
			return err
		}
		out.Set(keyName(key), decoded)
	}
	return nil
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
		if depth > maxMergeDepth {
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
func decodeFloat(literal string) (any, error) {
	rendered := literal
	if !(strings.ContainsAny(literal, ".eE") && json.Valid([]byte(literal))) {
		value, err := strconv.ParseFloat(strings.ReplaceAll(literal, "_", ""), 64)
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
// One case is left divergent and cannot be closed from here: jq can be built
// without decNumber, and such a build reads every number into a double and
// prints it back through that. The rule below follows the default build, which
// is what every published binary is.
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
