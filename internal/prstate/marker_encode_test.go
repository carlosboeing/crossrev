package prstate_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/carlosboeing/crossrev/internal/prstate"
)

// state_marker_encode is `jq -c .` (lib/state.sh:159), which parses and
// re-prints rather than only stripping whitespace. Every expectation below was
// measured by running `jq -c .` over the input on jq-1.8.1, and every rune is
// written as a Go escape so this file stays plain ASCII.
//
// Number literals are the registered divergence, pinned here only where jq and
// Go already agree. jq 1.7 and later print the literal a payload was written
// with; jq 1.6 printed the IEEE double it parsed to. Reproducing either
// exponent form in Go would pin one jq family into the contract, and no marker
// CrossRev writes carries an exponent.
func TestEncodeMarkerNormalisesTheWayJQDoes(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"escaped-solidus", "{\"path\":\"a\\/b.ts\"}", "{\"path\":\"a/b.ts\"}"},
		{"escape-for-a-printable-rune", "{\"s\":\"\\u0041\\u007e\"}", "{\"s\":\"A~\"}"},
		{"escape-for-a-control-rune", "{\"s\":\"\\u0007\"}", "{\"s\":\"\\u0007\"}"},
		{"escape-for-DEL", "{\"s\":\"\\u007f\"}", "{\"s\":\"\\u007f\"}"},
		{"raw-DEL-byte", "{\"s\":\"\u007f\"}", "{\"s\":\"\\u007f\"}"},
		{"escape-for-a-non-ASCII-rune", "{\"s\":\"\\u00e9\\u4e2d\"}", "{\"s\":\"\u00e9\u4e2d\"}"},
		{"surrogate-pair", "{\"s\":\"\\ud83d\\ude00\"}", "{\"s\":\"\U0001f600\"}"},
		{"escape-for-a-line-separator", "{\"s\":\"\\u2028\\u2029\"}", "{\"s\":\"\u2028\u2029\"}"},
		{"raw-line-separator", "{\"s\":\"\u2028\u2029\"}", "{\"s\":\"\u2028\u2029\"}"},
		{"the-five-two-character-escapes", "{\"s\":\"a\\tb\\nc\\rd\\be\\ff\"}", "{\"s\":\"a\\tb\\nc\\rd\\be\\ff\"}"},
		{"quote-and-backslash", "{\"s\":\"a\\\"b\\\\c\"}", "{\"s\":\"a\\\"b\\\\c\"}"},
		{"html-characters", "{\"s\":\"a < b && c > d\"}", "{\"s\":\"a < b && c > d\"}"},
		{"duplicate-key", "{\"a\":1,\"b\":0,\"a\":2}", "{\"a\":2,\"b\":0}"},
		{"insignificant-whitespace", "{ \"a\" : [1, 2], \"b\" : { \"c\" : 3 } }", "{\"a\":[1,2],\"b\":{\"c\":3}}"},
		{"number-literals-pass-through", "{\"n\":1.50,\"m\":-0.0,\"k\":12345678901234567890,\"j\":0.1,\"i\":-0}", "{\"n\":1.50,\"m\":-0.0,\"k\":12345678901234567890,\"j\":0.1,\"i\":-0}"},
		{"escapes-inside-a-payload", "{\"findings\":[{\"path\":\"a\\/b.ts\",\"title\":\"T\"}]}", "{\"findings\":[{\"path\":\"a/b.ts\",\"title\":\"T\"}]}"},
		{"an-escaped-key", "{\"\\u0061bc\":1}", "{\"abc\":1}"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := prstate.EncodeMarker(json.RawMessage(c.in))
			if err != nil {
				t.Fatalf("encoding: %v", err)
			}
			want := "\n\n" + prstate.MarkerPrefix + " " + c.want + " -->"
			if got != want {
				t.Errorf("encoded\n got % x\nwant % x", got, want)
			}
		})
	}
}

// X4. The shell does not refuse an unparseable payload: `jq -c .` fails, the
// command substitution comes back empty, and state_marker_encode prints the 21
// bytes "\n\n<!-- crossrev:  -->". Go refuses instead, which is the better
// answer: an empty marker on a public comment reads as a pass that settled
// nothing, and every later read of that pull request would agree with it. A
// deliberate divergence, not parity.
func TestEncodeMarkerRefusesWhereTheShellWritesAnEmptyMarker(t *testing.T) {
	if _, err := prstate.EncodeMarker(json.RawMessage("{oops")); err == nil {
		t.Error("invalid JSON encoded without an error")
	}
}

