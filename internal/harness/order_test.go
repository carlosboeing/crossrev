package harness_test

import (
	"encoding/json"
	"testing"

	"github.com/carlosboeing/crossrev/internal/harness"
)

func TestOrderProbe(t *testing.T) {
	const in = `{"id":"z","file":"a.go","line":3,"severity":"blocking"}`
	var n harness.Node
	if err := json.Unmarshal([]byte(in), &n); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	out, err := json.Marshal(n)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(out) != in {
		t.Fatalf("key order lost\n got %s\nwant %s", out, in)
	}
	const dup = `{"id":"first","file":"a.go","id":"last"}`
	var d harness.Node
	if err := json.Unmarshal([]byte(dup), &d); err != nil {
		t.Fatalf("unmarshal dup: %v", err)
	}
	got, _ := json.Marshal(d)
	if string(got) != `{"id":"last","file":"a.go"}` {
		t.Fatalf("jq duplicate rule broken: %s", got)
	}
}

// TestSetReplacesInPlace pins the property attachThreads depends on. The review
// leg writes thread_id:null during enrichment and Sets it again once the inline
// comment lands. If Set appended instead of replacing, the marker would hold
// thread_id twice; Member answers the first, which is null, and the resolve leg
// would have no GraphQL id to reply into. jq's = replaces in place, so this is
// parity as well as sanity.
func TestSetReplacesInPlace(t *testing.T) {
	var n harness.Node
	if err := json.Unmarshal([]byte(`{"a":1,"thread_id":null,"z":2}`), &n); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	n.Set("thread_id", harness.FromString("PRRT_9002"))
	out, err := json.Marshal(n)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	const want = `{"a":1,"thread_id":"PRRT_9002","z":2}`
	if string(out) != want {
		t.Fatalf("Set moved or duplicated the key\n got %s\nwant %s", out, want)
	}
}
