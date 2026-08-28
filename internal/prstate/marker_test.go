package prstate_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/prstate"
)

// TestParityDecodeMarker drives lib/state.sh's state_marker_of from the frozen
// oracle. The `decoded` field is the exact `jq -c` rendering of the migrated
// marker, so this compares bytes rather than a decoded shape.
func TestParityDecodeMarker(t *testing.T) {
	var f struct {
		Cases []struct {
			Name    string `json:"name"`
			Body    string `json:"body"`
			Decoded string `json:"decoded"`
		} `json:"cases"`
	}
	loadFixture(t, "marker_codec.json", &f)

	for _, c := range f.Cases {
		t.Run(c.Name, func(t *testing.T) {
			recordCase("marker_codec.json", c.Name)
			got, ok := prstate.DecodeMarker(c.Body)
			if c.Decoded == "" {
				if ok {
					t.Fatalf("decoded %q, want nothing", string(got))
				}
				return
			}
			if !ok {
				t.Fatalf("decoded nothing, want %q", c.Decoded)
			}
			if string(got) != c.Decoded {
				t.Errorf("decoded\n got %q\nwant %q", string(got), c.Decoded)
			}
		})
	}
}

// TestParityEncodeMarker drives lib/state.sh:159's state_marker_encode. The
// oracle's `input` is read as raw bytes rather than through a map, because the
// bytes go verbatim into a public pull-request comment and Go sorts a map's
// keys where `jq -c` keeps insertion order.
func TestParityEncodeMarker(t *testing.T) {
	var f struct {
		Cases []struct {
			Name string `json:"name"`
			// A case records its input one of two ways. Input is a JSON value,
			// which jq normalised when the vector was captured. InputRaw is the
			// text verbatim, which is what the cases pinning the normalisation
			// itself need: feeding a normalised input back would prove nothing
			// about the rewriting.
			Input    json.RawMessage `json:"input"`
			InputRaw string          `json:"input_raw"`
			Encoded  string          `json:"encoded"`
		} `json:"cases"`
	}
	loadFixture(t, "marker_encode.json", &f)

	// jq rewrites an exponent into its own canonical form. Which form that is
	// changed between jq 1.6 and 1.7, so reproducing it would put one jq family
	// into the frozen contract, and no marker CrossRev writes carries an
	// exponent. These two cases assert the divergence rather than a match, so
	// that closing it later fails here and is noticed.
	divergent := map[string]string{
		"raw-number-exponent-divergent":          `{"v":1,"leg":"review","pass":1,"n":1e2}`,
		"raw-number-exponent-negative-divergent": `{"v":1,"leg":"review","pass":1,"n":1e-2}`,
	}

	for _, c := range f.Cases {
		t.Run(c.Name, func(t *testing.T) {
			recordCase("marker_encode.json", c.Name)
			payload := c.Input
			if c.InputRaw != "" {
				payload = json.RawMessage(c.InputRaw)
			}
			got, err := prstate.EncodeMarker(payload)
			if err != nil {
				t.Fatalf("encoding: %v", err)
			}
			if want, ok := divergent[c.Name]; ok {
				if got == c.Encoded {
					t.Fatalf("this case records a divergence, and the port now matches the shell.\n"+
						"Either the port started reproducing jq's exponent form, or the vector moved.\n"+
						"Settle which, then move %s out of the divergent set.", c.Name)
				}
				if !strings.Contains(got, want) {
					t.Errorf("the literal is meant to pass through verbatim\n got %q\nwant it to contain %q", got, want)
				}
				return
			}
			if got != c.Encoded {
				t.Errorf("encoded\n got %q\nwant %q", got, c.Encoded)
			}
		})
	}
}

// A marker body is matched by a literal lowercase prefix. A case change breaks
// every pull request that already carries one, and it breaks it silently:
// nothing errors, the marker simply stops being found.
func TestMarkerPrefixesAreLiteralAndLowercase(t *testing.T) {
	if prstate.MarkerPrefix != "<!-- crossrev:" {
		t.Errorf("marker prefix is %q", prstate.MarkerPrefix)
	}
	if prstate.FindingMarkerPrefix != "<!-- crossrev:f" {
		t.Errorf("finding marker prefix is %q", prstate.FindingMarkerPrefix)
	}
}

// The extraction rule is per line: the last opening delimiter, then the last
// closing one after it (lib/state.sh:113-117).
func TestDecodeMarkerTakesTheLastDelimitersOnALine(t *testing.T) {
	body := `noise <!-- crossrev: {"leg":"first"} --> and <!-- crossrev: {"leg":"second"} --> tail`
	got, ok := prstate.DecodeMarker(body)
	if !ok {
		t.Fatal("decoded nothing")
	}
	if string(got) != `{"leg":"second"}` {
		t.Errorf("got %q", string(got))
	}
}

