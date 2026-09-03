package app_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/carlosboeing/crossrev/internal/app"
	"github.com/carlosboeing/crossrev/internal/cred"
	"github.com/carlosboeing/crossrev/internal/exec"
	"github.com/carlosboeing/crossrev/internal/harness"
)

// --- a vendor that answers without a network -------------------------------

// accessToken is a JWT-shaped access token carrying the three claims a refresh
// needs. Nothing verifies its signature — cred reads the payload alone.
func accessToken(expiry int64, issuer, clientID string) string {
	payload := fmt.Sprintf(`{"exp":%d,"iss":%q,"client_id":%q}`, expiry, issuer, clientID)
	return "aGVhZGVy." + base64.RawURLEncoding.EncodeToString([]byte(payload)) + ".c2ln"
}

const (
	vendorIssuer   = "https://auth.vendor.example"
	vendorClient   = "client-abc"
	vendorEndpoint = "https://auth.vendor.example/oauth/token"
)

// storedCredential is what the refresher workflow hands in through the
// environment: the vendor's own store, holding the two tokens.
func storedCredential(expiry int64, refreshToken string) string {
	return fmt.Sprintf(`{"tokens":{"access_token":%q,"refresh_token":%q},"other":"kept"}`,
		accessToken(expiry, vendorIssuer, vendorClient), refreshToken)
}

// vendor answers the discovery read and the token exchange, and records both.
type vendor struct {
	requests    []*http.Request
	bodies      []string
	tokenStatus int
	tokenBody   string
	endpoint    string
}

func (v *vendor) Do(req *http.Request) (*http.Response, error) {
	v.requests = append(v.requests, req)
	body := ""
	if req.Body != nil {
		raw, _ := io.ReadAll(req.Body)
		body = string(raw)
	}
	v.bodies = append(v.bodies, body)

	if strings.HasSuffix(req.URL.Path, "/.well-known/openid-configuration") {
		endpoint := v.endpoint
		if endpoint == "" {
			endpoint = vendorEndpoint
		}
		return answer(200, `{"token_endpoint":"`+endpoint+`"}`), nil
	}
	status := v.tokenStatus
	if status == 0 {
		status = 200
	}
	return answer(status, v.tokenBody), nil
}

func answer(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{},
	}
}

// refreshed is the vendor's answer carrying a new access token that expires
// later than the one it replaces.
func refreshed(expiry int64) string {
	return fmt.Sprintf(`{"access_token":%q,"refresh_token":"refresh-2"}`,
		accessToken(expiry, vendorIssuer, vendorClient))
}

// codexCredential wires the descriptor's own refresher harness into the bench
// and sets the secret it reads.
func (b *bench) codexCredential(value string) {
	b.env["CROSSREV_CODEX_AUTH"] = value
}

func (b *bench) vendor(v *vendor) *vendor {
	b.cmds.RefreshOptions = cred.RefreshOptions{HTTP: v, Now: func() time.Time { return at }}
	return v
}

// --- picking the harness ----------------------------------------------------

// The shipped descriptor names exactly one refresher, so an unnamed harness
// resolves without asking (lib/auth.sh:975-995).
func TestRefreshPicksTheOnlyHarnessConfiguredWithARefresher(t *testing.T) {
	b := newBench(t, exec.Result{})
	b.codexCredential(storedCredential(at.Unix()+3600, "refresh-1"))
	b.vendor(&vendor{tokenBody: refreshed(at.Unix() + 86400)})

	if err := b.cmds.Refresh(context.Background(), app.RefreshRequest{Repo: "acme/widget"}); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if !strings.Contains(b.text(), "CROSSREV_CODEX_AUTH now holds a credential valid for 24 hours") {
		t.Fatalf("printed:\n%s", b.text())
	}
}

