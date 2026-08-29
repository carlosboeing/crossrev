package cred_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/carlosboeing/crossrev/internal/cred"
)

// refreshedAt is the clock `.last_refresh` is stamped from.
var refreshedAt = time.Date(2026, 8, 29, 1, 2, 3, 0, time.UTC)

// vendor is a stand-in for the harness vendor's OAuth endpoints, served from
// this machine. No test here reaches a real host: httptest.Server listens on
// loopback, and the credential fixtures below are built in the test.
type vendor struct {
	server *httptest.Server

	// discoveryStatus and discoveryBody answer the well-known document.
	discoveryStatus int
	discoveryBody   string
	// tokenStatus and tokenBody answer the token endpoint.
	tokenStatus int
	tokenBody   string

	// requests records what the vendor was asked, so a test can assert on the
	// grant rather than only on the answer.
	discoveryPath string
	tokenBodySeen string
	tokenType     string
	tokenMethod   string
}

func newVendor(t *testing.T) *vendor {
	t.Helper()
	v := &vendor{discoveryStatus: 200, tokenStatus: 200}
	v.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			v.discoveryPath = r.URL.Path
			w.WriteHeader(v.discoveryStatus)
			body := v.discoveryBody
			if body == "" {
				body = `{"issuer":"` + v.server.URL + `","token_endpoint":"` + v.server.URL + `/api/accounts/oauth/token"}`
			}
			_, _ = io.WriteString(w, body)
		case "/api/accounts/oauth/token":
			raw, _ := io.ReadAll(r.Body)
			v.tokenBodySeen = string(raw)
			v.tokenType = r.Header.Get("Content-Type")
			v.tokenMethod = r.Method
			w.WriteHeader(v.tokenStatus)
			_, _ = io.WriteString(w, v.tokenBody)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(v.server.Close)
	return v
}

// storedFor builds a codex-shaped credential whose issuer points at this
// vendor, so the discovery read is derived from the token rather than
// hardcoded.
func storedFor(t *testing.T, v *vendor) []byte {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"exp":       refreshedAt.Unix() + 86400,
		"iss":       v.server.URL,
		"client_id": "app_test",
	})
	if err != nil {
		t.Fatalf("building the claims: %v", err)
	}
	token := "eyJhbGciOiJub25lIn0." + base64.RawURLEncoding.EncodeToString(payload) + ".signature"
	return []byte(`{"OPENAI_API_KEY":null,"auth_mode":"chatgpt","last_refresh":"2026-08-01T00:00:00Z",` +
		`"tokens":{"access_token":"` + token + `","refresh_token":"refresh-old","id_token":"id-old","account_id":"acct"}}`)
}

func refreshOptions() cred.RefreshOptions {
	return cred.RefreshOptions{Now: func() time.Time { return refreshedAt }}
}

// The happy path: the endpoint is read out of the issuer's discovery document,
// the grant is posted as JSON, and the answer is folded into the stored
// credential.
//
// The endpoint is not where the obvious guess puts it — this vendor serves it
// at /api/accounts/oauth/token, which is the shape lib/credentials.sh:229-234
// records as the reason discovery exists at all.
func TestRefreshDerivesTheEndpointFromTheTokensOwnIssuer(t *testing.T) {
	v := newVendor(t)
	v.tokenBody = `{"access_token":"new-access","refresh_token":"refresh-new","id_token":"id-new"}`

	got, err := cred.Refresh(context.Background(), codex(t), storedFor(t, v), refreshOptions())
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	if v.discoveryPath != "/.well-known/openid-configuration" {
		t.Errorf("the discovery document was read from %q", v.discoveryPath)
	}
	if v.tokenMethod != http.MethodPost {
		t.Errorf("the token request was %s, want POST", v.tokenMethod)
	}
	if v.tokenType != "application/json" {
		t.Errorf("the token request Content-Type was %q", v.tokenType)
	}

	var grant map[string]string
	if err := json.Unmarshal([]byte(v.tokenBodySeen), &grant); err != nil {
		t.Fatalf("the grant is not JSON: %v", err)
	}
	for name, want := range map[string]string{
		"grant_type":    "refresh_token",
		"client_id":     "app_test",
		"refresh_token": "refresh-old",
		"scope":         "openid profile email offline_access",
	} {
		if grant[name] != want {
			t.Errorf("grant %s = %q, want %q", name, grant[name], want)
		}
	}

	want := `{"OPENAI_API_KEY":null,"auth_mode":"chatgpt","last_refresh":"2026-08-29T01:02:03Z",` +
		`"tokens":{"access_token":"new-access","refresh_token":"refresh-new","id_token":"id-new","account_id":"acct"}}`
	if string(got) != want {
		t.Errorf("the refreshed credential is\n  %s\nwant\n  %s", got, want)
	}
}

