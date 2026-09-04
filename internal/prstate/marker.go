package prstate

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"
)

// MarkerPrefix opens a pass marker, and FindingMarkerPrefix opens the
// per-finding one.
//
// Both are matched literally and both stay lowercase. Every pull request
// CrossRev has ever touched carries these bytes, and a case change would find
// none of them: nothing errors, the pass simply reads as never having run.
const (
	MarkerPrefix        = "<!-- crossrev:"
	FindingMarkerPrefix = "<!-- crossrev:f"
)

// The delimiters the extraction actually splits on. The pass marker's opening
// delimiter ends in a space the finding one lacks, which is the only thing
// keeping the two readers apart (lib/state.sh:74-75).
const (
	markerOpen        = MarkerPrefix + " "
	findingMarkerOpen = FindingMarkerPrefix + " "
	markerClose       = " -->"
)

// ErrMarkerPayload is the parse failure that decodes to nothing.
//
// jq's `_migrate` throws on `has` for an array, a string or a number payload
// and the `?` swallows it; a null payload survives the migration untouched and
// is dropped by the trailing `select` (lib/state.sh:118-119). All three end as
// nothing, so a payload that is not one JSON object never becomes a marker.
//
// It is exported and wrapped rather than kept private because EditMarker is a
// caller-facing parse and every parser in internal/core exports the sentinel
// its refusal carries. DecodeMarker still collapses it to false, because
// nothing in Bash reports a marker it could not read.
var ErrMarkerPayload = errors.New("a marker payload is not one JSON object")

// DecodeMarker pulls the marker out of one comment body and applies the
// vocabulary migration, returning the compact bytes `jq -c` would print
// (lib/state.sh:123-127).
//
// The bytes are returned rather than a struct because they are what a caller
// compares, logs and re-embeds. The typed view is Marker, through ParseMarker,
// and it keeps only the keys it declares: a caller re-embedding a marker it did
// not build works from these bytes, which Marker.Raw hands back and EditMarker
// edits in place.
//
// A scalar VALUE is preserved as it was written rather than re-serialised; a
// key is re-printed, because the object is rebuilt around it. jq parses and
// prints, so it also rewrites an escaped code point to the character it names,
// an escaped solidus to a bare one, and `1e2` to `1E+2`. Every marker
// CrossRev writes has already been through `jq -c`, so it is in that form when
// it is read back and the two agree; a marker written by hand in another form
// keeps its own bytes here. Chasing the difference would mean reproducing one
// jq build's number formatting in Go, which is a worse contract than the bytes
// on the pull request. The write path is the other way round, and normalise
// says why.
func DecodeMarker(body string) (json.RawMessage, bool) {
	payload := extractMarkerPayload(body)
	if payload == "" {
		return nil, false
	}
	obj, err := parseObject([]byte(payload))
	if err != nil {
		return nil, false
	}
	migrated, err := migrate(obj)
	if err != nil {
		return nil, false
	}
	return migrated.marshal(), true
}

// EncodeMarker serialises a marker for embedding in a comment body
// (lib/state.sh:159).
//
// The payload is normalised rather than remarshalled, so the caller's key order
// survives. `jq -c .` keeps insertion order and Go sorts a map's keys, and the
// result is embedded verbatim in a public pull-request comment: rebuilding the
// object from a map would rewrite every marker on every edit and leave a noisy
// diff on the pull request forever.
//
// A payload that is not JSON is refused. The shell does not refuse it: `jq -c .`
// fails, the command substitution comes back empty, and state_marker_encode
// prints "\n\n<!-- crossrev:  -->". That is the worse answer, because an empty
// marker on a public comment reads as a pass that settled nothing and every
// later read of the pull request agrees with it.
//
// One more divergence, and Go is right about it too: jq-1.8.1 stops printing at
// a nesting depth of 256 and writes the literal text `<skipped: too deep>` in
// place of the value, then exits 0 — so the shell writes an invalid marker and
// says nothing, where Go writes the correct bytes. Measured by nesting one key
// 257 deep. A marker nests three deep, so nothing reaches it.
func EncodeMarker(payload json.RawMessage) (string, error) {
	normalised, err := normalise(payload)
	if err != nil {
		return "", fmt.Errorf("rendering a marker payload: %w", err)
	}
	return "\n\n" + markerOpen + string(normalised) + markerClose, nil
}

