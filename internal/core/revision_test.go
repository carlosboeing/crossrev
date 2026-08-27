package core

import (
	"encoding/json"
	"errors"
	"testing"
)

const (
	shaA = "0123456789abcdef0123456789abcdef01234567"
	shaB = "fedcba9876543210fedcba9876543210fedcba98"
)

func TestNewRevisionRefusesAnythingButFortyLowercaseHex(t *testing.T) {
	tests := []struct {
		name string
		sha  string
	}{
		{name: "empty", sha: ""},
		{name: "too short", sha: "0123456789abcdef0123456789abcdef0123456"},
		{name: "too long", sha: "0123456789abcdef0123456789abcdef012345678"},
		{name: "uppercase hex", sha: "0123456789ABCDEF0123456789abcdef01234567"},
		{name: "non-hex byte", sha: "0123456789abcdefg123456789abcdef01234567"},
		{name: "leading space", sha: " 123456789abcdef0123456789abcdef01234567"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NewRevision(tt.sha); err == nil {
				t.Fatalf("NewRevision(%q) = nil error, want a refusal", tt.sha)
			}
		})
	}
}

func TestNewRevisionAcceptsFortyLowercaseHex(t *testing.T) {
	r, err := NewRevision(shaA)
	if err != nil {
		t.Fatalf("NewRevision(%q) = %v, want no error", shaA, err)
	}
	if got := r.SHA(); got != shaA {
		t.Fatalf("SHA() = %q, want %q", got, shaA)
	}
	if got := r.Ref(); got != "" {
		t.Fatalf("Ref() = %q, want empty", got)
	}
	if r.IsZero() {
		t.Fatal("IsZero() = true on a constructed revision")
	}
}

func TestRevisionShortIsTheSevenCharacterPrefix(t *testing.T) {
	r, err := NewRevision(shaA)
	if err != nil {
		t.Fatalf("NewRevision: %v", err)
	}
	// lib/state.sh:330 and lib/run.sh:2271 both print "${sha:0:7}".
	if got, want := r.Short(), "0123456"; got != want {
		t.Fatalf("Short() = %q, want %q", got, want)
	}
}

func TestZeroRevisionIsZeroAndShortensToNothing(t *testing.T) {
	var r Revision
	if !r.IsZero() {
		t.Fatal("IsZero() = false on the zero revision")
	}
	if got := r.Short(); got != "" {
		t.Fatalf("Short() = %q, want empty", got)
	}
}

func TestRevisionRefIsProvenanceAndNeverIdentity(t *testing.T) {
	bare, err := NewRevision(shaA)
	if err != nil {
		t.Fatalf("NewRevision: %v", err)
	}
	base := bare.WithRef("refs/heads/main")
	if got := base.Ref(); got != "refs/heads/main" {
		t.Fatalf("Ref() = %q, want refs/heads/main", got)
	}
	if !base.Equal(bare) {
		t.Fatal("Equal() = false for one SHA carrying two different refs")
	}
	otherSHA, err := NewRevision(shaB)
	if err != nil {
		t.Fatalf("NewRevision: %v", err)
	}
	if base.Equal(otherSHA.WithRef("refs/heads/main")) {
		t.Fatal("Equal() = true for two different SHAs sharing one ref")
	}
}

func TestWithRefKeepsIdentityAndReplacesProvenance(t *testing.T) {
	r, err := NewRevision(shaA)
	if err != nil {
		t.Fatalf("NewRevision: %v", err)
	}
	tagged := r.WithRef("refs/pull/42/head")
	if got := tagged.Ref(); got != "refs/pull/42/head" {
		t.Fatalf("Ref() = %q, want refs/pull/42/head", got)
	}
	if !tagged.Equal(r) {
		t.Fatal("WithRef changed identity")
	}
}