func TestDecodeMarkerKeepsEverythingBeforeTheLastClose(t *testing.T) {
	body := `<!-- crossrev: {"title":"a --> b"} -->`
	got, ok := prstate.DecodeMarker(body)
	if !ok {
		t.Fatal("decoded nothing")
	}
	if string(got) != `{"title":"a --> b"}` {
		t.Errorf("got %q", string(got))
	}
}

// A finding marker's prefix ends where the state marker's has a space, so the
// state decoder must not see one (lib/state.sh:74-75).
func TestDecodeMarkerIgnoresAFindingMarker(t *testing.T) {
	id := prstate.NewFindingID("app.ts", "a title", prstate.AnchorAt([]byte("x\n"), 1))
	body := "text" + prstate.EncodeFindingMarker(id, 2, "review")
	if _, ok := prstate.DecodeMarker(body); ok {
		t.Error("a finding marker decoded as a state marker")
	}
}

// Every payload that is not one JSON object decodes to nothing, whichever kind
// it is (lib/state.sh:118-119, and the fixture's two-markers case).
func TestDecodeMarkerRefusesAPayloadThatIsNotOneObject(t *testing.T) {
	for _, payload := range []string{"null", "[1,2]", `"text"`, "7", "true", "", "{"} {
		body := `<!-- crossrev: ` + payload + ` -->`
		if got, ok := prstate.DecodeMarker(body); ok {
			t.Errorf("payload %q decoded as %q", payload, string(got))
		}
	}
}

// A resolutions or findings entry that is not an object makes jq's migration
// throw, and `?` swallows it, so the whole marker decodes to nothing.
func TestDecodeMarkerRefusesANonObjectInsideAMigratedArray(t *testing.T) {
	for _, body := range []string{
		`<!-- crossrev: {"resolutions":["x"]} -->`,
		`<!-- crossrev: {"findings":[3]} -->`,
	} {
		if got, ok := prstate.DecodeMarker(body); ok {
			t.Errorf("%s decoded as %q", body, string(got))
		}
	}
}

// A non-array under either key is left alone, because the migration guards on
// `type == "array"` (lib/state.sh:103 and :108).
func TestDecodeMarkerLeavesANonArrayUnderAMigratedKey(t *testing.T) {
	body := `<!-- crossrev: {"resolutions":"none","findings":4} -->`
	got, ok := prstate.DecodeMarker(body)
	if !ok {
		t.Fatal("decoded nothing")
	}
	if string(got) != `{"resolutions":"none","findings":4}` {
		t.Errorf("got %q", string(got))
	}
}

// jq's `.[$to] = .[$from]` puts a new key at the end of the object, and the
// marker goes into a comment body verbatim, so the position is observable.
func TestDecodeMarkerRenamesToTheEndOfTheObject(t *testing.T) {
	body := `<!-- crossrev: {"a":1,"dispositions":[],"z":9} -->`
	got, ok := prstate.DecodeMarker(body)
	if !ok {
		t.Fatal("decoded nothing")
	}
	if string(got) != `{"a":1,"z":9,"resolutions":[]}` {
		t.Errorf("got %q", string(got))
	}
}

// A marker already carrying the new key keeps it, and the old one is left
// where it is: jq renames only when the new key is absent (lib/state.sh:99).
func TestDecodeMarkerDoesNotRenameOverAnExistingKey(t *testing.T) {
	body := `<!-- crossrev: {"dispositions":[],"resolutions":[]} -->`
	got, ok := prstate.DecodeMarker(body)
	if !ok {
		t.Fatal("decoded nothing")
	}
	if string(got) != `{"dispositions":[],"resolutions":[]}` {
		t.Errorf("got %q", string(got))
	}
}

