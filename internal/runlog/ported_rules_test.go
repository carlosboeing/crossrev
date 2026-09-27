package runlog_test

import (
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/runlog"
)

// fragments constructs every fixture at runtime so no complete fake credential
// appears as a literal in the source. Positives follow the token expressions in
// gitleaks v8.30.1's cmd/generate/config/rules files. Negatives copy those
// generators' false positives where present; the others use the maintainer's
// approved short or invalid-alphabet near misses.
func fragments(parts ...string) string { return strings.Join(parts, "") }

// The varied alphabets below build positive bodies that clear the ported
// upstream entropy thresholds. Each holds distinct characters only, so a body
// cycled from one carries close to log2 of its length in bits.
const (
	alphaWord     = "aB1cD2eF3gH4iJ5kL6mN7oP8qR9sT0uVwXyZ"
	alphaHex      = "0123456789abcdef"
	alphaHexCI    = "0123456789abcdefABCDEF"
	alphaLower    = "abcdefghijklmnopqrstuvwxyz"
	alphaNumLower = "abcdefghijklmnopqrstuvwxyz0123456789"
	alphaBase64   = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789+/"
	alphaFlw      = "abcdefgh0123456789"
	alphaUpperNum = "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
)

// varied returns the first n characters of the alphabet repeated endlessly.
func varied(alphabet string, n int) string {
	var b strings.Builder
	for b.Len() < n {
		b.WriteString(alphabet)
	}
	return b.String()[:n]
}

