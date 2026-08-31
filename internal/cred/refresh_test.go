package cred_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
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
	// tokenLocation, when set, is the Location header the token endpoint
	// answers with, which makes tokenStatus a redirect.
	tokenLocation string
	// discoveryLocation does the same for the well-known document.
	discoveryLocation string
	// reflectToken makes the token endpoint echo the refresh token it was
	// given back in its error body. That is a real identity-provider shape and
	// the reason a refusal redacts what it holds.
	reflectToken bool

	// requests records what the vendor was asked, so a test can assert on the
	// grant rather than only on the answer.
	discoveryPath string
	tokenBodySeen string
	tokenType     string
	tokenMethod   string
}

func newVendor(t *testing.T) *vendor {
	t.Helper()
	return startVendor(t, httptest.NewServer)
}

// newTLSVendor serves the same vendor over TLS, so its issuer is https. The
// downgrade check is a fact about a pair of schemes, and a plaintext issuer has
// nothing to downgrade from.
func newTLSVendor(t *testing.T) *vendor {
	t.Helper()
	return startVendor(t, httptest.NewTLSServer)
}

func startVendor(t *testing.T, start func(http.Handler) *httptest.Server) *vendor {
	t.Helper()
	v := &vendor{discoveryStatus: 200, tokenStatus: 200}
	v.server = start(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			v.discoveryPath = r.URL.Path
			if v.discoveryLocation != "" {
				w.Header().Set("Location", v.discoveryLocation)
			}
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
			if v.reflectToken {
				var grant struct {
					RefreshToken string `json:"refresh_token"`
				}
				_ = json.Unmarshal(raw, &grant)
				v.tokenBody = `{"error_description":"the refresh token ` + grant.RefreshToken + ` is expired"}`
			}
			if v.tokenLocation != "" {
				w.Header().Set("Location", v.tokenLocation)
			}
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
// (lib/credentials.sh:315-317). The id token falls back to the stored one for
// the same reason (:318).
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
		{"the claims carry no client id", func(t *testing.T, v *vendor) []byte {
			payload, _ := json.Marshal(map[string]any{"exp": 1, "iss": v.server.URL})
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
		{"the token endpoint cannot be reached at all", func(t *testing.T, v *vendor) []byte {
			// Discovery has to succeed for the POST path to be reached at all.
			// The existing unreachable-vendor case closes the whole server, so
			// it fails one call earlier and never gets here.
			closed := httptest.NewServer(http.NotFoundHandler())
			gone := closed.URL
			closed.Close()
			v.discoveryBody = `{"token_endpoint":"` + gone + `/token"}`
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
// (lib/credentials.sh:293-295).
//
// The seven shapes here are the seven tests/test-credentials.sh asserts against
// _cred_refusal_reason (lib/credentials.sh:265-268), so the two sides are held
// to the same table rather than to two readings of it.
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
		{"an error object with no message", `{"error":{"code":401}}`, "no reason given"},
		{"an error array", `{"error":["invalid_client"]}`, "no reason given"},
		{"something that is not JSON", `<html>502</html>`, "no reason given"},
		{"an empty body", ``, "no reason given"},
		// The one divergence. `jq -r` prints a number; a bare number is not a
		// reason, and the shell's own `strings` guard already refuses one on
		// the arm below this. See rejectionReason.
		{"an error description that is not a string", `{"error_description":123}`, "no reason given"},
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
// (lib/credentials.sh:301, :305).
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

// elsewhere is a host the vendor's discovery document did not name. It records
// everything it is asked, which is what a redirect that was followed would put
// there.
type elsewhere struct {
	server *httptest.Server

	mu       sync.Mutex
	requests int
	bodies   []string
}

func newElsewhere(t *testing.T) *elsewhere {
	t.Helper()
	e := &elsewhere{}
	e.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		e.mu.Lock()
		e.requests++
		e.bodies = append(e.bodies, string(raw))
		e.mu.Unlock()
		_, _ = io.WriteString(w, `{"access_token":"attacker-issued"}`)
	}))
	t.Cleanup(e.server.Close)
	return e
}

func (e *elsewhere) saw() (int, []string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.requests, append([]string(nil), e.bodies...)
}

// A redirect is the vendor's final answer, never a second request.
//
// http.DefaultClient follows up to ten, and 307 and 308 replay the request body
// — so a token endpoint answering `308 Location: <other host>` would hand the
// whole refresh grant, refresh token included, to a host the discovery document
// did not name, and Refresh would return that host's answer as a success. The
// caller then writes a credential back believing the chain is intact.
//
// `curl -sS` at lib/credentials.sh:299 carries no `-L`, so the shell does not
// follow one either.
func TestARedirectFromTheTokenEndpointIsNeverFollowed(t *testing.T) {
	for _, status := range []int{
		http.StatusMovedPermanently,  // 301
		http.StatusFound,             // 302
		http.StatusSeeOther,          // 303
		http.StatusTemporaryRedirect, // 307
		http.StatusPermanentRedirect, // 308
	} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			attacker := newElsewhere(t)
			v := newVendor(t)
			v.tokenStatus = status
			v.tokenLocation = attacker.server.URL + "/token"
			v.tokenBody = ""

			got, err := cred.Refresh(context.Background(), codex(t), storedFor(t, v), refreshOptions())
			if err == nil {
				t.Fatalf("a redirect was read as a token response: %s", got)
			}
			if got != nil {
				t.Errorf("Refresh returned %d bytes alongside its error", len(got))
			}
			if !strings.Contains(err.Error(), fmt.Sprintf("HTTP %d", status)) {
				t.Errorf("the refusal does not name the status it stopped on: %q", err)
			}

			requests, bodies := attacker.saw()
			if requests != 0 {
				t.Fatalf("the redirect target was asked %d times, carrying %q", requests, bodies)
			}
		})
	}
}

