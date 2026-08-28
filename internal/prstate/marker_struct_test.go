package prstate_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/prstate"
)

// The three marker writers in lib/run.sh build their object with one jq
// expression each, and `jq -c` keeps insertion order. The byte strings below
// were produced by sourcing lib/state.sh and running those exact expressions
// through state_marker_encode, so this test fails on any field the Go struct
// declares out of order.
func TestMarkerEncodesInTheWritersOrder(t *testing.T) {
	cases := []struct {
		name string
		in   prstate.Marker
		want string
	}{
		{
			// lib/run.sh:1095-1103, the fresh review claim.
			name: "review-started",
			in: prstate.Marker{
				Version:       core.MarkerVersion,
				Leg:           core.LegReview,
				Pass:          2,
				State:         core.PassStarted,
				TS:            1700000000,
				DoneTS:        prstate.Null[int64](),
				RunID:         prstate.Some("run-77"),
				HeadSHA:       prstate.Some("9f3c1abdeadbeef"),
				Harness:       prstate.Some("codex"),
				Model:         prstate.Some("gpt-5"),
				Effort:        prstate.Some("high"),
				Endpoint:      prstate.Null[string](),
				ModelReported: prstate.Null[string](),
				Tokens:        json.RawMessage("null"),
				Usage:         json.RawMessage("null"),
				Billing:       prstate.Null[string](),
				Verdict:       prstate.Null[string](),
				BlockedReason: prstate.Null[string](),
				Findings:      json.RawMessage("[]"),
			},
			want: `{"v":1,"leg":"review","pass":2,"state":"started","ts":1700000000,"done_ts":null,"run_id":"run-77","head_sha":"9f3c1abdeadbeef","harness":"codex","model":"gpt-5","effort":"high","endpoint":null,"model_reported":null,"tokens":null,"usage":null,"billing":null,"verdict":null,"blocked_reason":null,"findings":[]}`,
		},
		{
			// lib/run.sh:1957-1965, the fresh resolve claim.
			name: "resolve-started",
			in: prstate.Marker{
				Version:       core.MarkerVersion,
				Leg:           core.LegResolve,
				Pass:          2,
				State:         core.PassStarted,
				TS:            1700000000,
				DoneTS:        prstate.Null[int64](),
				RunID:         prstate.Some("run-77"),
				HeadSHA:       prstate.Some("9f3c1abdeadbeef"),
				Harness:       prstate.Some("codex"),
				Model:         prstate.Some("gpt-5"),
				Effort:        prstate.Some("high"),
				Endpoint:      prstate.Null[string](),
				ModelReported: prstate.Null[string](),
				Tokens:        json.RawMessage("null"),
				Usage:         json.RawMessage("null"),
				Billing:       prstate.Null[string](),
				Blocked:       prstate.Some(false),
				BlockedReason: prstate.Null[string](),
				CommitSHA:     prstate.Null[string](),
				CommitSubject: prstate.Null[string](),
				Summary:       prstate.Some(""),
				Resolutions:   json.RawMessage("[]"),
			},
			want: `{"v":1,"leg":"resolve","pass":2,"state":"started","ts":1700000000,"done_ts":null,"run_id":"run-77","head_sha":"9f3c1abdeadbeef","harness":"codex","model":"gpt-5","effort":"high","endpoint":null,"model_reported":null,"tokens":null,"usage":null,"billing":null,"blocked":false,"blocked_reason":null,"commit_sha":null,"commit_subject":null,"summary":"","resolutions":[]}`,
		},
		{
			// lib/run.sh:1050-1056, the pass a cap refused to start.
			name: "declined",
			in: prstate.Marker{
				Version:       core.MarkerVersion,
				Leg:           core.LegReview,
				Pass:          3,
				State:         core.PassDeclined,
				TS:            1700000000,
				DoneTS:        prstate.Some[int64](1700000000),
				RunID:         prstate.Some("run-77"),
				HeadSHA:       prstate.Some("9f3c1abdeadbeef"),
				Harness:       prstate.Null[string](),
				Model:         prstate.Null[string](),
				Effort:        prstate.Null[string](),
				Endpoint:      prstate.Null[string](),
				ModelReported: prstate.Null[string](),
				Tokens:        json.RawMessage("null"),
				Usage:         json.RawMessage("null"),
				Billing:       prstate.Null[string](),
				Verdict:       prstate.Some(string(core.PassDeclined)),
				Reason:        prstate.Some("reached max_passes_per_cycle (3)"),
				Findings:      json.RawMessage("[]"),
			},
			want: `{"v":1,"leg":"review","pass":3,"state":"declined","ts":1700000000,"done_ts":1700000000,"run_id":"run-77","head_sha":"9f3c1abdeadbeef","harness":null,"model":null,"effort":null,"endpoint":null,"model_reported":null,"tokens":null,"usage":null,"billing":null,"verdict":"declined","reason":"reached max_passes_per_cycle (3)","findings":[]}`,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			raw, err := c.in.MarshalJSON()
			if err != nil {
				t.Fatalf("marshalling: %v", err)
			}
			if string(raw) != c.want {
				t.Errorf("marshalled\n got %s\nwant %s", raw, c.want)
			}
			encoded, err := c.in.Encode()
			if err != nil {
				t.Fatalf("encoding: %v", err)
			}
			if encoded != "\n\n<!-- crossrev: "+c.want+" -->" {
				t.Errorf("encoded %q", encoded)
			}
		})
	}
}