// Two refreshers is an ambiguity the operator has to settle, and the refusal
// names both (lib/auth.sh:1041-1043).
func TestRefreshRefusesWhenTwoHarnessesCarryARefresher(t *testing.T) {
	b := newBench(t)
	b.cmds.Harnesses = harnessesWithRefreshers(t, "claude", "codex")

	err := b.cmds.Refresh(context.Background(), app.RefreshRequest{Repo: "acme/widget"})
	wantRefusal(t, err,
		"more than one harness is configured with a refresher (claude, codex)",
		"Specify which harness to refresh with --harness <name>.")
}

func TestRefreshRefusesWhenNoHarnessCarriesARefresher(t *testing.T) {
	b := newBench(t)
	b.cmds.Harnesses = harnessesWithRefreshers(t)

	err := b.cmds.Refresh(context.Background(), app.RefreshRequest{Repo: "acme/widget"})
	wantRefusal(t, err,
		"no harness is configured with a refresher",
		"CrossRev only refreshes credentials that rotate on ephemeral runners.")
}

// harnessesWithRefreshers rewrites the shipped descriptor so exactly the named
// harnesses carry a refresher.
func harnessesWithRefreshers(t *testing.T, names ...string) harness.Document {
	t.Helper()
	var document map[string]json.RawMessage
	if err := json.Unmarshal(harness.DescriptorJSON(), &document); err != nil {
		t.Fatalf("reading the descriptor: %v", err)
	}
	var entries []map[string]json.RawMessage
	if err := json.Unmarshal(document["harnesses"], &entries); err != nil {
		t.Fatalf("reading the harnesses: %v", err)
	}
	wanted := map[string]bool{}
	for _, name := range names {
		wanted[name] = true
	}
	for _, entry := range entries {
		var name string
		if err := json.Unmarshal(entry["name"], &name); err != nil {
			t.Fatalf("reading a harness name: %v", err)
		}
		var credential map[string]json.RawMessage
		if err := json.Unmarshal(entry["credential"], &credential); err != nil {
			t.Fatalf("reading a credential block: %v", err)
		}
		credential["refresher"] = json.RawMessage(fmt.Sprintf("%t", wanted[name]))
		raw, err := json.Marshal(credential)
		if err != nil {
			t.Fatalf("rewriting a credential block: %v", err)
		}
		entry["credential"] = raw
	}
	raw, err := json.Marshal(entries)
	if err != nil {
		t.Fatalf("rewriting the harnesses: %v", err)
	}
	document["harnesses"] = raw
	rewritten, err := json.Marshal(document)
	if err != nil {
		t.Fatalf("rewriting the descriptor: %v", err)
	}
	doc, err := harness.Load(rewritten)
	if err != nil {
		t.Fatalf("loading the rewritten descriptor: %v", err)
	}
	return doc
}

// A harness whose credential does not rotate has nothing to refresh, and an
// unknown name answers the same way (lib/auth.sh:997-1000).
func TestRefreshRefusesAHarnessThatNeedsNoRefresher(t *testing.T) {
	for _, name := range []string{"claude", "agy", "nope"} {
		t.Run(name, func(t *testing.T) {
			b := newBench(t)
			err := b.cmds.Refresh(context.Background(), app.RefreshRequest{Harness: name, Repo: "acme/widget"})
			wantRefusal(t, err,
				"crossrev only refreshes credentials that rotate on ephemeral runners, and '"+name+"' does not need a refresher",
				"Claude's setup-token is long-lived and needs no refresher; Antigravity uses seed-and-self-refresh; only single-writer rotating credentials use the refresher workflow.")
		})
	}
}

// --- the secret the credential arrives in ----------------------------------

func TestRefreshRefusesWhenTheSecretIsNotSet(t *testing.T) {
	b := newBench(t)
	err := b.cmds.Refresh(context.Background(), app.RefreshRequest{Harness: "codex", Repo: "acme/widget"})
	wantRefusal(t, err,
		"CROSSREV_CODEX_AUTH is not set, so there is no credential to refresh",
		"The refresher workflow passes the secret in as this variable. seed once from a machine with a browser: `codex login`, then `gh secret set CROSSREV_CODEX_AUTH < ~/.codex/auth.json`")
}

