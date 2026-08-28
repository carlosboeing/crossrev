package prstate

import "encoding/json"

// Opt is a marker field in all three of the states a marker distinguishes:
// absent, present and null, present and set.
//
// Every marker writer in lib/run.sh writes an explicit null for a value it does
// not have yet, and a later edit fills it in. A plain pointer collapses absent
// into null, and a plain value collapses both into the zero value; either one
// rewrites bytes that are already on a public pull request.
//
// It lives in its own file because it is a general JSON helper rather than
// anything to do with pass numbering, and both the marker and anything a later
// leg declares will want it.
//
// One thing it does not defend on its own: outside an `omitzero` field, an
// absent Opt marshals as its value rather than as null, because MarshalJSON is
// reached only once the encoder has decided to write the field at all. Every
// field on Marker is `omitzero`, so the absent state is dropped before it gets
// there. A future field that is not `omitzero` would need a pointer or its own
// tag rather than this type.
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
//
// It cannot tell a present empty string from a null one. Get can, and a reader
// that renders the field or decides on it should use Get instead: a null
// harness means the writer had not resolved one yet, where an empty string
// means it resolved to nothing.
func (o Opt[T]) Value() T { return o.value }

// Get is the value and whether the marker actually carried one. It answers
// false for both absent and present-and-null.
func (o Opt[T]) Get() (T, bool) { return o.value, o.present && !o.null }

// IsZero reports the absent state, and is what the `omitzero` struct tag reads.
func (o Opt[T]) IsZero() bool { return !o.present }

// MarshalJSON writes null for a present null and the value otherwise. The
// absent state never reaches this method, because `omitzero` drops the field.
func (o Opt[T]) MarshalJSON() ([]byte, error) {
	if o.null {
		return []byte("null"), nil
	}
	return jqMarshal(o.value)
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
