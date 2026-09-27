package runlog_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/runlog"
)

// TestPublishIsIdempotent runs a masked body through a second time, which is
// the case gh_review_comment_create creates every time GitHub refuses to anchor
// a line and it falls back to a plain comment (lib/log.sh:138-141).
func TestPublishIsIdempotent(t *testing.T) {
	var noLog *runlog.Log
	once, err := noLog.Publish("the token is ghp_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")
	if err != nil {
		t.Fatalf("first pass: %v", err)
	}
	twice, err := noLog.Publish(once)
	if err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if twice != once {
		t.Errorf("a second pass changed the body:\n got %q\nwant %q", twice, once)
	}
}

// TestRedactFileRewritesInPlace covers the transcript kept on disk
// (log_redact_file, lib/log.sh:174), including the mode the rewrite leaves
// behind: the file is 0600 afterwards whatever it was before, because the
// rewrite goes through a private temporary file.
func TestRedactFileRewritesInPlace(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "review.attempt-1.stdout")
	if err := os.WriteFile(path, []byte("start\nghp_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA\nend\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var noLog *runlog.Log
	noLog.RedactFile(path)

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := "start\nghp_AAAAAA…[redacted]\nend\n"
	if string(got) != want {
		t.Errorf("file = %q, want %q", got, want)
	}
	assertMode(t, path, 0o600)
}

// TestRedactFileLeavesEverythingElseAlone: the Bash function returns early for
// anything that is not a regular file, because it runs from an EXIT trap that
// must not care.
func TestRedactFileLeavesEverythingElseAlone(t *testing.T) {
	dir := t.TempDir()
	var noLog *runlog.Log
	noLog.RedactFile(filepath.Join(dir, "does-not-exist"))
	noLog.RedactFile(dir)
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("the directory did not survive: %v", err)
	}
}

// TestRedactMasksCloudKeys covers the vendor prefixes beyond the oracle's
// frozen five: AWS access key ids, Google API keys and OAuth tokens, and
// Slack tokens. The values below are fabricated strings in the shapes the
// filter matches, not credentials. Each keeps its prefix so a masked line
// still names the kind of token it held, and a second pass changes nothing.
func TestRedactMasksCloudKeys(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"aws access key id",
			"key AKIAIOSFODNN7EXAMPLE here",
			"key AKIAIOSFOD…[redacted] here"},
		{"aws temporary key id",
			"key ASIAIOSFODNN7EXAMPLE here",
			"key ASIAIOSFOD…[redacted] here"},
		{"google api key",
			"key AIzaABCDEF0123456789abcdef0123456789XYZ here",
			"key AIzaABCDEF…[redacted] here"},
		{"google oauth token",
			"token ya29.a0AfH6SMBxYz0123456789_abcdefghij here",
			"token ya29.a0AfH6…[redacted] here"},
		{"slack bot token",
			"token xoxb-abcdefEXAMPLEabcdefEXAMPLE here",
			"token xoxb-abcdef…[redacted] here"},
		{"slack user token",
			"token xoxp-abcdefEXAMPLEabcdefEXAMPLE here",
			"token xoxp-abcdef…[redacted] here"},
		{"slack refresh token",
			"token xoxe-1-1234567890-abcdefgh here",
			"token xoxe-1-1234…[redacted] here"},
		{"slack app token",
			"token xapp-1-A0123456789-1234567890123-abcdef here",
			"token xapp-1-A012…[redacted] here"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := runlog.Redact(c.in)
			if got != c.want {
				t.Errorf("Redact(%q) = %q, want %q", c.in, got, c.want)
			}
			if again := runlog.Redact(got); again != got {
				t.Errorf("a second pass changed the body:\n got %q\nwant %q", again, got)
			}
		})
	}
}

