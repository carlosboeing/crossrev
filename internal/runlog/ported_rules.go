// Ported from gitleaks v8.30.1 (83d9cd684c87d95d656c1458ef04895a7f1cbd8e).
// Source: config/gitleaks.toml and cmd/generate/config/rules/.
// The following is the complete gitleaks LICENSE notice:
// MIT License
//
// Copyright (c) 2019 Zachary Rice
//
// Permission is hereby granted, free of charge, to any person obtaining a copy
// of this software and associated documentation files (the "Software"), to deal
// in the Software without restriction, including without limitation the rights
// to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
// copies of the Software, and to permit persons to whom the Software is
// furnished to do so, subject to the following conditions:
//
// The above copyright notice and this permission notice shall be included in all
// copies or substantial portions of the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
// OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
// SOFTWARE.

package runlog

import (
	"regexp"
	"strings"
)

type portedCredentialPattern struct {
	id      string
	re      *regexp.Regexp
	keep    int
	entropy float64
}

// Specific formats precede overlapping, shorter formats. The expressions
// come from the pinned gitleaks config, with a token-end boundary on glrt-
// to avoid matching the beginning of a routable-token false positive. keep
// preserves the literal prefix and six following characters. entropy carries
// the rule's upstream threshold where gitleaks sets one, and 0 leaves the
// rule regex-only, as upstream does.
var portedCredentialPatterns = []portedCredentialPattern{
	{"pypi-upload-token", regexp.MustCompile(`pypi-AgEIcHlwaS5vcmc[\w-]{50,1000}`), 26, 3},
	{"age-secret-key", regexp.MustCompile(`AGE-SECRET-KEY-1[QPZRY9X8GF2TVDW0S3JN54KHCE6MUA7L]{58}`), 22, 0},
	{"sentry-org-token", regexp.MustCompile(`\bsntrys_eyJpYXQiO[a-zA-Z0-9+/]{10,200}(?:LCJyZWdpb25fdXJs|InJlZ2lvbl91cmwi|cmVnaW9uX3VybCI6)[a-zA-Z0-9+/]{10,200}={0,2}_[a-zA-Z0-9+/]{43}(?:[^a-zA-Z0-9+/]|\z)`), 22, 4.5},
	{"flutterwave-secret-key", regexp.MustCompile(`FLWSECK_TEST-(?i)[a-h0-9]{32}-X`), 19, 2},
	{"flutterwave-encryption-key", regexp.MustCompile(`FLWSECK_TEST-(?i)[a-h0-9]{12}`), 19, 2},
	{"planetscale-oauth-token", regexp.MustCompile(`\b(pscale_oauth_[\w=\.-]{32,64})(?:[\x60'"\s;]|\\[nr]|$)`), 19, 3},
	{"duffel-api-token", regexp.MustCompile(`duffel_(?:test|live)_(?i)[a-z0-9_\-=]{43}`), 18, 2},
	{"shippo-api-token", regexp.MustCompile(`\b(shippo_(?:live|test)_[a-fA-F0-9]{40})(?:[\x60'"\s;]|\\[nr]|$)`), 18, 2},
	{"planetscale-api-token", regexp.MustCompile(`\b(pscale_tkn_(?i)[\w=\.-]{32,64})(?:[\x60'"\s;]|\\[nr]|$)`), 17, 3},
	{"planetscale-password", regexp.MustCompile(`(?i)\b(pscale_pw_(?i)[\w=\.-]{32,64})(?:[\x60'"\s;]|\\[nr]|$)`), 16, 3},
	{"gitlab-rrt", regexp.MustCompile(`GR1348941[\w-]{20}`), 15, 3},
	{"intra42-client-secret", regexp.MustCompile(`\b(s-s4t2(?:ud|af)-(?i)[abcdef0123456789]{64})(?:[\x60'"\s;]|\\[nr]|$)`), 15, 3},
	{"rubygems-api-token", regexp.MustCompile(`\b(rubygems_[a-f0-9]{48})(?:[\x60'"\s;]|\\[nr]|$)`), 15, 2},
	{"clojars-api-token", regexp.MustCompile(`(?i)CLOJARS_[a-z0-9]{60}`), 14, 2},
	{"gitlab-kubernetes-agent-token", regexp.MustCompile(`glagent-[0-9a-zA-Z_\-]{50}`), 14, 3},
	{"gitlab-runner-authentication-token-routable", regexp.MustCompile(`\bglrt-t\d_[0-9a-zA-Z_\-]{27,300}\.[0-9a-z]{2}[0-9a-z]{7}\b`), 14, 4},
	{"huggingface-organization-api-token", regexp.MustCompile(`\b(api_org_(?i:[a-z]{34}))(?:[\x60'"\s;]|\\[nr]|$)`), 14, 2},
	{"linear-api-key", regexp.MustCompile(`lin_api_(?i)[a-z0-9]{40}`), 14, 2},
	{"sendinblue-api-token", regexp.MustCompile(`\b(xkeysib-[a-f0-9]{64}\-(?i)[a-z0-9]{16})(?:[\x60'"\s;]|\\[nr]|$)`), 14, 2},
	{"stripe-access-token", regexp.MustCompile(`\b((?:sk|rk)_(?:test|live|prod)_[a-zA-Z0-9]{10,99})(?:[\x60'"\s;]|\\[nr]|$)`), 14, 2},
	{"1password-service-account-token", regexp.MustCompile(`ops_eyJ[a-zA-Z0-9+/]{250,}={0,3}`), 13, 4},
	{"digitalocean-access-token", regexp.MustCompile(`\b(doo_v1_[a-f0-9]{64})(?:[\x60'"\s;]|\\[nr]|$)`), 13, 3},
	{"digitalocean-pat", regexp.MustCompile(`\b(dop_v1_[a-f0-9]{64})(?:[\x60'"\s;]|\\[nr]|$)`), 13, 3},
	{"digitalocean-refresh-token", regexp.MustCompile(`(?i)\b(dor_v1_[a-f0-9]{64})(?:[\x60'"\s;]|\\[nr]|$)`), 13, 0},
	{"dynatrace-api-token", regexp.MustCompile(`dt0c01\.(?i)[a-z0-9]{24}\.[a-z0-9]{64}`), 13, 4},
	{"gitlab-feature-flag-client-token", regexp.MustCompile(`glffct-[0-9a-zA-Z_\-]{20}`), 13, 3},
	{"gitlab-scim-token", regexp.MustCompile(`glsoat-[0-9a-zA-Z_\-]{20}`), 13, 3},
	{"heroku-api-key-v2", regexp.MustCompile(`\b((HRKU-AA[0-9a-zA-Z_-]{58}))(?:[\x60'"\s;]|\\[nr]|$)`), 13, 4},
	{"openshift-user-token", regexp.MustCompile(`\b(sha256~[\w-]{43})(?:[^\w-]|\z)`), 13, 3.5},
	{"sentry-user-token", regexp.MustCompile(`\b(sntryu_[a-f0-9]{64})(?:[\x60'"\s;]|\\[nr]|$)`), 13, 3.5},
	{"settlemint-application-access-token", regexp.MustCompile(`\b(sm_aat_[a-zA-Z0-9]{16})(?:[\x60'"\s;]|\\[nr]|$)`), 13, 3},
	{"settlemint-personal-access-token", regexp.MustCompile(`\b(sm_pat_[a-zA-Z0-9]{16})(?:[\x60'"\s;]|\\[nr]|$)`), 13, 3},
	{"settlemint-service-access-token", regexp.MustCompile(`\b(sm_sat_[a-zA-Z0-9]{16})(?:[\x60'"\s;]|\\[nr]|$)`), 13, 3},
	{"square-access-token", regexp.MustCompile(`\b((?:EAAA|sq0atp-)[\w-]{22,60})(?:[\x60'"\s;]|\\[nr]|$)`), 13, 2},
	{"doppler-api-token", regexp.MustCompile(`dp\.pt\.(?i)[a-z0-9]{43}`), 12, 2},
	{"frameio-api-token", regexp.MustCompile(`fio-u-(?i)[a-z0-9\-_=]{64}`), 12, 0},
	{"gitlab-cicd-job-token", regexp.MustCompile(`glcbt-[0-9a-zA-Z]{1,5}_[0-9a-zA-Z_-]{20}`), 12, 3},
	{"gitlab-incoming-mail-token", regexp.MustCompile(`glimt-[0-9a-zA-Z_\-]{25}`), 12, 3},
	{"gitlab-oauth-app-secret", regexp.MustCompile(`gloas-[0-9a-zA-Z_\-]{64}`), 12, 3},
	{"gitlab-ptt", regexp.MustCompile(`glptt-[0-9a-f]{40}`), 12, 3},
	{"scalingo-api-token", regexp.MustCompile(`\b(tk-us-[\w-]{48})(?:[\x60'"\s;]|\\[nr]|$)`), 12, 2},
	{"shopify-access-token", regexp.MustCompile(`shpat_[a-fA-F0-9]{32}`), 12, 2},
	{"shopify-custom-access-token", regexp.MustCompile(`shpca_[a-fA-F0-9]{32}`), 12, 2},
	{"shopify-private-app-access-token", regexp.MustCompile(`shppa_[a-fA-F0-9]{32}`), 12, 2},
	{"shopify-shared-secret", regexp.MustCompile(`shpss_[a-fA-F0-9]{32}`), 12, 2},
	{"gitlab-deploy-token", regexp.MustCompile(`gldt-[0-9a-zA-Z_\-]{20}`), 11, 3},
	{"gitlab-feed-token", regexp.MustCompile(`glft-[0-9a-zA-Z_\-]{20}`), 11, 3},
	{"gitlab-runner-authentication-token", regexp.MustCompile(`glrt-[0-9a-zA-Z_\-]{20}(?:[^0-9a-zA-Z_-]|$)`), 11, 3},
	{"grafana-service-account-token", regexp.MustCompile(`(?i)\b(glsa_[A-Za-z0-9]{32}_[A-Fa-f0-9]{8})(?:[\x60'"\s;]|\\[nr]|$)`), 11, 3},
	{"perplexity-api-key", regexp.MustCompile(`\b(pplx-[a-zA-Z0-9]{48})(?:[\x60'"\s;]|\\[nr]|$|\b)`), 11, 4},
	{"postman-api-token", regexp.MustCompile(`\b(PMAK-(?i)[a-f0-9]{24}\-[a-f0-9]{34})(?:[\x60'"\s;]|\\[nr]|$)`), 11, 3},
	{"readme-api-token", regexp.MustCompile(`\b(rdme_[a-z0-9]{70})(?:[\x60'"\s;]|\\[nr]|$)`), 11, 2},
	{"adobe-client-secret", regexp.MustCompile(`\b(p8e-(?i)[a-z0-9]{32})(?:[\x60'"\s;]|\\[nr]|$)`), 10, 2},
	{"alibaba-access-key-id", regexp.MustCompile(`\b(LTAI(?i)[a-z0-9]{20})(?:[\x60'"\s;]|\\[nr]|$)`), 10, 2},
	{"artifactory-api-key", regexp.MustCompile(`\bAKCp[A-Za-z0-9]{69}\b`), 10, 4.5},
	{"aws-amazon-bedrock-api-key-long-lived", regexp.MustCompile(`\b(ABSK[A-Za-z0-9+/]{109,269}={0,2})(?:[\x60'"\s;]|\\[nr]|$)`), 10, 3},
	{"databricks-api-token", regexp.MustCompile(`\b(dapi[a-f0-9]{32}(?:-\d)?)(?:[\x60'"\s;]|\\[nr]|$)`), 10, 3},
	{"easypost-api-token", regexp.MustCompile(`\bEZAK(?i)[a-z0-9]{54}\b`), 10, 2},
	{"easypost-test-api-token", regexp.MustCompile(`\bEZTK(?i)[a-z0-9]{54}\b`), 10, 2},
	{"facebook-page-access-token", regexp.MustCompile(`\b(EAA[MC](?i)[a-z0-9]{100,})(?:[\x60'"\s;]|\\[nr]|$)`), 10, 4},
	{"flyio-access-token", regexp.MustCompile(`\b((?:fo1_[\w-]{43}|fm1[ar]_[a-zA-Z0-9+\/]{100,}={0,3}|fm2_[a-zA-Z0-9+\/]{100,}={0,3}))(?:[\x60'"\s;]|\\[nr]|$)`), 10, 4},
	{"infracost-api-token", regexp.MustCompile(`\b(ico-[a-zA-Z0-9]{32})(?:[\x60'"\s;]|\\[nr]|$)`), 10, 3},
	{"notion-api-token", regexp.MustCompile(`\b(ntn_[0-9]{11}[A-Za-z0-9]{32}[A-Za-z0-9]{3})(?:[\x60'"\s;]|\\[nr]|$)`), 10, 4},
	{"npm-access-token", regexp.MustCompile(`(?i)\b(npm_[a-z0-9]{36})(?:[\x60'"\s;]|\\[nr]|$)`), 10, 2},
	{"octopus-deploy-api-key", regexp.MustCompile(`\b(API-[A-Z0-9]{26})(?:[\x60'"\s;]|\\[nr]|$)`), 10, 3},
	{"prefect-api-token", regexp.MustCompile(`\b(pnu_[a-zA-Z0-9]{36})(?:[\x60'"\s;]|\\[nr]|$)`), 10, 2},
	{"pulumi-api-token", regexp.MustCompile(`\b(pul-[a-f0-9]{40})(?:[\x60'"\s;]|\\[nr]|$)`), 10, 2},
	{"vault-batch-token", regexp.MustCompile(`\b(hvb\.[\w-]{138,300})(?:[\x60'"\s;]|\\[nr]|$)`), 10, 4},
	{"authress-service-client-access-key", regexp.MustCompile(`\b((?:sc|ext|scauth|authress)_(?i)[a-z0-9]{5,30}\.[a-z0-9]{4,6}\.(?-i:acc)[_-][a-z0-9-]{10,32}\.[a-z0-9+/_=-]{30,120})(?:[\x60'"\s;]|\\[nr]|$)`), 9, 2},
	{"sendgrid-api-token", regexp.MustCompile(`\b(SG\.(?i)[a-z0-9=_\-\.]{66})(?:[\x60'"\s;]|\\[nr]|$)`), 9, 2},
}

