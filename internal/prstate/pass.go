package prstate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/carlosboeing/crossrev/internal/core"
)

// Marker is one pass marker, with its fields in the order the writers in
// lib/run.sh build them.
//
// The order is the point of the type. The three writers each build their object
// with one jq expression, `jq -c` keeps insertion order, and the result is
// embedded verbatim in a comment body. Go sorts a map's keys, so a marker built
// from a map would rewrite every comment on every edit.
//
// The union covers all three writers: the review claim at lib/run.sh:1101, the
// resolve claim at lib/run.sh:1963 and the declined marker at lib/run.sh:1056.
// A field one writer never sets is absent from its marker, not null.
//
// A union orders fields no single writer orders. Three pairs never co-occur —
// `verdict` with `blocked`, `findings` with `resolutions`, `unanchored` with
// `unthreaded` — because the first of each is the review leg's and the second
// is the resolve leg's. Their relative order here is a choice with nothing to
// check it against, and no test can pin it, because no marker carries both.
// Every other neighbouring pair is observable, and the round-trip tables in
// marker_struct_test.go hold the bytes.
//
// Payload fields stay as raw bytes. `findings` and `resolutions` carry the
// model's own key order through unchanged, and `tokens` and `usage` are
// accounted for elsewhere; remarshalling any of them through a Go type here
// would reorder bytes this package has no reason to touch. They are still
// normalised on the way out, because `jq -c .` normalises them in Bash too, and
// normalise says what that covers.
//
// The comment id is not a field. Every one of the twelve marker writes in
// lib/run.sh wraps its marker in `jq -c 'del(.comment_id)'` first, so the id
// exists on the in-memory marker and never on the wire. Keeping it unexported
// and reachable only through CommentID means encoding/json cannot see it at
// all, so the natural edit — read a marker, change one field, write it back —
// cannot add a key to a public comment. The alternative, stripping it inside
// Encode, would stop it happening today without making it impossible.
//
// It still has to be READ, because state_markers writes it into the marker
// object rather than beside it (lib/state.sh:151). UnmarshalJSON lifts it out
// of the bytes into commentID, so the byte route through Raw cannot carry it
// back either.
type Marker struct {
	Version        int             `json:"v,omitzero"`
	Leg            core.Leg        `json:"leg,omitzero"`
	Pass           int             `json:"pass,omitzero"`
	State          core.PassState  `json:"state,omitzero"`
	TS             int64           `json:"ts,omitzero"`
	DoneTS         Opt[int64]      `json:"done_ts,omitzero"`
	RunID          Opt[string]     `json:"run_id,omitzero"`
	HeadSHA        Opt[string]     `json:"head_sha,omitzero"`
	Harness        Opt[string]     `json:"harness,omitzero"`
	Model          Opt[string]     `json:"model,omitzero"`
	Effort         Opt[string]     `json:"effort,omitzero"`
	Endpoint       Opt[string]     `json:"endpoint,omitzero"`
	ModelReported  Opt[string]     `json:"model_reported,omitzero"`
	Tokens         json.RawMessage `json:"tokens,omitzero"`
	Usage          json.RawMessage `json:"usage,omitzero"`
	Billing        Opt[string]     `json:"billing,omitzero"`
	Verdict        Opt[string]     `json:"verdict,omitzero"`
	Blocked        Opt[bool]       `json:"blocked,omitzero"`
	BlockedReason  Opt[string]     `json:"blocked_reason,omitzero"`
	Reason         Opt[string]     `json:"reason,omitzero"`
	CommitSHA      Opt[string]     `json:"commit_sha,omitzero"`
	CommitSubject  Opt[string]     `json:"commit_subject,omitzero"`
	Summary        Opt[string]     `json:"summary,omitzero"`
	Findings       json.RawMessage `json:"findings,omitzero"`
	Resolutions    json.RawMessage `json:"resolutions,omitzero"`
	EffortReported Opt[string]     `json:"effort_reported,omitzero"`
	Unanchored     Opt[int]        `json:"unanchored,omitzero"`
	Unthreaded     Opt[int]        `json:"unthreaded,omitzero"`

	// commentID is which comment the marker was read off, and raw is the
	// bytes it was read as. Both are unexported so no encoder can reach
	// them; the accessors below are the only route.
	commentID int64
	raw       json.RawMessage
}

// The string fields above are Opt rather than plain strings for the reason Opt
// exists: `omitzero` on a plain string drops a present empty value, and
// lib/run.sh:1056 and :1095 write `run_id`, `head_sha` and `reason`
// unconditionally, so an empty one is present on the wire. The numeric and
// enum fields keep the plain form, because no writer produces a zero version, a
// zero pass, a zero timestamp or an empty leg or state: for those the zero
// value really does mean absent.

// markerFields is Marker under a name with no methods, so MarshalJSON can hand
// the struct to the encoder without calling itself.
type markerFields Marker

// CommentID is which comment the marker was read off, or zero for a marker that
// was built rather than read.
func (m Marker) CommentID() int64 { return m.commentID }

