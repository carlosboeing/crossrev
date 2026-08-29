// errors.go — the refusals, carrying the two halves a `ui_die` prints.
//
// Every fatal path in lib/credentials.sh is a `ui_die "<reason>" "<action>"`
// (lib/credentials.sh:132, :214 and :219). internal/ui already models that pair
// as ui.FatalError, and this package cannot use it: the tier rules in
// internal/archtest/dependencies_test.go:33 allow internal/cred internal/core,
// tier 1 and internal/exec, and internal/ui is a tier-2 peer. So the pair
// travels as a value of its own and the command layer turns it into a
// ui.FatalError.
//
// No message here quotes a credential. Not the token, not the secret's value,
// not a decoded claim set. A refusal that printed one would put it in a run
// log, a terminal and a CI transcript — three more places than the one the
// refusal exists to keep it out of, which is the reasoning
// internal/exec/errors.go:20-24 states for the same rule.

package cred

import (
	"errors"
	"fmt"
)

// The kinds a caller can ask about with errors.Is. Each is the sentinel a
// Refusal reports itself as; none of them is ever returned on its own.
var (
	// ErrSecretMissing is a GitHub-hosted runner whose harness credential
	// secret is unset or empty (lib/credentials.sh:122-134).
	ErrSecretMissing = errors.New("the harness credential secret did not arrive")

	// ErrExpiryUnreadable is a credential whose access token carries no expiry
	// this build can read (lib/credentials.sh:214-216).
	ErrExpiryUnreadable = errors.New("the credential does not carry a readable expiry")

	// ErrStale is a credential under the one-hour floor
	// (lib/credentials.sh:218-221).
	ErrStale = errors.New("the credential is under CrossRev's one-hour floor")

	// ErrRefresh is any failure on the refresher path
	// (lib/credentials.sh:253-301).
	ErrRefresh = errors.New("the refresh did not produce a new credential")

	// ErrStaging is a scratch directory or staged file that could not be
	// written (lib/credentials.sh:144-150).
	ErrStaging = errors.New("the restored credential could not be staged")
)

// Refusal is a fatal credential decision: what went wrong, and what to do about
// it.
type Refusal struct {
	// Reason is the sentence a `ui_die` prints first, and the text a pull
	// request marker carries when a leg dies.
	Reason string
	// Action is the second half — rule 4 of the output voice. Printed, never
	// stored on the pull request.
	Action string
	// Kind is the sentinel this refusal matches under errors.Is.
	Kind error
	// Err is the underlying failure, when there was one. Nil for a decision
	// this package made on its own.
	Err error
}

func (e *Refusal) Error() string { return e.Reason }

// Is answers errors.Is for the sentinel this refusal was minted under.
func (e *Refusal) Is(target error) bool { return target == e.Kind }

// Unwrap exposes the underlying failure, so errors.Is still reaches a wrapped
// fs.ErrPermission or context.DeadlineExceeded.
func (e *Refusal) Unwrap() error { return e.Err }

// ErrMalformedToken is a value that does not read as a JWT.
//
// It carries no Reason and Action pair because it is never the thing an
// operator is shown: cred_jwt_claims returns non-zero and the caller decides
// what to say. cred_seconds_left turns it into ErrExpiryUnreadable at
// lib/credentials.sh:79, which is the sentence an operator does see.
var ErrMalformedToken = errors.New("the value does not read as a JWT")

// malformed reports a token this build refuses, saying which structural rule it
// broke and never what it contained.
func malformed(because string) error {
	return fmt.Errorf("%w: %s", ErrMalformedToken, because)
}
