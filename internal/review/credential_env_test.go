package review_test

import (
	"encoding/base64"
	"encoding/json"
	"slices"
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

// The staged credential has to reach the harness process, and the leg's
// environment is read before anything is staged.
//
// cmd/crossrev/legs.go builds Leg.Env with exec.Inherit at the composition
// root, which is before the leg runs and therefore before cred.Prepare exports
// CODEX_HOME. A leg that hands the child that first list points codex at
// whatever home the parent had — none, on a fresh runner — so codex reads no
// login, calls the vendor unauthenticated, and the leg dies on a 401 that names
// no cause. Measured on carlosboeing/crossrev-testbed#12 under v0.6.0: seven
// 401s from wss://api.openai.com/v1/responses, and the same credential
// authenticated the moment CODEX_HOME was present before the leg was built.
//
// So this asserts on the spec the runner received rather than on the leg's
// verdict. The verdict is not the property: a leg can fail for a dozen reasons
// after the child starts, and none of them says whether the credential arrived.
func TestTheStagedCredentialReachesTheHarnessProcess(t *testing.T) {
	e := newEnv(t)
	t.Setenv("CROSSREV_CODEX_AUTH", stagedCodexCredential(t))

	req := e.request(t)
	req.HarnessOverride = "codex"
	_ = runLeg(t, e, req)

	if len(e.runner.specs) == 0 {
		t.Fatal("the runner was never called, so nothing was staged for it")
	}

	spec := e.runner.specs[0]
	index := slices.IndexFunc(spec.Env, func(entry string) bool {
		return strings.HasPrefix(entry, "CODEX_HOME=")
	})
	if index < 0 {
		t.Fatalf("the child environment names no CODEX_HOME, so codex reads its own store instead of the staged copy: %v", spec.Env)
	}

	home := strings.TrimPrefix(spec.Env[index], "CODEX_HOME=")
	if home == "" {
		t.Fatal("CODEX_HOME is empty, which points codex at nothing")
	}

	// One entry, not two. Go's exec takes the last of a repeated name, so a
	// second one would work by accident rather than by design.
	for _, entry := range spec.Env[index+1:] {
		if strings.HasPrefix(entry, "CODEX_HOME=") {
			t.Errorf("CODEX_HOME appears more than once: %v", spec.Env)
		}
	}
}