// The marker is embedded verbatim in a comment body, so `<`, `>` and `&` in a
// finding title must survive unescaped. Go's encoding/json escapes all three by
// default and jq escapes none of them.
func TestEncodeMarkerDoesNotEscapeHTML(t *testing.T) {
	got, err := prstate.EncodeMarker(json.RawMessage(`{"title":"a < b && c > d"}`))
	if err != nil {
		t.Fatalf("encoding: %v", err)
	}
	want := "\n\n<!-- crossrev: {\"title\":\"a < b && c > d\"} -->"
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

// state_marker_encode pipes its argument through `jq -c .`, which drops
// insignificant whitespace and keeps key order.
func TestEncodeMarkerCompactsWithoutReordering(t *testing.T) {
	got, err := prstate.EncodeMarker(json.RawMessage("{ \"z\" : 1,\n  \"a\" : 2 }"))
	if err != nil {
		t.Fatalf("encoding: %v", err)
	}
	want := "\n\n<!-- crossrev: {\"z\":1,\"a\":2} -->"
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

// writtenMarkers are the finished markers of the three writers in lib/run.sh,
// produced by sourcing lib/state.sh and running state_marker_encode. Every
// marker CrossRev writes goes out through `jq -c`, so a marker read back off a
// pull request is already in this form.
var writtenMarkers = []string{
	`{"v":1,"leg":"review","pass":2,"state":"complete","ts":1700000000,"done_ts":1700000900,"run_id":"run-77","head_sha":"9f3c1abdeadbeef","harness":"codex","model":"gpt-5","effort":"high","endpoint":null,"model_reported":"gpt-5","tokens":1234,"usage":null,"billing":"subscription","verdict":"issues","blocked_reason":null,"findings":[{"id":"aaaa000000000001","title":"a < b && c > d","path":"lib/auth.ts"}],"effort_reported":null,"unanchored":0}`,
	`{"v":1,"leg":"resolve","pass":2,"state":"complete","ts":1700000000,"done_ts":1700000900,"run_id":"run-77","head_sha":"9f3c1abdeadbeef","harness":"claude","model":null,"effort":null,"endpoint":null,"model_reported":null,"tokens":null,"usage":null,"billing":null,"blocked":false,"blocked_reason":null,"commit_sha":"abc1234","commit_subject":"fix: a thing","summary":"one fix","resolutions":[{"finding_id":"aaaa000000000001","resolution":"fixed","crossrev_tracked":""}],"effort_reported":null,"unthreaded":0}`,
	`{"v":1,"leg":"review","pass":3,"state":"declined","ts":1700000000,"done_ts":1700000000,"run_id":"run-77","head_sha":"9f3c1abdeadbeef","harness":null,"model":null,"effort":null,"endpoint":null,"model_reported":null,"tokens":null,"usage":null,"billing":null,"verdict":"declined","reason":"reached max_passes_per_cycle (3)","findings":[]}`,
}

// A decode followed by an encode must reproduce a written marker byte for byte.
func TestDecodeThenEncodeReproducesAWrittenMarker(t *testing.T) {
	for _, payload := range writtenMarkers {
		body := "Summary.\n\n" + prstate.MarkerPrefix + " " + payload + " -->"
		raw, ok := prstate.DecodeMarker(body)
		if !ok {
			t.Fatalf("decoded nothing from %q", payload)
		}
		got, err := prstate.EncodeMarker(raw)
		if err != nil {
			t.Fatalf("encoding: %v", err)
		}
		if got != "\n\n"+prstate.MarkerPrefix+" "+payload+" -->" {
			t.Errorf("round trip changed the bytes\n got %q\nwant %q", got, payload)
		}
	}
}

// A duplicate key keeps the first key's position and the last value, which is
// what jq's parser does. The position is observable because the marker is
// embedded verbatim.
func TestDecodeMarkerKeepsTheFirstPositionOfADuplicateKey(t *testing.T) {
	got, ok := prstate.DecodeMarker(`<!-- crossrev: {"a":1,"b":0,"a":2} -->`)
	if !ok {
		t.Fatal("decoded nothing")
	}
	if string(got) != `{"a":2,"b":0}` {
		t.Errorf("got %q", string(got))
	}
}

// M3. A marker written by somebody else can carry `<`, `>` or `&` in a key, and
// the decoder re-emits the key. jq escapes none of the three.
func TestDecodeMarkerDoesNotEscapeHTMLInAKey(t *testing.T) {
	got, ok := prstate.DecodeMarker(`<!-- crossrev: {"a<b&c>d":1} -->`)
	if !ok {
		t.Fatal("decoded nothing")
	}
	if string(got) != `{"a<b&c>d":1}` {
		t.Errorf("got %q", string(got))
	}
}

// M4. `jq -c` drops insignificant whitespace inside a payload, nested included.
// Measured: state_marker_of on this body prints the compacted form below.
func TestDecodeMarkerCompactsInsideAPayload(t *testing.T) {
	got, ok := prstate.DecodeMarker(`<!-- crossrev: {"a": [1, 2], "b": {"c": 3}} -->`)
	if !ok {
		t.Fatal("decoded nothing")
	}
	if string(got) != `{"a":[1,2],"b":{"c":3}}` {
		t.Errorf("got %q", string(got))
	}
}