// The same for the discovery read. It carries no token, but a followed redirect
// moves the endpoint lookup to a host the issuer did not name — and the
// endpoint is what the refresh token is then posted to.
func TestARedirectFromTheDiscoveryDocumentIsNeverFollowed(t *testing.T) {
	attacker := newElsewhere(t)
	v := newVendor(t)
	v.discoveryStatus = http.StatusPermanentRedirect
	v.discoveryLocation = attacker.server.URL + "/.well-known/openid-configuration"

	_, err := cred.Refresh(context.Background(), codex(t), storedFor(t, v), refreshOptions())
	if err == nil {
		t.Fatal("a redirected discovery document produced a credential")
	}
	if !strings.Contains(err.Error(), "HTTP 308") {
		t.Errorf("the refusal does not name the status it stopped on: %q", err)
	}
	if requests, bodies := attacker.saw(); requests != 0 {
		t.Fatalf("the redirect target was asked %d times, carrying %q", requests, bodies)
	}
}

// The control for the two above: with a client that does follow, the same
// vendor hands the refresh token to the redirect target and Refresh reports
// success. This is the measurement, kept as a test so that a build which
// stopped setting CheckRedirect fails here rather than in production.
func TestAFollowingClientIsWhatTheDefaultWouldHaveDone(t *testing.T) {
	attacker := newElsewhere(t)
	v := newVendor(t)
	v.tokenStatus = http.StatusPermanentRedirect
	v.tokenLocation = attacker.server.URL + "/token"

	options := refreshOptions()
	options.HTTP = &http.Client{} // follows, which is http.DefaultClient's policy

	got, err := cred.Refresh(context.Background(), codex(t), storedFor(t, v), options)
	if err != nil {
		t.Fatalf("the following client did not reach the redirect target: %v", err)
	}
	if !strings.Contains(string(got), "attacker-issued") {
		t.Fatalf("the redirect target did not answer the refresh: %s", got)
	}
	requests, bodies := attacker.saw()
	if requests == 0 {
		t.Fatal("the redirect target was never asked, so this control proves nothing")
	}
	t.Logf("MEASUREMENT: the redirect target was asked %d times, and received %q", requests, bodies)
	if !strings.Contains(strings.Join(bodies, " "), "refresh-old") {
		t.Fatalf("the redirect target did not receive the refresh token, so this control proves nothing: %q", bodies)
	}
}