// The stored document's key order survives, and so does every key CrossRev
// knows nothing about. jq keeps a document's own order and appends a key it had
// to create; a map would have sorted the lot.
func TestTheRefreshedCredentialKeepsItsKeyOrderAndItsUnknownKeys(t *testing.T) {
	v := newVendor(t)
	v.tokenBody = `{"access_token":"new-access"}`

	stored := storedFor(t, v)
	got, err := cred.Refresh(context.Background(), codex(t), stored, refreshOptions())
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	if !strings.HasPrefix(string(got), `{"OPENAI_API_KEY":null,"auth_mode":"chatgpt","last_refresh":`) {
		t.Errorf("the key order changed: %s", got)
	}
	if !strings.Contains(string(got), `"account_id":"acct"`) {
		t.Errorf("a key CrossRev does not read was dropped: %s", got)
	}
}

// A key the stored document does not carry is appended at the end, which is
// where jq puts one. Measured on jq 1.7: a credential with no last_refresh and
// no id_token came back as
// {"auth_mode":…,"tokens":{…,"id_token":""},"last_refresh":"T"}.
func TestACreatedKeyIsAppendedRatherThanSorted(t *testing.T) {
	v := newVendor(t)
	v.tokenBody = `{"access_token":"new-access"}`

	payload, _ := json.Marshal(map[string]any{"exp": refreshedAt.Unix() + 86400, "iss": v.server.URL, "client_id": "c"})
	token := "h." + base64.RawURLEncoding.EncodeToString(payload) + ".s"
	stored := []byte(`{"auth_mode":"chatgpt","tokens":{"account_id":"acct","access_token":"` + token + `","refresh_token":"r"}}`)

	got, err := cred.Refresh(context.Background(), codex(t), stored, refreshOptions())
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	want := `{"auth_mode":"chatgpt","tokens":{"account_id":"acct","access_token":"new-access","refresh_token":"r","id_token":""},"last_refresh":"2026-08-29T01:02:03Z"}`
	if string(got) != want {
		t.Errorf("the refreshed credential is\n  %s\nwant\n  %s", got, want)
	}
}

// A response that returns no replacement refresh token means this one was not
// consumed, so keeping it is correct rather than a fallback
// (lib/credentials.sh:298-300). The id token falls back to the stored one for
// the same reason (:301).
func TestAnAbsentReplacementKeepsWhatTheStoreHeld(t *testing.T) {
	v := newVendor(t)
	v.tokenBody = `{"access_token":"new-access"}`

	got, err := cred.Refresh(context.Background(), codex(t), storedFor(t, v), refreshOptions())
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if !strings.Contains(string(got), `"refresh_token":"refresh-old"`) {
		t.Errorf("the unconsumed refresh token was not kept: %s", got)
	}
	if !strings.Contains(string(got), `"id_token":"id-old"`) {
		t.Errorf("the stored id token was not kept: %s", got)
	}
}