func TestRevisionPairEqualityIgnoresRefs(t *testing.T) {
	bareBase, err := NewRevision(shaA)
	if err != nil {
		t.Fatalf("NewRevision: %v", err)
	}
	bareHead, err := NewRevision(shaB)
	if err != nil {
		t.Fatalf("NewRevision: %v", err)
	}
	base := bareBase.WithRef("refs/heads/main")
	head := bareHead.WithRef("refs/pull/42/head")

	withRefs := RevisionPair{Base: base, Head: head}
	withoutRefs := RevisionPair{Base: bareBase, Head: bareHead}
	if !withRefs.Equal(withoutRefs) {
		t.Fatal("RevisionPair.Equal() = false where only the refs differ")
	}
	swapped := RevisionPair{Base: bareHead, Head: bareBase}
	if withRefs.Equal(swapped) {
		t.Fatal("RevisionPair.Equal() = true for a swapped pair")
	}
}

func TestRevisionPairIsIncompleteWhenEitherSideIsUnset(t *testing.T) {
	head, _ := NewRevision(shaB)
	if !(RevisionPair{Head: head}).Incomplete() {
		t.Fatal("Incomplete() = false for a pair with no base")
	}
	base, _ := NewRevision(shaA)
	if (RevisionPair{Base: base, Head: head}).Incomplete() {
		t.Fatal("Incomplete() = true for a complete pair")
	}
}

// Every marker carries head_sha (lib/run.sh:1098), so a Revision has to be able
// to appear in one. Unexported fields marshal to {} and decode to nothing at
// all without a codec.
func TestRevisionMarshalsAsItsSHA(t *testing.T) {
	r, err := NewRevision(shaA)
	if err != nil {
		t.Fatalf("NewRevision: %v", err)
	}
	b, err := json.Marshal(struct {
		HeadSHA Revision `json:"head_sha"`
	}{HeadSHA: r.WithRef("refs/pull/42/head")})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if got, want := string(b), `{"head_sha":"`+shaA+`"}`; got != want {
		t.Fatalf("Marshal = %s, want %s", got, want)
	}
}

// The wire form validates at the same single point the constructor does, so a
// malformed SHA cannot enter through a marker either.
func TestRevisionUnmarshalRoutesThroughNewRevision(t *testing.T) {
	var v struct {
		HeadSHA Revision `json:"head_sha"`
	}
	if err := json.Unmarshal([]byte(`{"head_sha":"`+shaA+`"}`), &v); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got := v.HeadSHA.SHA(); got != shaA {
		t.Fatalf("SHA() = %q, want %q", got, shaA)
	}
	for _, raw := range []string{
		`{"head_sha":""}`,
		`{"head_sha":"0123456"}`,
		`{"head_sha":"0123456789ABCDEF0123456789abcdef01234567"}`,
	} {
		var bad struct {
			HeadSHA Revision `json:"head_sha"`
		}
		err := json.Unmarshal([]byte(raw), &bad)
		if err == nil {
			t.Fatalf("Unmarshal(%s) = nil error, want a refusal", raw)
		}
		if !errors.Is(err, ErrRevisionSHA) {
			t.Fatalf("Unmarshal(%s) error = %v, want ErrRevisionSHA", raw, err)
		}
	}
	// A non-string is refused too, rather than decoding as the zero revision.
	var wrongType struct {
		HeadSHA Revision `json:"head_sha"`
	}
	if err := json.Unmarshal([]byte(`{"head_sha":42}`), &wrongType); err == nil {
		t.Fatal("Unmarshal of a number = nil error, want a refusal")
	}
}

// A marker with no head SHA is not a marker, so the zero revision refuses to
// become one. A field that is genuinely optional carries `omitzero`, which
// leaves it out without reaching MarshalJSON at all.
func TestTheZeroRevisionRefusesToReachTheWireAndOmitzeroOmitsIt(t *testing.T) {
	if _, err := json.Marshal(struct {
		HeadSHA Revision `json:"head_sha"`
	}{}); err == nil {
		t.Fatal("Marshal of the zero revision = nil error, want a refusal")
	}
	b, err := json.Marshal(struct {
		Base Revision `json:"base_sha,omitzero"`
	}{})
	if err != nil {
		t.Fatalf("Marshal with omitzero: %v", err)
	}
	if got := string(b); got != "{}" {
		t.Fatalf("Marshal with omitzero = %s, want {}", got)
	}
}