// A transport failure at the token endpoint says which call failed.
//
// The unreachable-vendor case above closes the whole server, so discovery fails
// first and the POST is never attempted — the arm at refresh.go's exchange had
// no test reaching it at all.
func TestAnUnreachableTokenEndpointNamesTheTokenCall(t *testing.T) {
	closed := httptest.NewServer(http.NotFoundHandler())
	gone := closed.URL
	closed.Close()

	v := newVendor(t)
	v.discoveryBody = `{"token_endpoint":"` + gone + `/token"}`

	_, err := cred.Refresh(context.Background(), codex(t), storedFor(t, v), refreshOptions())
	if !errors.Is(err, cred.ErrRefresh) {
		t.Fatalf("error = %v, want ErrRefresh", err)
	}
	if !strings.Contains(err.Error(), "could not reach "+gone+"/token at all") {
		t.Errorf("the reason does not name the token endpoint it could not reach: %q", err)
	}
	if strings.Contains(err.Error(), "discovery document") {
		t.Errorf("a token-endpoint failure was reported as a discovery failure: %q", err)
	}
}

// A response larger than CrossRev reads is reported as that, not as whatever
// the truncation failed to parse as.
//
// The cap exists because a refresher job is unattended and the whole body is
// held in memory. Without this test it can be raised to 1<<40 — removed — with
// nothing failing, and a truncated discovery document reports "names no
// token_endpoint", which reads as a vendor fault.
func TestAResponsePastTheCapIsRefusedAsTooLarge(t *testing.T) {
	// One byte over. A body sitting exactly on the cap must still be read, so
	// the fixture is built from the cap rather than from a round number.
	const cap = 1 << 20
	oversize := `{"token_endpoint":"x","padding":"` + strings.Repeat("p", cap) + `"}`
	if len(oversize) <= cap {
		t.Fatalf("the fixture is %d bytes, which is not over the %d-byte cap", len(oversize), cap)
	}

	t.Run("the discovery document", func(t *testing.T) {
		v := newVendor(t)
		v.discoveryBody = oversize

		_, err := cred.Refresh(context.Background(), codex(t), storedFor(t, v), refreshOptions())
		if !errors.Is(err, cred.ErrRefresh) {
			t.Fatalf("error = %v, want ErrRefresh", err)
		}
		if !strings.Contains(err.Error(), "is larger than the 1048576 bytes CrossRev reads") {
			t.Errorf("the reason does not name the cap: %q", err)
		}
		if strings.Contains(err.Error(), "names no token_endpoint") {
			t.Errorf("a truncated document was reported as a vendor fault: %q", err)
		}
	})

	t.Run("the token response", func(t *testing.T) {
		v := newVendor(t)
		v.tokenBody = `{"access_token":"a","padding":"` + strings.Repeat("p", cap) + `"}`

		_, err := cred.Refresh(context.Background(), codex(t), storedFor(t, v), refreshOptions())
		if !errors.Is(err, cred.ErrRefresh) {
			t.Fatalf("error = %v, want ErrRefresh", err)
		}
		if !strings.Contains(err.Error(), "is larger than the 1048576 bytes CrossRev reads") {
			t.Errorf("the reason does not name the cap: %q", err)
		}
	})
}

// A body sitting exactly on the cap is read, which is what makes the refusal
// above about the cap rather than about a byte count near it.
func TestAResponseExactlyOnTheCapIsRead(t *testing.T) {
	v := newVendor(t)
	prefix := `{"access_token":"new-access","padding":"`
	suffix := `"}`
	v.tokenBody = prefix + strings.Repeat("p", (1<<20)-len(prefix)-len(suffix)) + suffix
	if len(v.tokenBody) != 1<<20 {
		t.Fatalf("the fixture is %d bytes, not exactly the cap", len(v.tokenBody))
	}

	got, err := cred.Refresh(context.Background(), codex(t), storedFor(t, v), refreshOptions())
	if err != nil {
		t.Fatalf("a response exactly on the cap was refused: %v", err)
	}
	if !strings.Contains(string(got), `"access_token":"new-access"`) {
		t.Errorf("the refreshed credential is %s", got)
	}
}

