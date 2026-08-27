package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
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
func renderScalar(value any) string {
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
	var document yaml.Node
	if err := yaml.Unmarshal(source, &document); err != nil {
		return nil, err
	}
	value, err := decodeNode(&document)
	if err != nil {
		return nil, err
	}
	switch shaped := value.(type) {
	case *Object:
		return shaped, nil
	case nil:
		return NewObject(), nil
	default:
		return nil, errNotMapping
	}
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
		for i := 0; i+1 < len(node.Content); i += 2 {
			key, err := decodeNode(node.Content[i])
			if err != nil {
				return nil, err
			}
			value, err := decodeNode(node.Content[i+1])
			if err != nil {
				return nil, err
			}
			out.Set(renderScalar(key), value)
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

// decodeScalar turns one YAML scalar into the value yq would have printed.
//
// The tag is go-yaml's and the rendering is yq's, because the Bash reads YAML
// through `yq -o=json` and reads values back out through `jq -r`: a number
// reaches a comparison as the text yq wrote, which is why
// `logs.retention_days: 5.0` is refused at lib/config.sh:225. The two
// resolutions differ on more than formatting. go-yaml reads a leading zero as
// octal where yq reads it as decimal, so `0777` is 511 to one and 777 to the
// other; go-yaml accepts `0b101` where yq errors and the Bash then refuses the
// whole file; and go-yaml tags `08` a float whose literal is not JSON at all,
// which would put `crossrev config show` outside its own format.
//
// One case is left as it decodes: go-yaml tags `-0` an integer and renders it
// `0`, where yq tags it a float and renders it `-0.0`. Both sides refuse it
// wherever a whole number is required, so only the text a refusal quotes
// differs.
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

// decodeFloat renders a float the way yq renders one.
//
// yq keeps the literal where the literal is already a JSON number carrying a
// decimal point or an exponent, so `5.00` stays `5.00` and `1e3` stays `1e3`.
// Anything else it parses and writes back with a decimal point, which is where
// `08` becomes `8.0` and `.5` becomes `0.5`. Keeping the literal is the half
// that matters: a refusal quotes it, and reformatting every float would turn
// `5.0` into `5` and accept a value lib/config.sh:225 refuses.
func decodeFloat(literal string) (any, error) {
	text := strings.ReplaceAll(literal, "_", "")
	if strings.ContainsAny(text, ".eE") && json.Valid([]byte(text)) {
		return Number(text), nil
	}
	value, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return nil, fmt.Errorf("cannot read %q as a number: %w", literal, err)
	}
	rendered := strconv.FormatFloat(value, 'f', -1, 64)
	if !strings.ContainsAny(rendered, ".eE") {
		rendered += ".0"
	}
	return Number(rendered), nil
}
