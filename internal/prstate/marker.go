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
// keeping the two readers apart (lib/state.sh:83).
const (
	markerOpen        = MarkerPrefix + " "
	findingMarkerOpen = FindingMarkerPrefix + " "
	markerClose       = " -->"
)

// errNotOneObject is the parse failure that decodes to nothing.
//
// jq's `_migrate` throws on `has` for an array, a string or a number payload
// and the `?` swallows it; a null payload survives the migration untouched and
// is dropped by the trailing `select` (lib/state.sh:110-119). All three end as
// nothing, so a payload that is not one JSON object never becomes a marker.
var errNotOneObject = errors.New("a marker payload is not one JSON object")

// DecodeMarker pulls the marker out of one comment body and applies the
// vocabulary migration, returning the compact bytes `jq -c` would print
// (lib/state.sh:122-127).
//
// The bytes are returned rather than a struct because they are what a caller
// compares, logs and re-embeds. The typed view is Marker, through ParseMarker,
// and it keeps only the keys it declares: a caller re-embedding a marker it did
// not build works from these bytes instead.
//
// A scalar is preserved as it was written rather than re-serialised. jq parses
// and prints, so it also rewrites an escaped code point to the character it
// names, an escaped solidus to a bare one, and `1e2` to `1E+2`. Every marker
// CrossRev writes has already been through
// `jq -c`, so it is in that form when it is read back and the two agree; a
// marker written by hand in another form keeps its own bytes here. Chasing the
// difference would mean reproducing one jq build's number formatting in Go,
// which is a worse contract than the bytes on the pull request.
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
// The payload is compacted rather than remarshalled, so the caller's key order
// survives. `jq -c .` keeps insertion order and Go sorts a map's keys, and the
// result is embedded verbatim in a public pull-request comment: rebuilding the
// object from a map would rewrite every marker on every edit and leave a noisy
// diff on the pull request forever.
func EncodeMarker(payload json.RawMessage) (string, error) {
	var buf bytes.Buffer
	if err := json.Compact(&buf, payload); err != nil {
		return "", fmt.Errorf("compacting a marker payload: %w", err)
	}
	return "\n\n" + markerOpen + buf.String() + markerClose, nil
}

// extractMarkerPayload applies the per-line rule the decoder's jq applies: on
// each line, the text after the LAST opening delimiter and before the LAST
// closing one after it, with every line's result concatenated
// (lib/state.sh:104-108).
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

func (o object) del(key string) object {
	if i := o.index(key); i >= 0 {
		return append(o[:i], o[i+1:]...)
	}
	return o
}

// marshal writes the object compactly, without Go's HTML escaping.
//
// encoding/json rewrites `<`, `>` and `&` as escapes by default and jq rewrites
// none of them. A finding title carrying any of the three would then read
// differently in the marker than it does in the comment above it.
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
		return nil, errNotOneObject
	}
	var obj object
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, ok := keyTok.(string)
		if !ok {
			return nil, errNotOneObject
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
		return nil, errNotOneObject
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

// migrate applies the vocabulary rename at the single point every marker is
// decoded through (lib/state.sh:91-105).
//
// `dispositions` became `resolutions` and `rebutted` became `disputed`, because
// both were borrowed vocabulary for a field bug trackers have called a
// resolution for decades. Reading the old keys here rather than at each of the
// dozen places a marker is read is what stops a reader forgetting the fallback:
// a read that forgot it would not fail loudly, it would report a settled pass
// as having settled nothing.
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
// both alone when it is present (lib/state.sh:92-94).
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

// appendJSONString writes a JSON string without Go's HTML escaping, which is
// what jq writes.
func appendJSONString(dst []byte, s string) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(s); err != nil {
		// A Go string always encodes. The fallback keeps the function total.
		return append(dst, '"', '"')
	}
	return append(dst, bytes.TrimRight(buf.Bytes(), "\n")...)
}