// MarkerEdit is one change to a decoded marker, in the two shapes lib/run.sh
// uses: jq's `.[$k] = v`, and `del(.k)` when Delete is set.
type MarkerEdit struct {
	Key    string
	Value  json.RawMessage
	Delete bool
}

// EditMarker applies edits to a decoded marker's bytes, keeping every key it
// does not name and the order those keys sit in.
//
// This is the route the struct view cannot offer. Marker keeps only the fields
// it declares, so reading a marker into it and writing it back drops whatever a
// future writer added and whatever an older one left behind — `wrap_up` is a
// live example, still migrated on resume at lib/run.sh:1999-2005. Every marker
// writer in lib/run.sh edits in place on a jq object for exactly that reason,
// and this is that operation.
//
// An assignment to a key already present replaces it where it sits; a new key
// lands at the end. That is jq's `.[$k] = v`, and the difference is observable,
// because the bytes go verbatim into a public comment.
func EditMarker(raw json.RawMessage, edits ...MarkerEdit) (json.RawMessage, error) {
	obj, err := parseObject(raw)
	if err != nil {
		return nil, fmt.Errorf("editing a marker: %w", err)
	}
	for _, edit := range edits {
		if edit.Delete {
			obj = obj.del(edit.Key)
			continue
		}
		value, err := compactValue(edit.Value)
		if err != nil {
			return nil, fmt.Errorf("editing a marker's %s: %w", edit.Key, err)
		}
		obj = obj.set(edit.Key, value)
	}
	return obj.marshal(), nil
}

// extractMarkerPayload applies the per-line rule the decoder's jq applies: on
// each line, the text after the LAST opening delimiter and before the LAST
// closing one after it, with every line's result concatenated
// (lib/state.sh:113-117).
//
// A body carrying two markers therefore concatenates to two JSON values, which
// no parser accepts, and the comment is skipped. That is deliberate: the
// earlier reader fed the pair to jq's own stream parser, and its caller then
// rejected the pair and returned nothing at all, losing every marker on the
// pull request rather than that one comment.
func extractMarkerPayload(body string) string {
	var out strings.Builder
	for line := range strings.SplitSeq(body, "\n") {
		i := strings.LastIndex(line, markerOpen)
		if i < 0 {
			continue
		}
		rest := line[i+len(markerOpen):]
		j := strings.LastIndex(rest, markerClose)
		if j < 0 {
			continue
		}
		out.WriteString(rest[:j])
	}
	return out.String()
}

// member is one key and its raw value, in the position the object holds it.
type member struct {
	key   string
	value json.RawMessage
}

// object is a JSON object that remembers its key order.
//
// Go's map does not, and encoding/json sorts a map's keys on the way out. The
// marker's bytes are read by a person on the pull request and rewritten on
// every comment edit, so the order is part of the value.
type object []member

func (o object) index(key string) int {
	for i := range o {
		if o[i].key == key {
			return i
		}
	}
	return -1
}

func (o object) get(key string) (json.RawMessage, bool) {
	if i := o.index(key); i >= 0 {
		return o[i].value, true
	}
	return nil, false
}

// set replaces an existing key in place, or appends a new one at the end.
// jq's `.[$k] = v` behaves exactly this way, and the difference is observable:
// the renamed `resolutions` key lands at the end of the marker.
func (o object) set(key string, value json.RawMessage) object {
	if i := o.index(key); i >= 0 {
		o[i].value = value
		return o
	}
	return append(o, member{key: key, value: value})
}

// del returns the object without one key, leaving the receiver alone.
//
// `append(o[:i], o[i+1:]...)` would shift the elements left inside the array
// the receiver still points at, so a caller holding the object it passed in
// would find its last member duplicated.
func (o object) del(key string) object {
	i := o.index(key)
	if i < 0 {
		return o
	}
	out := make(object, 0, len(o)-1)
	out = append(out, o[:i]...)
	return append(out, o[i+1:]...)
}

// marshal writes the object compactly, with jq's string escaping.
func (o object) marshal() []byte {
	out := []byte{'{'}
	for i, m := range o {
		if i > 0 {
			out = append(out, ',')
		}
		out = appendJSONString(out, m.key)
		out = append(out, ':')
		out = append(out, m.value...)
	}
	return append(out, '}')
}

