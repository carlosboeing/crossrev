package core

import "testing"

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
	// lib/run.sh:330 and lib/run.sh:2271 both print "${sha:0:7}".
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
	base, err := NewRevisionWithRef(shaA, "refs/heads/main")
	if err != nil {
		t.Fatalf("NewRevisionWithRef: %v", err)
	}
	bare, err := NewRevision(shaA)
	if err != nil {
		t.Fatalf("NewRevision: %v", err)
	}
	if got := base.Ref(); got != "refs/heads/main" {
		t.Fatalf("Ref() = %q, want refs/heads/main", got)
	}
	if !base.Equal(bare) {
		t.Fatal("Equal() = false for one SHA carrying two different refs")
	}
	other, err := NewRevisionWithRef(shaB, "refs/heads/main")
	if err != nil {
		t.Fatalf("NewRevisionWithRef: %v", err)
	}
	if base.Equal(other) {
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
	base, err := NewRevisionWithRef(shaA, "refs/heads/main")
	if err != nil {
		t.Fatalf("NewRevisionWithRef: %v", err)
	}
	head, err := NewRevisionWithRef(shaB, "refs/pull/42/head")
	if err != nil {
		t.Fatalf("NewRevisionWithRef: %v", err)
	}
	bareBase, _ := NewRevision(shaA)
	bareHead, _ := NewRevision(shaB)

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

func TestRevisionPairIsZeroWhenEitherSideIsUnset(t *testing.T) {
	head, _ := NewRevision(shaB)
	if !(RevisionPair{Head: head}).IsZero() {
		t.Fatal("IsZero() = false for a pair with no base")
	}
	base, _ := NewRevision(shaA)
	if (RevisionPair{Base: base, Head: head}).IsZero() {
		t.Fatal("IsZero() = true for a complete pair")
	}
}
