package config

import (
	"bytes"
	"encoding/json"
	"strings"
)

// Version is the only configuration shape this build understands. It is
// CROSSREV_CONFIG_VERSION at lib/config.sh:21, compared as text rather than as
// a number, so `version: 1.0` is a mismatch exactly as it is in Bash
// (lib/config.sh:298).
const Version = "1"

// Number is a YAML number carried as the text a JSON encoder must emit.
//
// The Bash implementation reads YAML through `yq -o=json` and then reads values
// back out through `jq -r`, so a number reaches a comparison as the text yq
// wrote. `logs.retention_days: 5.0` is refused there because "5.0" does not
// match `^[0-9]+$` (lib/config.sh:225). Decoding into float64 and reformatting
// would turn that into "5" and accept it, so the literal is kept instead.
//
// Integers are normalised the way yq normalises them, because yq resolves
// `0x10`, `0o17` and `1_000` before it prints; floats keep their source text.
type Number string

// Refusal is a stop with a message and a hint, which is the only shape of
// refusal CrossRev prints (ui_die, lib/ui.sh:113).
//
// It is a value rather than a call to a printer because this package is tier 2
// and imports no other tier-2 package, `ui` included. The caller renders it.
type Refusal struct {
	Message string
	Hint    string
}

// Error is the message alone, which is what a wrapped error should read as.
func (r *Refusal) Error() string { return r.Message }

// Rendered is the two lines ui_die writes to stderr, with the colour escapes
// and the trailing blank line removed. It is the form the parity vectors in
// tests/fixtures/parity/config_merge.json record.
func (r *Refusal) Rendered() string {
	return "\nerror  " + r.Message + "\n       " + r.Hint
}

// Object is a JSON object that remembers the order its keys arrived in.
//
// jq's object multiplication keeps the left operand's key order and appends the
// right operand's new keys after it, and `crossrev config show` prints the
// merge. Ordering it alphabetically instead would change that output for no
// reason, so the order is carried rather than discarded.
type Object struct {
	keys []string
	vals map[string]any
}

// NewObject returns an empty object.
func NewObject() *Object {
	return &Object{vals: map[string]any{}}
}

// Len is the number of keys.
func (o *Object) Len() int {
	if o == nil {
		return 0
	}
	return len(o.keys)
}

// Keys lists the keys in insertion order.
func (o *Object) Keys() []string {
	if o == nil {
		return nil
	}
	out := make([]string, len(o.keys))
	copy(out, o.keys)
	return out
}

// Has reports whether the key is present, which is not the same question as
// whether its value is null. cfg_resolve_backlog asks exactly this of the
// repository layer at lib/config.sh:396.
func (o *Object) Has(key string) bool {
	if o == nil {
		return false
	}
	_, ok := o.vals[key]
	return ok
}

// Value returns the value at key, or nil when the key is absent.
func (o *Object) Value(key string) any {
	if o == nil {
		return nil
	}
	return o.vals[key]
}

// Object returns the value at key when it is an object, and nil otherwise.
func (o *Object) Object(key string) *Object {
	nested, _ := o.Value(key).(*Object)
	return nested
}

// Set writes key, appending it when it is new and keeping its position when it
// is not.
func (o *Object) Set(key string, value any) {
	if o.vals == nil {
		o.vals = map[string]any{}
	}
	if _, ok := o.vals[key]; !ok {
		o.keys = append(o.keys, key)
	}
	o.vals[key] = value
}

// SetLast writes key, moving it to the end when it is already present.
//
// It is Set with the other of the two positions a repeated key can take, and
// decodeMapping picks between them per mapping. yq rebuilds a mapping that
// holds a merge key, and a repeat there lands where its last write sits; a
// repeat in a mapping with no merge key reaches jq as a literal duplicate,
// which jq answers with the first position and the last value — which is Set.
func (o *Object) SetLast(key string, value any) {
	if o.vals == nil {
		o.vals = map[string]any{}
	}
	if _, ok := o.vals[key]; ok {
		for i, existing := range o.keys {
			if existing == key {
				o.keys = append(o.keys[:i], o.keys[i+1:]...)
				break
			}
		}
	}
	o.keys = append(o.keys, key)
	o.vals[key] = value
}

// Clone is a deep copy, so a merge never aliases a default into its result.
func (o *Object) Clone() *Object {
	if o == nil {
		return nil
	}
	out := NewObject()
	for _, k := range o.keys {
		out.Set(k, cloneValue(o.vals[k]))
	}
	return out
}

func cloneValue(v any) any {
	switch t := v.(type) {
	case *Object:
		return t.Clone()
	case []any:
		out := make([]any, len(t))
		for i, item := range t {
			out[i] = cloneValue(item)
		}
		return out
	default:
		return v
	}
}

// MarshalJSON writes the object in key order.
func (o *Object) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	if err := encodeJSON(&buf, o); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// encodeJSON writes the compact JSON `jq -c` would write for a decoded YAML
// document.
func encodeJSON(buf *bytes.Buffer, v any) error {
	switch t := v.(type) {
	case nil:
		buf.WriteString("null")
	case bool:
		if t {
			buf.WriteString("true")
		} else {
			buf.WriteString("false")
		}
	case Number:
		buf.WriteString(string(t))
	case string:
		return encodeString(buf, t)
	case []any:
		buf.WriteByte('[')
		for i, item := range t {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := encodeJSON(buf, item); err != nil {
				return err
			}
		}
		buf.WriteByte(']')
	case *Object:
		buf.WriteByte('{')
		if t != nil {
			for i, k := range t.keys {
				if i > 0 {
					buf.WriteByte(',')
				}
				if err := encodeString(buf, k); err != nil {
					return err
				}
				buf.WriteByte(':')
				if err := encodeJSON(buf, t.vals[k]); err != nil {
					return err
				}
			}
		}
		buf.WriteByte('}')
	default:
		encoded, err := json.Marshal(t)
		if err != nil {
			return err
		}
		buf.Write(encoded)
	}
	return nil
}

// encodeString escapes a string the way jq does. Go's encoding/json escapes
// `<`, `>` and `&` into \u form by default and jq does not, so HTML escaping is
// turned off.
func encodeString(buf *bytes.Buffer, s string) error {
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(s); err != nil {
		return err
	}
	buf.WriteString(strings.TrimRight(out.String(), "\n"))
	return nil
}