// The 2xx boundary, at both calls. 299 succeeds and 300 does not.
//
// 300 is the one that matters: it carries no Location, so the client above has
// nothing to follow and hands the body straight back. Read as success, an empty
// body would be a token response with no access token.
func TestTheSuccessBoundaryIsTwoHundredAndNinetyNine(t *testing.T) {
	t.Run("the discovery document", func(t *testing.T) {
		for _, tc := range []struct {
			status int
			refuse bool
		}{{200, false}, {299, false}, {300, true}} {
			t.Run(fmt.Sprint(tc.status), func(t *testing.T) {
				v := newVendor(t)
				v.discoveryStatus = tc.status
				v.tokenBody = `{"access_token":"new-access"}`

				_, err := cred.Refresh(context.Background(), codex(t), storedFor(t, v), refreshOptions())
				if tc.refuse {
					if err == nil {
						t.Fatalf("HTTP %d was read as a discovery document", tc.status)
					}
					if !strings.Contains(err.Error(), fmt.Sprintf("answered HTTP %d", tc.status)) {
						t.Errorf("the reason does not name the status: %q", err)
					}
					return
				}
				if err != nil {
					t.Fatalf("HTTP %d was refused: %v", tc.status, err)
				}
			})
		}
	})

	t.Run("the token response", func(t *testing.T) {
		for _, tc := range []struct {
			status int
			refuse bool
		}{{200, false}, {299, false}, {300, true}} {
			t.Run(fmt.Sprint(tc.status), func(t *testing.T) {
				v := newVendor(t)
				v.tokenStatus = tc.status
				v.tokenBody = `{"access_token":"new-access"}`

				got, err := cred.Refresh(context.Background(), codex(t), storedFor(t, v), refreshOptions())
				if tc.refuse {
					if err == nil {
						t.Fatalf("HTTP %d was read as a token response: %s", tc.status, got)
					}
					if !strings.Contains(err.Error(), fmt.Sprintf("HTTP %d", tc.status)) {
						t.Errorf("the reason does not name the status: %q", err)
					}
					return
				}
				if err != nil {
					t.Fatalf("HTTP %d was refused: %v", tc.status, err)
				}
			})
		}
	})
}

// The cause of a discovery failure reaches the operator, not just the sentinel.
//
// The shell prints one sentence for all of them (lib/credentials.sh:291). This
// build names which, and the names are what a refusal is for.
func TestEveryDiscoveryFailureNamesItsOwnCause(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*vendor)
		want  string
	}{
		{"a 404", func(v *vendor) { v.discoveryStatus = 404 }, "answered HTTP 404"},
		{"a document that is not JSON", func(v *vendor) { v.discoveryBody = `not json` }, "is not a JSON object"},
		{"a document naming no token endpoint", func(v *vendor) { v.discoveryBody = `{"issuer":"x"}` }, "names no token_endpoint"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := newVendor(t)
			tc.setup(v)

			_, err := cred.Refresh(context.Background(), codex(t), storedFor(t, v), refreshOptions())
			if !errors.Is(err, cred.ErrRefresh) {
				t.Fatalf("error = %v, want ErrRefresh", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("the reason is %q, want it to carry %q", err, tc.want)
			}
			if !strings.Contains(err.Error(), "discovery document") {
				t.Errorf("the reason does not say which call failed: %q", err)
			}
		})
	}
}