// TestRedactMasksPrivateKeyBlocks covers a PEM block quoted into a log: the
// whole block goes, and the BEGIN line survives so the redacted line still
// names what it held. The terminator goes with the body, so a masked block
// no longer matches.
func TestRedactMasksPrivateKeyBlocks(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"rsa",
			"deploy key:\n-----BEGIN RSA PRIVATE KEY-----\nMIIBOgIBAAJBAKq7Z2Jv3Jv3Jv3Jv3Jv3Jv3Jv3Jv3Jv3Jv3Jv3Jv3Jv3\n-----END RSA PRIVATE KEY-----\nend\n",
			"deploy key:\n-----BEGIN RSA PRIVATE KEY-----…[redacted]\nend\n"},
		{"openssh",
			"-----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNzaC1rZXktdjEAAAAABG5vbmUAAAAEbm9uZQAAAAAAAAABAAAAMwAAAAtzc2gtZW\n-----END OPENSSH PRIVATE KEY-----\n",
			"-----BEGIN OPENSSH PRIVATE KEY-----…[redacted]\n"},
		{"pkcs8",
			"-----BEGIN PRIVATE KEY-----\nMIGHAgEAMBMGByqGSM49AgEGCCqGSM49AwEHBG0wawIBAQQgmbihuqpXfnzQ9PJKjRw0\n-----END PRIVATE KEY-----\n",
			"-----BEGIN PRIVATE KEY-----…[redacted]\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := runlog.Redact(c.in)
			if got != c.want {
				t.Errorf("Redact(%q) = %q, want %q", c.in, got, c.want)
			}
			if again := runlog.Redact(got); again != got {
				t.Errorf("a second pass changed the body:\n got %q\nwant %q", again, got)
			}
		})
	}
}

// TestRedactMasksTheCheckoutHeader covers the authorization header the
// checkout persists, in any letter case: its base64 hides the token's own
// prefix from the token rules, so the header is a shape of its own.
func TestRedactMasksTheCheckoutHeader(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"upper",
			"http.https://github.com/.extraheader AUTHORIZATION: basic eC1hY2Nlc3MtdG9rZW46Z2hzXzEyMzQ1Njc4OTBhYmNkZWY=",
			"http.https://github.com/.extraheader AUTHORIZATION: basic eC1hY2…[redacted]"},
		{"lower header, capital scheme",
			"http.https://github.com/.extraheader authorization: Basic dGVzdDpzZWNyZXQ=",
			"http.https://github.com/.extraheader authorization: Basic dGVzdD…[redacted]"},
		{"capital header, upper scheme",
			"Authorization: BASIC Z2l0aHViOmdocF8xMjM0NTY3ODkwYWJjZGVm",
			"Authorization: BASIC Z2l0aH…[redacted]"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := runlog.Redact(c.in)
			if got != c.want {
				t.Errorf("Redact(%q) = %q, want %q", c.in, got, c.want)
			}
			if again := runlog.Redact(got); again != got {
				t.Errorf("a second pass changed the body:\n got %q\nwant %q", again, got)
			}
		})
	}
}

// TestRedactMasksTheCheckoutHeaderBeforeItsContents pins the order: the
// header's base64 can itself hold a token-shaped substring, so the header
// rule runs before the token rules and the whole value is masked rather
// than the fragment. The value is a fabricated shape, not a credential: it
// decodes to a username and password pair, and the fragment it carries
// matches the AWS rule on its own, which is what makes the order load-bearing.
func TestRedactMasksTheCheckoutHeaderBeforeItsContents(t *testing.T) {
	const value = "AKIAIOSFODNN7EXAMPLE+1NvbWVVc2VyOnNvbWVwYXNz"
	const in = "AUTHORIZATION: basic " + value
	const want = "AUTHORIZATION: basic AKIAIO…[redacted]"
	if alone := runlog.Redact(value); strings.Contains(alone, "AKIAIOSFODNN7EXAMPLE") {
		t.Fatalf("the fragment no longer matches the AWS rule alone: %q", alone)
	}
	got := runlog.Redact(in)
	if got != want {
		t.Errorf("Redact(%q) = %q, want %q", in, got, want)
	}
	if strings.Contains(got, "AKIAIOSFODNN7EXAMPLE") {
		t.Errorf("the header's contents survived inside the masked body: %q", got)
	}
	if again := runlog.Redact(got); again != got {
		t.Errorf("a second pass changed the body:\n got %q\nwant %q", again, got)
	}
}

