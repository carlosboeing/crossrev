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

func TestPortedCredentialRules(t *testing.T) {
	cases := []struct{ id, positive, negative string }{
		{"adobe-client-secret", fragments("p8e-", strings.Repeat("a0", 16)), fragments("p8e-", "abc")},
		{"age-secret-key", fragments("AGE-SECRET-KEY-1", strings.Repeat("Q", 58)), fragments("AGE-SECRET-KEY-1", strings.Repeat("Q", 57))},
		{"alibaba-access-key-id", fragments("LTAI", strings.Repeat("a1", 10)), fragments("LTAI", "abc")},
		{"authress-service-client-access-key", fragments("sc_", strings.Repeat("a1", 5), ".", "ab12", ".acc_", strings.Repeat("a1", 8), ".", strings.Repeat("a1", 15)), fragments("sc_", "abc", ".acc_", "short")},
		{"aws-amazon-bedrock-api-key-long-lived", fragments("ABSK", strings.Repeat("Aa01", 28)), fragments("ABSK", "QmVkcm9ja0FQSUtleS1EXAMPLE")},
		{"clojars-api-token", fragments("CLOJARS_", strings.Repeat("a0", 30)), fragments("CLOJARS_", "abc")},
		{"databricks-api-token", fragments("dapi", strings.Repeat("a0", 16)), fragments("DATABRICKS_TOKEN=dapi", "123456789012345678a9bc01234defg5")},
		{"digitalocean-access-token", fragments("doo_v1_", strings.Repeat("a0", 32)), fragments("doo_v1_", "abc")},
		{"digitalocean-pat", fragments("dop_v1_", strings.Repeat("a0", 32)), fragments("dop_v1_", "abc")},
		{"digitalocean-refresh-token", fragments("dor_v1_", strings.Repeat("a0", 32)), fragments("dor_v1_", "abc")},
		{"doppler-api-token", fragments("dp.pt.", strings.Repeat("a0", 21), "a"), fragments("dp.pt.", "abc")},
		{"duffel-api-token", fragments("duffel_test_", strings.Repeat("a0", 21), "a"), fragments("duffel_test_", "abc")},
		{"dynatrace-api-token", fragments("dt0c01.", strings.Repeat("a0", 12), ".", strings.Repeat("a0", 32)), fragments("dt0c01.", "abc")},
		{"easypost-api-token", fragments("EZAK", strings.Repeat("a0", 27)), fragments("...6wqX6fNUXA/rYqRvfQ+", "EZAK", "GqQRiRyqAFRQshGPWOIAwNWGORfKHSBnVNFtVmWYoW6PH23lkqbbDWep95C/3VmWq/edti6...")},
		{"easypost-test-api-token", fragments("EZTK", strings.Repeat("a0", 27)), fragments("...6wqX6fNUXA/rYqRvfQ+", "EZTK", "GqQRiRyqAFRQshGPWOIAwNWGORfKHSBnVNFtVmWYoW6PH23lkqbbDWep95C/3VmWq/edti6...")},
		{"flutterwave-encryption-key", fragments("FLWSECK_TEST-", strings.Repeat("a0", 6)), fragments("FLWSECK_TEST-", "abc")},
		{"flutterwave-secret-key", fragments("FLWSECK_TEST-", strings.Repeat("a0", 16), "-X"), fragments("FLWSECK_TEST-", "abc", "-X")},
		{"frameio-api-token", fragments("fio-u-", strings.Repeat("a0", 32)), fragments("fio-u-", "abc")},
		{"gitlab-cicd-job-token", fragments("glcbt-", "a1b2c", "_", strings.Repeat("a0", 10)), fragments("glcbt-", "abc")},
		{"gitlab-deploy-token", fragments("gldt-", strings.Repeat("a0", 10)), fragments("gldt-", "abc")},
		{"gitlab-feature-flag-client-token", fragments("glffct-", strings.Repeat("a0", 10)), fragments("glffct-", "abc")},
		{"gitlab-feed-token", fragments("glft-", strings.Repeat("a0", 10)), fragments("glft-", "abc")},
		{"gitlab-incoming-mail-token", fragments("glimt-", strings.Repeat("a0", 12), "a"), fragments("glimt-", "abc")},
		{"gitlab-kubernetes-agent-token", fragments("glagent-", strings.Repeat("a0", 25)), fragments("glagent-", "abc")},
		{"gitlab-oauth-app-secret", fragments("gloas-", strings.Repeat("a0", 32)), fragments("gloas-", "abc")},
		{"gitlab-ptt", fragments("glptt-", strings.Repeat("a0", 20)), fragments("glptt-", strings.Repeat("x", 40))},
		{"gitlab-runner-authentication-token", fragments("glrt-", strings.Repeat("a0", 10)), fragments("glrt-", "abc")},
		{"gitlab-runner-authentication-token-routable", fragments("glrt-t1_", strings.Repeat("a0", 13), "a", ".", "ab", "1234567"), fragments("glrt-tx_", strings.Repeat("x", 27), ".xxxxxxxxx")},
		{"gitlab-scim-token", fragments("glsoat-", strings.Repeat("a0", 10)), fragments("glsoat-", "abc")},
		{"heroku-api-key-v2", fragments("HRKU-AA", strings.Repeat("a0", 29)), fragments("HRKU-AA", "abc")},
		{"huggingface-organization-api-token", fragments("api_org_", strings.Repeat("aB", 17)), "const api_org_controller = require('api')"},
		{"intra42-client-secret", fragments("s-s4t2ud-", strings.Repeat("ab01", 16)), fragments("s-s4t2ud-", "abc")},
		{"linear-api-key", fragments("lin_api_", strings.Repeat("a0", 20)), fragments("lin_api_", "abc")},
		{"notion-api-token", fragments("ntn_", "12345678901", strings.Repeat("a0", 16), "abc"), fragments("ntn_", "12345678901")},
		{"npm-access-token", fragments("npm_", strings.Repeat("a0", 18)), fragments("npm_", strings.Repeat("a0", 17))},
		{"planetscale-api-token", fragments("pscale_tkn_", strings.Repeat("a0", 16)), fragments("pscale_tkn_", "abc")},
		{"planetscale-oauth-token", fragments("pscale_oauth_", strings.Repeat("a0", 16)), fragments("pscale_oauth_", "abc")},
		{"planetscale-password", fragments("pscale_pw_", strings.Repeat("a0", 16)), fragments("pscale_pw_", "abc")},
		{"postman-api-token", fragments("PMAK-", strings.Repeat("a0", 12), "-", strings.Repeat("a0", 17)), fragments("PMAK-", "abc")},
		{"pulumi-api-token", fragments("pul-", strings.Repeat("a0", 20)), "<img src=\"./assets/vipul-f0eb1acf0da84c06a50c5b2c59932001997786b176dec02bd16\">"},
		{"pypi-upload-token", fragments("pypi-AgEIcHlwaS5vcmc", strings.Repeat("a0", 32)), fragments("pypi-AgEIcHlwaS5vcmc", "abc")},
		{"readme-api-token", fragments("rdme_", strings.Repeat("a0", 35)), fragments("rdme_", strings.Repeat("X", 70))},
		{"rubygems-api-token", fragments("rubygems_", strings.Repeat("a0", 24)), fragments("rubygems_", "abc")},
		{"sendgrid-api-token", fragments("SG.", strings.Repeat("a0", 33)), fragments("SG.", "abc")},
		{"sendinblue-api-token", fragments("xkeysib-", strings.Repeat("a0", 32), "-", strings.Repeat("a0", 8)), fragments("xkeysib-", "abc")},
		{"sentry-org-token", fragments("sntrys_eyJpYXQiO", strings.Repeat("a0", 5), "LCJyZWdpb25fdXJs", strings.Repeat("a0", 5), "_", strings.Repeat("a0", 21), "a"), fragments("sntrys_", strings.Repeat("a0", 45), "_", strings.Repeat("a0", 21), "a")},
		{"sentry-user-token", fragments("sntryu_", strings.Repeat("a0", 32)), fragments("sntryu_", strings.Repeat("a", 63))},
		{"settlemint-application-access-token", fragments("sm_aat_", strings.Repeat("a0", 8)), fragments("sm_aat_", strings.Repeat("a0", 5))},
		{"settlemint-personal-access-token", fragments("sm_pat_", strings.Repeat("a0", 8)), fragments("sm_pat_", strings.Repeat("a0", 5))},
		{"settlemint-service-access-token", fragments("sm_sat_", strings.Repeat("a0", 8)), fragments("sm_sat_", strings.Repeat("a0", 5))},
		{"shippo-api-token", fragments("shippo_live_", strings.Repeat("a0", 20)), fragments("shippo_live_", "abc")},
		{"shopify-access-token", fragments("shpat_", strings.Repeat("a0", 16)), fragments("shpat_", "abc")},
		{"shopify-custom-access-token", fragments("shpca_", strings.Repeat("a0", 16)), fragments("shpca_", "abc")},
		{"shopify-private-app-access-token", fragments("shppa_", strings.Repeat("a0", 16)), fragments("shppa_", "abc")},
		{"shopify-shared-secret", fragments("shpss_", strings.Repeat("a0", 16)), fragments("shpss_", "abc")},
		{"stripe-access-token", fragments("sk_live_", strings.Repeat("a0", 15)), fragments("task_test_", strings.Repeat("a0", 15))},
		{"vault-batch-token", fragments("hvb.", strings.Repeat("a0", 69)), fragments("hvb.", "abc")},
	}
	if len(cases) != 57 {
		t.Fatalf("test cases = %d, want 57", len(cases))
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

func TestEntropyDependentLookalikesStayPlain(t *testing.T) {
	// These strings are false positives in the pinned generators. Their regexes
	// alone match, so importing those rules without entropy would mask prose.
	cases := []string{
		fragments("hf_", strings.Repeat("x", 34)),
		fragments("glpat-", "XXXXXXXXXXX-XXXXXXXX"),
		fragments("glc_", strings.Repeat("x", 68)),
	}
	for _, value := range cases {
		if got := runlog.Redact(value); got != value {
			t.Errorf("Redact(%q) = %q, want unchanged", value, got)
		}
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
		{fragments("npm_", strings.Repeat("a0", 18)), ";"},
		{fragments("glrt-", strings.Repeat("a0", 10)), ")"},
		{fragments("sntrys_eyJpYXQiO", strings.Repeat("a0", 5), "LCJyZWdpb25fdXJs", strings.Repeat("a0", 5), "_", strings.Repeat("a0", 21), "a"), ")"},
		{fragments("CLOJARS_", strings.Repeat("a0", 30)), "."},
	}
	for _, c := range cases {
		got := runlog.Redact(c.token + c.delimiter)
		if !strings.HasSuffix(got, "…[redacted]"+c.delimiter) {
			t.Errorf("Redact with delimiter = %q", got)
		}
	}
}