// --secret names a different variable, and it is both what is read and what is
// written (lib/auth.sh:1002-1005).
func TestRefreshReadsAndWritesTheSecretItWasGiven(t *testing.T) {
	b := newBench(t, exec.Result{})
	b.env["CROSSREV_ALT_AUTH"] = storedCredential(at.Unix()+3600, "refresh-1")
	b.vendor(&vendor{tokenBody: refreshed(at.Unix() + 86400)})

	if err := b.cmds.Refresh(context.Background(), app.RefreshRequest{
		Harness: "codex", Repo: "acme/widget", Secret: "CROSSREV_ALT_AUTH",
	}); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if got, want := b.gh.specs[0].Args, []string{"secret", "set", "CROSSREV_ALT_AUTH", "--repo", "acme/widget"}; !equalArgs(got, want) {
		t.Fatalf("argv = %q, want %q", got, want)
	}
	if !strings.Contains(b.text(), "CROSSREV_ALT_AUTH now holds a credential valid for 24 hours") {
		t.Fatalf("printed:\n%s", b.text())
	}
}

// --- where the refreshed credential is written -----------------------------

func TestRefreshWritesTheRepositorySecretAndSaysHowLongItIsGoodFor(t *testing.T) {
	b := newBench(t, exec.Result{})
	b.codexCredential(storedCredential(at.Unix()+3600, "refresh-1"))
	b.vendor(&vendor{tokenBody: refreshed(at.Unix() + 86400)})

	if err := b.cmds.Refresh(context.Background(), app.RefreshRequest{
		Harness: "codex", Repo: "acme/widget",
	}); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	wantBlock(t, b.text(), "\n◇  Refreshed\n"+
		"│  ✓ CROSSREV_CODEX_AUTH now holds a credential valid for 24 hours\n"+
		"└  This is the only job that writes it. Every leg restores a copy and discards it.\n\n")

	spec := b.gh.specs[0]
	if got, want := spec.Args, []string{"secret", "set", "CROSSREV_CODEX_AUTH", "--repo", "acme/widget"}; !equalArgs(got, want) {
		t.Fatalf("argv = %q, want %q", got, want)
	}
	// The credential arrives on stdin, which is the only place it may be: an
	// argument is visible in the process table to anything on the machine.
	var written map[string]any
	if err := json.Unmarshal(spec.Stdin, &written); err != nil {
		t.Fatalf("the credential was not written to stdin: %v", err)
	}
	tokens, _ := written["tokens"].(map[string]any)
	if tokens["access_token"] != accessToken(at.Unix()+86400, vendorIssuer, vendorClient) {
		t.Fatalf("the refreshed access token was not written: %v", tokens["access_token"])
	}
	if tokens["refresh_token"] != "refresh-2" {
		t.Fatalf("the replacement refresh token was not written: %v", tokens["refresh_token"])
	}
	// Every key the vendor's store carries and CrossRev knows nothing about
	// survives the round trip.
	if written["other"] != "kept" {
		t.Fatalf("an unknown key was dropped: %v", written)
	}
}

// An organisation secret is written with --visibility all, because a secret no
// workflow can read is not a credential (lib/auth.sh:1056).
func TestRefreshWritesAnOrganisationSecretVisibleToEveryWorkflow(t *testing.T) {
	b := newBench(t, exec.Result{})
	b.codexCredential(storedCredential(at.Unix()+3600, "refresh-1"))
	b.vendor(&vendor{tokenBody: refreshed(at.Unix() + 86400)})

	if err := b.cmds.Refresh(context.Background(), app.RefreshRequest{
		Harness: "codex", Org: "acme",
	}); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	want := []string{"secret", "set", "CROSSREV_CODEX_AUTH", "--org", "acme", "--visibility", "all"}
	if got := b.gh.specs[0].Args; !equalArgs(got, want) {
		t.Fatalf("argv = %q, want %q", got, want)
	}
}