// A caller has to be able to change one field of a decoded marker and keep
// every key it does not know about, in the position that key held. The struct
// view drops what it does not declare, and lib/run.sh only ever edits in place.
func TestEditMarkerKeepsUnknownKeysAndTheirOrder(t *testing.T) {
	raw, ok := prstate.DecodeMarker(`<!-- crossrev: {"v":1,"leg":"resolve","pass":1,"wrap_up":"old","state":"started"} -->`)
	if !ok {
		t.Fatal("decoded nothing")
	}
	got, err := prstate.EditMarker(raw,
		prstate.MarkerEdit{Key: "state", Value: json.RawMessage(`"complete"`)},
		prstate.MarkerEdit{Key: "summary", Value: json.RawMessage(`"done"`)},
	)
	if err != nil {
		t.Fatalf("editing: %v", err)
	}
	want := `{"v":1,"leg":"resolve","pass":1,"wrap_up":"old","state":"complete","summary":"done"}`
	if string(got) != want {
		t.Errorf("edited\n got %s\nwant %s", got, want)
	}
}

// jq's `del(.k)` is the other half of what the writers do, at lib/run.sh:1995
// and :1999.
func TestEditMarkerDeletesAKey(t *testing.T) {
	got, err := prstate.EditMarker(json.RawMessage(`{"a":1,"wrap_up":"old","z":9}`),
		prstate.MarkerEdit{Key: "wrap_up", Delete: true})
	if err != nil {
		t.Fatalf("editing: %v", err)
	}
	if string(got) != `{"a":1,"z":9}` {
		t.Errorf("got %s", got)
	}
}

// M7. A payload that is not one JSON object is refused under a sentinel a
// caller can test for, the way every parser in internal/core does it.
func TestEditMarkerRefusesAPayloadThatIsNotOneObject(t *testing.T) {
	if _, err := prstate.EditMarker(json.RawMessage(`[1,2]`)); !errors.Is(err, prstate.ErrMarkerPayload) {
		t.Errorf("got %v, want ErrMarkerPayload", err)
	}
}

// An edit whose value is not JSON is refused rather than written into a public
// comment body.
func TestEditMarkerRefusesAValueThatIsNotJSON(t *testing.T) {
	if _, err := prstate.EditMarker(json.RawMessage(`{"a":1}`),
		prstate.MarkerEdit{Key: "a", Value: json.RawMessage(`oops`)}); err == nil {
		t.Error("an invalid value was written without an error")
	}
}

// X2. The struct view keeps only the keys it declares, so the bytes a marker
// was read with have to survive alongside it. `wrap_up` is live legacy state:
// lib/run.sh:1993-1999 still migrates it on resume.
func TestMarkerKeepsTheBytesItWasReadWith(t *testing.T) {
	stream := `{"id":4,"body":"<!-- crossrev: {\"v\":1,\"leg\":\"resolve\",\"pass\":2,\"state\":\"complete\",\"wrap_up\":\"old\"} -->"}`
	markers := prstate.Markers([]byte(stream))
	if len(markers) != 1 {
		t.Fatalf("got %d markers", len(markers))
	}
	want := `{"v":1,"leg":"resolve","pass":2,"state":"complete","wrap_up":"old"}`
	if string(markers[0].Raw()) != want {
		t.Errorf("raw\n got %s\nwant %s", markers[0].Raw(), want)
	}
	// M8. The resume migration belongs to the leg that performs it, and this is
	// the route it needs.
	migrated, err := prstate.EditMarker(markers[0].Raw(),
		prstate.MarkerEdit{Key: "summary", Value: json.RawMessage(`"old"`)},
		prstate.MarkerEdit{Key: "wrap_up", Delete: true})
	if err != nil {
		t.Fatalf("editing: %v", err)
	}
	if string(migrated) != `{"v":1,"leg":"resolve","pass":2,"state":"complete","summary":"old"}` {
		t.Errorf("got %s", migrated)
	}
}

// Raw is a copy, so a caller editing it cannot reach the marker it came from.
func TestMarkerRawIsNotShared(t *testing.T) {
	m, err := prstate.ParseMarker(json.RawMessage(`{"v":1,"leg":"review","pass":1}`))
	if err != nil {
		t.Fatalf("parsing: %v", err)
	}
	first := m.Raw()
	first[1] = 'X'
	if string(m.Raw()) != `{"v":1,"leg":"review","pass":1}` {
		t.Errorf("editing the returned bytes reached the marker: %s", m.Raw())
	}
}

// A raw C0 control byte inside a string is not JSON, and jq refuses it with
// "control characters from U+0000 through U+001F must be escaped". Go refuses
// it too, so the two agree on the refusal rather than on a rendering.
func TestEncodeMarkerRefusesARawControlByte(t *testing.T) {
	if _, err := prstate.EncodeMarker(json.RawMessage("{\"s\":\"a\u0001b\"}")); err == nil {
		t.Error("a raw control byte encoded without an error")
	}
}
