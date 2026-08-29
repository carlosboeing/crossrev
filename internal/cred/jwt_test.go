package cred_test

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/cred"
)

// jwt builds an unsigned token carrying the given payload. Unsigned is the
// honest fixture: CrossRev reads the claims for expiry, issuer and client id
// and never treats them as an authorisation decision
// (tests/test-credentials.sh:36-39).
func jwt(payload string) string {
	return "eyJhbGciOiJub25lIn0." + base64.RawURLEncoding.EncodeToString([]byte(payload)) + ".signature"
}

func TestParseClaimsReadsTheThreeFieldsCrossRevUses(t *testing.T) {
	got, err := cred.ParseClaims(jwt(`{"exp":1893456000,"iss":"https://auth.example.com","client_id":"app_test"}`))
	if err != nil {
		t.Fatalf("ParseClaims: %v", err)
	}
	if !got.HasExpiry || got.Expiry != 1893456000 {
		t.Errorf("expiry = %d (present %t), want 1893456000", got.Expiry, got.HasExpiry)
	}
	if got.Issuer != "https://auth.example.com" {
		t.Errorf("issuer = %q", got.Issuer)
	}
	if got.ClientID != "app_test" {
		t.Errorf("client id = %q", got.ClientID)
	}
}

// A payload whose length is not a multiple of four still has to decode: the
// padding is stripped by the encoder and putting it back is the reader's job.
// Getting this wrong fails on some tokens and not others, which is the worst
// kind (tests/test-credentials.sh:64-74).
func TestAPayloadOfAnyLengthDecodes(t *testing.T) {
	for pad := 1; pad <= 5; pad++ {
		payload := `{"exp":1,"pad":"` + strings.Repeat("x", pad) + `"}`
		got, err := cred.ParseClaims(jwt(payload))
		if err != nil {
			t.Fatalf("pad %d: ParseClaims: %v", pad, err)
		}
		if got.Expiry != 1 {
			t.Errorf("pad %d: expiry = %d, want 1", pad, got.Expiry)
		}
	}
}

// A segment that arrived with its padding intact decodes too. The shell adds
// padding to a multiple of four and openssl accepts what is already there;
// RawURLEncoding needs it removed first.
func TestAPaddedSegmentDecodes(t *testing.T) {
	padded := base64.URLEncoding.EncodeToString([]byte(`{"exp":77}`))
	if !strings.HasSuffix(padded, "=") {
		t.Fatalf("the fixture %q carries no padding, so it proves nothing", padded)
	}
	got, err := cred.ParseClaims("header." + padded + ".signature")
	if err != nil {
		t.Fatalf("ParseClaims: %v", err)
	}
	if got.Expiry != 77 {
		t.Errorf("expiry = %d, want 77", got.Expiry)
	}
}

// Everything after the second dot is ignored, which is what the shell's suffix
// trims do (lib/credentials.sh:61).
func TestASegmentPastTheSignatureIsIgnored(t *testing.T) {
	got, err := cred.ParseClaims(jwt(`{"exp":3}`) + ".extra")
	if err != nil {
		t.Fatalf("ParseClaims: %v", err)
	}
	if got.Expiry != 3 {
		t.Errorf("expiry = %d, want 3", got.Expiry)
	}
}

// An absent expiry is not a zero one. `[[ -n "$exp" ]] || return 1` at
// lib/credentials.sh:81 tells them apart, and so does HasExpiry.
func TestAClaimSetWithNoExpiryParsesAndSaysSo(t *testing.T) {
	got, err := cred.ParseClaims(jwt(`{"iss":"https://auth.example.com"}`))
	if err != nil {
		t.Fatalf("ParseClaims: %v", err)
	}
	if got.HasExpiry {
		t.Error("a claim set with no exp reports one")
	}
	if got.Issuer != "https://auth.example.com" {
		t.Errorf("issuer = %q", got.Issuer)
	}
}