// Absent, present-and-null and present-and-set are three states, and a marker
// round trip must keep them apart.
func TestMarkerKeepsAbsentApartFromNull(t *testing.T) {
	var m prstate.Marker
	if err := json.Unmarshal([]byte(`{"v":1,"leg":"review","pass":1,"harness":null}`), &m); err != nil {
		t.Fatalf("unmarshalling: %v", err)
	}
	if !m.Harness.Present() || !m.Harness.IsNull() {
		t.Errorf("harness is %+v, want present and null", m.Harness)
	}
	if m.Model.Present() {
		t.Error("an absent model read as present")
	}
	raw, err := m.MarshalJSON()
	if err != nil {
		t.Fatalf("marshalling: %v", err)
	}
	if string(raw) != `{"v":1,"leg":"review","pass":1,"harness":null}` {
		t.Errorf("got %s", raw)
	}
}

// A title carrying `<`, `>` or `&` goes into a public comment verbatim.
func TestMarkerDoesNotEscapeHTML(t *testing.T) {
	m := prstate.Marker{Version: 1, Leg: core.LegResolve, Pass: 1, Summary: prstate.Some("a < b && c > d")}
	raw, err := m.MarshalJSON()
	if err != nil {
		t.Fatalf("marshalling: %v", err)
	}
	if string(raw) != `{"v":1,"leg":"resolve","pass":1,"summary":"a < b && c > d"}` {
		t.Errorf("got %s", raw)
	}
}

// F1. A marker read off a comment carries the id of the comment it came from,
// and every writer in lib/run.sh strips that key before encoding: twelve call
// sites wrap the marker in `jq -c 'del(.comment_id)'`. The id must not be able
// to reach a public comment body.
func TestEncodeNeverWritesTheCommentID(t *testing.T) {
	stream := `{"id":4242,"body":"Pass.\n\n<!-- crossrev: {\"v\":1,\"leg\":\"review\",\"pass\":1,\"state\":\"complete\",\"unanchored\":0} -->"}`
	markers := prstate.Markers([]byte(stream))
	if len(markers) != 1 {
		t.Fatalf("got %d markers", len(markers))
	}
	encoded, err := markers[0].Encode()
	if err != nil {
		t.Fatalf("encoding: %v", err)
	}
	want := "\n\n<!-- crossrev: {\"v\":1,\"leg\":\"review\",\"pass\":1,\"state\":\"complete\",\"unanchored\":0} -->"
	if encoded != want {
		t.Errorf("encoded\n got %q\nwant %q", encoded, want)
	}
}

// state_markers adds `comment_id` INTO every marker object in the array it
// prints (lib/state.sh:151), which is the shape ParseMarkers reads. Left in the
// bytes it hands back, the key rides the documented edit route — Raw,
// EditMarker, EncodeMarker — straight onto a public comment, which is the one
// thing every marker write in lib/run.sh deletes first; ignored without being
// read, the recovery id is lost and the leg posts a second claim.
func TestParseMarkersAdoptsTheCommentID(t *testing.T) {
	const in = `[{"v":1,"leg":"review","pass":1,"state":"complete","wrap_up":"old summary","comment_id":12345}]`
	markers, err := prstate.ParseMarkers([]byte(in))
	if err != nil {
		t.Fatalf("parsing: %v", err)
	}
	if len(markers) != 1 {
		t.Fatalf("read %d markers, want 1", len(markers))
	}
	if got := markers[0].CommentID(); got != 12345 {
		t.Errorf("comment id %d, want 12345", got)
	}
	const wantRaw = `{"v":1,"leg":"review","pass":1,"state":"complete","wrap_up":"old summary"}`
	if got := string(markers[0].Raw()); got != wantRaw {
		t.Errorf("raw\n got %s\nwant %s", got, wantRaw)
	}
	edited, err := prstate.EditMarker(markers[0].Raw(),
		prstate.MarkerEdit{Key: "state", Value: json.RawMessage(`"started"`)})
	if err != nil {
		t.Fatalf("editing: %v", err)
	}
	body, err := prstate.EncodeMarker(edited)
	if err != nil {
		t.Fatalf("encoding: %v", err)
	}
	if strings.Contains(body, "comment_id") {
		t.Errorf("the edit route wrote comment_id onto a comment: %s", body)
	}
}