// parseObject reads one JSON object into ordered members, keeping each value's
// compact bytes.
//
// Anything that is not one complete object is refused, which is the whole of
// the payload rule. A duplicate key keeps the last value at the first key's
// position, which is what jq's parser does.
func parseObject(b []byte) (object, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if delim, ok := tok.(json.Delim); !ok || delim != '{' {
		return nil, ErrMarkerPayload
	}
	var obj object
	for dec.More() {
		// The key is taken from the input bytes rather than from the token.
		// A token is a Go string, and encoding/json has already replaced any
		// invalid UTF-8 in it one U+FFFD per byte where jq replaces one per
		// sequence; unquoteJSONString leaves those bytes alone so that the
		// one rule lives in appendJSONString and applies to keys and values
		// alike. InputOffset brackets the token, and the only bytes it can
		// hold ahead of the literal are whitespace and one comma, so the
		// first quote in the bracket opens the key.
		start := dec.InputOffset()
		keyTok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		if _, ok := keyTok.(string); !ok {
			return nil, ErrMarkerPayload
		}
		lit := b[start:dec.InputOffset()]
		if q := bytes.IndexByte(lit, '"'); q >= 0 {
			lit = lit[q:]
		}
		key, err := unquoteJSONString(lit)
		if err != nil {
			return nil, err
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, err
		}
		compact, err := compactValue(raw)
		if err != nil {
			return nil, err
		}
		obj = obj.set(key, compact)
	}
	if _, err := dec.Token(); err != nil { // the closing brace
		return nil, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, ErrMarkerPayload
	}
	return obj, nil
}

func compactValue(raw json.RawMessage) (json.RawMessage, error) {
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return nil, err
	}
	return json.RawMessage(buf.Bytes()), nil
}

// normalise rewrites a JSON value into the bytes `jq -c .` prints for it.
//
// state_marker_encode is `jq -c .`, which parses and re-prints. json.Compact
// only strips whitespace, and the difference is reachable: the payload a marker
// carries under `findings`, `resolutions`, `tokens` and `usage` is model output
// that has never met jq, where in Bash the jq that assembles the marker has
// already normalised it. Measured against jq-1.8.1, that covers an escaped
// solidus, a `\uXXXX` escape for anything that does not need one, a duplicate
// key, and insignificant whitespace inside a nested value.
//
// A number keeps the literal it was written with, which is the one place this
// is not `jq -c .`, and it is deliberate. jq 1.7 and later print the literal
// too, so the two already agree on every form measured except an exponent,
// where jq prints decNumber's canonical `1E+2` for `1e2` and `0.01` for `1e-2`.
// jq 1.6 printed the IEEE double instead, so `1.50` came out `1.5`. Reproducing
// either would pin one jq family into the contract for a form no marker
// CrossRev writes carries.
func normalise(raw json.RawMessage) (json.RawMessage, error) {
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return nil, err
	}
	b := json.RawMessage(buf.Bytes())
	if len(b) == 0 {
		return nil, ErrMarkerPayload
	}
	switch b[0] {
	case '{':
		obj, err := parseObject(b)
		if err != nil {
			return nil, err
		}
		for i := range obj {
			value, err := normalise(obj[i].value)
			if err != nil {
				return nil, err
			}
			obj[i].value = value
		}
		return obj.marshal(), nil
	case '[':
		return normaliseArray(b)
	case '"':
		s, err := unquoteJSONString(b)
		if err != nil {
			return nil, err
		}
		return appendJSONString(nil, s), nil
	default:
		return b, nil
	}
}

func normaliseArray(raw json.RawMessage) (json.RawMessage, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	if _, err := dec.Token(); err != nil { // the opening bracket
		return nil, err
	}
	out := []byte{'['}
	first := true
	for dec.More() {
		var element json.RawMessage
		if err := dec.Decode(&element); err != nil {
			return nil, err
		}
		value, err := normalise(element)
		if err != nil {
			return nil, err
		}
		if !first {
			out = append(out, ',')
		}
		first = false
		out = append(out, value...)
	}
	return append(out, ']'), nil
}

// jqMarshal encodes v into the bytes `jq -c .` would print for it, so the
// struct view and the byte view cannot disagree about what a marker looks like.
func jqMarshal(v any) ([]byte, error) {
	raw, err := marshalNoEscape(v)
	if err != nil {
		return nil, err
	}
	return normalise(raw)
}

