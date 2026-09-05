package resolve

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

// stagedCodexCredential is a codex auth.json whose access token expires far
// enough ahead that cred.AssertFresh passes under the frozen test clock and
// under the real one.
func stagedCodexCredential(t *testing.T) string {
	t.Helper()
	claims, err := json.Marshal(map[string]any{
		"exp":       int64(4_070_908_800), // 2099-01-01
		"iss":       "https://auth.example.com",
		"client_id": "app_test",
	})
	if err != nil {
		t.Fatalf("building the claims: %v", err)
	}
	token := "eyJhbGciOiJub25lIn0." + base64.RawURLEncoding.EncodeToString(claims) + ".signature"

	raw, err := json.Marshal(map[string]any{
		"OPENAI_API_KEY": nil,
		"auth_mode":      "chatgpt",
		"last_refresh":   "2026-08-01T00:00:00Z",
		"tokens": map[string]any{
			"access_token":  token,
			"refresh_token": "refresh-abc",
			"id_token":      "id-abc",
			"account_id":    "acct",
		},
	})
	if err != nil {
		t.Fatalf("building the credential fixture: %v", err)
	}
	return string(raw)
}

// The resolve leg stages a credential the same way the review leg does, and it
// reached the same fault from the same cause: Leg.Env is read at the
// composition root (cmd/crossrev/legs.go:168) before cred.Prepare exports
// CODEX_HOME, so the child was handed a list that never named the scratch home.
//
// The review leg has the matching case. Both are kept: the fix is one line in
// each file, and one line is exactly what a later edit drops from one of them.
func TestTheStagedCredentialReachesTheResolveHarness(t *testing.T) {
	e := setup(t)
	t.Setenv("CROSSREV_CODEX_AUTH", stagedCodexCredential(t))
	e.addReview(t, defaultFindings(), "issues-remain")

	got := e.runReq(t, Request{
		PR:      42,
		Repo:    e.slug,
		Trigger: TriggerHuman,
		Harness: "codex",
	})

	var home string
	found := 0
	for _, entry := range got.Invocation.Env {
		if rest, ok := strings.CutPrefix(entry, "CODEX_HOME="); ok {
			home = rest
			found++
		}
	}

	if found == 0 {
		t.Fatalf("the child environment names no CODEX_HOME, so codex reads its own store instead of the staged copy: %v", got.Invocation.Env)
	}
	// One entry, not two. Go's exec takes the last of a repeated name, so a
	// second one would work by accident rather than by design.
	if found > 1 {
		t.Errorf("CODEX_HOME appears %d times: %v", found, got.Invocation.Env)
	}
	if home == "" {
		t.Error("CODEX_HOME is empty, which points codex at nothing")
	}
}