// A marker with no `comment_id` keeps the bytes it was read with, so Raw stays
// what DecodeMarker would have produced either way.
func TestParseMarkersWithoutACommentIDKeepsItsBytes(t *testing.T) {
	const in = `[{"v":1,"leg":"review","pass":1,"state":"complete"}]`
	markers, err := prstate.ParseMarkers([]byte(in))
	if err != nil {
		t.Fatalf("parsing: %v", err)
	}
	if got := markers[0].CommentID(); got != 0 {
		t.Errorf("comment id %d, want 0", got)
	}
	const wantRaw = `{"v":1,"leg":"review","pass":1,"state":"complete"}`
	if got := string(markers[0].Raw()); got != wantRaw {
		t.Errorf("raw\n got %s\nwant %s", got, wantRaw)
	}
}

// F2. `jq -c` prints U+2028 and U+2029 as the raw three bytes; Go's
// encoding/json escapes both unconditionally, escape-HTML off or not. All three
// of summary, reason and blocked_reason carry model and harness text.
func TestMarkerDoesNotEscapeTheLineSeparators(t *testing.T) {
	// The two separators are written as Go escapes so the source stays plain
	// ASCII; the value they build is the raw three bytes each, which is what
	// jq prints.
	m := prstate.Marker{Version: 1, Leg: core.LegResolve, Pass: 1, Summary: prstate.Some("a\u2028b\u2029c")}
	raw, err := m.MarshalJSON()
	if err != nil {
		t.Fatalf("marshalling: %v", err)
	}
	want := "{\"v\":1,\"leg\":\"resolve\",\"pass\":1,\"summary\":\"a\u2028b\u2029c\"}"
	if string(raw) != want {
		t.Errorf("marshalled\n got % x\nwant % x", raw, want)
	}
}

// F4. marker_test.go holds the three finished markers as byte strings but only
// runs them through the byte path, so the struct's field order is pinned for
// the three fresh claims and unpinned for everything a completed marker adds.
func TestCompletedMarkersRoundTripThroughTheStruct(t *testing.T) {
	for _, payload := range writtenMarkers {
		m, err := prstate.ParseMarker([]byte(payload))
		if err != nil {
			t.Fatalf("parsing %s: %v", payload, err)
		}
		raw, err := m.MarshalJSON()
		if err != nil {
			t.Fatalf("marshalling: %v", err)
		}
		if string(raw) != payload {
			t.Errorf("round trip changed the bytes\n got %s\nwant %s", raw, payload)
		}
	}
}

// json.RawMessage marshals its bytes verbatim, so a payload field that is empty
// but not nil is not a JSON value at all and fails the whole encode with
// "unexpected end of JSON input". A nil one is absent, and so is this.
func TestMarkerEncodesAnEmptyPayloadAsAbsent(t *testing.T) {
	for _, c := range []struct {
		name string
		in   prstate.Marker
	}{
		{"findings", prstate.Marker{Version: 1, Findings: json.RawMessage{}}},
		{"resolutions", prstate.Marker{Version: 1, Resolutions: json.RawMessage{}}},
		{"tokens", prstate.Marker{Version: 1, Tokens: json.RawMessage{}}},
		{"usage", prstate.Marker{Version: 1, Usage: json.RawMessage{}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := c.in.Encode()
			if err != nil {
				t.Fatalf("encoding: %v", err)
			}
			want := "\n\n" + prstate.MarkerPrefix + ` {"v":1} -->`
			if got != want {
				t.Errorf("encoded\n got %q\nwant %q", got, want)
			}
		})
	}
}

// X3. `omitzero` on a plain string collapses present-and-empty into absent, so
// a marker carrying an empty head_sha, run_id or reason re-encodes without the
// key. lib/run.sh:1050 and :1095 write all three unconditionally.
func TestMarkerKeepsAnEmptyStringField(t *testing.T) {
	for _, payload := range []string{
		`{"v":1,"leg":"review","pass":1,"run_id":"","head_sha":""}`,
		`{"v":1,"leg":"review","pass":3,"state":"declined","reason":""}`,
	} {
		m, err := prstate.ParseMarker([]byte(payload))
		if err != nil {
			t.Fatalf("parsing %s: %v", payload, err)
		}
		raw, err := m.MarshalJSON()
		if err != nil {
			t.Fatalf("marshalling: %v", err)
		}
		if string(raw) != payload {
			t.Errorf("an empty value was dropped\n got %s\nwant %s", raw, payload)
		}
	}
}