func TestPortedCredentialRules(t *testing.T) {
	cases := []struct{ id, positive, negative string }{
		{"1password-service-account-token", fragments("ops_eyJ", varied(alphaBase64, 250)), fragments("ops_eyJ", "zaWduSW5B..[Redacted]")},
		{"adobe-client-secret", fragments("p8e-", varied(alphaWord, 32)), fragments("p8e-", "abc")},
		{"age-secret-key", fragments("AGE-SECRET-KEY-1", strings.Repeat("Q", 58)), fragments("AGE-SECRET-KEY-1", strings.Repeat("Q", 57))},
		{"alibaba-access-key-id", fragments("LTAI", varied(alphaWord, 20)), fragments("LTAI", "abc")},
		{"artifactory-api-key", fragments("AKCp", varied(alphaWord, 69)), fragments("AkCp", strings.Repeat("a0", 34), "a")},
		{"artifactory-reference-token", fragments("cmVmd", varied(alphaWord, 59)), fragments("cmVMd", strings.Repeat("a0", 29), "a")},
		{"authress-service-client-access-key", fragments("sc_", varied(alphaWord, 8), ".", "ab12", ".acc_", varied(alphaWord, 12), ".", varied(alphaNumLower, 30)), fragments("sc_", "abc", ".acc_", "short")},
		{"aws-amazon-bedrock-api-key-long-lived", fragments("ABSK", varied(alphaBase64, 112)), fragments("ABSK", "QmVkcm9ja0FQSUtleS1EXAMPLE")},
		{"clojars-api-token", fragments("CLOJARS_", varied(alphaNumLower, 60)), fragments("CLOJARS_", "abc")},
		{"databricks-api-token", fragments("dapi", varied(alphaHex, 32)), fragments("DATABRICKS_TOKEN=dapi", "123456789012345678a9bc01234defg5")},
		{"digitalocean-access-token", fragments("doo_v1_", varied(alphaHex, 64)), fragments("doo_v1_", "abc")},
		{"digitalocean-pat", fragments("dop_v1_", varied(alphaHex, 64)), fragments("dop_v1_", "abc")},
		{"digitalocean-refresh-token", fragments("dor_v1_", strings.Repeat("a0", 32)), fragments("dor_v1_", "abc")},
		{"doppler-api-token", fragments("dp.pt.", varied(alphaWord, 43)), fragments("dp.pt.", "abc")},
		{"duffel-api-token", fragments("duffel_test_", varied(alphaWord, 43)), fragments("duffel_test_", "abc")},
		{"dynatrace-api-token", fragments("dt0c01.", varied(alphaNumLower, 24), ".", varied(alphaNumLower, 64)), fragments("dt0c01.", "abc")},
		{"easypost-api-token", fragments("EZAK", varied(alphaWord, 54)), fragments("...6wqX6fNUXA/rYqRvfQ+", "EZAK", "GqQRiRyqAFRQshGPWOIAwNWGORfKHSBnVNFtVmWYoW6PH23lkqbbDWep95C/3VmWq/edti6...")},
		{"easypost-test-api-token", fragments("EZTK", varied(alphaWord, 54)), fragments("...6wqX6fNUXA/rYqRvfQ+", "EZTK", "GqQRiRyqAFRQshGPWOIAwNWGORfKHSBnVNFtVmWYoW6PH23lkqbbDWep95C/3VmWq/edti6...")},
		{"facebook-page-access-token", fragments("EAAM", varied(alphaNumLower, 100)), fragments("eaaaC0b75a9329fded2ffa9a02b47e0117831b82")},
		{"flutterwave-encryption-key", fragments("FLWSECK_TEST-", varied(alphaFlw, 12)), fragments("FLWSECK_TEST-", "abc")},
		{"flutterwave-secret-key", fragments("FLWSECK_TEST-", varied(alphaFlw, 32), "-X"), fragments("FLWSECK_TEST-", "abc", "-X")},
		{"flyio-access-token", fragments("fo1_", varied(alphaWord, 43)), fragments("fo1_", strings.Repeat("a0", 21))},
		{"frameio-api-token", fragments("fio-u-", strings.Repeat("a0", 32)), fragments("fio-u-", "abc")},
		{"gitlab-cicd-job-token", fragments("glcbt-", "a1b2c", "_", varied(alphaWord, 20)), fragments("glcbt-", "abc")},
		{"gitlab-deploy-token", fragments("gldt-", varied(alphaWord, 20)), fragments("gldt-", "abc")},
		{"gitlab-feature-flag-client-token", fragments("glffct-", varied(alphaWord, 20)), fragments("glffct-", "abc")},
		{"gitlab-feed-token", fragments("glft-", varied(alphaWord, 20)), fragments("glft-", "abc")},
		{"gitlab-incoming-mail-token", fragments("glimt-", varied(alphaWord, 25)), fragments("glimt-", "abc")},
		{"gitlab-kubernetes-agent-token", fragments("glagent-", varied(alphaWord, 50)), fragments("glagent-", "abc")},
		{"gitlab-oauth-app-secret", fragments("gloas-", varied(alphaWord, 64)), fragments("gloas-", "abc")},
		{"gitlab-ptt", fragments("glptt-", varied(alphaHex, 40)), fragments("glptt-", strings.Repeat("x", 40))},
		{"gitlab-rrt", fragments("GR1348941", varied(alphaWord, 20)), fragments("GR1348941", strings.Repeat("a0", 9), "a")},
		{"gitlab-runner-authentication-token", fragments("glrt-", varied(alphaWord, 20)), fragments("glrt-", "abc")},
		{"gitlab-runner-authentication-token-routable", fragments("glrt-t1_", varied(alphaWord, 27), ".", "ab", varied("a1b2c3d4e5", 7)), fragments("glrt-tx_", strings.Repeat("x", 27), ".xxxxxxxxx")},
		{"gitlab-scim-token", fragments("glsoat-", varied(alphaWord, 20)), fragments("glsoat-", "abc")},
		{"grafana-service-account-token", fragments("glsa_", varied(alphaWord, 32), "_", varied(alphaHex, 8)), fragments("glsa_", strings.Repeat("a0", 15), "a", "_", strings.Repeat("a0", 4))},
		{"heroku-api-key-v2", fragments("HRKU-AA", varied(alphaWord, 58)), fragments("HRKU-AA", "abc")},
		{"huggingface-organization-api-token", fragments("api_org_", varied(alphaLower, 34)), "const api_org_controller = require('api')"},
		{"infracost-api-token", fragments("ico-", varied(alphaWord, 32)), fragments("ico-", "abc")},
		{"intra42-client-secret", fragments("s-s4t2ud-", varied(alphaHexCI, 64)), fragments("s-s4t2ud-", "abc")},
		{"linear-api-key", fragments("lin_api_", varied(alphaWord, 40)), fragments("lin_api_", "abc")},
		{"notion-api-token", fragments("ntn_", "12345678901", varied(alphaWord, 35)), fragments("ntn_", "12345678901")},
		{"npm-access-token", fragments("npm_", varied(alphaWord, 36)), fragments("npm_", strings.Repeat("a0", 17))},
		{"octopus-deploy-api-key", fragments("API-", varied(alphaUpperNum, 26)), fragments("msgstr \"GSSAPI-VIRHEKAPSELOINTIMERKKIJONO.\"")},
		{"openshift-user-token", fragments("sha256~", varied(alphaWord, 43)), fragments("sha256~", strings.Repeat("a0", 21))},
		{"perplexity-api-key", fragments("pplx-", varied(alphaWord, 48)), fragments("pplx-", strings.Repeat("a0", 23), "a")},
		{"planetscale-api-token", fragments("pscale_tkn_", varied(alphaWord, 32)), fragments("pscale_tkn_", "abc")},
		{"planetscale-oauth-token", fragments("pscale_oauth_", varied(alphaWord, 32)), fragments("pscale_oauth_", "abc")},
		{"planetscale-password", fragments("pscale_pw_", varied(alphaWord, 32)), fragments("pscale_pw_", "abc")},
		{"postman-api-token", fragments("PMAK-", varied(alphaHex, 24), "-", varied(alphaHex, 34)), fragments("PMAK-", "abc")},
		{"prefect-api-token", fragments("pnu_", varied(alphaWord, 36)), fragments("pnu_", strings.Repeat("a0", 17), "a")},
		{"pulumi-api-token", fragments("pul-", varied(alphaHex, 40)), "<img src=\"./assets/vipul-f0eb1acf0da84c06a50c5b2c59932001997786b176dec02bd16\">"},
		{"pypi-upload-token", fragments("pypi-AgEIcHlwaS5vcmc", varied(alphaWord, 64)), fragments("pypi-AgEIcHlwaS5vcmc", "abc")},
		{"readme-api-token", fragments("rdme_", varied(alphaNumLower, 70)), fragments("rdme_", strings.Repeat("X", 70))},
		{"rubygems-api-token", fragments("rubygems_", varied(alphaHex, 48)), fragments("rubygems_", "abc")},
		{"scalingo-api-token", fragments("tk-us-", varied(alphaWord, 48)), fragments("tk-us-", strings.Repeat("a0", 23), "a")},
		{"sendgrid-api-token", fragments("SG.", varied(alphaNumLower, 66)), fragments("SG.", "abc")},
		{"sendinblue-api-token", fragments("xkeysib-", varied(alphaHex, 64), "-", varied(alphaWord, 16)), fragments("xkeysib-", "abc")},
		{"sentry-org-token", fragments("sntrys_eyJpYXQiO", varied(alphaBase64, 12), "LCJyZWdpb25fdXJs", varied(alphaBase64, 12), "_", varied(alphaBase64, 43)), fragments("sntrys_", strings.Repeat("a0", 45), "_", strings.Repeat("a0", 21), "a")},
		{"sentry-user-token", fragments("sntryu_", varied(alphaHex, 64)), fragments("sntryu_", strings.Repeat("a", 63))},
		{"settlemint-application-access-token", fragments("sm_aat_", varied(alphaWord, 16)), fragments("sm_aat_", strings.Repeat("a0", 5))},
		{"settlemint-personal-access-token", fragments("sm_pat_", varied(alphaWord, 16)), fragments("sm_pat_", strings.Repeat("a0", 5))},
		{"settlemint-service-access-token", fragments("sm_sat_", varied(alphaWord, 16)), fragments("sm_sat_", strings.Repeat("a0", 5))},
		{"shippo-api-token", fragments("shippo_live_", varied(alphaHex, 40)), fragments("shippo_live_", "abc")},
		{"shopify-access-token", fragments("shpat_", varied(alphaHex, 32)), fragments("shpat_", "abc")},
		{"shopify-custom-access-token", fragments("shpca_", varied(alphaHex, 32)), fragments("shpca_", "abc")},
		{"shopify-private-app-access-token", fragments("shppa_", varied(alphaHex, 32)), fragments("shppa_", "abc")},
		{"shopify-shared-secret", fragments("shpss_", varied(alphaHex, 32)), fragments("shpss_", "abc")},
		{"square-access-token", fragments("sq0atp-", varied(alphaWord, 22)), fragments("aws-cli@sha256:", "eaaa7b11777babe28e6133a8b19ff71cea687e0d7f05158dee95a71f76ce3d00")},
		{"stripe-access-token", fragments("sk_live_", varied(alphaWord, 24)), fragments("task_test_", strings.Repeat("a0", 15))},
		{"vault-batch-token", fragments("hvb.", varied(alphaWord, 138)), fragments("hvb.", "abc")},
	}
	if len(cases) != 71 {
		t.Fatalf("test cases = %d, want 71", len(cases))
	}
	var noLog *runlog.Log
	for _, c := range cases {
		t.Run(c.id, func(t *testing.T) {
			masked := runlog.Redact(c.positive)
			if !strings.HasSuffix(masked, "…[redacted]") || strings.Contains(masked, c.positive) || !strings.HasPrefix(masked, c.positive[:4]) {
				t.Errorf("positive Redact = %q", masked)
			}
			if got := runlog.Redact(masked); got != masked {
				t.Errorf("second Redact = %q, want %q", got, masked)
			}
			published, err := noLog.Publish(c.positive)
			if err != nil || strings.Count(published, runlog.RedactNotice) != 1 {
				t.Errorf("first Publish = %q, %v", published, err)
			}
			if got, err := noLog.Publish(published); err != nil || got != published {
				t.Errorf("second Publish = %q, %v, want %q", got, err, published)
			}
			if got := runlog.Redact(c.negative); got != c.negative {
				t.Errorf("negative Redact = %q, want unchanged", got)
			}
			if got, err := noLog.Publish(c.negative); err != nil || got != c.negative {
				t.Errorf("negative Publish = %q, %v, want unchanged", got, err)
			}
		})
	}
}

