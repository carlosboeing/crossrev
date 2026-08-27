package config_test

import (
	"testing"

	"github.com/carlosboeing/crossrev/internal/config"
)

// The object keeps the order its keys arrived in, because jq's object
// multiplication does and `crossrev config show` prints the merge.
func TestObjectKeepsKeyOrder(t *testing.T) {
	object := config.NewObject()
	object.Set("zebra", "1")
	object.Set("apple", "2")
	object.Set("zebra", "3")

	keys := object.Keys()
	if len(keys) != 2 || keys[0] != "zebra" || keys[1] != "apple" {
		t.Errorf("Keys() = %v, want [zebra apple]", keys)
	}
	encoded, err := object.MarshalJSON()
	if err != nil {
		t.Fatalf("MarshalJSON: %v", err)
	}
	if got, want := string(encoded), `{"zebra":"3","apple":"2"}`; got != want {
		t.Errorf("MarshalJSON = %s, want %s", got, want)
	}
}

// Present-and-null is not the same question as absent, which is what
// cfg_resolve_backlog asks of the repository layer at lib/config.sh:396.
func TestHasSeparatesPresentFromNull(t *testing.T) {
	object := config.NewObject()
	object.Set("stated", nil)
	if !object.Has("stated") {
		t.Error("a key set to null reads as absent")
	}
	if object.Has("never_set") {
		t.Error("a key never set reads as present")
	}
	if (*config.Object)(nil).Has("anything") {
		t.Error("the nil object reports a key")
	}
}

// A refusal renders as the two lines ui_die writes, which is the form the
// parity vectors record.
func TestRefusalRendersAsTwoLines(t *testing.T) {
	refusal := &config.Refusal{Message: "something is wrong", Hint: "do this instead."}
	if got, want := refusal.Rendered(), "\nerror  something is wrong\n       do this instead."; got != want {
		t.Errorf("Rendered() = %q, want %q", got, want)
	}
	if got := refusal.Error(); got != "something is wrong" {
		t.Errorf("Error() = %q", got)
	}
}

// jq writes `<`, `>` and `&` as themselves. Go's encoding/json escapes them by
// default, which would change every URL carrying a query string.
func TestStringsAreNotHTMLEscaped(t *testing.T) {
	object := config.NewObject()
	object.Set("base_url", "http://x/?a=1&b=2")
	encoded, err := object.MarshalJSON()
	if err != nil {
		t.Fatalf("MarshalJSON: %v", err)
	}
	if got, want := string(encoded), `{"base_url":"http://x/?a=1&b=2"}`; got != want {
		t.Errorf("MarshalJSON = %s, want %s", got, want)
	}
}

// Clone is deep, so a merge never writes back into the defaults it started from.
func TestCloneIsDeep(t *testing.T) {
	nested := config.NewObject()
	nested.Set("value", "original")
	original := config.NewObject()
	original.Set("nested", nested)
	original.Set("list", []any{"a"})

	clone := original.Clone()
	clone.Object("nested").Set("value", "changed")
	clone.Value("list").([]any)[0] = "b"

	if got := nested.Value("value"); got != "original" {
		t.Errorf("the clone wrote through to the nested object: %v", got)
	}
	if got := original.Value("list").([]any)[0]; got != "a" {
		t.Errorf("the clone wrote through to the list: %v", got)
	}
}
