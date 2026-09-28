package initcmd_test

import (
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/initcmd"
)

// legWorkflows pairs each leg workflow template with the leg job a failure
// notice watches.
var legWorkflows = []struct {
	name     string
	template func() []byte
	leg      string
}{
	{"review", initcmd.ReviewWorkflowTemplate, "review"},
	{"resolve", initcmd.ResolveWorkflowTemplate, "resolve"},
}

// legTemplate is the raw template bytes for one leg workflow. Raw, not
// rendered: the notice job carries no runner-dependent content besides
// `runs-on: __RUNS_ON__`, which these tests do not read.
func legTemplate(name string, template func() []byte) string {
	return string(template())
}

// jobBlock is the lines of one job in a workflow document: everything after
// its `  <job>:` header up to the next job header. It cuts at `jobs:` first,
// because the `on:` block holds two-space headers of its own.
func jobBlock(t *testing.T, doc, job string) string {
	t.Helper()
	_, after, found := strings.Cut(doc, "\njobs:\n")
	if !found {
		t.Fatalf("no jobs block in the workflow")
	}
	var out []string
	inside := false
	for _, line := range strings.Split(after, "\n") {
		if len(line) > 2 && strings.HasPrefix(line, "  ") && line[2] != ' ' && strings.HasSuffix(line, ":") {
			inside = strings.TrimSuffix(strings.TrimSpace(line), ":") == job
			continue
		}
		if inside {
			out = append(out, line)
		}
	}
	if !inside && len(out) == 0 {
		t.Fatalf("no %q job in the workflow", job)
	}
	return strings.Join(out, "\n")
}

// jobPermissions is the key-to-scope map under a job's `permissions:`.
// Scopes GitHub does not list default to none, so the map is the whole
// answer for the token that job runs with.
func jobPermissions(block string) map[string]string {
	permissions := map[string]string{}
	inside := false
	for _, line := range strings.Split(block, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(line, "    ") && !strings.HasPrefix(line, "      ") && trimmed == "permissions:" {
			inside = true
			continue
		}
		if !inside {
			continue
		}
		if strings.HasPrefix(line, "      ") && strings.Contains(trimmed, ":") {
			key, value, _ := strings.Cut(trimmed, ":")
			permissions[strings.TrimSpace(key)] = strings.TrimSpace(value)
			continue
		}
		inside = false
	}
	return permissions
}

// TestNoticeWorkflowRunsOnlyWhenTheLegFails: each leg workflow carries a
// second job watching the leg job, and it runs only on its failure — never
// beside a green leg, and never in place of a skipped one.
func TestNoticeWorkflowRunsOnlyWhenTheLegFails(t *testing.T) {
	for _, row := range legWorkflows {
		t.Run(row.name, func(t *testing.T) {
			block := jobBlock(t, legTemplate(row.name, row.template), "notice")
			if !strings.Contains(block, "needs: ["+row.leg+"]") {
				t.Errorf("the notice job watches no leg job: %q", block)
			}
			if !strings.Contains(block, "if: failure()") {
				t.Errorf("the notice job runs unconditionally rather than on the leg's failure")
			}
		})
	}
}

// TestNoticeWorkflowCarriesOnlyTheNoticePermissions: a workflow token's
// permissions are set per job, which is the whole reason the notice is a
// separate job — the leg job stays read-only while the notice carries what
// commenting needs.
//
// `actions: read` is the third entry because the notice reads the failed
// steps off the run API, and that endpoint answers no lesser scope. It is
// read-only, like the leg's own scope.
func TestNoticeWorkflowCarriesOnlyTheNoticePermissions(t *testing.T) {
	for _, row := range legWorkflows {
		t.Run(row.name+"/notice", func(t *testing.T) {
			permissions := jobPermissions(jobBlock(t, legTemplate(row.name, row.template), "notice"))
			for key, want := range map[string]string{
				"pull-requests": "write",
				"issues":        "write",
			} {
				if got := permissions[key]; got != want {
					t.Errorf("the notice job grants %s: %q, want %q", key, got, want)
				}
			}
			for key, scope := range permissions {
				if scope == "write" && key != "pull-requests" && key != "issues" {
					t.Errorf("the notice job grants %s: write, which commenting does not need", key)
				}
			}
			if _, found := permissions["contents"]; found {
				t.Errorf("the notice job takes a contents scope, which commenting does not need")
			}
		})
		t.Run(row.name+"/leg", func(t *testing.T) {
			permissions := jobPermissions(jobBlock(t, legTemplate(row.name, row.template), row.leg))
			if len(permissions) != 1 || permissions["contents"] != "read" {
				t.Errorf("the %s job's permissions are %v, want only contents: read", row.leg, permissions)
			}
		})
	}
}

// TestNoticeWorkflowReadsTheRunAndCommentsWithTheWorkflowToken: the notice
// names the failed steps and their fixes, read off this run through the run
// API, and posts them to the pull request.
//
// It authenticates with the workflow's own token, never the App token: the
// step that mints the App token may be the step that failed, and a notice
// that needed it would go silent exactly when it is most needed.
func TestNoticeWorkflowReadsTheRunAndCommentsWithTheWorkflowToken(t *testing.T) {
	for _, row := range legWorkflows {
		t.Run(row.name, func(t *testing.T) {
			block := jobBlock(t, legTemplate(row.name, row.template), "notice")
			if !strings.Contains(block, "github.token") {
				t.Errorf("the notice job does not use the workflow's own token")
			}
			if strings.Contains(block, "steps.app.outputs.token") {
				t.Errorf("the notice job needs the App token, which is what may have failed")
			}
			if !strings.Contains(block, "actions/runs/") || !strings.Contains(block, "/jobs") {
				t.Errorf("the notice job does not read the failed steps off the run API")
			}
			if !strings.Contains(block, "pr comment") && !strings.Contains(block, "issues/") {
				t.Errorf("the notice job posts nothing to the pull request")
			}
			for _, secret := range []string{"APP_ID", "APP_PRIVATE_KEY"} {
				if !strings.Contains(block, secret) {
					t.Errorf("the notice job never names %s, the fix when the App-token step failed", secret)
				}
			}
		})
	}
}