// An http token endpoint named by an https issuer is refused. The refresh token
// travels in the request body, so a cleartext POST hands it to anything on the
// path — and the vendor's own document is the only thing that named the host.
//
// The vendor is served over TLS here, because the downgrade is a fact about the
// pair of schemes and an http issuer has nothing to downgrade from. The shell
// checks neither and would post to it (lib/credentials.sh:299).
func TestAnHttpsIssuerMayNotNameAnHttpTokenEndpoint(t *testing.T) {
	v := newTLSVendor(t)
	v.discoveryBody = `{"token_endpoint":"http://token.example.invalid/t"}`

	options := refreshOptions()
	options.HTTP = v.server.Client()

	_, err := cred.Refresh(context.Background(), codex(t), storedFor(t, v), options)
	if !errors.Is(err, cred.ErrRefresh) {
		t.Fatalf("error = %v, want ErrRefresh", err)
	}
	if !strings.Contains(err.Error(), "names a token endpoint CrossRev will not post to") {
		t.Errorf("the reason does not name the downgrade: %q", err)
	}
	var refusal *cred.Refusal
	if !errors.As(err, &refusal) {
		t.Fatalf("not a *cred.Refusal: %T", err)
	}
	if !strings.Contains(refusal.Action, "cleartext") {
		t.Errorf("the action does not say why: %q", refusal.Action)
	}
}

// And an https endpoint from the same https issuer is not refused, which is
// what keeps the check about the downgrade rather than about http.
func TestAnHttpsIssuerNamingAnHttpsEndpointIsFine(t *testing.T) {
	v := newTLSVendor(t)
	v.tokenBody = `{"access_token":"new-access"}`

	options := refreshOptions()
	options.HTTP = v.server.Client()

	got, err := cred.Refresh(context.Background(), codex(t), storedFor(t, v), options)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if !strings.Contains(string(got), `"access_token":"new-access"`) {
		t.Errorf("the refreshed credential is %s", got)
	}
}

// An http issuer naming an http endpoint is not a downgrade, so it is left
// alone — every other test in this file relies on that.
func TestAnHttpIssuerNamingAnHttpEndpointIsNotADowngrade(t *testing.T) {
	v := newVendor(t)
	v.tokenBody = `{"access_token":"new-access"}`

	if _, err := cred.Refresh(context.Background(), codex(t), storedFor(t, v), refreshOptions()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
}

// SetEscapeHTML(false) is the whole point of encodeString, and it had no test.
//
// encoding/json turns `<`, `>` and `&` into \u003c, \u003e and \u0026 by
// default, and jq leaves them alone. Measured: `jq -c .` on
// {"x":"a<b>&c"} prints it back unchanged. A credential whose account name or
// vendor field carries one would come back from a refresh looking edited.
func TestTheRefreshedCredentialDoesNotEscapeHTMLTheWayJqDoesNot(t *testing.T) {
	v := newVendor(t)
	v.tokenBody = `{"access_token":"a<b>&c","id_token":"<&>"}`

	payload, _ := json.Marshal(map[string]any{
		"exp": refreshedAt.Unix() + 86400, "iss": v.server.URL, "client_id": "c",
	})
	token := "h." + base64.RawURLEncoding.EncodeToString(payload) + ".s"
	stored := []byte(`{"a<b>&c":"kept<&>","tokens":{"access_token":"` + token + `","refresh_token":"r<&>"}}`)

	got, err := cred.Refresh(context.Background(), codex(t), stored, refreshOptions())
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if strings.Contains(string(got), `\u00`) {
		t.Errorf("the refreshed credential escaped HTML the way jq does not: %s", got)
	}
	for _, want := range []string{`"a<b>&c":"kept<&>"`, `"access_token":"a<b>&c"`, `"id_token":"<&>"`, `"refresh_token":"r<&>"`} {
		if !strings.Contains(string(got), want) {
			t.Errorf("the refreshed credential does not carry %s: %s", want, got)
		}
	}
}

// The stamp is UTC whatever zone the clock carries, which is `date -u` at
// lib/credentials.sh:321. A fake clock that was already UTC could not tell.
func TestTheRefreshStampIsUTCWhateverZoneTheClockCarries(t *testing.T) {
	v := newVendor(t)
	v.tokenBody = `{"access_token":"new-access"}`

	// A fixed zone rather than a named one: LoadLocation needs a zone database
	// the runner may not carry, and the arithmetic is the whole of the point.
	adelaide := time.FixedZone("ACST", 9*3600+1800)
	local := refreshedAt.In(adelaide)
	if local.Format("2006-01-02T15:04:05") == refreshedAt.Format("2006-01-02T15:04:05") {
		t.Fatal("the fixture clock reads the same in both zones, so it proves nothing")
	}

	got, err := cred.Refresh(context.Background(), codex(t), storedFor(t, v),
		cred.RefreshOptions{Now: func() time.Time { return local }})
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if !strings.Contains(string(got), `"last_refresh":"2026-08-29T01:02:03Z"`) {
		t.Errorf("the stamp is not the UTC instant: %s", got)
	}
	if strings.Contains(string(got), `"last_refresh":"2026-08-29T10:32:03Z"`) {
		t.Errorf("the stamp is the clock's own wall time with a Z on it: %s", got)
	}
}

// A duplicate key keeps the last value and the first position, which is what jq
// does with one (lib/harnesses.sh:11-15 records the same measurement).
func TestADuplicateKeyKeepsTheLastValueAndTheFirstPosition(t *testing.T) {
	v := newVendor(t)
	v.tokenBody = `{"access_token":"new-access","access_token":"second-access"}`

	payload, _ := json.Marshal(map[string]any{
		"exp": refreshedAt.Unix() + 86400, "iss": v.server.URL, "client_id": "c",
	})
	token := "h." + base64.RawURLEncoding.EncodeToString(payload) + ".s"
	stored := []byte(`{"auth_mode":"first","tokens":{"access_token":"` + token + `","refresh_token":"r"},"auth_mode":"second"}`)

	got, err := cred.Refresh(context.Background(), codex(t), stored, refreshOptions())
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if !strings.HasPrefix(string(got), `{"auth_mode":"second",`) {
		t.Errorf("a duplicate key did not keep the last value at the first position: %s", got)
	}
	if strings.Count(string(got), `"auth_mode"`) != 1 {
		t.Errorf("a duplicate key was written back twice: %s", got)
	}
	// And the same rule applied to the vendor's own answer.
	if !strings.Contains(string(got), `"access_token":"second-access"`) {
		t.Errorf("the vendor's duplicate key did not keep its last value: %s", got)
	}
}

// The lower half of the same boundary. net/http will not serve a 1xx as a final
// status — WriteHeader(199) sends it as informational and answers 200 anyway —
// so the response is handed to Refresh directly.
type fixedStatus struct {
	discovery int
	token     int
}

func (f fixedStatus) Do(req *http.Request) (*http.Response, error) {
	status, body := f.token, `{"access_token":"new-access"}`
	if strings.HasSuffix(req.URL.Path, "/.well-known/openid-configuration") {
		status, body = f.discovery, `{"token_endpoint":"https://vendor.example.invalid/t"}`
	}
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{},
		Request:    req,
	}, nil
}

