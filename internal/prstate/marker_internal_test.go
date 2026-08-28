package prstate

import (
	"encoding/json"
	"testing"
)

// M2. `append(o[:i], o[i+1:]...)` shifts the caller's backing array left, so a
// receiver a caller still holds is silently rewritten. `rename` is the only
// caller today and always reassigns, which makes the bug latent rather than
// absent.
func TestDelDoesNotMutateTheReceiver(t *testing.T) {
	obj := object{
		{key: "a", value: json.RawMessage("1")},
		{key: "b", value: json.RawMessage("2")},
		{key: "c", value: json.RawMessage("3")},
	}
	got := obj.del("a")

	if string(obj.marshal()) != `{"a":1,"b":2,"c":3}` {
		t.Errorf("del rewrote the receiver: %s", obj.marshal())
	}
	if string(got.marshal()) != `{"b":2,"c":3}` {
		t.Errorf("del returned %s", got.marshal())
	}
}