func TestRedactLeavesOrdinaryTextAlone(t *testing.T) {
	ordinary := []string{
		"Authorization is required before this request can continue.",
		"The token and key names are examples in this sentence.",
		"func parseAuthorizationToken(apiKey string) string { return apiKey }",
		"sha1: " + strings.Repeat("a1", 20),
		"sha256: " + strings.Repeat("b2", 32),
		"uuid: 550e8400-e29b-41d4-a716-446655440000",
		"base64: aGVsbG8gd29ybGQ=",
	}
	var noLog *runlog.Log
	for _, line := range ordinary {
		if got := runlog.Redact(line); got != line {
			t.Errorf("Redact(%q) = %q", line, got)
		}
		if got, err := noLog.Publish(line); err != nil || got != line {
			t.Errorf("Publish(%q) = %q, %v", line, got, err)
		}
	}
}

// TestPortedLowEntropyLookalikesStayPlain covers every ported rule that carries
// an upstream entropy threshold: each value matches the rule's bare expression
// but falls at or below the threshold, so it passes through unmasked and
// unnoted. flutterwave-encryption-key has no entry: its twelve-character body
// cannot dilute its thirteen-character varied prefix below the threshold of 2
// — even twelve E characters measure 2.35 bits — so its gate never fires and
// every bare match masks, exactly as in gitleaks.
func TestPortedLowEntropyLookalikesStayPlain(t *testing.T) {
	cases := []struct{ id, value string }{
		{"1password-service-account-token", fragments("ops_eyJ", strings.Repeat("x", 330))},
		{"adobe-client-secret", fragments("p8e-", strings.Repeat("a", 32))},
		{"alibaba-access-key-id", fragments("LTAI", strings.Repeat("a", 20))},
		{"artifactory-api-key", fragments("AKCp", strings.Repeat("X", 69))},
		{"artifactory-reference-token", fragments("cmVmd", strings.Repeat("X", 59))},
		{"authress-service-client-access-key", fragments("sc_", strings.Repeat("a", 30), ".", "ab12", ".acc_", strings.Repeat("b", 32), ".", strings.Repeat("c", 120))},
		{"aws-amazon-bedrock-api-key-long-lived", fragments("ABSK", strings.Repeat("A", 109))},
		{"clojars-api-token", fragments("CLOJARS_", strings.Repeat("a", 60))},
		{"databricks-api-token", fragments("dapi", strings.Repeat("a", 32))},
		{"digitalocean-access-token", fragments("doo_v1_", strings.Repeat("a", 64))},
		{"digitalocean-pat", fragments("dop_v1_", strings.Repeat("a", 64))},
		{"doppler-api-token", fragments("dp.pt.", strings.Repeat("a", 43))},
		{"duffel-api-token", fragments("duffel_test_", strings.Repeat("a", 43))},
		{"dynatrace-api-token", fragments("dt0c01.", strings.Repeat("a", 24), ".", strings.Repeat("b", 64))},
		{"easypost-api-token", fragments("EZAK", strings.Repeat("a", 54))},
		{"easypost-test-api-token", fragments("EZTK", strings.Repeat("a", 54))},
		{"facebook-page-access-token", fragments("EAAM", strings.Repeat("a", 100))},
		{"flutterwave-secret-key", fragments("FLWSECK_TEST-", strings.Repeat("E", 32), "-X")},
		{"flyio-access-token", fragments("fo1_", strings.Repeat("x", 43))},
		{"gitlab-cicd-job-token", fragments("glcbt-", strings.Repeat("a", 5), "_", strings.Repeat("a", 20))},
		{"gitlab-deploy-token", fragments("gldt-", strings.Repeat("a", 20))},
		{"gitlab-feature-flag-client-token", fragments("glffct-", strings.Repeat("a", 20))},
		{"gitlab-feed-token", fragments("glft-", strings.Repeat("a", 20))},
		{"gitlab-incoming-mail-token", fragments("glimt-", strings.Repeat("a", 25))},
		{"gitlab-kubernetes-agent-token", fragments("glagent-", strings.Repeat("a", 50))},
		{"gitlab-oauth-app-secret", fragments("gloas-", strings.Repeat("a", 64))},
		{"gitlab-ptt", fragments("glptt-", strings.Repeat("a", 40))},
		{"gitlab-rrt", fragments("GR1348941", strings.Repeat("X", 20))},
		{"gitlab-runner-authentication-token", fragments("glrt-", strings.Repeat("a", 20))},
		{"gitlab-runner-authentication-token-routable", fragments("glrt-t1_", strings.Repeat("a", 27), ".ab", strings.Repeat("a", 7))},
		{"gitlab-scim-token", fragments("glsoat-", strings.Repeat("a", 20))},
		{"grafana-service-account-token", fragments("glsa_", strings.Repeat("X", 32), "_", strings.Repeat("A", 8))},
		{"heroku-api-key-v2", fragments("HRKU-AA", strings.Repeat("A", 58))},
		{"huggingface-organization-api-token", fragments("api_org_", strings.Repeat("a", 34))},
		{"infracost-api-token", fragments("ico-", strings.Repeat("X", 32))},
		{"intra42-client-secret", fragments("s-s4t2ud-", strings.Repeat("a", 64))},
		{"linear-api-key", fragments("lin_api_", strings.Repeat("a", 40))},
		{"notion-api-token", fragments("ntn_", "12345678901", strings.Repeat("a", 35))},
		{"npm-access-token", fragments("npm_", strings.Repeat("a", 36))},
		{"octopus-deploy-api-key", fragments("API-", strings.Repeat("A", 26))},
		{"openshift-user-token", fragments("sha256~", strings.Repeat("X", 43))},
		{"perplexity-api-key", fragments("pplx-", strings.Repeat("x", 48))},
		{"planetscale-api-token", fragments("pscale_tkn_", strings.Repeat("a", 32))},
		{"planetscale-oauth-token", fragments("pscale_oauth_", strings.Repeat("a", 32))},
		{"planetscale-password", fragments("pscale_pw_", strings.Repeat("a", 32))},
		{"postman-api-token", fragments("PMAK-", strings.Repeat("a", 24), "-", strings.Repeat("b", 34))},
		{"prefect-api-token", fragments("pnu_", strings.Repeat("X", 36))},
		{"pulumi-api-token", fragments("pul-", strings.Repeat("a", 40))},
		{"pypi-upload-token", fragments("pypi-AgEIcHlwaS5vcmc", strings.Repeat("a", 50))},
		{"readme-api-token", fragments("rdme_", strings.Repeat("a", 70))},
		{"rubygems-api-token", fragments("rubygems_", strings.Repeat("a", 48))},
		{"scalingo-api-token", fragments("tk-us-", strings.Repeat("a", 48))},
		{"sendgrid-api-token", fragments("SG.", strings.Repeat("a", 66))},
		{"sendinblue-api-token", fragments("xkeysib-", strings.Repeat("a", 64), "-", strings.Repeat("b", 16))},
		{"sentry-org-token", fragments("sntrys_eyJpYXQiO", strings.Repeat("a", 10), "LCJyZWdpb25fdXJs", strings.Repeat("a", 10), "_", strings.Repeat("a", 43))},
		{"sentry-user-token", fragments("sntryu_", strings.Repeat("a", 64))},
		{"settlemint-application-access-token", fragments("sm_aat_", strings.Repeat("a", 16))},
		{"settlemint-personal-access-token", fragments("sm_pat_", strings.Repeat("a", 16))},
		{"settlemint-service-access-token", fragments("sm_sat_", strings.Repeat("a", 16))},
		{"shippo-api-token", fragments("shippo_live_", strings.Repeat("a", 40))},
		{"shopify-access-token", fragments("shpat_", strings.Repeat("a", 32))},
		{"shopify-custom-access-token", fragments("shpca_", strings.Repeat("a", 32))},
		{"shopify-private-app-access-token", fragments("shppa_", strings.Repeat("a", 32))},
		{"shopify-shared-secret", fragments("shpss_", strings.Repeat("a", 32))},
		{"square-access-token", fragments("sq0atp-", strings.Repeat("a", 22))},
		{"stripe-access-token", fragments("sk_live_", strings.Repeat("a", 24))},
		{"vault-batch-token", fragments("hvb.", strings.Repeat("a", 138))},
	}
	if len(cases) != 67 {
		t.Fatalf("lookalike cases = %d, want 67", len(cases))
	}
	var noLog *runlog.Log
	for _, c := range cases {
		if got := runlog.Redact(c.value); got != c.value {
			t.Errorf("%s: Redact = %q, want unchanged", c.id, got)
		}
		if got, err := noLog.Publish(c.value); err != nil || got != c.value {
			t.Errorf("%s: Publish = %q, %v, want unchanged", c.id, got, err)
		}
	}
}