// migrate applies the vocabulary rename at the single point every marker is
// decoded through (lib/state.sh:101-112).
//
// `dispositions` became `resolutions` and `rebutted` became `disputed`, because
// both were borrowed vocabulary for a field bug trackers have called a
// resolution for decades. Reading the old keys here rather than at each of the
// dozen places a marker is read is what stops a reader forgetting the fallback:
// a read that forgot it would not fail loudly, it would report a settled pass
// as having settled nothing.
//
// The `wrap_up` to `summary` fallback is deliberately NOT here, and the reason
// is the boundary this function sits on. Bash does that one at lib/run.sh:2005,
// inside the resolve leg's resume, not in state_marker_of. Moving it here would
// make DecodeMarker print a key state_marker_of does not, on a marker written
// before the rename — a divergence in the frozen decode contract, to save one
// call in one leg. The leg that resumes performs it, through Marker.Raw and
// EditMarker.
func migrate(obj object) (object, error) {
	obj = rename(obj, "dispositions", "resolutions")
	for _, key := range []string{"resolutions", "findings"} {
		value, ok := obj.get(key)
		if !ok || !isJSONArray(value) {
			continue
		}
		migrated, err := migrateEntries(value)
		if err != nil {
			return nil, err
		}
		obj = obj.set(key, migrated)
	}
	return obj, nil
}

// rename moves a value onto a new key when the new key is absent, and leaves
// both alone when it is present (lib/state.sh:98-100).
func rename(obj object, from, to string) object {
	value, ok := obj.get(from)
	if !ok {
		return obj
	}
	if _, exists := obj.get(to); exists {
		return obj
	}
	return obj.del(from).set(to, value)
}

// migrateEntries renames `disposition` to `resolution` on every entry and
// rewrites the one retired value.
//
// An entry that is not an object makes jq's `has` throw, and the `?` swallows
// the throw for the whole marker, so the marker decodes to nothing rather than
// to a partly migrated object. The error return is that behaviour.
func migrateEntries(raw json.RawMessage) (json.RawMessage, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	if _, err := dec.Token(); err != nil { // the opening bracket
		return nil, err
	}
	out := []byte{'['}
	first := true
	for dec.More() {
		var element json.RawMessage
		if err := dec.Decode(&element); err != nil {
			return nil, err
		}
		entry, err := parseObject(element)
		if err != nil {
			return nil, err
		}
		entry = rename(entry, "disposition", "resolution")
		// The value is decoded before it is compared, because jq compares
		// strings and not the bytes they were written with: `"\u0072ebutted"`
		// is the retired value and migrates in the shell. Keys a few lines up
		// are decoded for the same reason, and comparing one side raw and the
		// other decoded is the inconsistency this avoids.
		if value, ok := entry.get("resolution"); ok {
			if s, err := unquoteJSONString(value); err == nil && s == "rebutted" {
				entry = entry.set("resolution", json.RawMessage(`"disputed"`))
			}
		}
		if !first {
			out = append(out, ',')
		}
		first = false
		out = append(out, entry.marshal()...)
	}
	return append(out, ']'), nil
}

// stripKey removes one key from a compact JSON object, leaving the rest in the
// order and the bytes they were read with. An object that does not carry the
// key is returned untouched rather than re-printed.
func stripKey(raw json.RawMessage, key string) (json.RawMessage, error) {
	obj, err := parseObject(raw)
	if err != nil {
		return nil, err
	}
	if obj.index(key) < 0 {
		return raw, nil
	}
	return obj.del(key).marshal(), nil
}

func isJSONArray(raw json.RawMessage) bool {
	return len(raw) > 0 && raw[0] == '['
}

// hexDigits is the alphabet a \u escape is written in. jq writes lowercase.
const hexDigits = "0123456789abcdef"

