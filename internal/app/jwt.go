package app

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// An App authenticates as itself with a short-lived RS256 JWT
// (lib/auth.sh:151-172). This is what lets CrossRev confirm an installation
// actually landed rather than telling you to go and check. `gh` honours an
// Authorization header we set, so this needs no HTTP client of its own.

// The two offsets the shell chose (lib/auth.sh:165-167).
//
// The backdating is not a rounding allowance: GitHub rejects a JWT whose iat is
// in the future, and clock skew between here and GitHub is not ours to control.
const (
	jwtBackdate = 60 * time.Second
	jwtLifetime = 540 * time.Second
)

// jwtHeader is a literal in the shell rather than a jq expression, so these are
// its bytes exactly (lib/auth.sh:163).
const jwtHeader = `{"alg":"RS256","typ":"JWT"}`

// JWT mints a token that proves this process holds the App's private key
// (_auth_jwt, lib/auth.sh:160).
//
// now is a parameter rather than a read of the clock, so the claims are a fact
// the caller chose and a test can assert them rather than bound them.
//
// appID is a number, because the shell passes it with `--argjson iss`, which
// parses the value rather than quoting it, and GitHub reads iss as a number.
//
// # Where this answers differently from the shell
//
// `_auth_jwt` ends in a printf, so its exit status is the printf's. A missing
// key, an unreadable one, or a non-numeric App id leaves an empty segment and
// the function still returns 0 — measured, on all three. That makes the
// `ui_die` at lib/auth.sh:901-903 unreachable, and an operator who pointed
// --key at the wrong file is told GitHub rejected the token rather than that
// nothing signed it. This refuses at the point the fault happened, which is the
// message that shell line was written to print.
func JWT(pemPath string, appID int64, now time.Time) (string, error) {
	key, err := readRSAKey(pemPath)
	if err != nil {
		return "", fmt.Errorf("could not sign a token with %s: %w", pemPath, err)
	}

	// jq -cn '{iat:$iat, exp:$exp, iss:$iss}': three numbers, in that key
	// order, compact.
	payload := `{"iat":` + strconv.FormatInt(now.Add(-jwtBackdate).Unix(), 10) +
		`,"exp":` + strconv.FormatInt(now.Add(jwtLifetime).Unix(), 10) +
		`,"iss":` + strconv.FormatInt(appID, 10) + `}`

	signingInput := b64url([]byte(jwtHeader)) + "." + b64url([]byte(payload))

	digest := sha256.Sum256([]byte(signingInput))
	// `openssl dgst -sha256 -sign <pem>` is RSASSA-PKCS1-v1_5 over SHA-256. The
	// scheme is deterministic, so one key and one signing input give one
	// signature; nothing here needs a random source.
	signature, err := rsa.SignPKCS1v15(nil, key, crypto.SHA256, digest[:])
	if err != nil {
		return "", fmt.Errorf("could not sign a token with %s: %w", pemPath, err)
	}
	return signingInput + "." + b64url(signature), nil
}

// b64url is `openssl base64 -A | tr '+/' '-_' | tr -d '='` (lib/auth.sh:158):
// standard base64 on one line, the two alphabet substitutions, and the padding
// removed. Measured against the shell on three-, two- and one-byte inputs whose
// standard forms are `+/+/`, `+/8=` and `+w==`; the url forms are `-_-_`, `-_8`
// and `-w`. That is base64.RawURLEncoding exactly.
func b64url(raw []byte) string { return base64.RawURLEncoding.EncodeToString(raw) }

// readRSAKey reads the private key the way `openssl dgst -sign` reads it.
//
// Both PEM shapes are in play and openssl takes either: GitHub hands out a
// PKCS#1 body under `BEGIN RSA PRIVATE KEY`, and OpenSSL 3's `genrsa` — which
// is what the offline suite generates its fixture key with — writes a PKCS#8
// body under `BEGIN PRIVATE KEY`.
//
// No error here carries the file's contents. The private key belongs to the
// App, so whoever holds it can mint a token for every installation of that App,
// and an error string reaches a terminal and a run log.
func readRSAKey(path string) (*rsa.PrivateKey, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		// os.ReadFile's error is the path and the syscall, never the contents.
		return nil, err
	}
	block, _ := pem.Decode(body)
	if block == nil {
		return nil, fmt.Errorf("it is not a PEM file")
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		// The parse error names structure, not key material.
		return nil, fmt.Errorf("it is not an RSA private key: %w", err)
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("it holds a %T, and an App key is RSA", parsed)
	}
	return key, nil
}

// Installation is one account this App is installed on.
type Installation struct {
	// Account is the login the App is installed under.
	Account string
	// Selection is GitHub's repository_selection: `all` or `selected`.
	Selection string
}

// Installations lists the accounts this App is installed on
// (_auth_installations, lib/auth.sh:174).
//
// An App installed nowhere answers with an empty list and no error, and telling
// that apart from a refused call is the point: it is what lets `auth status`
// print "installed nowhere — it can reach no repository at all"
// (lib/auth.sh:445) rather than staying quiet.
//
// The jq filter has no `// empty`, so a response missing a field interpolates
// the literal `null` and the line survives. Measured against the stub, which
// prints `null null` for an entry with neither. Nothing is refused on that
// here, because the shell refuses nothing on it either.
func (g *GH) Installations(ctx context.Context, jwt string) ([]Installation, error) {
	const summary = "could not list the accounts this App is installed on"

	res := g.run(ctx, "api",
		"-H", "Authorization: Bearer "+jwt,
		"/app/installations",
		"--jq", `.[] | "\(.account.login) \(.repository_selection)"`)
	if !answered(res) {
		// ghFailure carries neither the arguments nor the streams, which is a
		// requirement rather than a convention here: the JWT is in an argument,
		// and whoever holds one can act as the App until it expires.
		return nil, ghFailure(summary, res)
	}

	text := output(res)
	if text == "" {
		return nil, nil
	}
	var installs []Installation
	for _, line := range strings.Split(text, "\n") {
		// `read -r acct sel`: the login up to the first space, the rest after
		// it. Neither field can hold a space — one is a GitHub login, the other
		// is `all` or `selected`.
		account, selection, _ := strings.Cut(line, " ")
		installs = append(installs, Installation{Account: account, Selection: selection})
	}
	return installs, nil
}
