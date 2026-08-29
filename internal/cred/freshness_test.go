package cred_test

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/carlosboeing/crossrev/internal/cred"
)

// epoch is the fixed clock every freshness test measures against. A credential
// fixture is built relative to it, so nothing here depends on how long the test
// binary took to get here — which is the load-dependent failure
// tests/test-credentials.sh:85-88 works around by choosing 930 seconds rather
// than 900.
var epoch = time.Unix(1_700_000_000, 0).UTC()

// credential is the shape codex actually stores, with an access token whose
// claims say what a test needs them to say (tests/test-credentials.sh:40-49).
func credential(t *testing.T, secondsLeft int64) []byte {
	t.Helper()
	return credentialWithToken(t, jwtWithExpiry(epoch.Unix()+secondsLeft))
}

func jwtWithExpiry(expiry int64) string {
	payload, err := json.Marshal(map[string]any{
		"exp":       expiry,
		"iss":       "https://auth.example.com",
		"client_id": "app_test",
	})
	if err != nil {
		panic(err)
	}
	return "eyJhbGciOiJub25lIn0." + base64.RawURLEncoding.EncodeToString(payload) + ".signature"
}

func credentialWithToken(t *testing.T, token string) []byte {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"OPENAI_API_KEY": nil,
		"auth_mode":      "chatgpt",
		"last_refresh":   "2026-08-01T00:00:00Z",
		"tokens": map[string]any{
			"access_token":  token,
			"refresh_token": "refresh-abc",
			"id_token":      "id-abc",
			"account_id":    "acct",
		},
	})
	if err != nil {
		t.Fatalf("building the credential fixture: %v", err)
	}
	return raw
}

func codex(t *testing.T) cred.Descriptor {
	t.Helper()
	d := descriptors(t).For("codex")
	if !d.Credential.AssertFresh || d.Credential.AccessTokenJQ == "" {
		t.Fatalf("the codex descriptor no longer drives the freshness path: %+v", d.Credential)
	}
	return d
}

// The expiry is read out of the access token's own claims, and so are the
// issuer and the client id — the other half of a refresh request. Nothing about
// the vendor is hardcoded (tests/test-credentials.sh:54-62).
func TestSecondsLeftReadsTheAccessTokensOwnClaims(t *testing.T) {
	got, err := cred.SecondsLeft(codex(t), credential(t, 86400), epoch)
	if err != nil {
		t.Fatalf("SecondsLeft: %v", err)
	}
	if got != 86400 {
		t.Errorf("seconds left = %d, want 86400", got)
	}

	claims, err := cred.TokenClaims(codex(t), credential(t, 86400))
	if err != nil {
		t.Fatalf("TokenClaims: %v", err)
	}
	if claims.Issuer != "https://auth.example.com" {
		t.Errorf("issuer = %q", claims.Issuer)
	}
	if claims.ClientID != "app_test" {
		t.Errorf("client id = %q", claims.ClientID)
	}
}

// Negative when it already has (lib/credentials.sh:76).
func TestSecondsLeftIsNegativeForAnExpiredToken(t *testing.T) {
	got, err := cred.SecondsLeft(codex(t), credential(t, -60), epoch)
	if err != nil {
		t.Fatalf("SecondsLeft: %v", err)
	}
	if got != -60 {
		t.Errorf("seconds left = %d, want -60", got)
	}
}

// An unreadable token is not silently treated as fresh
// (tests/test-credentials.sh:76-79).
func TestSecondsLeftRefusesWhatItCannotRead(t *testing.T) {
	for _, tc := range []struct {
		name       string
		credential []byte
	}{
		{"a token that is not a JWT", credentialWithToken(t, "not-a-jwt")},
		{"a token with no exp claim", credentialWithToken(t, "h."+base64.RawURLEncoding.EncodeToString([]byte(`{"iss":"x"}`))+".s")},
		{"no tokens object", []byte(`{"auth_mode":"chatgpt"}`)},
		{"no access token", []byte(`{"tokens":{}}`)},
		{"a null access token", []byte(`{"tokens":{"access_token":null}}`)},
		{"an empty access token", []byte(`{"tokens":{"access_token":""}}`)},
		{"a credential that is not an object", []byte(`[]`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := cred.SecondsLeft(codex(t), tc.credential, epoch); !errors.Is(err, cred.ErrMalformedToken) {
				t.Errorf("SecondsLeft error = %v, want ErrMalformedToken", err)
			}
		})
	}
}

