package config

import (
	"bytes"
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

// decodeDocument reads YAML into the document shape the merge works on.
//
// A document that resolves to null decodes to an empty object rather than to
// null. The Bash implementation hands yq's `null` straight to jq, which cannot
// multiply an object by null, so a comment-only or whitespace-only config dies
// with a raw jq error and exit 5 (issue 143). That defect is not reproduced and
// no refusal is added in its place: an empty document states no policy, which
// is what an absent file states.
func decodeDocument(source []byte) (*Object, error) {
	var document yaml.Node
	if err := yaml.Unmarshal(source, &document); err != nil {
		return nil, err
	}
	value, err := decodeNode(&document)
	if err != nil {
		return nil, err
	}
	if object, ok := value.(*Object); ok {
		return object, nil
	}
	return NewObject(), nil
}

func decodeNode(node *yaml.Node) (any, error) {
	if node == nil {
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
// yq resolves an integer before it prints, so `0x10`, `0o17`, `+5` and `1_000`
// arrive at jq as 16, 15, 5 and 1000. It prints a float as written, so `5.0`
// stays `5.0` — and `logs.retention_days: 5.0` is refused at lib/config.sh:225
// for exactly that reason.
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
		var whole int64
		if err := node.Decode(&whole); err != nil {
			// A value too wide for int64 keeps its own text rather than
			// failing the load, which is what yq does with it.
			return Number(strings.ReplaceAll(node.Value, "_", "")), nil
		}
		return Number(strconv.FormatInt(whole, 10)), nil
	case "!!float":
		return Number(strings.ReplaceAll(node.Value, "_", "")), nil
	default:
		return node.Value, nil
	}
}
