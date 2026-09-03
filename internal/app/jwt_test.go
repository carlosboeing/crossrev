package app_test

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/carlosboeing/crossrev/internal/app"
	"github.com/carlosboeing/crossrev/internal/exec"
)

// --- keys, generated here and never checked in ------------------------------
//
// A private key in the tree is a private key in every clone and every published
// tarball, so these are made at run time. One key serves the whole file:
// generating a 2048-bit key costs more than every assertion below put together.

var (
	testKeyOnce sync.Once
	testKey     *rsa.PrivateKey
)

func key(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	testKeyOnce.Do(func() {
		k, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			panic(err)
		}
		testKey = k
	})
	return testKey
}

// writePKCS1 writes the key in the shape GitHub hands out: a PKCS#1 body under
// `BEGIN RSA PRIVATE KEY`. 0600, which is what the registration path creates
// and what `auth status` warns about the absence of.
func writePKCS1(t *testing.T, k *rsa.PrivateKey) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "acme.loop.pem")
	block := &pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)}
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatalf("writing the key: %v", err)
	}
	return path
}

// writePKCS8 writes the same key the way OpenSSL 3's `genrsa` does: a PKCS#8
// body under `BEGIN PRIVATE KEY`. `openssl dgst -sign` reads both, so a port
// that read only one would refuse a key the shell accepts — the offline suite
// generates its fixture key with exactly this command (tests/test-auth.sh:185).
func writePKCS8(t *testing.T, k *rsa.PrivateKey) string {
	t.Helper()
	der, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		t.Fatalf("marshalling PKCS#8: %v", err)
	}
	path := filepath.Join(t.TempDir(), "acme.loop.pem")
	block := &pem.Block{Type: "PRIVATE KEY", Bytes: der}
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatalf("writing the key: %v", err)
	}
	return path
}

// segments splits a token and fails the test if it is not three parts.
func segments(t *testing.T, token string) (header, payload, signature string) {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("the token has %d segments, want 3: %q", len(parts), token)
	}
	return parts[0], parts[1], parts[2]
}

func decode(t *testing.T, segment string) []byte {
	t.Helper()
	raw, err := base64.RawURLEncoding.DecodeString(segment)
	if err != nil {
		t.Fatalf("decoding %q: %v", segment, err)
	}
	return raw
}

// --- the token itself -------------------------------------------------------

// The header is a literal in the shell, not a jq expression, so these are its
// bytes exactly (lib/auth.sh:163).
func TestJWTHeaderIsTheLiteralTheShellPrints(t *testing.T) {
	token, err := app.JWT(writePKCS1(t, key(t)), 12345, time.Unix(1700000000, 0))
	if err != nil {
		t.Fatalf("JWT: %v", err)
	}
	header, _, _ := segments(t, token)
	if got := string(decode(t, header)); got != `{"alg":"RS256","typ":"JWT"}` {
		t.Fatalf("header = %s", got)
	}
}

// The claims, in the key order jq wrote them and with the offsets the shell
// chose: backdated 60 seconds because GitHub rejects a JWT whose iat is in the
// future and clock skew between here and GitHub is not ours to control, and
// good for nine more minutes. Measured against `_auth_jwt` sourced directly.
func TestJWTClaimsAreBackdatedAndExpireInNineMinutes(t *testing.T) {
	now := time.Unix(1700000000, 0)
	token, err := app.JWT(writePKCS1(t, key(t)), 12345, now)
	if err != nil {
		t.Fatalf("JWT: %v", err)
	}
	_, payload, _ := segments(t, token)
	want := `{"iat":1699999940,"exp":1700000540,"iss":12345}`
	if got := string(decode(t, payload)); got != want {
		t.Fatalf("payload = %s\nwant      %s", got, want)
	}
}

// The clock is a parameter, so the claims are a fact a caller chose. Without
// that the two offsets could only be asserted as a range.
func TestJWTTakesItsClockFromTheCaller(t *testing.T) {
	path := writePKCS1(t, key(t))
	first, err := app.JWT(path, 7, time.Unix(1000000000, 0))
	if err != nil {
		t.Fatalf("JWT: %v", err)
	}
	second, err := app.JWT(path, 7, time.Unix(2000000000, 0))
	if err != nil {
		t.Fatalf("JWT: %v", err)
	}
	if first == second {
		t.Fatal("two different instants produced the same token")
	}
	_, payload, _ := segments(t, first)
	if got := string(decode(t, payload)); got != `{"iat":999999940,"exp":1000000540,"iss":7}` {
		t.Fatalf("payload = %s", got)
	}
}