// A store the descriptor gives no access token path has no access token to
// read: `[[ -n "$jq_path" ]] || return 1` at lib/credentials.sh:70.
func TestSecondsLeftRefusesAHarnessWithNoAccessTokenPath(t *testing.T) {
	claude := descriptors(t).For("claude")
	if claude.Credential.AccessTokenJQ != "" {
		t.Fatal("claude now carries an access token path, so this no longer covers the case")
	}
	if _, err := cred.SecondsLeft(claude, credential(t, 86400), epoch); !errors.Is(err, cred.ErrMalformedToken) {
		t.Errorf("SecondsLeft error = %v, want ErrMalformedToken", err)
	}
}

// The one non-null access_token_jq the descriptor ships has to be a path this
// build can walk. A descriptor entry that reached for jq's wider language would
// otherwise be read as a field name and silently answer nothing.
func TestEveryShippedAccessTokenPathIsAPlainObjectPath(t *testing.T) {
	doc := descriptors(t)
	checked := 0
	for _, name := range doc.Names() {
		d := doc.For(name)
		if d.Credential.AccessTokenJQ == "" {
			continue
		}
		checked++
		if _, err := cred.AccessToken(d, credential(t, 60)); err != nil {
			t.Errorf("%s: access token path %q is not usable: %v", name, d.Credential.AccessTokenJQ, err)
		}
	}
	if checked == 0 {
		t.Fatal("no shipped harness carries an access token path, so this proves nothing")
	}
}

func TestAccessTokenRefusesAPathThisBuildCannotWalk(t *testing.T) {
	for _, path := range []string{
		"tokens.access_token",
		".",
		".tokens..access_token",
		`.tokens["access_token"]`,
		".tokens | .access_token",
		".tokens.access_token // empty",
	} {
		d := cred.Descriptor{Harness: "probe", Credential: cred.Credential{AccessTokenJQ: path}}
		if _, err := cred.AccessToken(d, credential(t, 60)); !errors.Is(err, cred.ErrDescriptor) {
			t.Errorf("AccessToken(%q) error = %v, want ErrDescriptor", path, err)
		}
	}
}

// The boundaries _cred_human_duration draws, plural included. The text is what
// a refusal quotes, so it is reproduced rather than corrected.
func TestHumanDuration(t *testing.T) {
	for _, tc := range []struct {
		seconds int64
		want    string
	}{
		{-1, "expired"},
		{0, "0 minutes"},
		{59, "0 minutes"},
		{60, "1 minutes"},
		{3599, "59 minutes"},
		{3600, "1 hours"},
		{86400, "24 hours"},
		{172799, "47 hours"},
		{172800, "2 days"},
		{604800, "7 days"},
	} {
		if got := cred.HumanDuration(tc.seconds); got != tc.want {
			t.Errorf("HumanDuration(%d) = %q, want %q", tc.seconds, got, tc.want)
		}
	}
}

// A credential with a day left runs (tests/test-credentials.sh:82-83).
func TestAssertFreshAcceptsACredentialWithADayLeft(t *testing.T) {
	if err := cred.AssertFresh(codex(t), credential(t, 86400), epoch); err != nil {
		t.Errorf("AssertFresh: %v", err)
	}
}

// The floor is exactly an hour, and it is `<` rather than `<=`
// (lib/credentials.sh:218).
func TestTheFloorIsAnHourExactly(t *testing.T) {
	if cred.MinSeconds != 3600 {
		t.Fatalf("MinSeconds = %d, want 3600", cred.MinSeconds)
	}
	if err := cred.AssertFresh(codex(t), credential(t, cred.MinSeconds), epoch); err != nil {
		t.Errorf("a credential sitting exactly on the floor was refused: %v", err)
	}
	if err := cred.AssertFresh(codex(t), credential(t, cred.MinSeconds-1), epoch); !errors.Is(err, cred.ErrStale) {
		t.Errorf("one second under the floor error = %v, want ErrStale", err)
	}
}

// What the refusal has to say, in the words tests/test-credentials.sh:89-100
// asserts on: how much is left, the floor, why refreshing here is worse than
// stopping, and both ways out — including the manual recovery by name.
func TestAStaleCredentialRefusesAndNamesBothWaysOut(t *testing.T) {
	err := cred.AssertFresh(codex(t), credential(t, 930), epoch)
	if !errors.Is(err, cred.ErrStale) {
		t.Fatalf("AssertFresh error = %v, want ErrStale", err)
	}
	var refusal *cred.Refusal
	if !errors.As(err, &refusal) {
		t.Fatalf("AssertFresh error is not a *cred.Refusal: %T", err)
	}
	for _, want := range []string{"15 minutes", "one-hour floor"} {
		if !strings.Contains(refusal.Reason, want) {
			t.Errorf("the reason does not mention %q: %q", want, refusal.Reason)
		}
	}
	for _, want := range []string{"consume the refresh token", "crossrev-token-refresh", "codex login"} {
		if !strings.Contains(refusal.Action, want) {
			t.Errorf("the action does not mention %q: %q", want, refusal.Action)
		}
	}
}

