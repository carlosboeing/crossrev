package prstate

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/carlosboeing/crossrev/internal/core"
)

// Opt is a marker field in all three of the states a marker distinguishes:
// absent, present and null, present and set.
//
// Every marker writer in lib/run.sh writes an explicit null for a value it does
// not have yet, and a later edit fills it in. A plain pointer collapses absent
// into null, and a plain value collapses both into the zero value; either one
// rewrites bytes that are already on a public pull request.
type Opt[T any] struct {
	present bool
	null    bool
	value   T
}

// Some records a present value.
func Some[T any](v T) Opt[T] { return Opt[T]{present: true, value: v} }

// Null records a present null, which is what a writer puts in a field it has
// not filled in yet.
func Null[T any]() Opt[T] { return Opt[T]{present: true, null: true} }

// Present reports whether the marker carried the key at all.
func (o Opt[T]) Present() bool { return o.present }

// IsNull reports the present-and-null state.
func (o Opt[T]) IsNull() bool { return o.present && o.null }

// Value is what the key held, or the zero value when it was absent or null.
func (o Opt[T]) Value() T { return o.value }

// IsZero reports the absent state, and is what the `omitzero` struct tag reads.
func (o Opt[T]) IsZero() bool { return !o.present }

// MarshalJSON writes null for a present null and the value otherwise. The
// absent state never reaches this method, because `omitzero` drops the field.
func (o Opt[T]) MarshalJSON() ([]byte, error) {
	if o.null {
		return []byte("null"), nil
	}
	return marshalNoEscape(o.value)
}

// UnmarshalJSON reads a null as present-and-null. A missing key never reaches
// this method and leaves the zero value, which is the absent state.
func (o *Opt[T]) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		*o = Opt[T]{present: true, null: true}
		return nil
	}
	var v T
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	*o = Opt[T]{present: true, value: v}
	return nil
}

// Marker is one pass marker, with its fields in the order the writers in
// lib/run.sh build them.
//
// The order is the point of the type. The three writers each build their object
// with one jq expression, `jq -c` keeps insertion order, and the result is
// embedded verbatim in a comment body. Go sorts a map's keys, so a marker built
// from a map would rewrite every comment on every edit.
//
// The union covers all three writers: the review claim at lib/run.sh:1095, the
// resolve claim at lib/run.sh:1957 and the declined marker at lib/run.sh:1050.
// A field one writer never sets is absent from its marker, not null.
//
// Payload fields stay as raw bytes. `findings` and `resolutions` carry the
// model's own key order through unchanged, and `tokens` and `usage` are
// accounted for elsewhere; remarshalling any of them here would reorder bytes
// this package has no reason to touch.
type Marker struct {
	Version        int             `json:"v,omitzero"`
	Leg            core.Leg        `json:"leg,omitzero"`
	Pass           int             `json:"pass,omitzero"`
	State          core.PassState  `json:"state,omitzero"`
	TS             int64           `json:"ts,omitzero"`
	DoneTS         Opt[int64]      `json:"done_ts,omitzero"`
	RunID          string          `json:"run_id,omitzero"`
	HeadSHA        string          `json:"head_sha,omitzero"`
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
	Reason         string          `json:"reason,omitzero"`
	CommitSHA      Opt[string]     `json:"commit_sha,omitzero"`
	CommitSubject  Opt[string]     `json:"commit_subject,omitzero"`
	Summary        Opt[string]     `json:"summary,omitzero"`
	Findings       json.RawMessage `json:"findings,omitzero"`
	Resolutions    json.RawMessage `json:"resolutions,omitzero"`
	EffortReported Opt[string]     `json:"effort_reported,omitzero"`
	Unanchored     Opt[int]        `json:"unanchored,omitzero"`
	Unthreaded     Opt[int]        `json:"unthreaded,omitzero"`
	CommentID      int64           `json:"comment_id,omitzero"`
}

// markerFields is Marker under a name with no methods, so MarshalJSON can hand
// the struct to the encoder without calling itself.
type markerFields Marker

// MarshalJSON writes the marker in the writers' field order, without Go's HTML
// escaping. A finding title carrying `<`, `>` or `&` reaches the comment body
// as the reviewer wrote it, which is what jq produces.
func (m Marker) MarshalJSON() ([]byte, error) {
	return marshalNoEscape(markerFields(m))
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

// ParseMarkers reads a JSON array of markers, which is the shape every reader
// below takes.
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

// Pass is the pass number the next review belongs to (lib/state.sh:272-277).
// No trusted marker means pass 1.
func Pass(markers []Marker) int { return CurrentReviewPass(markers) + 1 }

// MaxPass is the highest pass number any marker mentions, refused passes
// included (lib/state.sh:281-283). `status` renders every pass, and a refused
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
// pass, not the next one (lib/state.sh:286-288).
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
// (lib/state.sh:290-295).
func CurrentPassComplete(markers []Marker, pass int, leg core.Leg) bool {
	for _, m := range markers {
		if m.Pass == pass && m.Leg == leg && m.State == core.PassComplete {
			return true
		}
	}
	return false
}

// MarkerFor is the newest marker for a (pass, leg), whatever its state
// (lib/state.sh:297-302).
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
	return newest, found
}

// IsNewRevision compares the pull request head against the last non-declined
// review marker's head SHA (lib/state.sh:344-350).
//
// GitHub has no "new revision" event and `synchronize` fires per push, so the
// marker is the comparison rather than the event.
func IsNewRevision(markers []Marker, headSHA string) bool {
	last := ""
	for _, m := range ran(markers) {
		if m.Leg == core.LegReview {
			last = m.HeadSHA
		}
	}
	return last == "" || last != headSHA
}