// The App id is a JSON number, not a string. The shell passes it with
// `--argjson iss`, which parses the value rather than quoting it, and GitHub
// reads iss as a number.
func TestJWTWritesTheAppIDAsANumber(t *testing.T) {
	token, err := app.JWT(writePKCS1(t, key(t)), 987654, time.Unix(1700000000, 0))
	if err != nil {
		t.Fatalf("JWT: %v", err)
	}
	_, payload, _ := segments(t, token)
	if got := string(decode(t, payload)); !strings.HasSuffix(got, `,"iss":987654}`) {
		t.Fatalf("payload = %s, want a bare number for iss", got)
	}
}

// --- base64url, without padding ---------------------------------------------
//
// `_b64url` is `openssl base64 -A | tr '+/' '-_' | tr -d '='` (lib/auth.sh:158).
// Measured on the three padding cases: `+/+/` for three bytes, `+/8=` for two,
// `+w==` for one — and the url form drops the `=` and rewrites `+` and `/`.
//
// The three cases are reached through the real token rather than through a
// helper, by choosing App ids that move the payload's length: the payload is
// `{"iat":N,"exp":N,"iss":M}` with ten-digit timestamps, so a three-digit id
// makes 45 bytes (no padding), a four-digit one 46 (two `=`) and a five-digit
// one 47 (one `=`). A port that used the standard alphabet, or kept the
// padding, fails on all three.

func TestJWTSegmentsCarryNoPaddingAndNoStandardAlphabet(t *testing.T) {
	path := writePKCS1(t, key(t))
	for _, tc := range []struct {
		appID       int64
		wantPadding int
	}{
		{123, 0},
		{1234, 2},
		{12345, 1},
	} {
		token, err := app.JWT(path, tc.appID, time.Unix(1700000000, 0))
		if err != nil {
			t.Fatalf("JWT: %v", err)
		}
		if i := strings.IndexAny(token, "=+/"); i >= 0 {
			t.Fatalf("token for App id %d carries %q at %d: %s", tc.appID, token[i], i, token)
		}
		_, payload, _ := segments(t, token)
		raw := decode(t, payload)
		// The case this row exists to reach: the standard encoding of these
		// same bytes would have carried this much padding.
		if got := strings.Count(base64.StdEncoding.EncodeToString(raw), "="); got != tc.wantPadding {
			t.Fatalf("App id %d makes a payload with %d padding characters, want %d — the row no longer reaches the case it names",
				tc.appID, got, tc.wantPadding)
		}
	}
}

// --- the signature ----------------------------------------------------------

// `openssl dgst -sha256 -sign` is RSASSA-PKCS1-v1_5 over SHA-256, which is
// deterministic: one key and one signing input give one signature. So the
// segment is asserted byte for byte against the signature computed here, not
// merely verified.
//
// The `+` and `/` the url alphabet rewrites are forced rather than hoped for:
// the instant is advanced until the signature's standard encoding carries both.
func TestJWTSignsWithRSASHA256OverTheHeaderAndPayload(t *testing.T) {
	k := key(t)
	path := writePKCS1(t, k)

	var token string
	var now time.Time
	for offset := int64(0); ; offset++ {
		if offset > 200 {
			t.Fatal("no instant in 200 produced a signature carrying both + and /")
		}
		now = time.Unix(1700000000+offset, 0)
		signed, err := app.JWT(path, 12345, now)
		if err != nil {
			t.Fatalf("JWT: %v", err)
		}
		_, _, signature := segments(t, signed)
		standard := base64.StdEncoding.EncodeToString(decode(t, signature))
		if strings.Contains(standard, "+") && strings.Contains(standard, "/") {
			token = signed
			break
		}
	}

	header, payload, signature := segments(t, token)
	signingInput := header + "." + payload
	digest := sha256.Sum256([]byte(signingInput))

	// Verified with the key's public half: a token signed with the wrong key,
	// or over the wrong bytes, fails here.
	if err := rsa.VerifyPKCS1v15(&k.PublicKey, crypto.SHA256, digest[:], decode(t, signature)); err != nil {
		t.Fatalf("the signature does not verify under the key's own public half: %v", err)
	}

	want, err := rsa.SignPKCS1v15(nil, k, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatalf("signing: %v", err)
	}
	if signature != base64.RawURLEncoding.EncodeToString(want) {
		t.Fatalf("signature = %s\nwant        %s", signature, base64.RawURLEncoding.EncodeToString(want))
	}
}

