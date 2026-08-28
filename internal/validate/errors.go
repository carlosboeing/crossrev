// errors.go — the two failure kinds, and the jq value plumbing their messages
// are built from.
//
// The shell says which kind failed with an exit code, and lib/validate.sh:4-25
// explains why the two are kept apart: a shape failure on a schema-native
// harness is an adapter or harness bug, so a retry reproduces it, while a
// semantic failure is model drift and earns one more attempt. Conflating them
// either retries a bug or discards a pass over a typo.
//
// Go carries the same split as two error types rather than as a returned int, so
// a caller that forgets to look still gets a non-nil error. The code is on the
// type for the command layer, which maps it back to the same 1 and 2.

package validate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

// ShapeError is a key that is missing, a type that is wrong, or an enum value
// out of range: exit 1 in lib/validate.sh:7-13.
//
// It is deliberately not general JSON Schema validation. Neither jq nor yq can
// do that, and the schemas stay flat enough that required keys, right types and
// in-range enums are sufficient.
type ShapeError struct {
	// Problem is the one line naming the first thing wrong, in the words the
	// shell prints.
	Problem string
}

func (e *ShapeError) Error() string { return e.Problem }

// Code is the exit status the shell returns for this kind.
func (e *ShapeError) Code() int { return 1 }

// SemanticError is a payload whose shape is perfect and whose content
// contradicts what the orchestrator itself supplied: exit 2 in
// lib/validate.sh:14-17.
type SemanticError struct {
	// Problem is the one line naming the first thing wrong, in the words the
	// shell prints.
	Problem string
}

func (e *SemanticError) Error() string { return e.Problem }

// Code is the exit status the shell returns for this kind.
func (e *SemanticError) Code() int { return 2 }

func shapef(format string, a ...any) *ShapeError {
	return &ShapeError{Problem: fmt.Sprintf(format, a...)}
}

func semanticf(format string, a ...any) *SemanticError {
	return &SemanticError{Problem: fmt.Sprintf(format, a...)}
}

// ---------------------------------------------------------------------------
// The jq value plumbing
// ---------------------------------------------------------------------------
//
// Both messages quote what they refused, and both quote it the way jq does: a
// verdict reaches the message through string interpolation, and the first bad
// element reaches it through `tojson`. Neither is Go's `encoding/json` default
// output, so the rules are reproduced here rather than approximated.

// jqType names a raw value the way jq's `type` does. An absent key is null,
// because jq indexing an object for a key it does not hold yields null.
func jqType(raw json.RawMessage) string {
	for _, b := range raw {
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

// jqString reports a raw value's string content, and whether it was a string at
// all. Everything that compares against a literal in lib/validate.sh goes
// through this, so a number never equals the word it prints as.
func jqString(raw json.RawMessage) (string, bool) {
	if jqType(raw) != "string" {
		return "", false
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", false
	}
	return s, true
}

// jqTruthy is jq's own notion, which the `//` alternative operator reads: only
// null and false are false, and everything else — 0, "" and [] included — is
// true.
func jqTruthy(raw json.RawMessage) bool {
	switch jqType(raw) {
	case "null":
		return false
	case "boolean":
		return !bytes.Contains(raw, []byte("false"))
	default:
		return true
	}
}

// jqAlt is `a // b`: b whenever a is absent, null or false.
func jqAlt(raw json.RawMessage, fallback string) json.RawMessage {
	if jqTruthy(raw) {
		return raw
	}
	return json.RawMessage(fallback)
}

// jqIn is `IN(...)`, which compares the value itself rather than its text: a
// number or an object is simply not equal to any of the words, so it is out of
// range rather than an error.
func jqIn(raw json.RawMessage, allowed ...string) bool {
	s, ok := jqString(raw)
	if !ok {
		return false
	}
	for _, a := range allowed {
		if s == a {
			return true
		}
	}
	return false
}

// jqInterp renders a value the way jq's `\(...)` and `jq -r` render it: a string
// arrives as its own content, and everything else as compact JSON.
func jqInterp(raw json.RawMessage) string {
	if s, ok := jqString(raw); ok {
		return s
	}
	return jqCompact(raw)
}

// jqCompact re-prints a value the way `tojson` and `jq -c` do.
//
// Re-prints rather than passes through, because jq parses before it writes:
// insignificant whitespace goes, `\/` comes back as a bare solidus, and an
// escaped code point comes back as the character it names. Measured against
// jq 1.8.1 rather than read off the JSON grammar.
//
// One thing is passed through: a number keeps the literal it was written as.
// jq since 1.7 links decNumber and does the same for everything except an
// exponent, which it rewrites into the General Decimal Arithmetic
// to-scientific-string form — `1e2` becomes `1E+2`. That rewrite is a property
// of how jq was built rather than of JSON, so the port keeps the literal and
// ADR 0019 records the difference. A finding's line number and a resolution's
// finding number are whole decimals, so nothing a shipped harness returns
// reaches it.
//
// Key order is the order the value was written in, which is jq's order too.
func jqCompact(raw json.RawMessage) string {
	if len(bytes.TrimSpace(raw)) == 0 {
		return "null"
	}
	var b strings.Builder
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := writeJQValue(&b, dec); err != nil {
		// Unreachable through the two validators, which parse the whole payload
		// before any message is built. Returning the input keeps a message
		// readable rather than empty if a caller ever hands over a fragment.
		return string(raw)
	}
	return b.String()
}

func writeJQValue(b *strings.Builder, dec *json.Decoder) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	switch v := tok.(type) {
	case json.Delim:
		switch v {
		case '{':
			b.WriteByte('{')
			for first := true; dec.More(); first = false {
				key, err := dec.Token()
				if err != nil {
					return err
				}
				if !first {
					b.WriteByte(',')
				}
				writeJQString(b, key.(string))
				b.WriteByte(':')
				if err := writeJQValue(b, dec); err != nil {
					return err
				}
			}
			if _, err := dec.Token(); err != nil {
				return err
			}
			b.WriteByte('}')
		case '[':
			b.WriteByte('[')
			for first := true; dec.More(); first = false {
				if !first {
					b.WriteByte(',')
				}
				if err := writeJQValue(b, dec); err != nil {
					return err
				}
			}
			if _, err := dec.Token(); err != nil {
				return err
			}
			b.WriteByte(']')
		}
	case string:
		writeJQString(b, v)
	case json.Number:
		b.WriteString(v.String())
	case bool:
		if v {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case nil:
		b.WriteString("null")
	}
	return nil
}

// writeJQString escapes one string the way jq escapes it: the seven short
// escapes, `\u00xx` for every other C0 control and for DEL, and every other
// character raw.
//
// Raw is the half Go's own encoder disagrees with. `encoding/json` escapes the
// three HTML-significant code points U+003C, U+003E and U+0026 by default, and
// jq writes all three as themselves — reached here through a finding whose title
// holds a comparison or an ampersand.
func writeJQString(b *strings.Builder, s string) {
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 || r == 0x7f {
				fmt.Fprintf(b, `\u%04x`, r)
				continue
			}
			if r == utf8.RuneError {
				// Go's decoder has already replaced each malformed byte with
				// one replacement character, where jq replaces each malformed
				// sequence with one. ADR 0019 records that difference and does
				// not close it; a valid U+FFFD in the payload writes through
				// here unchanged either way.
				b.WriteRune(utf8.RuneError)
				continue
			}
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
}