// --org wins outright: the repository is neither detected nor written to.
func TestRefreshWithAnOrgNamedDoesNotDetectARepository(t *testing.T) {
	b := newBench(t, exec.Result{})
	b.codexCredential(storedCredential(at.Unix()+3600, "refresh-1"))
	b.vendor(&vendor{tokenBody: refreshed(at.Unix() + 86400)})

	if err := b.cmds.Refresh(context.Background(), app.RefreshRequest{
		Harness: "codex", Org: "acme",
	}); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if len(b.gh.specs) != 1 {
		t.Fatalf("gh was invoked %d times, want the one write", len(b.gh.specs))
	}
}

func TestRefreshDetectsTheRepositoryWhenNeitherIsNamed(t *testing.T) {
	b := newBench(t, out("acme/widget\n"), exec.Result{})
	b.codexCredential(storedCredential(at.Unix()+3600, "refresh-1"))
	b.vendor(&vendor{tokenBody: refreshed(at.Unix() + 86400)})

	if err := b.cmds.Refresh(context.Background(), app.RefreshRequest{Harness: "codex"}); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	want := []string{"repo", "view", "--json", "nameWithOwner", "--jq", ".nameWithOwner"}
	if got := b.gh.specs[0].Args; !equalArgs(got, want) {
		t.Fatalf("argv = %q, want %q", got, want)
	}
	if got := b.gh.specs[1].Args[4]; got != "acme/widget" {
		t.Fatalf("wrote to %q", got)
	}
}

func TestRefreshRefusesWhenTheRepositoryCannotBeWorkedOut(t *testing.T) {
	b := newBench(t, bad())
	b.codexCredential(storedCredential(at.Unix()+3600, "refresh-1"))

	err := b.cmds.Refresh(context.Background(), app.RefreshRequest{Harness: "codex"})
	wantRefusal(t, err,
		"could not work out which repository this is",
		"Run crossrev from a checkout with a GitHub remote, or pass --repo owner/name.")
}

// --- the three refusals that leave the stored secret alone -----------------

func TestRefreshRefusesWhenTheVendorAnsweredNothingUsable(t *testing.T) {
	b := newBench(t, exec.Result{})
	b.codexCredential(storedCredential(at.Unix()+3600, "refresh-1"))
	b.vendor(&vendor{tokenStatus: 400, tokenBody: `{"error":"invalid_grant"}`})

	err := b.cmds.Refresh(context.Background(), app.RefreshRequest{Harness: "codex", Repo: "acme/widget"})
	wantRefusal(t, err,
		"the refresh did not produce a new credential",
		"The stored secret is untouched, so the chain still holds until it expires. Re-seed it by hand if this keeps failing: seed once from a machine with a browser: `codex login`, then `gh secret set CROSSREV_CODEX_AUTH < ~/.codex/auth.json`.")
	if len(b.gh.specs) != 0 {
		t.Fatal("a secret was written after a failed refresh")
	}
}

// An expiry that cannot be read is unverified, not "probably fine"
// (lib/auth.sh:1039-1046).
func TestRefreshRefusesACredentialWhoseExpiryCannotBeRead(t *testing.T) {
	b := newBench(t, exec.Result{})
	b.codexCredential(storedCredential(at.Unix()+3600, "refresh-1"))
	b.vendor(&vendor{tokenBody: `{"access_token":"not-a-jwt","refresh_token":"refresh-2"}`})

	err := b.cmds.Refresh(context.Background(), app.RefreshRequest{Harness: "codex", Repo: "acme/widget"})
	wantRefusal(t, err,
		"the refreshed credential's expiry cannot be read, so CrossRev will not write it back",
		"The vendor answered, but what came back does not parse as a token with an exp claim. The stored secret is untouched and still works until it expires. Re-seed it by hand if this repeats: seed once from a machine with a browser: `codex login`, then `gh secret set CROSSREV_CODEX_AUTH < ~/.codex/auth.json`.")
	if len(b.gh.specs) != 0 {
		t.Fatal("a secret was written for a credential whose expiry could not be read")
	}
}

