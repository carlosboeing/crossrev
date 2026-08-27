package prstate

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
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
// A scalar is preserved as it was written rather than re-serialised. jq parses
// and prints, so it also rewrites an escaped code point to the character it
// names, an escaped solidus to a bare one, and `1e2` to `1E+2`. Every marker
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
// live example, still migrated on resume at lib/run.sh:1993-1999. Every marker
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
		keyTok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, ok := keyTok.(string)
		if !ok {
			return nil, ErrMarkerPayload
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
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
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
// is the boundary this function sits on. Bash does that one at lib/run.sh:1999,
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
		if value, ok := entry.get("resolution"); ok && string(value) == `"rebutted"` {
			entry = entry.set("resolution", json.RawMessage(`"disputed"`))
		}
		if !first {
			out = append(out, ',')
		}
		first = false
		out = append(out, entry.marshal()...)
	}
	return append(out, ']'), nil
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
// The walk is byte-oriented, which is safe because every byte of a multi-byte
// rune is at or above 0x80 and falls through unchanged. The strings that reach
// here have all been through encoding/json, which has already replaced any
// invalid UTF-8 with U+FFFD, so there is nothing left for jq's own replacement
// to disagree with.
func appendJSONString(dst []byte, s string) []byte {
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
