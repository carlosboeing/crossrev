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