// An expiry no later than the one it replaces means the refresh did not happen,
// and writing it back would burn a refresh token for nothing
// (lib/auth.sh:1048-1053).
func TestRefreshRefusesACredentialThatExpiresNoLater(t *testing.T) {
	for name, expiry := range map[string]int64{
		"the same expiry": at.Unix() + 3600,
		"an older expiry": at.Unix() + 60,
		"already expired": at.Unix() - 1,
	} {
		t.Run(name, func(t *testing.T) {
			b := newBench(t, exec.Result{})
			b.codexCredential(storedCredential(at.Unix()+3600, "refresh-1"))
			b.vendor(&vendor{tokenBody: refreshed(expiry)})

			err := b.cmds.Refresh(context.Background(), app.RefreshRequest{Harness: "codex", Repo: "acme/widget"})
			wantRefusal(t, err,
				"the refreshed credential expires no later than the one it replaces",
				"The vendor answered but did not issue a new token. The stored secret is untouched. Check the account's session has not been revoked: codex login status")
			if len(b.gh.specs) != 0 {
				t.Fatal("a secret was written for a credential that did not move")
			}
		})
	}
}

// A stored credential whose own expiry cannot be read is not a reason to
// refuse: `before` is simply unknown, and the comparison is skipped
// (lib/auth.sh:1030 and :1026).
func TestRefreshCarriesOnWhenTheStoredExpiryCannotBeRead(t *testing.T) {
	b := newBench(t, exec.Result{})
	b.codexCredential(`{"tokens":{"access_token":"` +
		accessToken(0, vendorIssuer, vendorClient) + `","refresh_token":"refresh-1"}}`)
	b.vendor(&vendor{tokenBody: refreshed(at.Unix() + 86400)})

	if err := b.cmds.Refresh(context.Background(), app.RefreshRequest{Harness: "codex", Repo: "acme/widget"}); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if len(b.gh.specs) != 1 {
		t.Fatalf("gh was invoked %d times, want the one write", len(b.gh.specs))
	}
}

func TestRefreshRefusesWhenTheSecretCannotBeWritten(t *testing.T) {
	b := newBench(t, bad())
	b.codexCredential(storedCredential(at.Unix()+3600, "refresh-1"))
	b.vendor(&vendor{tokenBody: refreshed(at.Unix() + 86400)})

	err := b.cmds.Refresh(context.Background(), app.RefreshRequest{Harness: "codex", Repo: "acme/widget"})
	wantRefusal(t, err,
		"could not write CROSSREV_CODEX_AUTH on acme/widget",
		"The refresher App needs secrets:write on that repository. Check: crossrev auth status")
}

func TestRefreshRefusesWhenTheOrganisationSecretCannotBeWritten(t *testing.T) {
	b := newBench(t, bad())
	b.codexCredential(storedCredential(at.Unix()+3600, "refresh-1"))
	b.vendor(&vendor{tokenBody: refreshed(at.Unix() + 86400)})

	err := b.cmds.Refresh(context.Background(), app.RefreshRequest{Harness: "codex", Org: "acme"})
	wantRefusal(t, err,
		"could not write CROSSREV_CODEX_AUTH at the acme organisation level",
		"The refresher App needs secrets:write on that organisation. Check: crossrev auth status")
}

// --- what must never happen -------------------------------------------------