func TestAStatusBelowTwoHundredIsNotSuccess(t *testing.T) {
	v := newVendor(t)
	stored := storedFor(t, v)

	for _, tc := range []struct {
		name   string
		client fixedStatus
		want   string
	}{
		{"the discovery document", fixedStatus{discovery: 199, token: 200}, "answered HTTP 199"},
		{"the token response", fixedStatus{discovery: 200, token: 199}, "HTTP 199"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			options := refreshOptions()
			options.HTTP = tc.client

			got, err := cred.Refresh(context.Background(), codex(t), stored, options)
			if err == nil {
				t.Fatalf("HTTP 199 was read as success: %s", got)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("the reason is %q, want it to carry %q", err, tc.want)
			}
		})
	}
}

// storedTokens reads the two secrets out of a credential storedFor built, so a
// test asserts against the real values rather than against a copy that could
// drift from the fixture.
func storedTokens(t *testing.T, credential []byte) (access, refresh string) {
	t.Helper()
	var stored struct {
		Tokens struct {
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
		} `json:"tokens"`
	}
	if err := json.Unmarshal(credential, &stored); err != nil {
		t.Fatalf("reading the fixture's tokens: %v", err)
	}
	if stored.Tokens.AccessToken == "" || stored.Tokens.RefreshToken == "" {
		t.Fatal("the fixture carries no tokens, so this test would prove nothing")
	}
	return stored.Tokens.AccessToken, stored.Tokens.RefreshToken
}

