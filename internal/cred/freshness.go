// freshness.go — refuse rather than refresh in flight.
//
// Only a credential the descriptor marks assert_fresh is a freshness question.
// An archetype-A store holds no expiry to read — Claude Code's setup token and
// opencode's {type, key} auth.json both — and demanding one would stop exactly
// the harnesses that cannot answer it (lib/credentials.sh:205-210). A rotating
// token under its floor means the stored copy is one use from dead.

package cred

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// MinSeconds is CrossRev's floor: an hour (lib/credentials.sh:36).
//
// A leg with less than this refuses rather than running, because the refresh it
// would trigger mid-flight is the one that breaks the chain. The value is
// frozen in tests/fixtures/parity/credentials.json under cred_min_seconds.
const MinSeconds int64 = 3600

// AccessToken reads the access token out of a stored credential, at the path
// `.credential.access_token_jq` names.
//
// The descriptor writes that path as a jq program — `.tokens.access_token` is
// the only non-null value it carries — and this build has no jq. What it
// supports instead is the dotted object path that program is: a leading dot,
// then bare keys separated by dots. Anything else is refused rather than
// approximated, because a path this build silently read as something narrower
// would read the wrong field out of a credential and report the answer as fact.
func AccessToken(d Descriptor, credential []byte) (string, error) {
	path := d.Credential.AccessTokenPath
	if path == "" {
		// `[[ -n "$jq_path" ]] || return 1` at lib/credentials.sh:70. A store
		// with no access token path has no access token to read.
		return "", fmt.Errorf("%w: %s carries no access token path", ErrMalformedToken, d.Harness)
	}
	keys, err := objectPath(path)
	if err != nil {
		return "", err
	}

	value := json.RawMessage(credential)
	for _, key := range keys {
		var object map[string]json.RawMessage
		if err := json.Unmarshal(value, &object); err != nil {
			return "", fmt.Errorf("%w: the credential is not an object at %q", ErrMalformedToken, key)
		}
		next, present := object[key]
		if !present {
			return "", fmt.Errorf("%w: the credential carries no %q", ErrMalformedToken, key)
		}
		value = next
	}

	token := stringClaim(value)
	if token == "" {
		// `[[ -n "$token" ]] || return 1` at lib/credentials.sh:72, which is
		// also where a JSON null lands: `jq -r '… // empty'` prints nothing.
		return "", fmt.Errorf("%w: the credential's access token is empty", ErrMalformedToken)
	}
	return token, nil
}

// objectPath turns `.tokens.access_token` into its keys.
func objectPath(path string) ([]string, error) {
	if !strings.HasPrefix(path, ".") {
		return nil, fmt.Errorf("%w: the access token path %q does not start at the document root", ErrDescriptor, path)
	}
	keys := strings.Split(strings.TrimPrefix(path, "."), ".")
	for _, key := range keys {
		if key == "" {
			return nil, fmt.Errorf("%w: the access token path %q carries an empty key", ErrDescriptor, path)
		}
		// An allowlist, not a list of jq's punctuation. A blocklist names the
		// characters somebody thought of, and `.tokens.access_token?` carries
		// none of them: the `?` would have been read as part of a field name,
		// the lookup would have missed, and the failure would arrive later as a
		// malformed token rather than here as a descriptor error.
		//
		// A jq object key that is not this shape has to be quoted or bracketed,
		// so what this refuses is exactly what jq itself would not accept bare.
		if !bareKey(key) {
			return nil, fmt.Errorf("%w: the access token path %q is not a plain object path", ErrDescriptor, path)
		}
	}
	return keys, nil
}

// bareKey is one segment of a plain object path: `[A-Za-z_][A-Za-z0-9_]*`.
func bareKey(key string) bool {
	for at, r := range key {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == '_':
		case at > 0 && r >= '0' && r <= '9':
		default:
			return false
		}
	}
	return key != ""
}

// TokenClaims reads the claims of the stored credential's access token, which
// is cred_access_token_claims (lib/credentials.sh:67-74).
func TokenClaims(d Descriptor, credential []byte) (Claims, error) {
	token, err := AccessToken(d, credential)
	if err != nil {
		return Claims{}, err
	}
	return ParseClaims(token)
}

// SecondsLeft is how long the stored access token has, negative when it has
// already expired: cred_seconds_left (lib/credentials.sh:77-84).
//
// `now` is a parameter rather than a call to time.Now because
// lib/auth.sh:1006-1020 reads it twice around a refresh and compares the two,
// and a test of that comparison cannot wait for the clock.
func SecondsLeft(d Descriptor, credential []byte, now time.Time) (int64, error) {
	claims, err := TokenClaims(d, credential)
	if err != nil {
		return 0, err
	}
	if !claims.HasExpiry {
		return 0, fmt.Errorf("%w: its access token carries no exp claim", ErrMalformedToken)
	}
	return claims.Expiry - now.Unix(), nil
}

// HumanDuration renders a remaining time the way _cred_human_duration does
// (lib/credentials.sh:86-93).
//
// Every boundary is frozen in tests/fixtures/parity/credentials.json under
// duration_cases, plural included: the shell prints "1 minutes" and "1 hours",
// and this is the text a refusal quotes, so it is reproduced rather than
// corrected.
func HumanDuration(seconds int64) string {
	switch {
	case seconds < 0:
		return "expired"
	case seconds < 3600:
		return fmt.Sprintf("%d minutes", seconds/60)
	case seconds < 172800:
		return fmt.Sprintf("%d hours", seconds/3600)
	default:
		return fmt.Sprintf("%d days", seconds/86400)
	}
}

// AssertFresh refuses a credential that is one use from dead: cred_assert_fresh
// (lib/credentials.sh:211-223).
//
// It takes the credential's bytes rather than a path. Prepare has them in hand
// — it just wrote them — and freshness is a decision about a value, so the file
// system stays out of the one function that has no reason to touch it.
func AssertFresh(d Descriptor, credential []byte, now time.Time) error {
	if !d.Credential.AssertFresh {
		return nil
	}

	left, err := SecondsLeft(d, credential, now)
	if err != nil {
		return &Refusal{
			Kind:   ErrExpiryUnreadable,
			Err:    err,
			Reason: fmt.Sprintf("the restored %s credential does not carry a readable expiry", d.Harness),
			Action: fmt.Sprintf("crossrev reads the access token's exp claim to decide whether it is safe to run. "+
				"A credential it cannot read is one it cannot reason about, so it stops. "+
				"Re-seed the secret from a fresh `%s login`.", d.Harness),
		}
	}

	if left < MinSeconds {
		return &Refusal{
			Kind: ErrStale,
			Reason: fmt.Sprintf("the restored %s credential has %s left, under CrossRev's one-hour floor",
				d.Harness, HumanDuration(left)),
			Action: fmt.Sprintf("Refreshing it here would consume the refresh token and leave the stored copy dead, "+
				"so this leg stops instead. Run the crossrev-token-refresh workflow, "+
				"or re-seed the secret with `%s login` on a machine with a browser.", d.Harness),
		}
	}
	return nil
}