// An expiry bash arithmetic cannot read is not an expiry. The shell puts `.exp`
// straight into `$(( exp - now ))` after `jq -r`, so what counts is what bash
// accepts there. Measured on a codex credential carrying each value, all four
// below made cred_seconds_left return 1.
func TestAnExpiryBashArithmeticRefusesIsNotAnExpiry(t *testing.T) {
	for _, payload := range []string{
		`{"exp":1.5}`,
		`{"exp":null}`,
		`{"exp":[1]}`,
		`{"exp":true}`,
	} {
		got, err := cred.ParseClaims(jwt(payload))
		if err != nil {
			t.Fatalf("%s: ParseClaims: %v", payload, err)
		}
		if got.HasExpiry {
			t.Errorf("%s: reports an expiry of %d", payload, got.Expiry)
		}
	}
}

// A string holding digits IS an expiry, because `jq -r` strips the quotes
// before bash sees it. Measured: {"exp":"1893456000"} made cred_seconds_left
// return 0 with the same number of seconds as {"exp":1893456000}.
func TestAnExpiryHeldAsAStringIsStillAnExpiry(t *testing.T) {
	got, err := cred.ParseClaims(jwt(`{"exp":"1893456000"}`))
	if err != nil {
		t.Fatalf("ParseClaims: %v", err)
	}
	if !got.HasExpiry || got.Expiry != 1893456000 {
		t.Errorf("expiry = %d (present %t), want 1893456000", got.Expiry, got.HasExpiry)
	}
}

// A claim that has to be a string and is not reads as absent, which is
// `jq -r '.iss // empty'`. An issuer that came back as a number would be
// interpolated into a discovery URL.
func TestANonStringClaimReadsAsAbsent(t *testing.T) {
	got, err := cred.ParseClaims(jwt(`{"iss":42,"client_id":{"a":1}}`))
	if err != nil {
		t.Fatalf("ParseClaims: %v", err)
	}
	if got.Issuer != "" || got.ClientID != "" {
		t.Errorf("issuer = %q, client id = %q, want both empty", got.Issuer, got.ClientID)
	}
}

func TestParseClaimsRefusesAValueThatIsNotAToken(t *testing.T) {
	for _, tc := range []struct {
		name  string
		token string
	}{
		{"empty", ""},
		{"no dots", "abcdef"},
		{"one dot", "abc.def"},
		{"an empty payload segment", "aaa..ccc"},
		{"a payload that is not base64url", "aaa.!!!!.ccc"},
		{"a payload that decodes to something that is not JSON", "aaa.bm90IGpzb24.ccc"},
		{"a payload that decodes to a bare number", "aaa." + base64.RawURLEncoding.EncodeToString([]byte("42")) + ".ccc"},
		{"two documents in one payload", "a.eyJhIjoxfQeyJhIjoxfQ.b"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := cred.ParseClaims(tc.token); !errors.Is(err, cred.ErrMalformedToken) {
				t.Errorf("ParseClaims(%q) error = %v, want ErrMalformedToken", tc.token, err)
			}
		})
	}
}

// Nothing a refusal prints may carry the token, its payload, or a claim.
//
// encoding/base64's own error names the offending byte and its offset, and a
// byte of a credential is still a byte of a credential — which is why
// decodeSegment drops it rather than wrapping it.
func TestARefusalNeverQuotesTheToken(t *testing.T) {
	secret := "s3cr3tCLAIMVALUE"
	for _, tc := range []struct {
		name    string
		payload string
	}{
		{"trailing content after the object", `{"iss":"` + secret + `"} trailing`},
		{"not JSON at all", `iss=` + secret},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// The control: a leak would have something to leak.
			if !strings.Contains(tc.payload, secret) {
				t.Fatalf("the fixture payload holds no secret, so this proves nothing")
			}
			encoded := base64.RawURLEncoding.EncodeToString([]byte(tc.payload))
			token := "header." + encoded + ".signature"

			_, err := cred.ParseClaims(token)
			if err == nil {
				t.Fatal("the payload parsed")
			}
			for _, forbidden := range []string{secret, tc.payload, encoded, token} {
				if strings.Contains(err.Error(), forbidden) {
					t.Errorf("the refusal quotes the credential: %q", err.Error())
				}
			}
		})
	}
}