// A vendor that echoes the submitted refresh token does not get it printed.
//
// The refusal reaches the refresher's run log and CI output, so vendor text
// arriving with a token in it would write that token somewhere permanent. This
// is a declared divergence from lib/credentials.sh, which prints the reason
// unredacted (lib/credentials.sh:265-268, :305).
func TestARejectionDoesNotPrintTheSubmittedRefreshToken(t *testing.T) {
	v := newVendor(t)
	v.tokenStatus = 400
	v.reflectToken = true

	stored := storedFor(t, v)
	_, refreshToken := storedTokens(t, stored)

	_, err := cred.Refresh(context.Background(), codex(t), stored, refreshOptions())
	if err == nil {
		t.Fatal("a rejected refresh produced a credential")
	}
	if !strings.Contains(v.tokenBody, refreshToken) {
		t.Fatalf("the vendor did not echo the token, so this test proves nothing: %q", v.tokenBody)
	}
	if strings.Contains(err.Error(), refreshToken) {
		t.Errorf("the refusal printed the refresh token: %q", err.Error())
	}
	if !strings.Contains(err.Error(), "[redacted]") {
		t.Errorf("the refusal does not say something was removed: %q", err.Error())
	}
	// The rest of the vendor's sentence survives, so the operator still learns
	// what happened.
	if !strings.Contains(err.Error(), "is expired") {
		t.Errorf("redaction removed the reason as well as the token: %q", err.Error())
	}
}

// The access token the refresh is replacing is redacted on the same terms. The
// vendor is not sent it, but a provider that looks the session up can name it.
func TestARejectionDoesNotPrintTheStoredAccessToken(t *testing.T) {
	v := newVendor(t)
	v.tokenStatus = 400

	stored := storedFor(t, v)
	accessToken, _ := storedTokens(t, stored)
	v.tokenBody = `{"error_description":"the session for ` + accessToken + ` is gone"}`

	_, err := cred.Refresh(context.Background(), codex(t), stored, refreshOptions())
	if err == nil {
		t.Fatal("a rejected refresh produced a credential")
	}
	if strings.Contains(err.Error(), accessToken) {
		t.Errorf("the refusal printed the access token: %q", err.Error())
	}
	if !strings.Contains(err.Error(), "[redacted]") {
		t.Errorf("the refusal does not say something was removed: %q", err.Error())
	}
}

// Vendor text is bounded for a status line and stripped of anything a terminal
// would act on rather than show.
//
// A megabyte is the cap on the response BODY. A one-line refusal that lands in
// a run log needs its own, and a newline or an escape sequence in one is the
// problem internal/forge/ghexec's excerpt exists for.
func TestARejectionBoundsAndCleansTheVendorsText(t *testing.T) {
	v := newVendor(t)
	v.tokenStatus = 400
	// A description carrying a newline, a carriage return, a tab and an ANSI
	// escape, then far more text than a status line should hold.
	v.tokenBody = "{\"error_description\":\"first\\nsecond\\r\\tthird\\u001b[31mred" +
		strings.Repeat("x", 4000) + "\"}"

	_, err := cred.Refresh(context.Background(), codex(t), storedFor(t, v), refreshOptions())
	if err == nil {
		t.Fatal("a rejected refresh produced a credential")
	}
	message := err.Error()

	for name, forbidden := range map[string]string{
		"a newline":         "\n",
		"a carriage return": "\r",
		"a tab":             "\t",
		"an escape":         "\x1b",
	} {
		if strings.Contains(message, forbidden) {
			t.Errorf("the refusal carries %s: %q", name, message)
		}
	}
	if !strings.Contains(message, "first") {
		t.Errorf("cleaning removed the reason as well: %q", message)
	}
	if !strings.Contains(message, "…") {
		t.Errorf("the refusal does not mark the text as cut: %q", message)
	}
	// 200 characters of vendor text, plus the ellipsis, plus CrossRev's own
	// sentences around it. A whole 4000-character run would be many times this.
	if runes := []rune(message); len(runes) > 600 {
		t.Errorf("the refusal is %d characters long: %q", len(runes), message)
	}
}
