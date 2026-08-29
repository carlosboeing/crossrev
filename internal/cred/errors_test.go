package cred_test

import (
	"errors"
	"fmt"
	"io/fs"
	"testing"

	"github.com/carlosboeing/crossrev/internal/cred"
)

// The sentinels are distinct values, so a caller asking for one never matches
// another. Two `errors.New` calls with the same text would compare unequal, but
// a copy-paste that reused a variable would not — and that is what this catches.
func TestTheSentinelsAreDistinct(t *testing.T) {
	sentinels := map[string]error{
		"ErrSecretMissing":    cred.ErrSecretMissing,
		"ErrExpiryUnreadable": cred.ErrExpiryUnreadable,
		"ErrStale":            cred.ErrStale,
		"ErrRefresh":          cred.ErrRefresh,
		"ErrStaging":          cred.ErrStaging,
		"ErrMalformedToken":   cred.ErrMalformedToken,
		"ErrDescriptor":       cred.ErrDescriptor,
	}
	for oneName, one := range sentinels {
		if one == nil {
			t.Fatalf("%s is nil", oneName)
		}
		for otherName, other := range sentinels {
			if oneName == otherName {
				continue
			}
			if errors.Is(one, other) {
				t.Errorf("%s matches %s", oneName, otherName)
			}
		}
	}
}

// A Refusal reports itself as exactly the sentinel it was minted under, and as
// nothing else.
func TestARefusalMatchesOnlyItsOwnKind(t *testing.T) {
	refusal := &cred.Refusal{Kind: cred.ErrStale, Reason: "r", Action: "a"}

	if !errors.Is(refusal, cred.ErrStale) {
		t.Error("a stale refusal does not match ErrStale")
	}
	if errors.Is(refusal, cred.ErrSecretMissing) {
		t.Error("a stale refusal matches ErrSecretMissing")
	}
	if refusal.Error() != "r" {
		t.Errorf("Error() = %q, want the reason", refusal.Error())
	}
}

// A Refusal wrapping an underlying failure still answers for it, so a caller
// asking about fs.ErrPermission or a context error reaches past the refusal.
func TestARefusalUnwrapsWhatCausedIt(t *testing.T) {
	refusal := &cred.Refusal{
		Kind:   cred.ErrStaging,
		Err:    fmt.Errorf("writing: %w", fs.ErrPermission),
		Reason: "r",
		Action: "a",
	}
	if !errors.Is(refusal, cred.ErrStaging) {
		t.Error("the refusal does not match its own kind")
	}
	if !errors.Is(refusal, fs.ErrPermission) {
		t.Error("the refusal does not reach the underlying failure")
	}
}

// A Refusal with no underlying failure unwraps to nil rather than to itself,
// which is what stops errors.Is looping.
func TestARefusalWithNoCauseUnwrapsToNil(t *testing.T) {
	refusal := &cred.Refusal{Kind: cred.ErrStale, Reason: "r", Action: "a"}
	if got := errors.Unwrap(refusal); got != nil {
		t.Errorf("Unwrap = %v, want nil", got)
	}
	if errors.Is(refusal, errors.New("something else")) {
		t.Error("a refusal matched an unrelated error")
	}
}