// entropyClears reports whether a match clears the ported upstream threshold.
// None of the ported rules sets secretGroup, so the measured span is the one
// gitleaks measures without it: the first capture group when the expression
// has one, else the whole match. A match at or below the threshold stays
// unmasked.
func entropyClears(in []byte, loc []int, threshold float64) bool {
	secret := in[loc[0]:loc[1]]
	for i := 2; i+1 < len(loc); i += 2 {
		if loc[i] >= 0 && loc[i+1] > loc[i] {
			secret = in[loc[i]:loc[i+1]]
			break
		}
	}
	return shannonEntropy(string(secret)) > threshold
}

// redactPorted preserves a surrounding delimiter if the source regex consumes
// one. A match inside a longer token passes through: some source expressions
// have no right boundary and would otherwise mask only the first few bytes.
func redactPorted(in []byte) []byte {
	for _, pattern := range portedCredentialPatterns {
		matches := pattern.re.FindAllSubmatchIndex(in, -1)
		if len(matches) == 0 {
			continue
		}
		expression := pattern.re.String()
		bounded := strings.HasSuffix(expression, `\b`) || strings.HasSuffix(expression, `$)`) || strings.HasSuffix(expression, `\z)`)
		out := make([]byte, 0, len(in))
		last := 0
		for _, loc := range matches {
			start := loc[0]
			end := loc[1]
			if len(loc) >= 4 && loc[2] == start && loc[3] >= 0 {
				end = loc[3]
			} else if pattern.id == "sentry-org-token" && end > start && !base64TokenByte(in[end-1]) {
				end--
			} else if pattern.id == "gitlab-runner-authentication-token" && end > start && !tokenContinuation(in[end-1]) {
				end--
			}
			if end-start <= pattern.keep || !bounded && end < len(in) && tokenContinuation(in[end]) {
				continue
			}
			if pattern.entropy != 0 && !entropyClears(in, loc, pattern.entropy) {
				continue
			}
			out = append(out, in[last:start]...)
			out = append(out, in[start:start+pattern.keep]...)
			out = append(out, mask...)
			out = append(out, in[end:loc[1]]...)
			last = loc[1]
		}
		if last != 0 {
			in = append(out, in[last:]...)
		}
	}
	return in
}

func tokenContinuation(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b == '_' || b == '-'
}

func base64TokenByte(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b == '+' || b == '/'
}