// appendJSONString writes a JSON string the way jq prints one.
//
// jq escapes the quote, the backslash and the five C0 controls that have a
// two-character form, writes every other C0 control and DEL as \u00xx, and
// leaves everything else as raw UTF-8. Measured over all 128 ASCII code points
// plus U+0085, U+00A0, U+2028, U+2029, U+FEFF and an astral pair, on jq-1.8.1.
//
// Go's own encoder differs in three places, and all three reach a public
// comment through model text: it escapes `<`, `>` and `&` unless that is turned
// off, it writes DEL raw, and it escapes U+2028 and U+2029 whatever
// SetEscapeHTML says. Hence the loop rather than a call into encoding/json.
//
// Invalid UTF-8 is replaced first, by replaceInvalidUTF8, so the walk itself
// can stay byte-oriented: after that every byte at or above 0x80 belongs to a
// valid multi-byte rune and falls through unchanged.
func appendJSONString(dst []byte, s string) []byte {
	s = replaceInvalidUTF8(s)
	dst = append(dst, '"')
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case '"':
			dst = append(dst, '\\', '"')
		case '\\':
			dst = append(dst, '\\', '\\')
		case '\b':
			dst = append(dst, '\\', 'b')
		case '\f':
			dst = append(dst, '\\', 'f')
		case '\n':
			dst = append(dst, '\\', 'n')
		case '\r':
			dst = append(dst, '\\', 'r')
		case '\t':
			dst = append(dst, '\\', 't')
		default:
			if c < 0x20 || c == 0x7f {
				dst = append(dst, '\\', 'u', '0', '0', hexDigits[c>>4], hexDigits[c&0xf])
				continue
			}
			dst = append(dst, c)
		}
	}
	return append(dst, '"')
}

// errNotJSONString is what unquoteJSONString refuses with. It is unexported
// because no caller distinguishes it: every one of them is reading a literal
// encoding/json has already accepted, so a refusal here is a bug rather than a
// payload a user wrote.
var errNotJSONString = errors.New("a value is not a JSON string")

// unquoteJSONString reads one JSON string literal back to the bytes it stands
// for, leaving every byte that is not part of an escape exactly as it was.
//
// encoding/json's own unquote cannot be used for this. It replaces invalid
// UTF-8 on the way through, one U+FFFD per byte, and jq replaces one per
// sequence; keeping the bytes intact until appendJSONString writes them is what
// lets that single rule cover keys and values alike.
//
// A lone surrogate escape becomes U+FFFD here, which is what encoding/json does
// and what jq-1.8.1 does for a lone LOW surrogate. jq refuses the whole payload
// for a lone HIGH one — measured on `{"s":"\ud800"}`, which is a parse error,
// against `{"s":"\udc00"}`, which is one U+FFFD. Under the shell that refusal
// prints the 21-byte empty marker EncodeMarker exists to avoid, and Go can
// never emit a lone surrogate escape of its own, so the divergence is left
// where it is rather than reproduced.
func unquoteJSONString(lit []byte) (string, error) {
	if len(lit) < 2 || lit[0] != '"' || lit[len(lit)-1] != '"' {
		return "", errNotJSONString
	}
	body := lit[1 : len(lit)-1]
	if bytes.IndexByte(body, '\\') < 0 {
		return string(body), nil
	}
	out := make([]byte, 0, len(body))
	for i := 0; i < len(body); {
		if body[i] != '\\' {
			out = append(out, body[i])
			i++
			continue
		}
		if i+1 >= len(body) {
			return "", errNotJSONString
		}
		switch body[i+1] {
		case '"', '\\', '/':
			out = append(out, body[i+1])
			i += 2
		case 'b':
			out = append(out, '\b')
			i += 2
		case 'f':
			out = append(out, '\f')
			i += 2
		case 'n':
			out = append(out, '\n')
			i += 2
		case 'r':
			out = append(out, '\r')
			i += 2
		case 't':
			out = append(out, '\t')
			i += 2
		case 'u':
			r, next, ok := unicodeEscape(body, i)
			if !ok {
				return "", errNotJSONString
			}
			out = utf8.AppendRune(out, r)
			i = next
		default:
			return "", errNotJSONString
		}
	}
	return string(out), nil
}

// unicodeEscape reads the \uXXXX escape whose backslash is body[i], joining a
// surrogate pair when the escape after it completes one, and answers the rune
// and the index just past what it consumed.
func unicodeEscape(body []byte, i int) (rune, int, bool) {
	r, ok := hex4(body, i+2)
	if !ok {
		return 0, 0, false
	}
	i += 6
	if !utf16.IsSurrogate(r) {
		return r, i, true
	}
	if i+6 <= len(body) && body[i] == '\\' && body[i+1] == 'u' {
		if low, ok := hex4(body, i+2); ok {
			if joined := utf16.DecodeRune(r, low); joined != unicode.ReplacementChar {
				return joined, i + 6, true
			}
		}
	}
	return unicode.ReplacementChar, i, true
}