// The credential never reaches an argument, an error, a printed line, or a
// file. The shell writes it to two temp files and removes them; nothing here
// writes it anywhere but gh's stdin.
func TestRefreshKeepsTheCredentialOutOfEverythingButStdin(t *testing.T) {
	b := newBench(t, exec.Result{})
	stored := storedCredential(at.Unix()+3600, "refresh-1")
	b.codexCredential(stored)
	v := b.vendor(&vendor{tokenBody: refreshed(at.Unix() + 86400)})

	if err := b.cmds.Refresh(context.Background(), app.RefreshRequest{Harness: "codex", Repo: "acme/widget"}); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	secrets := []string{"refresh-1", "refresh-2", accessToken(at.Unix()+3600, vendorIssuer, vendorClient)}
	for _, secret := range secrets {
		if strings.Contains(b.text(), secret) {
			t.Fatalf("a credential was printed")
		}
		for _, spec := range b.gh.specs {
			for _, arg := range spec.Args {
				if strings.Contains(arg, secret) {
					t.Fatalf("a credential reached a gh argument")
				}
			}
			for _, entry := range spec.Env {
				if strings.Contains(entry, secret) {
					t.Fatalf("a credential reached gh's environment")
				}
			}
		}
	}
	// It did reach the vendor, in the request body and nowhere else.
	if len(v.bodies) != 2 || !strings.Contains(v.bodies[1], "refresh-1") {
		t.Fatalf("the refresh token did not reach the token endpoint: %q", v.bodies)
	}
	if !bytes.Contains(b.gh.specs[0].Stdin, []byte("refresh-2")) {
		t.Fatal("the refreshed credential did not reach gh's stdin")
	}
}

// The discovery read goes to the issuer's own well-known path, and the token
// exchange to the endpoint that document names.
func TestRefreshAsksTheIssuerWhereItsTokenEndpointIs(t *testing.T) {
	b := newBench(t, exec.Result{})
	b.codexCredential(storedCredential(at.Unix()+3600, "refresh-1"))
	v := b.vendor(&vendor{tokenBody: refreshed(at.Unix() + 86400)})

	if err := b.cmds.Refresh(context.Background(), app.RefreshRequest{Harness: "codex", Repo: "acme/widget"}); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if len(v.requests) != 2 {
		t.Fatalf("the vendor was asked %d times, want two", len(v.requests))
	}
	if got, want := v.requests[0].URL.String(), vendorIssuer+"/.well-known/openid-configuration"; got != want {
		t.Fatalf("discovery went to %q, want %q", got, want)
	}
	if got, want := v.requests[1].URL.String(), vendorEndpoint; got != want {
		t.Fatalf("the exchange went to %q, want %q", got, want)
	}
}

// The secret's name comes out of the descriptor, not out of the harness's
// name. For codex the two happen to agree, so this asks a harness where they
// do not (lib/auth.sh:1003).
func TestRefreshReadsTheSecretNameOutOfTheDescriptor(t *testing.T) {
	b := newBench(t)
	b.cmds.Harnesses = harnessesWithRefreshers(t, "claude")

	err := b.cmds.Refresh(context.Background(), app.RefreshRequest{Repo: "acme/widget"})
	wantRefusal(t, err,
		"CLAUDE_CODE_OAUTH_TOKEN is not set, so there is no credential to refresh",
		"The refresher workflow passes the secret in as this variable. crossrev runs `claude setup-token` and captures the output; the token is never printed")
}

// A descriptor entry naming no secret falls back to CROSSREV_<NAME>_AUTH, and
// its refusal ends with the generic hint (lib/auth.sh:1004 and :985).
func TestRefreshFallsBackToTheHarnessesOwnVariableName(t *testing.T) {
	b := newBench(t)
	b.cmds.Harnesses = harnessesWithRefreshers(t, "agy")

	err := b.cmds.Refresh(context.Background(), app.RefreshRequest{Repo: "acme/widget"})
	wantRefusal(t, err,
		"CROSSREV_AGY_AUTH is not set, so there is no credential to refresh",
		"The refresher workflow passes the secret in as this variable. re-seed the secret by hand")
}
