// jwt.go — reading a token without trusting it.
//
// The claims are read for expiry, issuer and client id only. Nothing here
// treats a claim as an authorisation decision — the vendor does that — so an
// unsigned read is the right tool and verifying the signature would need the
// vendor's JWKS for no gain (lib/credentials.sh:41-45).
//
// # Where this is stricter than the shell, and why it changes nothing
//
// _cred_b64url_decode pipes into `openssl base64 -d -A 2>/dev/null` and
// cred_jwt_claims pipes that into `jq -c . 2>/dev/null`
// (lib/credentials.sh:54, :62). The openssl on this platform ignores characters
// outside the alphabet instead of failing, so a payload of garbage decodes to
// nothing, jq reads empty input, prints nothing and exits 0 — and cred_jwt_claims
// reports SUCCESS with an empty answer. Measured:
//
//	cred_jwt_claims 'aaa.!!!!.ccc'  -> rc=0, output ""
//	cred_jwt_claims 'aaa..ccc'      -> rc=0, output ""
//
// Both are frozen that way in tests/fixtures/parity/credentials.json, under
// payload-is-not-base64url and empty-payload-segment.
//
// ParseClaims returns an error for both instead. That is a divergence in this
// function and in nothing above it, because an empty claim set has no `exp` and
// every caller of cred_access_token_claims immediately asks for one. Measured
// on a credential file whose access token is each of those values:
//
//	cred_seconds_left codex <file>  -> rc=1, no output
//	cred_assert_fresh codex <file>  -> rc=1, "does not carry a readable expiry"
//
// which is exactly what SecondsLeft and AssertFresh return here. The shell
// reaches the refusal one step later; the answer an operator sees is the same.
//
// One further case the shell gets half right: a payload that decodes to two
// concatenated JSON documents makes jq print the first and then fail, so
// cred_jwt_claims returns 1 with a value on stdout. Measured on
// `a.eyJhIjoxfQeyJhIjoxfQ.b`: rc=1, output `{"a":1}`. A Go function cannot
// return both, and returning the value would be the wrong half.

package cred

import (
	"encoding/base64"
	"encoding/json"
	"strings"
)

// Claims are the three fields CrossRev reads out of an access token.
//
// Nothing else is kept. A struct holding every claim would put the whole token
// payload in memory under a name a later caller could log, and the three below
// are the only ones lib/credentials.sh reads: `.exp` at :80, `.iss` and
// `.client_id` at :260-261.
type Claims struct {
	// Expiry is the `exp` claim as seconds since the epoch.
	Expiry int64
	// HasExpiry says whether `exp` was present. A token with no expiry is a
	// different fact from one expiring at the epoch, and the shell tells them
	// apart the same way: `[[ -n "$exp" ]] || return 1` at
	// lib/credentials.sh:81.
	HasExpiry bool
	// Issuer is the `iss` claim, whose discovery document names the token
	// endpoint Refresh posts to.
	Issuer string
	// ClientID is the `client_id` claim, the other half of a refresh request.
	ClientID string
}

// ParseClaims reads the payload segment of a JWT.
//
// The structural rules are the shell's: a value with fewer than two dots is not
// a JWT (`[[ "$jwt" == *.*.* ]]` at lib/credentials.sh:60), and the payload is
// what sits between the first dot and the second (:61). Everything after the
// second dot is ignored, signature included, so a four-segment value parses the
// same as a three-segment one — which is what the shell's suffix trims do.
func ParseClaims(token string) (Claims, error) {
	first := strings.Index(token, ".")
	if first < 0 {
		return Claims{}, malformed("it carries no segment separator")
	}
	afterHeader := token[first+1:]
	second := strings.Index(afterHeader, ".")
	if second < 0 {
		return Claims{}, malformed("it carries one segment separator and needs two")
	}
	payload := afterHeader[:second]

	decoded, err := decodeSegment(payload)
	if err != nil {
		return Claims{}, err
	}

	// An object, not any JSON value. `jq -c .` accepts a bare number or string
	// and the shell then asks it for `.exp`, which errors and leaves the expiry
	// empty — so a non-object reaches the same refusal one step later, the way
	// an undecodable payload does.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(decoded, &raw); err != nil {
		return Claims{}, malformed("its payload does not decode to a JSON object")
	}

	var claims Claims
	if expiry, present := raw["exp"]; present {
		// A whole number, or a string holding one. The shell reads `.exp` with
		// `jq -r` and puts it straight into `$(( exp - now ))`, so what counts
		// is what bash arithmetic accepts, and `-r` has already stripped the
		// quotes off a string by then. Measured on a codex credential whose
		// access token carries each value:
		//
		//	{"exp":1893456000}    rc=0, 105459827
		//	{"exp":"1893456000"}  rc=0, 105459827
		//	{"exp":1.5}           rc=1, "invalid arithmetic operator"
		//	{"exp":[1]}           rc=1, "operand expected"
		//	{"exp":true}          rc=1, "true: unbound variable"
		//	{"exp":null}          rc=1, no output
		//
		// json.Number accepts a JSON number and a JSON string alike, and
		// Int64 refuses everything the shell's four failures refuse.
		var seconds json.Number
		if err := json.Unmarshal(expiry, &seconds); err == nil {
			if whole, err := seconds.Int64(); err == nil {
				claims.Expiry = whole
				claims.HasExpiry = true
			}
		}
	}
	claims.Issuer = stringClaim(raw["iss"])
	claims.ClientID = stringClaim(raw["client_id"])
	return claims, nil
}

// stringClaim reads a claim that has to be a string, answering "" for absent,
// null, or any other type. That is `jq -r '.iss // empty'`: a non-string value
// would be rendered by `-r` as its JSON text, and no caller here would accept
// one as an issuer.
func stringClaim(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return ""
	}
	return value
}

// decodeSegment decodes one base64url JWT segment.
//
// The shell pads to a multiple of four and refuses a length of 4n+1, which no
// base64 encoding can produce (lib/credentials.sh:49-53). RawURLEncoding does
// the same arithmetic, so the padding is stripped rather than added: a segment
// that arrived with `=` characters still decodes, which is the case
// `a.eyJhIjoxfQ==.b` covers.
func decodeSegment(segment string) ([]byte, error) {
	trimmed := strings.TrimRight(segment, "=")
	if trimmed == "" {
		// `_cred_b64url_decode ""` succeeds and prints nothing, so the shell
		// hands jq empty input and reports success with no claims. There is no
		// claim set here to hand back.
		return nil, malformed("its payload segment is empty")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(trimmed)
	if err != nil {
		// The error from encoding/base64 names the offending byte, and a byte
		// of a credential is still a byte of a credential. It is dropped.
		return nil, malformed("its payload segment is not base64url")
	}
	return decoded, nil
}
