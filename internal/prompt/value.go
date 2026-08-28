// value.go — one value from a record the orchestrator supplied, kept as the
// JSON it arrived as rather than decoded into a Go type.

package prompt

import (
	"bytes"
	"encoding/json"
	"strconv"
)

// Value is one JSON value from an orchestrator record, held as its own bytes.
//
// A Go zero value cannot tell an absent key from an empty string, and it cannot
// tell either from false. jq can, and every rendering rule in lib/prompt.sh
// turns on that distinction:
//
//   - `\(.x)` prints `null` for an absent key and the empty string for `""`.
//   - `.x // "-"` falls through on null and on false, and prints `""` for an
//     empty string, because jq counts "" as true.
//   - `.isResolved == false` is strict, so an absent key is not equal to false
//     and the thread is dropped rather than shown.
//   - `\(.line)` prints whatever number the payload wrote, 1.5 included, which
//     lib/validate.sh accepts for a finding's line and a Go int cannot hold.
//
// Keeping the bytes reproduces all four at once, which is why this type stands
// where nine separate string, int and bool fields used to. Author was already
// this shape for the same reason and is now an alias for it.
//
// The zero Value is jq's absent key: it renders `null`, is not truthy, and is
// not equal to false.
type Value struct {
	raw json.RawMessage
}

// Str is a value the caller already holds as a string.
func Str(s string) Value {
	raw, err := json.Marshal(s)
	if err != nil {
		// json.Marshal of a string fails only on invalid UTF-8, which it
		// replaces rather than refusing.
		return Value{raw: json.RawMessage(`""`)}
	}
	return Value{raw: raw}
}

// Num is a value the caller already holds as a whole number.
func Num(n int) Value { return Value{raw: json.RawMessage(strconv.Itoa(n))} }

// Bool is a value the caller already holds as a boolean.
func Bool(b bool) Value {
	if b {
		return Value{raw: json.RawMessage("true")}
	}
	return Value{raw: json.RawMessage("false")}
}

// Raw is any other JSON, for a caller reproducing a shape the three
// constructors above cannot express — a null, a fractional line number, an
// object.
func Raw(b []byte) Value { return Value{raw: append(json.RawMessage(nil), b...)} }

// Login is the shipped author shape: one the orchestrator already projected to
// a login string (lib/github.sh:133-139).
func Login(login string) Author { return Str(login) }

// Author is a comment's author as the orchestrator received it.
//
// gh_review_threads projects it to `.author.login`, so the shipped path supplies
// a bare login — or a JSON null, for a comment left by a deleted account. jq's
// interpolation renders whatever it is given, and the frozen prompt oracle was
// captured from threads carrying the unprojected `{"login":"alice"}`, which
// reaches the prompt as that object's compact JSON. Keeping the raw value
// reproduces all three.
type Author = Value

// MarshalJSON writes the value back exactly as it arrived. An absent value is
// null, which is what jq indexing a missing key yields.
func (v Value) MarshalJSON() ([]byte, error) {
	if len(v.raw) == 0 {
		return []byte("null"), nil
	}
	return v.raw, nil
}

// UnmarshalJSON keeps the bytes rather than a decoded shape.
func (v *Value) UnmarshalJSON(b []byte) error {
	v.raw = append(json.RawMessage(nil), b...)
	return nil
}

// IsZero reports the absent state, which is the zero Value.
func (v Value) IsZero() bool { return len(v.raw) == 0 }

// String is jq's `\(...)` and `jq -r`: a string arrives as its own content, and
// anything else as compact JSON.
//
// The kind is tested before the string is decoded, because json.Unmarshal of
// the four bytes `null` into a string returns a nil error and leaves the string
// empty — which rendered a deleted account's comment as `- ****: hi` where jq
// renders `- **null**: hi`.
//
// One thing jq's own `tojson` does that json.Compact does not: re-print
// escapes rather than pass them through, so an object holding a `\u0041`
// escape reaches jq as the letter and reaches here as the escape. Only a
// non-string value takes that branch, and the shipped projection never
// produces one.
func (v Value) String() string {
	if len(bytes.TrimSpace(v.raw)) == 0 {
		return "null"
	}
	if v.kind() == "string" {
		var s string
		if err := json.Unmarshal(v.raw, &s); err == nil {
			return s
		}
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, v.raw); err != nil {
		return string(v.raw)
	}
	return compact.String()
}

// Or is jq's `x // fallback`, rendered: the fallback whenever the value is
// absent, null or false, and the value itself otherwise. The fallback is given
// as the text jq would print for it, because that is all the caller ever wants
// back.
func (v Value) Or(fallback string) string {
	if !v.Truthy() {
		return fallback
	}
	return v.String()
}

// Truthy is jq's own notion, which `//` reads: only null and false are false,
// and everything else — 0, "" and [] included — is true.
func (v Value) Truthy() bool {
	switch v.kind() {
	case "null":
		return false
	case "boolean":
		return !bytes.Contains(v.raw, []byte("false"))
	default:
		return true
	}
}

// IsFalse is jq's `x == false`, which is strict: an absent key is null, and
// null is not equal to false.
func (v Value) IsFalse() bool {
	return v.kind() == "boolean" && bytes.Contains(v.raw, []byte("false"))
}

// EqualsString is jq's `x == "s"`, which compares the value rather than its
// text: a number is simply not equal to the word it prints as.
func (v Value) EqualsString(s string) bool {
	if v.kind() != "string" {
		return false
	}
	var got string
	if err := json.Unmarshal(v.raw, &got); err != nil {
		return false
	}
	return got == s
}

// kind names the value the way jq's `type` does. An absent value is null,
// because jq indexing an object for a key it does not hold yields null.
func (v Value) kind() string {
	for _, b := range v.raw {
		switch b {
		case ' ', '\t', '\n', '\r':
			continue
		case 'n':
			return "null"
		case 't', 'f':
			return "boolean"
		case '"':
			return "string"
		case '[':
			return "array"
		case '{':
			return "object"
		default:
			return "number"
		}
	}
	return "null"
}