// Nothing on any failure path. A caller cannot mistake a half-answer for a
// credential and write it back over the good one
// (lib/credentials.sh:244-245).
func TestEveryRefreshFailureReturnsNoCredential(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*testing.T, *vendor) []byte
	}{
		{"the stored credential has no readable access token", func(t *testing.T, v *vendor) []byte {
			return []byte(`{"tokens":{"access_token":"not-a-jwt","refresh_token":"r"}}`)
		}},
		{"the stored credential is not an object", func(t *testing.T, v *vendor) []byte {
			return []byte(`[]`)
		}},
		{"the claims carry no issuer", func(t *testing.T, v *vendor) []byte {
			payload, _ := json.Marshal(map[string]any{"exp": 1, "client_id": "c"})
			return []byte(`{"tokens":{"access_token":"h.` +
				base64.RawURLEncoding.EncodeToString(payload) + `.s","refresh_token":"r"}}`)
		}},
		{"the store carries no refresh token", func(t *testing.T, v *vendor) []byte {
			payload, _ := json.Marshal(map[string]any{"exp": 1, "iss": v.server.URL, "client_id": "c"})
			return []byte(`{"tokens":{"access_token":"h.` +
				base64.RawURLEncoding.EncodeToString(payload) + `.s"}}`)
		}},
		{"the discovery document is a 404", func(t *testing.T, v *vendor) []byte {
			v.discoveryStatus = 404
			return storedFor(t, v)
		}},
		{"the discovery document names no token endpoint", func(t *testing.T, v *vendor) []byte {
			v.discoveryBody = `{"issuer":"x"}`
			return storedFor(t, v)
		}},
		{"the discovery document is not JSON", func(t *testing.T, v *vendor) []byte {
			v.discoveryBody = `not json`
			return storedFor(t, v)
		}},
		{"the vendor rejected the refresh", func(t *testing.T, v *vendor) []byte {
			v.tokenStatus = 400
			v.tokenBody = `{"error":"invalid_client"}`
			return storedFor(t, v)
		}},
		{"the vendor answered with no access token", func(t *testing.T, v *vendor) []byte {
			v.tokenBody = `{"refresh_token":"r"}`
			return storedFor(t, v)
		}},
		{"the vendor answered with something that is not JSON", func(t *testing.T, v *vendor) []byte {
			v.tokenBody = `not json`
			return storedFor(t, v)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := newVendor(t)
			if v.tokenBody == "" {
				v.tokenBody = `{"access_token":"new-access"}`
			}
			stored := tc.setup(t, v)

			got, err := cred.Refresh(context.Background(), codex(t), stored, refreshOptions())
			if err == nil {
				t.Fatalf("Refresh returned a credential: %s", got)
			}
			if got != nil {
				t.Errorf("Refresh returned %d bytes alongside its error", len(got))
			}
			if !errors.Is(err, cred.ErrRefresh) {
				t.Errorf("error = %v, want ErrRefresh", err)
			}
			var refusal *cred.Refusal
			if !errors.As(err, &refusal) {
				t.Fatalf("not a *cred.Refusal: %T", err)
			}
			if refusal.Reason == "" || refusal.Action == "" {
				t.Errorf("reason %q, action %q — both are required", refusal.Reason, refusal.Action)
			}
		})
	}
}

// The reason the shell reads the body at all: token_expired and invalid_client
// need different fixes and the difference is only in there
// (lib/credentials.sh:276-278).
//
// The third case is a divergence. The shell writes the read as one jq filter,
// `.error.message // .error_description // .error // "no reason given"`, and
// that filter cannot reach its third arm: `.error.message` on a string is a
// hard jq error rather than null, so jq exits 5 and prints nothing. Measured:
//
//	echo '{"error":{"message":"m"}}'  ->  m
//	echo '{"error_description":"d"}'  ->  d
//	echo '{"error":"e"}'              ->  "", jq exit 5
//	echo '{}'                         ->  no reason given
//
// `{"error":"invalid_client"}` is the shape RFC 6749 section 5.2 specifies, so
// the shell drops the reason for the most common rejection there is.
func TestARejectionCarriesTheVendorsOwnReason(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want string
	}{
		{"a nested message", `{"error":{"message":"token_expired"}}`, "token_expired"},
		{"an error description", `{"error_description":"invalid_client"}`, "invalid_client"},
		{"a bare error string", `{"error":"invalid_client"}`, "invalid_client"},
		{"an empty object", `{}`, "no reason given"},
		{"something that is not JSON", `<html>502</html>`, "no reason given"},
		{"an empty body", ``, "no reason given"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := newVendor(t)
			v.tokenStatus = 400
			v.tokenBody = tc.body

			_, err := cred.Refresh(context.Background(), codex(t), storedFor(t, v), refreshOptions())
			if err == nil {
				t.Fatal("a rejected refresh produced a credential")
			}
			if !strings.Contains(err.Error(), "HTTP 400") {
				t.Errorf("the reason does not name the status: %q", err.Error())
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("the reason is %q, want it to carry %q", err.Error(), tc.want)
			}
		})
	}
}

// A vendor that cannot be reached at all is one failure and a vendor that
// rejects is another, and the messages say which
// (lib/credentials.sh:284, :288).
func TestAnUnreachableVendorIsRefusedWithoutAStatus(t *testing.T) {
	v := newVendor(t)
	stored := storedFor(t, v)
	v.server.Close()

	_, err := cred.Refresh(context.Background(), codex(t), stored, refreshOptions())
	if !errors.Is(err, cred.ErrRefresh) {
		t.Fatalf("error = %v, want ErrRefresh", err)
	}
	if !strings.Contains(err.Error(), "discovery document") {
		t.Errorf("the reason does not say which call failed: %q", err.Error())
	}
}