// The signature covers the two segments and the dot between them, which is what
// makes a token whose claims were edited fail at GitHub.
func TestJWTSignsTheEncodedSegmentsAndNotTheClaims(t *testing.T) {
	k := key(t)
	token, err := app.JWT(writePKCS1(t, k), 12345, time.Unix(1700000000, 0))
	if err != nil {
		t.Fatalf("JWT: %v", err)
	}
	header, payload, signature := segments(t, token)

	tampered := sha256.Sum256([]byte(header + "." + payload + "x"))
	if err := rsa.VerifyPKCS1v15(&k.PublicKey, crypto.SHA256, tampered[:], decode(t, signature)); err == nil {
		t.Fatal("the signature verified over bytes it did not cover")
	}
}

// --- the key on disk --------------------------------------------------------

// `openssl dgst -sign` reads a PKCS#8 body as readily as a PKCS#1 one, and both
// are in play: GitHub hands out PKCS#1, and OpenSSL 3's `genrsa` — which the
// offline suite uses for its fixture key — writes PKCS#8.
func TestJWTReadsBothPEMShapes(t *testing.T) {
	k := key(t)
	now := time.Unix(1700000000, 0)

	fromPKCS1, err := app.JWT(writePKCS1(t, k), 12345, now)
	if err != nil {
		t.Fatalf("PKCS#1: %v", err)
	}
	fromPKCS8, err := app.JWT(writePKCS8(t, k), 12345, now)
	if err != nil {
		t.Fatalf("PKCS#8: %v", err)
	}
	if fromPKCS1 != fromPKCS8 {
		t.Fatal("the same key in two encodings signed differently")
	}
}

// A key that cannot sign refuses, and this is the one place the port answers
// differently from the shell. `_auth_jwt` ends in a printf, so its exit status
// is the printf's: openssl failing leaves an empty signature and the function
// still returns 0. That makes the ui_die at lib/auth.sh:901-903 — the one
// reading "could not sign a token with <path>" — unreachable, and the operator
// gets the later "GitHub rejected a token" message about a token that was never
// signed. The port refuses at the point the fault happened.
func TestJWTRefusesAKeyItCannotSignWith(t *testing.T) {
	dir := t.TempDir()

	garbage := filepath.Join(dir, "garbage.pem")
	if err := os.WriteFile(garbage, []byte("not a pem file at all\n"), 0o600); err != nil {
		t.Fatalf("writing: %v", err)
	}

	notRSA := filepath.Join(dir, "notrsa.pem")
	block := &pem.Block{Type: "PRIVATE KEY", Bytes: []byte("still not a key")}
	if err := os.WriteFile(notRSA, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatalf("writing: %v", err)
	}

	for _, path := range []string{
		filepath.Join(dir, "absent.pem"),
		garbage,
		notRSA,
	} {
		token, err := app.JWT(path, 12345, time.Unix(1700000000, 0))
		if err == nil {
			t.Errorf("JWT(%s) returned %q and no error", filepath.Base(path), token)
		}
		if token != "" {
			t.Errorf("JWT(%s) returned a token beside its error: %q", filepath.Base(path), token)
		}
		if err != nil && !strings.Contains(err.Error(), path) {
			t.Errorf("the error for %s does not name the file: %v", filepath.Base(path), err)
		}
	}
}

// An error reaches a terminal and a run log, and the private key can mint a
// token for every repository the App is installed on. So the key's bytes are
// never in one, however the read failed.
func TestJWTKeepsTheKeyOutOfItsError(t *testing.T) {
	k := key(t)
	path := writePKCS1(t, k)
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading: %v", err)
	}
	// A key GitHub would accept, made unusable at the last moment by truncation,
	// so the failure happens with real key material in hand.
	broken := filepath.Join(t.TempDir(), "broken.pem")
	if err := os.WriteFile(broken, body[:len(body)/2], 0o600); err != nil {
		t.Fatalf("writing: %v", err)
	}

	if _, err := app.JWT(broken, 12345, time.Unix(1700000000, 0)); err == nil {
		t.Fatal("a truncated key signed something")
	} else {
		for _, line := range strings.Split(string(body), "\n") {
			if len(line) < 20 || strings.HasPrefix(line, "-----") {
				continue
			}
			if strings.Contains(err.Error(), line) {
				t.Fatalf("the error carries a line of the private key: %v", err)
			}
		}
	}
}

// --- the installations read -------------------------------------------------