// hex4 reads the four hex digits at body[i:i+4].
func hex4(body []byte, i int) (rune, bool) {
	if i+4 > len(body) {
		return 0, false
	}
	var r rune
	for _, c := range body[i : i+4] {
		var v rune
		switch {
		case c >= '0' && c <= '9':
			v = rune(c - '0')
		case c >= 'a' && c <= 'f':
			v = rune(c-'a') + 10
		case c >= 'A' && c <= 'F':
			v = rune(c-'A') + 10
		default:
			return 0, false
		}
		r = r<<4 | v
	}
	return r, true
}

// utf8LeadLength is how many bytes a lead byte claims, or zero for a byte that
// can never lead a sequence: a continuation byte, the two overlong-only leads
// 0xC0 and 0xC1, and 0xF5 upwards, which lead either a code point past U+10FFFF
// or one of the withdrawn five and six byte forms.
//
// The boundaries decide how many U+FFFD an invalid run produces, so each was
// measured rather than assumed: `\xc0\x80` is two U+FFFD under jq and
// `\xe0\x80\x80` is one, which is 0xC0 leading nothing where 0xE0 leads three.
func utf8LeadLength(c byte) int {
	switch {
	case c < 0x80:
		return 1
	case c < 0xc2:
		return 0
	case c < 0xe0:
		return 2
	case c < 0xf0:
		return 3
	case c < 0xf5:
		return 4
	default:
		return 0
	}
}

// replaceInvalidUTF8 rewrites s the way jq's own string constructor does, so a
// harness that emits a truncated multi-byte sequence produces the same marker
// bytes under Go as under the shell.
//
// Go and jq both replace invalid UTF-8 with U+FFFD and disagree on how much
// each U+FFFD stands for. encoding/json replaces one per BYTE, so `\xe4\xb8`
// becomes two; jq replaces one per SEQUENCE, so it becomes one. Measured
// against jq-1.8.1, feeding raw bytes through `jq -c .`:
//
//	\xe4\xb8         one U+FFFD        (truncated 3-byte sequence)
//	\xf0\x9f\x98     one U+FFFD        (truncated 4-byte sequence)
//	\xe4\xb8Z        one U+FFFD then Z
//	\xc3\xc3         two U+FFFD        (neither byte continues the other)
//	\xf0Z            one U+FFFD, no Z  (a truncated tail is consumed whole)
//	\xe0\x80\x80     one U+FFFD        (overlong, structurally complete)
//	\xf7\xbf\xbf\xbf  four U+FFFD       (0xF7 leads nothing, nor do the rest)
//
// The rule those measurements give, applied at each position:
//
//   - a lead byte that leads nothing is one U+FFFD and one byte consumed;
//   - a sequence that runs past the end of the string is one U+FFFD and the
//     rest of the string consumed, whatever those bytes are;
//   - a sequence whose continuation bytes stop early is one U+FFFD and the
//     lead plus the continuation bytes it did have consumed;
//   - a structurally complete sequence encoding an overlong form, a surrogate
//     or a code point past U+10FFFF is one U+FFFD and the whole sequence
//     consumed.
//
// The rule is derived from what jq does rather than from Unicode's
// maximal-subpart recommendation, which the fifth line above departs from: the
// recommendation would keep the Z.
//
// One route it cannot reach: jqMarshal marshals a struct through encoding/json
// before normalise re-reads it, so a Go string field carrying invalid UTF-8 has
// already been replaced byte by byte before it arrives. Closing that would mean
// replacing encoding/json's struct encoder. The payload fields — `findings`,
// `resolutions`, `tokens` and `usage`, where a harness's own bytes land — are
// raw JSON, and they reach unquoteJSONString untouched.
func replaceInvalidUTF8(s string) string {
	if utf8.ValidString(s) {
		return s
	}
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); {
		n := utf8LeadLength(s[i])
		switch {
		case n == 1:
			out = append(out, s[i])
			i++
		case n == 0:
			out = utf8.AppendRune(out, utf8.RuneError)
			i++
		case i+n > len(s):
			out = utf8.AppendRune(out, utf8.RuneError)
			i = len(s)
		default:
			k := 1
			for k < n && s[i+k]&0xc0 == 0x80 {
				k++
			}
			if k < n {
				out = utf8.AppendRune(out, utf8.RuneError)
				i += k
				continue
			}
			if r, size := utf8.DecodeRuneInString(s[i : i+n]); r == utf8.RuneError && size == 1 {
				out = utf8.AppendRune(out, utf8.RuneError)
			} else {
				out = append(out, s[i:i+n]...)
			}
			i += n
		}
	}
	return string(out)
}