// A cancelled context ends the refresh rather than the refresh ending itself.
func TestACancelledContextStopsTheRefresh(t *testing.T) {
	v := newVendor(t)
	v.tokenBody = `{"access_token":"new-access"}`

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := cred.Refresh(ctx, codex(t), storedFor(t, v), refreshOptions())
	if err == nil {
		t.Fatal("a cancelled refresh produced a credential")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want it to wrap context.Canceled", err)
	}
}

// No refresh failure quotes the credential it was given or the token it asked
// with. A refresher job's output is a CI transcript.
func TestARefreshRefusalNeverQuotesTheCredential(t *testing.T) {
	v := newVendor(t)
	v.tokenStatus = 400
	v.tokenBody = `{"error":"invalid_client"}`

	stored := storedFor(t, v)
	_, err := cred.Refresh(context.Background(), codex(t), stored, refreshOptions())
	if err == nil {
		t.Fatal("a rejected refresh produced a credential")
	}
	var refusal *cred.Refusal
	if !errors.As(err, &refusal) {
		t.Fatalf("not a *cred.Refusal: %T", err)
	}
	printed := refusal.Reason + " " + refusal.Action
	for _, forbidden := range []string{"refresh-old", "id-old", string(stored)} {
		if strings.Contains(printed, forbidden) {
			t.Errorf("the refusal quotes the credential: %q", printed)
		}
	}
}

// Refresh writes nothing. It hands back bytes and the caller decides where they
// go, which is what keeps "a leg restores, reads and discards" a property of
// the type: lib/auth.sh:1020 re-reads the expiry out of them and refuses to
// write back one it cannot read, and :1034-1042 is the only `gh secret set`.
func TestRefreshReturnsBytesAndTouchesTheStoredCredential(t *testing.T) {
	v := newVendor(t)
	v.tokenBody = `{"access_token":"new-access"}`

	stored := storedFor(t, v)
	before := string(stored)

	if _, err := cred.Refresh(context.Background(), codex(t), stored, refreshOptions()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if string(stored) != before {
		t.Error("Refresh changed the credential it was given")
	}
}

// The round trip lib/auth.sh:1006-1032 performs: read the expiry, refresh, read
// the expiry back out of what came back, and refuse an expiry no later than the
// one it replaces.
func TestTheRefreshedCredentialCarriesAReadableLaterExpiry(t *testing.T) {
	v := newVendor(t)
	newExpiry := refreshedAt.Unix() + 10*86400
	payload, _ := json.Marshal(map[string]any{"exp": newExpiry, "iss": v.server.URL, "client_id": "app_test"})
	v.tokenBody = `{"access_token":"h.` + base64.RawURLEncoding.EncodeToString(payload) + `.s"}`

	stored := storedFor(t, v)
	before, err := cred.SecondsLeft(codex(t), stored, refreshedAt)
	if err != nil {
		t.Fatalf("reading the stored expiry: %v", err)
	}

	fresh, err := cred.Refresh(context.Background(), codex(t), stored, refreshOptions())
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	after, err := cred.SecondsLeft(codex(t), fresh, refreshedAt)
	if err != nil {
		t.Fatalf("reading the refreshed expiry: %v", err)
	}
	if after <= before {
		t.Errorf("the refreshed credential expires no later than the one it replaces: %d vs %d", after, before)
	}
	if want := 10 * 86400; int(after) != want {
		t.Errorf("seconds left = %d, want %d", after, want)
	}
}

// A trailing slash on the issuer is trimmed before the well-known path is
// joined, which is `${issuer%/}` at lib/credentials.sh:238.
func TestATrailingSlashOnTheIssuerIsTrimmed(t *testing.T) {
	v := newVendor(t)
	v.tokenBody = `{"access_token":"new-access"}`

	payload, _ := json.Marshal(map[string]any{
		"exp": refreshedAt.Unix() + 86400, "iss": v.server.URL + "/", "client_id": "c",
	})
	stored := []byte(`{"tokens":{"access_token":"h.` +
		base64.RawURLEncoding.EncodeToString(payload) + `.s","refresh_token":"r"}}`)

	if _, err := cred.Refresh(context.Background(), codex(t), stored, refreshOptions()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if v.discoveryPath != "/.well-known/openid-configuration" {
		t.Errorf("the discovery document was read from %q", v.discoveryPath)
	}
}
