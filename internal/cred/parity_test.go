package cred_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/carlosboeing/crossrev/internal/cred"
)

// parityFixtureDir is the single package-relative route to the frozen Bash
// oracle. Go reads that file and never writes it, and no test here invokes the
// capture script: a Go run that could recapture would freeze Go's answer rather
// than Bash's.
const parityFixtureDir = "../../tests/fixtures/parity"

// credentialsOracle is the one oracle file this package answers for. It freezes
// cred_env_strip_for, cred_jwt_claims, _cred_human_duration and
// CRED_MIN_SECONDS, and tests/test-parity.sh:384-407 holds the Bash side of the
// same contract.
const credentialsOracle = "credentials.json"

type credentialsFixture struct {
	Captured struct {
		Platform         string `json:"platform"`
		TrImplementation string `json:"tr_implementation"`
		Locale           string `json:"locale"`
	} `json:"captured"`
	Function       string `json:"function"`
	CredMinSeconds int64  `json:"cred_min_seconds"`
	StripSets      []struct {
		Harness string   `json:"harness"`
		Strip   []string `json:"strip"`
	} `json:"strip_sets"`
	JWTCases []struct {
		Name   string `json:"name"`
		JWT    string `json:"jwt"`
		Claims string `json:"claims"`
		RC     int    `json:"rc"`
	} `json:"jwt_cases"`
	DurationCases []struct {
		Seconds int64  `json:"seconds"`
		Human   string `json:"human"`
	} `json:"duration_cases"`
}

func oracle(t *testing.T) credentialsFixture {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(parityFixtureDir, credentialsOracle))
	if err != nil {
		t.Fatalf("reading the %s oracle: %v", credentialsOracle, err)
	}
	var fixture credentialsFixture
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatalf("decoding the %s oracle: %v", credentialsOracle, err)
	}
	return fixture
}

// An oracle with no provenance cannot be re-derived, and a divergence found
// against it could not be attributed.
func TestParityFixtureCarriesProvenance(t *testing.T) {
	fixture := oracle(t)
	if fixture.Captured.Platform == "" || fixture.Captured.TrImplementation == "" || fixture.Captured.Locale == "" {
		t.Errorf("incomplete provenance: %+v", fixture.Captured)
	}
	if fixture.Function == "" {
		t.Error("the oracle names no Bash function")
	}
}

// The floor is frozen, not chosen here.
func TestMinSecondsMatchesTheOracle(t *testing.T) {
	if want := oracle(t).CredMinSeconds; cred.MinSeconds != want {
		t.Errorf("MinSeconds = %d, want %d", cred.MinSeconds, want)
	}
}

// Every strip set the oracle recorded, including the unknown harness the
// capture deliberately included.
func TestVendorStripSetsMatchTheOracle(t *testing.T) {
	doc := descriptors(t)
	fixture := oracle(t)
	if len(fixture.StripSets) == 0 {
		t.Fatal("the oracle records no strip sets, so this proves nothing")
	}
	for _, want := range fixture.StripSets {
		got := cred.VendorStripFor(doc, want.Harness)
		// The oracle is sorted, and so is the answer: jq's `unique` sorts and
		// `$all - $keep` keeps the left operand's order.
		if !slices.Equal(got, want.Strip) {
			t.Errorf("strip set for %s = %v, want %v", want.Harness, got, want.Strip)
		}
	}
}

// Every duration boundary the oracle recorded.
func TestHumanDurationMatchesTheOracle(t *testing.T) {
	fixture := oracle(t)
	if len(fixture.DurationCases) == 0 {
		t.Fatal("the oracle records no durations, so this proves nothing")
	}
	for _, want := range fixture.DurationCases {
		if got := cred.HumanDuration(want.Seconds); got != want.Human {
			t.Errorf("HumanDuration(%d) = %q, want %q", want.Seconds, got, want.Human)
		}
	}
}

// jwtDivergences are the oracle cases where ParseClaims deliberately answers
// differently from cred_jwt_claims, and what it answers instead.
//
// Both are the same fault: openssl on the capture platform ignores characters
// outside the base64 alphabet rather than failing, so the shell decodes nothing,
// hands jq empty input, and reports SUCCESS with an empty claim set. jwt.go
// carries the measurement showing that every caller of the shell function turns
// that empty answer into the same refusal one step later, so the divergence is
// in this function and in nothing above it.
var jwtDivergences = map[string]string{
	"payload-is-not-base64url": "a payload of characters outside the alphabet",
	"empty-payload-segment":    "an empty payload segment",
}

// Every JWT case the oracle recorded, with the two divergences named rather
// than skipped: a case that stopped diverging would fail here too, which is
// what stops this list outliving its reason.
func TestJWTClaimsMatchTheOracle(t *testing.T) {
	fixture := oracle(t)
	if len(fixture.JWTCases) == 0 {
		t.Fatal("the oracle records no JWT cases, so this proves nothing")
	}

	diverged := map[string]bool{}
	for _, want := range fixture.JWTCases {
		t.Run(want.Name, func(t *testing.T) {
			got, err := cred.ParseClaims(want.JWT)

			if reason, expected := jwtDivergences[want.Name]; expected {
				diverged[want.Name] = true
				if err == nil {
					t.Errorf("%s parsed; Go refuses %s", want.Name, reason)
				}
				if want.RC != 0 || want.Claims != "" {
					t.Errorf("%s is no longer the shell's empty success (rc %d, claims %q), so this divergence needs rereading",
						want.Name, want.RC, want.Claims)
				}
				return
			}

			if want.RC != 0 {
				if err == nil {
					t.Errorf("ParseClaims(%q) succeeded; the shell returned %d", want.JWT, want.RC)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseClaims(%q) = %v; the shell returned 0 with %s", want.JWT, err, want.Claims)
			}

			// The oracle records the whole claim set as jq printed it. Claims
			// keeps three fields, so the comparison is against those three read
			// back out of the recorded text rather than against a re-encoding
			// of it.
			var recorded struct {
				Exp      *int64 `json:"exp"`
				Iss      string `json:"iss"`
				ClientID string `json:"client_id"`
			}
			if err := json.Unmarshal([]byte(want.Claims), &recorded); err != nil {
				t.Fatalf("the oracle's claims %q are not JSON: %v", want.Claims, err)
			}
			if recorded.Exp == nil {
				if got.HasExpiry {
					t.Errorf("expiry = %d, want none", got.Expiry)
				}
			} else if !got.HasExpiry || got.Expiry != *recorded.Exp {
				t.Errorf("expiry = %d (present %t), want %d", got.Expiry, got.HasExpiry, *recorded.Exp)
			}
			if got.Issuer != recorded.Iss {
				t.Errorf("issuer = %q, want %q", got.Issuer, recorded.Iss)
			}
			if got.ClientID != recorded.ClientID {
				t.Errorf("client id = %q, want %q", got.ClientID, recorded.ClientID)
			}
		})
	}

	// A divergence named for a case the oracle no longer carries is a stale
	// exemption, and a stale exemption is how a real regression hides.
	for name := range jwtDivergences {
		if !diverged[name] {
			t.Errorf("the oracle no longer carries the case %q, so its divergence is stale", name)
		}
	}
}
