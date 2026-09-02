package app

import "github.com/carlosboeing/crossrev/internal/ui"

// RolePermissions is what a role is allowed to do, as the App manifest asks for
// it (_auth_role_permissions, lib/auth.sh:56).
//
// ADR 0006 is the decision: three repository permissions for the loop, all at
// write, and nothing else — no Secrets, no Administration, no Workflows.
// `issues` is not trimmable, because GitHub models pull request labels under
// the Issues API and the whole loop is label-driven.
//
// The refresher gets `secrets`, which is GitHub's repository secret permission.
// `organization_secrets` is a separate permission and is deliberately not
// requested: a rotating credential stored at organisation level would be
// refreshed by every repository that reads it. Concurrency groups are
// repository-scoped, so several repositories means several writers, and the
// first one to refresh invalidates the rest — the exact collision the single
// writer exists to prevent. One credential, one repository, one writer.
//
// The bytes are what `jq -cn` prints, in that key order, and they go into the
// manifest as a JSON value (lib/auth.sh:566). jq's trailing newline is not
// here: both call sites read this through `$( )`, which drops it.
//
// An unknown role refuses rather than returning an empty set. The shell calls
// this for its exit status alone at lib/auth.sh:511, before the browser and the
// listener open, and that ordering is the point — a role typed wrong is caught
// while nothing has happened yet.
func RolePermissions(role string) ([]byte, error) {
	switch role {
	case RoleLoop:
		return []byte(`{"contents":"write","issues":"write","pull_requests":"write"}`), nil
	case RoleRefresher:
		return []byte(`{"secrets":"write"}`), nil
	}
	// An empty role is unknown here, and that is the shell's own asymmetry:
	// _auth_pem reads `${2:-loop}` and this one reads a bare `$1`.
	return nil, &ui.FatalError{
		Reason: "unknown App role '" + role + "'",
		Action: "Roles are: loop (the review and resolve jobs) and refresher (the credential refresh job).",
	}
}