// TestRedactLeavesLookalikesAlone is the negative case per shape: a short
// prefix, a wrong family, a block that is not a private key, a block whose
// terminator names another key, a header that is not basic auth, and a basic
// header whose value is prose rather than a username and password pair all
// pass through, with no notice published.
func TestRedactLeavesLookalikesAlone(t *testing.T) {
	cases := []struct{ name, in string }{
		{"aws id too short", "key AKIA12 here"},
		{"aws short identifier", "key AKIA1234567 here"},
		{"aws id lowercase", "key akiaiosfodnn7example here"},
		{"google key too short", "key AIza12 here"},
		{"google key short identifier", "key AIzaConfigValue here"},
		{"google oauth too short", "token ya29.abc here"},
		{"slack token too short", "token xoxb-abc here"},
		{"slack refresh token too short", "token xoxe-abc here"},
		{"slack unknown family", "token xoxz-1234567890abcdef here"},
		{"slack app token too short", "token xapp-abc here"},
		{"certificate block", "-----BEGIN CERTIFICATE-----\nMIIBOgIBAAJBAKq7Z2Jv3Jv3Jv3Jv3Jv3Jv3Jv3Jv3Jv3Jv3Jv3Jv3Jv3\n-----END CERTIFICATE-----\n"},
		{"key block without terminator", "-----BEGIN RSA PRIVATE KEY-----\nMIIBOgIBAAJBAKq7Z2Jv3Jv3Jv3Jv3Jv3Jv3Jv3Jv3Jv3Jv3Jv3Jv3Jv3\n"},
		{"key block with mismatched labels", "-----BEGIN RSA PRIVATE KEY-----\nMIIBOgIBAAJBAKq7Z2Jv3Jv3Jv3Jv3Jv3Jv3Jv3Jv3Jv3Jv3Jv3Jv3Jv3\n-----END EC PRIVATE KEY-----\n"},
		{"bearer header", "extraheader = AUTHORIZATION: bearer eC1hY2Nlc3MtdG9rZW4="},
		{"basic header too short", "AUTHORIZATION: basic abc"},
		{"basic header is prose", "Authorization: Basic authentication"},
		{"basic header without user and password", "Authorization: Basic aGVsbG8gd29ybGQ="},
	}
	var noLog *runlog.Log
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := runlog.Redact(c.in); got != c.in {
				t.Errorf("Redact(%q) = %q, want it unchanged", c.in, got)
			}
			got, err := noLog.Publish(c.in)
			if err != nil {
				t.Fatalf("Publish: %v", err)
			}
			if got != c.in {
				t.Errorf("Publish(%q) = %q, want it unchanged and unnoted", c.in, got)
			}
		})
	}
}

// TestRedactPublishNotesNewShapesOnce runs a body carrying every new shape
// through Publish twice: masked once, noted once, and the second pass
// changes nothing.
func TestRedactPublishNotesNewShapesOnce(t *testing.T) {
	const body = "keys AKIAIOSFODNN7EXAMPLE ASIAIOSFODNN7EXAMPLE " +
		"AIzaABCDEF0123456789abcdef0123456789XYZ " +
		"ya29.a0AfH6SMBxYz0123456789_abcdefghij " +
		"xoxb-abcdefEXAMPLEabcdefEXAMPLE " +
		"xoxe-1-1234567890-abcdefgh " +
		"xapp-1-A0123456789-1234567890123-abcdef\n" +
		"-----BEGIN RSA PRIVATE KEY-----\nMIIBOgIBAAJBAKq7Z2Jv3Jv3Jv3Jv3Jv3Jv3Jv3Jv3Jv3Jv3Jv3Jv3Jv3\n-----END RSA PRIVATE KEY-----\n" +
		"AUTHORIZATION: basic eC1hY2Nlc3MtdG9rZW46Z2hzXzEyMzQ1Njc4OTBhYmNkZWY="
	secrets := []string{
		"AKIAIOSFODNN7EXAMPLE",
		"ASIAIOSFODNN7EXAMPLE",
		"AIzaABCDEF0123456789abcdef0123456789XYZ",
		"ya29.a0AfH6SMBxYz0123456789_abcdefghij",
		"xoxb-abcdefEXAMPLEabcdefEXAMPLE",
		"xoxe-1-1234567890-abcdefgh",
		"xapp-1-A0123456789-1234567890123-abcdef",
		"MIIBOgIBAAJBAKq7Z2Jv3Jv3Jv3Jv3Jv3Jv3Jv3Jv3Jv3Jv3Jv3Jv3Jv3",
		"eC1hY2Nlc3MtdG9rZW46Z2hzXzEyMzQ1Njc4OTBhYmNkZWY=",
	}
	var noLog *runlog.Log
	once, err := noLog.Publish(body)
	if err != nil {
		t.Fatalf("first pass: %v", err)
	}
	for _, secret := range secrets {
		if strings.Contains(once, secret) {
			t.Errorf("the published body still carries %q:\n%s", secret, once)
		}
	}
	if n := strings.Count(once, runlog.RedactNotice); n != 1 {
		t.Errorf("the published body carries %d notices, want one:\n%s", n, once)
	}
	twice, err := noLog.Publish(once)
	if err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if twice != once {
		t.Errorf("a second pass changed the body:\n got %q\nwant %q", twice, once)
	}
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Errorf("%s mode = %04o, want %04o", path, got, want)
	}
}