// The argument array, read out of the stub's own log by running
// `_auth_installations` against tests/stub/gh: the header, the path, and the jq
// filter exactly as the shell writes it.
func TestInstallationsArgv(t *testing.T) {
	rec := &recorder{results: []exec.Result{out("acme selected\n")}}
	if _, err := app.NewGH(rec).Installations(context.Background(), "the-jwt"); err != nil {
		t.Fatalf("Installations: %v", err)
	}
	rec.wantArgv(t,
		"api",
		"-H", "Authorization: Bearer the-jwt",
		"/app/installations",
		"--jq", `.[] | "\(.account.login) \(.repository_selection)"`,
	)
}

// One account per line, as "<login> <selection>". The shell reads them with
// `while read -r acct sel`.
func TestInstallationsReadsAnAccountAndItsSelectionPerLine(t *testing.T) {
	rec := &recorder{results: []exec.Result{out("acme selected\nbeta all\n")}}

	got, err := app.NewGH(rec).Installations(context.Background(), "the-jwt")
	if err != nil {
		t.Fatalf("Installations: %v", err)
	}
	want := []app.Installation{
		{Account: "acme", Selection: "selected"},
		{Account: "beta", Selection: "all"},
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("installations = %v, want %v", got, want)
	}
}

// An App installed nowhere is a successful read of an empty list, not a
// failure. It is the case `auth status` reports as "installed nowhere — it can
// reach no repository at all" (lib/auth.sh:445), and telling it apart from a
// refused call is the whole reason that line can be printed.
func TestInstallationsOnAnAppInstalledNowhere(t *testing.T) {
	rec := &recorder{results: []exec.Result{out("")}}

	got, err := app.NewGH(rec).Installations(context.Background(), "the-jwt")
	if err != nil {
		t.Fatalf("an empty list was reported as a failure: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("installations = %v, want none", got)
	}
}

// jq's `\(…)` interpolation has no `// empty` here, so a response missing a
// field prints the literal `null` rather than dropping the line. Measured
// against the stub with `[{"account":{},"repository_selection":null}]`, which
// prints `null null`. The port reads it back the same way rather than inventing
// a refusal the shell does not have.
func TestInstallationsCarriesJQsNullThrough(t *testing.T) {
	rec := &recorder{results: []exec.Result{out("null null\n")}}

	got, err := app.NewGH(rec).Installations(context.Background(), "the-jwt")
	if err != nil {
		t.Fatalf("Installations: %v", err)
	}
	if len(got) != 1 || got[0].Account != "null" || got[0].Selection != "null" {
		t.Fatalf("installations = %v, want one entry reading null null", got)
	}
}

// A refused API call and a child that never ran are one answer at the shell:
// `2>/dev/null || return 1` fires for both.
func TestInstallationsRefusesWhenGhDoesNotAnswer(t *testing.T) {
	for name, result := range map[string]exec.Result{
		"gh exited non-zero":               bad(),
		"gh never produced an exit status": unresolved(),
	} {
		rec := &recorder{results: []exec.Result{result}}
		if _, err := app.NewGH(rec).Installations(context.Background(), "the-jwt"); err == nil {
			t.Errorf("%s: Installations returned no error", name)
		}
	}
}

// Whoever holds the JWT can act as the App until it expires. The argument array
// is the one place it may appear, because that is where gh needs it; an error
// string reaches a terminal and a run log.
func TestInstallationsKeepsTheJWTOutOfItsError(t *testing.T) {
	const secret = "eyJhbGciOiJSUzI1NiJ9.the-signed-token"

	for name, result := range map[string]exec.Result{
		"refused":    bad(),
		"unresolved": unresolved(),
	} {
		rec := &recorder{results: []exec.Result{result}}
		_, err := app.NewGH(rec).Installations(context.Background(), secret)
		if err == nil {
			t.Fatalf("%s: Installations returned no error", name)
		}
		if strings.Contains(err.Error(), secret) {
			t.Errorf("%s: the error carries the JWT: %v", name, err)
		}
		// And it did reach gh, in the header and nowhere else.
		spec := rec.only(t)
		var carried int
		for _, arg := range spec.Args {
			if strings.Contains(arg, secret) {
				carried++
				if arg != "Authorization: Bearer "+secret {
					t.Errorf("%s: the JWT appears in an argument that is not the header: %q", name, arg)
				}
			}
		}
		if carried != 1 {
			t.Errorf("%s: the JWT appears in %d arguments, want the header alone", name, carried)
		}
		for _, entry := range spec.Env {
			if strings.Contains(entry, secret) {
				t.Errorf("%s: the JWT was put in the environment: %q", name, entry)
			}
		}
	}
}