// An expired credential says so plainly rather than in negative minutes
// (tests/test-credentials.sh:102-105).
func TestAnExpiredCredentialSaysExpired(t *testing.T) {
	err := cred.AssertFresh(codex(t), credential(t, -60), epoch)
	if !errors.Is(err, cred.ErrStale) {
		t.Fatalf("AssertFresh error = %v, want ErrStale", err)
	}
	if !strings.Contains(err.Error(), "expired") {
		t.Errorf("the reason does not say expired: %q", err.Error())
	}
	if strings.Contains(err.Error(), "-1 minutes") {
		t.Errorf("the reason counts in negative minutes: %q", err.Error())
	}
}

// A credential with no readable expiry refuses rather than assuming it is fine
// (tests/test-credentials.sh:107-110).
func TestACredentialWithNoReadableExpiryRefuses(t *testing.T) {
	err := cred.AssertFresh(codex(t), []byte(`{"tokens":{}}`), epoch)
	if !errors.Is(err, cred.ErrExpiryUnreadable) {
		t.Fatalf("AssertFresh error = %v, want ErrExpiryUnreadable", err)
	}
	if !strings.Contains(err.Error(), "does not carry a readable expiry") {
		t.Errorf("the reason reads %q", err.Error())
	}
	var refusal *cred.Refusal
	if !errors.As(err, &refusal) {
		t.Fatalf("not a *cred.Refusal: %T", err)
	}
	if !strings.Contains(refusal.Action, "cannot reason about") {
		t.Errorf("the action reads %q", refusal.Action)
	}
}

// Only a credential the descriptor marks assert_fresh is a freshness question.
// An archetype-A store holds no expiry to read, and demanding one would stop
// exactly the harnesses that cannot answer it (lib/credentials.sh:205-210).
//
// Measured on the shell for the same input: `cred_assert_fresh opencode` and
// `cred_assert_fresh not-a-harness` both returned 0 with no output on a
// credential holding the token `garbage`.
func TestAssertFreshSkipsAHarnessThatCarriesNoExpiry(t *testing.T) {
	doc := descriptors(t)
	for _, name := range []string{"claude", "grok", "opencode", "not-a-harness"} {
		d := doc.For(name)
		if d.Credential.AssertFresh {
			t.Fatalf("%s is marked assert_fresh, so it no longer covers this case", name)
		}
		if err := cred.AssertFresh(d, []byte(`{"anything":"at all"}`), epoch); err != nil {
			t.Errorf("%s: AssertFresh = %v, want nil", name, err)
		}
	}
}

// The refusals travel as values, and the whole reason for that is stated in
// errors.go: internal/cred cannot import internal/ui. A refusal that lost its
// action half would print half a `ui_die`.
func TestEveryRefusalCarriesBothHalves(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"stale", cred.AssertFresh(codex(t), credential(t, 60), epoch)},
		{"unreadable", cred.AssertFresh(codex(t), []byte(`{"tokens":{}}`), epoch)},
	} {
		var refusal *cred.Refusal
		if !errors.As(tc.err, &refusal) {
			t.Fatalf("%s: not a *cred.Refusal: %v", tc.name, tc.err)
		}
		if refusal.Reason == "" || refusal.Action == "" {
			t.Errorf("%s: reason %q, action %q — both are required", tc.name, refusal.Reason, refusal.Action)
		}
		if refusal.Error() != refusal.Reason {
			t.Errorf("%s: Error() is not the reason", tc.name)
		}
	}
}

// No refusal quotes the credential it refused.
func TestAFreshnessRefusalNeverQuotesTheCredential(t *testing.T) {
	token := jwtWithExpiry(epoch.Unix() + 60)
	raw := credentialWithToken(t, token)

	err := cred.AssertFresh(codex(t), raw, epoch)
	if err == nil {
		t.Fatal("a credential a minute from death was accepted")
	}
	var refusal *cred.Refusal
	if !errors.As(err, &refusal) {
		t.Fatalf("not a *cred.Refusal: %T", err)
	}
	printed := refusal.Reason + " " + refusal.Action
	for _, forbidden := range []string{token, "refresh-abc", "id-abc", string(raw)} {
		if strings.Contains(printed, forbidden) {
			t.Errorf("the refusal quotes the credential: %q", printed)
		}
	}
}
