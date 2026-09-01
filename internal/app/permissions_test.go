package app_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/app"
	"github.com/carlosboeing/crossrev/internal/ui"
)

// --- what each role is allowed to do ----------------------------------------
//
// The permission set goes into the App manifest GitHub registers from
// (lib/auth.sh:566), so these bytes are what the App ends up holding. ADR 0006
// is the decision they encode: three repository permissions for the loop, all
// at write, and nothing else.

// The exact bytes `jq -cn` prints, key order included. Measured by sourcing
// lib/auth.sh and running the function: the shell prints a trailing newline
// that both call sites drop, because both read it through `$( )`.
func TestRolePermissionsBytes(t *testing.T) {
	for _, tc := range []struct{ role, want string }{
		{app.RoleLoop, `{"contents":"write","issues":"write","pull_requests":"write"}`},
		{app.RoleRefresher, `{"secrets":"write"}`},
	} {
		got, err := app.RolePermissions(tc.role)
		if err != nil {
			t.Fatalf("RolePermissions(%q): %v", tc.role, err)
		}
		if string(got) != tc.want {
			t.Errorf("RolePermissions(%q) =\n%s\nwant\n%s", tc.role, got, tc.want)
		}
	}
}

// The assertion ADR 0006 exists for: three permissions and no fourth.
//
// A fourth key is how this App would quietly grow a capability nobody decided
// on, and the manifest is registered once — an operator who has already
// approved it does not re-approve it. Reading the JSON back rather than
// comparing the string means the test still fires if the bytes are reformatted.
func TestTheLoopRoleRequestsThreeRepositoryPermissionsAndNoFourth(t *testing.T) {
	raw, err := app.RolePermissions(app.RoleLoop)
	if err != nil {
		t.Fatalf("RolePermissions: %v", err)
	}
	var perms map[string]string
	if err := json.Unmarshal(raw, &perms); err != nil {
		t.Fatalf("the permission set is not an object of strings: %v", err)
	}
	want := map[string]string{
		"contents":      "write",
		"issues":        "write",
		"pull_requests": "write",
	}
	if len(perms) != len(want) {
		t.Fatalf("the loop role requests %d permissions, want exactly %d: %s", len(perms), len(want), raw)
	}
	for key, value := range want {
		if perms[key] != value {
			t.Errorf("permission %q = %q, want %q", key, perms[key], value)
		}
	}
	// Secrets is the one that must never appear here: the loop's jobs check out
	// a pull request branch and run a model over a diff, so secrets:write on
	// this App would put secret rewriting one injection away from
	// attacker-controlled text.
	if _, held := perms["secrets"]; held {
		t.Error("the loop role requests secrets, which is the refresher's alone")
	}
}

// The refresher gets `secrets`, which is GitHub's repository secret permission.
// `organization_secrets` is a separate permission and is deliberately not
// requested: a rotating credential at organisation level would be refreshed by
// every repository that reads it, and the first refresh invalidates the rest.
func TestTheRefresherRoleRequestsRepositorySecretsAlone(t *testing.T) {
	raw, err := app.RolePermissions(app.RoleRefresher)
	if err != nil {
		t.Fatalf("RolePermissions: %v", err)
	}
	var perms map[string]string
	if err := json.Unmarshal(raw, &perms); err != nil {
		t.Fatalf("the permission set is not an object of strings: %v", err)
	}
	if len(perms) != 1 || perms["secrets"] != "write" {
		t.Fatalf("the refresher role requests %s, want {\"secrets\":\"write\"} alone", raw)
	}
	if _, held := perms["organization_secrets"]; held {
		t.Error("the refresher role requests organization_secrets, which is deliberately not asked for")
	}
}

// The summary `auth status` prints and the permissions the manifest asks for
// are two spellings of one fact, and nothing joins them. This is the join: a
// permission added to one and not the other is a status line that lies.
func TestTheSummaryNamesEveryPermissionTheRoleRequests(t *testing.T) {
	for _, role := range []string{app.RoleLoop, app.RoleRefresher} {
		raw, err := app.RolePermissions(role)
		if err != nil {
			t.Fatalf("RolePermissions(%q): %v", role, err)
		}
		var perms map[string]string
		if err := json.Unmarshal(raw, &perms); err != nil {
			t.Fatalf("the permission set is not an object of strings: %v", err)
		}
		summary := app.RoleSummary(role)
		for key, value := range perms {
			if !strings.Contains(summary, key+":"+value) {
				t.Errorf("role %q requests %s:%s, and the summary %q does not say so", role, key, value, summary)
			}
		}
	}
}

// An unknown role refuses, and it refuses before anything opens: the shell
// calls this for its exit status alone at lib/auth.sh:511, ahead of the browser
// and the listener. The text is measured, not written from the source.
func TestRolePermissionsRefusesAnUnknownRole(t *testing.T) {
	for _, role := range []string{"bogus", "", "Loop", "secrets"} {
		raw, err := app.RolePermissions(role)
		if err == nil {
			t.Fatalf("RolePermissions(%q) returned %s and no error", role, raw)
		}
		if raw != nil {
			t.Errorf("RolePermissions(%q) returned %s beside its error", role, raw)
		}
		var fatal *ui.FatalError
		if !errors.As(err, &fatal) {
			t.Fatalf("RolePermissions(%q) returned %T, want a *ui.FatalError", role, err)
		}
		wantReason := "unknown App role '" + role + "'"
		wantAction := "Roles are: loop (the review and resolve jobs) and refresher (the credential refresh job)."
		if fatal.Reason != wantReason {
			t.Errorf("reason = %q, want %q", fatal.Reason, wantReason)
		}
		if fatal.Action != wantAction {
			t.Errorf("action = %q, want %q", fatal.Action, wantAction)
		}
	}
}

// An empty role is an unknown role here, and that differs from the paths: the
// shell writes `${2:-loop}` in _auth_pem and a bare `$1` in this one. Measured
// both ways: an empty role dies naming an empty role, rather than the loop's.
func TestAnEmptyRoleIsNotTheLoopHere(t *testing.T) {
	if _, err := app.RolePermissions(""); err == nil {
		t.Fatal("an empty role was treated as the loop's")
	}
}
