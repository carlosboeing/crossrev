package ghexec_test

import (
	"context"
	"testing"
)

func TestWorkflowRunStatusArgv(t *testing.T) {
	c, r := client(t, out("in_progress\n"))

	got := c.WorkflowRunStatus(context.Background(), testSlug(t), "12345")
	if got.String() != "in_progress" || !got.Known() {
		t.Errorf("status = %q", got)
	}
	r.wantArgs(t, 0, "run", "view", "12345", "--repo", "acme/widget", "--json", "status", "--jq", ".status // empty")
}

// Unknown is not finished. A run in another repository, a token without
// actions:read and no network all answer this way.
func TestWorkflowRunStatusAnswersUnknown(t *testing.T) {
	c, _ := client(t, bad())
	if got := c.WorkflowRunStatus(context.Background(), testSlug(t), "12345"); got.Known() {
		t.Errorf("status = %q, want unknown", got)
	}
}

// A run id that is not a number is not asked about at all.
func TestWorkflowRunStatusRefusesANonNumericRunID(t *testing.T) {
	for _, id := range []string{"", "abc", "12a", "-1", "1 2"} {
		c, r := client(t, out("completed\n"))
		if got := c.WorkflowRunStatus(context.Background(), testSlug(t), id); got.Known() {
			t.Errorf("run id %q answered %q, want unknown", id, got)
		}
		if len(r.specs) != 0 {
			t.Errorf("run id %q reached gh as %v", id, r.argvs())
		}
	}
}
