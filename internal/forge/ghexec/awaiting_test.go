package ghexec_test

import (
	"context"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/exec"
	"github.com/carlosboeing/crossrev/internal/forge/ghexec"
)

// wantAwaitingJQ is the filter lib/run.sh:3692-3693 sends, written out here
// rather than read from the package so that changing it has to be done twice.
// The newline and the eleven-space continuation indent are the shell's own
// bytes: `gh` hands the whole string to jq, so the layout is part of what the
// stub's `jq -r "$jq_expr"` receives rather than decoration.
const wantAwaitingJQ = "[.[] | select([.labels[].name] | any(startswith(\"crossrev/awaiting-\")))\n" +
	"           | {number, labels: [.labels[].name], head: .head.sha}]"

var fixtureSlug = mustSlug("acme/widget")

func mustSlug(s string) core.Slug {
	slug, err := core.ParseSlug(s)
	if err != nil {
		panic(err)
	}
	return slug
}

func TestAwaitingPullRequestsAsksGHTheShellsWay(t *testing.T) {
	r := &recorder{results: []exec.Result{out(`[]`)}}
	c := ghexec.New(r, passthrough{})

	c.AwaitingPullRequests(context.Background(), fixtureSlug)

	r.wantArgs(t, 0, "api", "repos/acme/widget/pulls?state=open&per_page=100",
		"--jq", wantAwaitingJQ)
}

func TestAwaitingPullRequestsReadsTheThreeFields(t *testing.T) {
	body := `[{"number":42,"labels":["crossrev/awaiting-review","crossrev/watchdog-retried"],"head":"abc123"},` +
		`{"number":7,"labels":["crossrev/awaiting-resolution"],"head":"def456"}]`
	r := &recorder{results: []exec.Result{out(body)}}
	c := ghexec.New(r, passthrough{})

	got := c.AwaitingPullRequests(context.Background(), fixtureSlug)

	if len(got) != 2 {
		t.Fatalf("got %d pull requests, want 2: %+v", len(got), got)
	}
	if got[0].Number != 42 || got[0].HeadSHA != "abc123" {
		t.Fatalf("first row = %+v", got[0])
	}
	if strings.Join(got[0].Labels, " ") != "crossrev/awaiting-review crossrev/watchdog-retried" {
		t.Fatalf("first row labels = %q", got[0].Labels)
	}
	if got[1].Number != 7 || got[1].HeadSHA != "def456" {
		t.Fatalf("second row = %+v", got[1])
	}
}

// A read that fails answers as no pull requests, because the shell's
// `|| stuck="[]"` cannot tell an unreachable API from an empty repository
// (lib/run.sh:3693).
//
// The last two rows are the ones that separate the exit code from the bytes.
// `stuck` is assigned from a command substitution, so the shell's `||` fires on
// the exit status alone: `gh` that printed a whole array and then exited
// non-zero leaves `stuck="[]"` and the watchdog loop runs zero times, whatever
// arrived on stdout. Without them the first two rows prove nothing about the
// exit code, because empty stdout fails to unmarshal anyway.
func TestAwaitingPullRequestsAnswersNoneWhenTheReadFails(t *testing.T) {
	answeredBody := `[{"number":42,"labels":["crossrev/awaiting-review"],"head":"abc123"}]`
	cases := map[string]exec.Result{
		"gh exited non-zero": bad(),
		"gh never started":   unresolved(),
		"gh printed junk":    out("not json"),
		"gh printed nothing": out(""),
		"gh printed the array and then exited non-zero": {
			ExitCode: 1,
			Stdout:   []byte(answeredBody),
		},
		"gh printed the array and never started": {
			Err:    errNoStatus,
			Stdout: []byte(answeredBody),
		},
	}
	for name, res := range cases {
		t.Run(name, func(t *testing.T) {
			r := &recorder{results: []exec.Result{res}}
			c := ghexec.New(r, passthrough{})
			if got := c.AwaitingPullRequests(context.Background(), fixtureSlug); got != nil {
				t.Fatalf("got %+v, want none", got)
			}
		})
	}
}

// The same read through the offline suite's fake gh. The stub runs the jq
// filter for real, so this is what proves the filter selects on the awaiting
// prefix and reads the three fields out of GitHub's own shape — the recorder
// tests above see only the string.
func TestAwaitingPullRequestsThroughTheStub(t *testing.T) {
	prs := `[{"number":42,"labels":[{"name":"crossrev/awaiting-review"}],"head":{"sha":"abc123"}},` +
		`{"number":9,"labels":[{"name":"something-else"}],"head":{"sha":"zzz"}}]`
	c, log := stubClient(t, "api repos/*/pulls?state=open*\t"+prs+"\n")

	got := c.AwaitingPullRequests(context.Background(), fixtureSlug)

	if len(got) != 1 || got[0].Number != 42 || got[0].HeadSHA != "abc123" {
		t.Fatalf("got %+v, want only #42", got)
	}
	if len(got[0].Labels) != 1 || got[0].Labels[0] != "crossrev/awaiting-review" {
		t.Fatalf("labels = %q", got[0].Labels)
	}
	// The stub logs one entry, and the entry carries the filter's own newline,
	// so it reads back as two lines. Measured against the shell in this
	// checkout: `bash bin/crossrev watchdog --repo acme/widget` writes exactly
	// these bytes to CROSSREV_GH_LOG.
	want := "api repos/acme/widget/pulls?state=open&per_page=100 --jq " + wantAwaitingJQ
	if got := strings.Join(log(), "\n"); got != want {
		t.Fatalf("the stub logged\n%q\nwant\n%q", got, want)
	}
}