// Raw is the marker exactly as DecodeMarker read it, before the struct dropped
// the keys it does not declare.
//
// The struct view is lossy on purpose — it is a fixed field list — so a caller
// continuing state an older writer left behind reads it from here and edits it
// with EditMarker. `wrap_up` is the live example: lib/run.sh:1999-2005 still
// migrates it on resume, and no field on Marker holds it.
//
// `comment_id` is not one of those keys, even though lib/state.sh:151 adds it
// to every marker in the array ParseMarkers reads. UnmarshalJSON lifts it into
// commentID and takes it back out of these bytes, so the documented edit route
// — Raw, EditMarker, EncodeMarker — cannot put on a public comment the one key
// all twelve marker writes in lib/run.sh delete first.
//
// The bytes are a copy, so editing them cannot reach the marker.
func (m Marker) Raw() json.RawMessage { return bytes.Clone(m.raw) }

// MarshalJSON writes the marker in the writers' field order, in `jq -c`'s form.
//
// A payload field that is empty but not nil is dropped rather than written.
// json.RawMessage marshals its bytes verbatim, so a zero-length one is not a
// JSON value at all and fails the whole encode with "unexpected end of JSON
// input". Every reader here already reads a zero-length payload as absent —
// DecodeFindings and DecodeResolutions both — so the writer agrees with them.
func (m Marker) MarshalJSON() ([]byte, error) {
	for _, payload := range []*json.RawMessage{&m.Tokens, &m.Usage, &m.Findings, &m.Resolutions} {
		if len(*payload) == 0 {
			*payload = nil
		}
	}
	return jqMarshal(markerFields(m))
}