func TestEntropyDependentLookalikesStayPlain(t *testing.T) {
	// These strings are false positives in the pinned generators for rules
	// that stay excluded for their generic shapes. The per-rule gated
	// lookalikes for ported rules live in
	// TestPortedLowEntropyLookalikesStayPlain.
	cases := []struct{ id, value string }{
		{"huggingface-token", fragments("hf_", strings.Repeat("x", 34))},
		{"gitlab-pat", fragments("glpat-", "XXXXXXXXXXX-XXXXXXXX")},
		{"grafana-cloud-api-token", fragments("glc_", strings.Repeat("x", 68))},
	}
	var noLog *runlog.Log
	for _, c := range cases {
		if got := runlog.Redact(c.value); got != c.value {
			t.Errorf("%s: Redact = %q, want unchanged", c.id, got)
		}
		if got, err := noLog.Publish(c.value); err != nil || got != c.value {
			t.Errorf("%s: Publish = %q, %v, want unchanged", c.id, got, err)
		}
	}
}

func TestFacebookClosingParenthesisStaysPlain(t *testing.T) {
	// The pinned rule ends with a delimiter group that admits no closing
	// parenthesis, so a candidate followed by ")" is not a token.
	value := fragments("EAAM", strings.Repeat("a0", 50), ")")
	if got := runlog.Redact(value); got != value {
		t.Errorf("Redact = %q, want unchanged", got)
	}
	var noLog *runlog.Log
	if got, err := noLog.Publish(value); err != nil || got != value {
		t.Errorf("Publish = %q, %v, want unchanged", got, err)
	}
}

func TestPortedRuleDoesNotMaskPartOfALongerIdentifier(t *testing.T) {
	value := fragments("gldt-", strings.Repeat("a0", 10), "suffix")
	if got := runlog.Redact(value); got != value {
		t.Errorf("Redact(longer identifier) = %q, want unchanged", got)
	}
}

func TestPortedRulesKeepConsumedDelimiters(t *testing.T) {
	cases := []struct{ token, delimiter string }{
		{fragments("npm_", varied(alphaWord, 36)), ";"},
		{fragments("glrt-", varied(alphaWord, 20)), ")"},
		{fragments("sntrys_eyJpYXQiO", varied(alphaBase64, 12), "LCJyZWdpb25fdXJs", varied(alphaBase64, 12), "_", varied(alphaBase64, 43)), ")"},
		{fragments("CLOJARS_", varied(alphaNumLower, 60)), "."},
	}
	for _, c := range cases {
		got := runlog.Redact(c.token + c.delimiter)
		if !strings.HasSuffix(got, "…[redacted]"+c.delimiter) {
			t.Errorf("Redact with delimiter = %q", got)
		}
	}
}