// UnmarshalJSON reads a marker field by field, keeping the marker when one
// field carries a JSON type the struct cannot hold.
//
// Decoding straight into the struct fails the whole marker on the first
// mismatch, and the caller above drops it. jq does not: the field stays in the
// object with whatever type it has, and every reader that guards with `// null`
// carries on. The difference is not academic — `commit_subject` is
// model-supplied at lib/run.sh:2115 — and it fails in the worst way, because
// nothing errors: every marker on the pull request stops decoding at once, Pass
// answers 1 forever, and the loop reviews a finished pull request again.
//
// So a field the struct cannot hold reads as absent and the marker survives.
// The value itself is not lost: Raw returns the bytes it was read with.
//
// One number form is absent here where jq reads a value: `{"pass":2.0}` leaves
// Pass at zero, because Go refuses a fractional literal for an int, where jq's
// `.pass == 2` is true. Every writer sets `pass` through `--argjson` with an
// integer, so only a hand-edited marker reaches it.
//
// The walk is reflective rather than a hand-written field list because the list
// would rot. A field added to Marker without a matching line would decode
// strictly again, and nothing would say so. The lookup is exact where
// encoding/json's is case-insensitive, which is the closer answer: jq's `.pass`
// is null for `{"PASS":3}`.
//
// `comment_id` is read here rather than by the walk, because it is not a field:
// state_markers puts it into every marker object it prints (lib/state.sh:151)
// and every marker write in lib/run.sh deletes it again, so it is lifted into
// commentID and taken out of the bytes Raw hands back.
func (m *Marker) UnmarshalJSON(b []byte) error {
	// A JSON null leaves a struct untouched, which is encoding/json's own
	// convention and what the strict decode did.
	if string(b) == "null" {
		return nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(b, &fields); err != nil {
		return err
	}
	raw, err := compactValue(b)
	if err != nil {
		return err
	}

	*m = Marker{raw: raw}
	if id, ok := fields[commentIDKey]; ok {
		// A type mismatch leaves the id at zero, the rule the walk below
		// applies to every other field.
		_ = json.Unmarshal(id, &m.commentID)
		if stripped, err := stripKey(raw, commentIDKey); err == nil {
			m.raw = stripped
		}
	}
	v := reflect.ValueOf(m).Elem()
	t := v.Type()
	for i := range t.NumField() {
		field := t.Field(i)
		// An unexported field has no json tag today, but tagging raw or
		// commentID would make Set below panic — on the path that writes a
		// public comment. The check is on what actually stops that rather
		// than on the missing tag standing in for it.
		if !field.IsExported() {
			continue
		}
		name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		if name == "-" {
			continue
		}
		if name == "" {
			// `json:",omitzero"` names no key, and encoding/json falls
			// back to the Go name. Skipping instead would decode the
			// field never, and say nothing.
			name = field.Name
		}
		value, ok := fields[name]
		if !ok {
			continue
		}
		// Decoded into a fresh value and assigned only once that
		// succeeded, so a field is never left half written whatever type
		// it grows into. Today every field is scalar-atomic and assigns
		// at the end anyway; a slice or a map would not be.
		scratch := reflect.New(field.Type)
		if json.Unmarshal(value, scratch.Interface()) == nil {
			v.Field(i).Set(scratch.Elem())
		}
	}
	return nil
}

// clone copies the four payloads a marker carries as raw bytes.
//
// Marker is returned by value, but a slice header shares its backing array, so
// a caller editing what a reader handed it would reach the marker list the
// reader read from.
func (m Marker) clone() Marker {
	m.Tokens = bytes.Clone(m.Tokens)
	m.Usage = bytes.Clone(m.Usage)
	m.Findings = bytes.Clone(m.Findings)
	m.Resolutions = bytes.Clone(m.Resolutions)
	return m
}

// Encode renders the marker for embedding in a comment body.
func (m Marker) Encode() (string, error) {
	raw, err := m.MarshalJSON()
	if err != nil {
		return "", err
	}
	return EncodeMarker(raw)
}

// DecodeResolutions unmarshals the marker's `resolutions` payload into v. A
// marker with no resolutions leaves v untouched.
func (m Marker) DecodeResolutions(v any) error {
	if len(m.Resolutions) == 0 {
		return nil
	}
	return json.Unmarshal(m.Resolutions, v)
}

// DecodeFindings unmarshals the marker's `findings` payload into v. A marker
// with no findings leaves v untouched.
func (m Marker) DecodeFindings(v any) error {
	if len(m.Findings) == 0 {
		return nil
	}
	return json.Unmarshal(m.Findings, v)
}

// ParseMarker reads one decoded marker payload into the typed view.
func ParseMarker(raw json.RawMessage) (Marker, error) {
	var m Marker
	if err := json.Unmarshal(raw, &m); err != nil {
		return Marker{}, fmt.Errorf("reading a marker: %w", err)
	}
	return m, nil
}

// commentIDKey is the key state_markers adds to every marker object it prints,
// and the one every marker write in lib/run.sh deletes before the bytes reach a
// comment.
const commentIDKey = "comment_id"

// ParseMarkers reads a JSON array of markers, which is the shape every reader
// below takes: the array state_markers prints, `comment_id` and all.
func ParseMarkers(raw []byte) ([]Marker, error) {
	var markers []Marker
	if err := json.Unmarshal(raw, &markers); err != nil {
		return nil, fmt.Errorf("reading a marker list: %w", err)
	}
	return markers, nil
}

// marshalNoEscape encodes v without rewriting `<`, `>` and `&` as escapes.
func marshalNoEscape(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// ran drops the markers that record a pass which never happened
// (lib/state.sh:268).
//
// A pass a cap refused to start still writes a marker, so `status` has
// something to render the refusal from. Three readers would be wrong without
// the filter: pass numbering would report the refused pass as the current one,
// revision detection would take its head SHA as reviewed, and the daily cap
// would count a run that never ran against the cap that stopped it.
func ran(markers []Marker) []Marker {
	kept := make([]Marker, 0, len(markers))
	for _, m := range markers {
		if !m.State.Declined() {
			kept = append(kept, m)
		}
	}
	return kept
}

// Pass is the pass number the next review belongs to (lib/state.sh:273-278).
// No trusted marker means pass 1.
func Pass(markers []Marker) int { return CurrentReviewPass(markers) + 1 }

// MaxPass is the highest pass number any marker mentions, refused passes
// included (lib/state.sh:283-285). `status` renders every pass, and a refused
// one is among the things it has to show.
func MaxPass(markers []Marker) int {
	highest := 0
	for _, m := range markers {
		if m.Pass > highest {
			highest = m.Pass
		}
	}
	return highest
}

// CurrentReviewPass is the pass the resolve leg belongs to: the newest review
// pass, not the next one (lib/state.sh:294-296).
func CurrentReviewPass(markers []Marker) int {
	highest := 0
	for _, m := range ran(markers) {
		if m.Leg == core.LegReview && m.Pass > highest {
			highest = m.Pass
		}
	}
	return highest
}

// CurrentPassComplete reports whether one leg of one pass settled
// (lib/state.sh:287-291).
func CurrentPassComplete(markers []Marker, pass int, leg core.Leg) bool {
	for _, m := range markers {
		if m.Pass == pass && m.Leg == leg && m.State == core.PassComplete {
			return true
		}
	}
	return false
}

// MarkerFor is the newest marker for a (pass, leg), whatever its state
// (lib/state.sh:301-305).
//
// The resolve leg needs the review leg's to read the finding list, and recovery
// needs its own to reuse the comment id rather than posting a second claim.
func MarkerFor(markers []Marker, pass int, leg core.Leg) (Marker, bool) {
	found := false
	var newest Marker
	for _, m := range markers {
		if m.Pass == pass && m.Leg == leg {
			newest, found = m, true
		}
	}
	if !found {
		return Marker{}, false
	}
	return newest.clone(), true
}

// IsNewRevision compares the pull request head against the last non-declined
// review marker's head SHA (lib/state.sh:344-349).
//
// GitHub has no "new revision" event and `synchronize` fires per push, so the
// marker is the comparison rather than the event.
func IsNewRevision(markers []Marker, headSHA string) bool {
	last := ""
	for _, m := range ran(markers) {
		if m.Leg == core.LegReview {
			last = m.HeadSHA.Value()
		}
	}
	return last == "" || last != headSHA
}
