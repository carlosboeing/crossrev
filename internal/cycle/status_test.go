package cycle_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/carlosboeing/crossrev/internal/config"
	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/cycle"
	"github.com/carlosboeing/crossrev/internal/forge"
	"github.com/carlosboeing/crossrev/internal/prstate"
	"github.com/carlosboeing/crossrev/internal/ui"
)

// statusCase is one row of the table, read from internal/cycle/testdata/status.
//
// Every field was measured against lib/run.sh rather than written by hand: the
// markers are built by the same jq expressions tests/test-status.sh builds them
// with, and `want` is what _status_state, _status_leg_row and _status_next
// printed for them under a frozen clock. `shell` names the case in
// tests/test-status.sh the fixture stands for.
type statusCase struct {
	Name              string            `json:"name"`
	Shell             string            `json:"shell"`
	Now               int64             `json:"now"`
	HeadSHA           string            `json:"head_sha"`
	MinFixSeverity    string            `json:"min_fix_severity"`
	MaxPassesPerCycle int               `json:"max_passes_per_cycle"`
	Liveness          statusCaseLife    `json:"liveness"`
	Labels            []string          `json:"labels"`
	Markers           []json.RawMessage `json:"markers"`
	Want              statusWant        `json:"want"`
}

type statusCaseLife struct {
	Life   string `json:"life"`
	Detail string `json:"detail"`
}

type statusWant struct {
	State   string           `json:"state"`
	Pass    int              `json:"pass"`
	MaxPass int              `json:"max_pass"`
	Rows    []statusWantRow  `json:"rows"`
	Next    []statusWantNext `json:"next"`
}

type statusWantRow struct {
	Pass        int    `json:"pass"`
	Leg         string `json:"leg"`
	Step        string `json:"step"`
	Description string `json:"description"`
	// Rendered is the whole line ui_row printed, glyph included. Step 2 lays
	// the rows out and asserts on this; step 1 only checks that the
	// description it derives is the tail of it, so a fixture cannot drift
	// away from the text it was measured from.
	Rendered string `json:"rendered"`
}

type statusWantNext struct {
	Kind string `json:"kind"`
	Text string `json:"t"`
}

const (
	statusRepo   = "acme/widget"
	statusPR     = 42
	statusAuthor = "tester"
	statusBase   = "0913bf7b99dcecf746d0e6fcef5a9c1d64aaf3b0"
)

// statusStepNames maps the ui.Step values back onto the `ui_row` kind words the
// fixtures record (lib/ui.sh:54-59).
var statusStepNames = map[ui.Step]string{
	ui.StepIdle: "opt",
	ui.StepOK:   "ok",
	ui.StepNo:   "no",
	ui.StepRun:  "run",
}

func statusCases(t *testing.T) []statusCase {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join("testdata", "status", "*.json"))
	if err != nil {
		t.Fatalf("globbing the status fixtures: %v", err)
	}
	if len(paths) == 0 {
		t.Fatal("no status fixtures found")
	}
	cases := make([]statusCase, 0, len(paths))
	for _, path := range paths {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		var c statusCase
		if err := json.Unmarshal(body, &c); err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		cases = append(cases, c)
	}
	return cases
}

// statusLoad drives the real Load for one fixture: the markers go onto comments
// the way the loop writes them, and the forge answers from there.
func statusLoad(t *testing.T, c statusCase) cycle.Report {
	t.Helper()
	head, err := core.NewRevision(c.HeadSHA)
	if err != nil {
		t.Fatalf("%s: head revision: %v", c.Name, err)
	}
	base, err := core.NewRevision(statusBase)
	if err != nil {
		t.Fatalf("%s: base revision: %v", c.Name, err)
	}
	slug, err := core.ParseSlug(statusRepo)
	if err != nil {
		t.Fatalf("%s: slug: %v", c.Name, err)
	}

	labels := make([]forge.Label, 0, len(c.Labels))
	for _, name := range c.Labels {
		labels = append(labels, forge.Label{Name: name})
	}
	comments := make([]forge.IssueComment, 0, len(c.Markers))
	for i, raw := range c.Markers {
		body, err := prstate.EncodeMarker(raw)
		if err != nil {
			t.Fatalf("%s: encoding marker %d: %v", c.Name, i, err)
		}
		comments = append(comments, forge.IssueComment{
			ID:          int64(9001 + i),
			AuthorLogin: statusAuthor,
			Body:        "Summary." + body,
		})
	}

	f := &statusForge{
		pr: forge.PullRequest{
			Number:       statusPR,
			Title:        "Add refresh",
			URL:          "https://github.com/x",
			HeadRefName:  "feature",
			HeadRefOid:   head,
			BaseRefName:  "main",
			BaseRefOid:   base,
			ChangedFiles: 1,
			Labels:       labels,
			State:        "OPEN",
		},
		comments: comments,
	}
	s := &cycle.Status{
		Forge:    f,
		Liveness: statusLife{life: cycle.Life(c.Liveness.Life), detail: c.Liveness.Detail},
		Now:      func() time.Time { return time.Unix(c.Now, 0) },
		Show:     statusShow(c),
	}
	report, err := s.Load(context.Background(), slug, statusPR)
	if err != nil {
		t.Fatalf("%s: Load: %v", c.Name, err)
	}
	return report
}

// statusShow answers the configuration read from the base revision, in the
// shape tests/harness.sh writes into every fixture checkout.
func statusShow(c statusCase) config.ShowFile {
	yaml := fmt.Sprintf(`version: 1
mode: local
policy:
  min_fix_severity: %s
  max_passes_per_cycle: %d
  max_files_changed_per_pr: 200
  max_prs_per_day: 25
reviewer:
  harness: claude
  model: reviewer-model
resolver:
  harness: claude
  model: resolver-model
backlog:
  destination: none
`, c.MinFixSeverity, c.MaxPassesPerCycle)
	return func(_ context.Context, rev core.Revision, path string) ([]byte, config.FileStatus, error) {
		if rev.SHA() == statusBase && path == ".github/crossrev.yml" {
			return []byte(yaml), config.IsFile, nil
		}
		return nil, config.NotFound, nil
	}
}

// TestStatusState pins the header word for every fixture: the label-first rule
// at lib/run.sh:3112-3130 and the marker fallback at lib/run.sh:3131-3181.
func TestStatusState(t *testing.T) {
	for _, c := range statusCases(t) {
		t.Run(c.Name, func(t *testing.T) {
			got := statusLoad(t, c)
			if string(got.State) != c.Want.State {
				t.Errorf("state = %q, want %q (%s)", got.State, c.Want.State, c.Shell)
			}
		})
	}
}

// TestStatusStateIsOneOfFive keeps the header vocabulary closed. A sixth word
// would be a place for the terminal and the label to disagree, which is the
// thing reading the header off the label was meant to stop
// (tests/test-status.sh:613-626).
func TestStatusStateIsOneOfFive(t *testing.T) {
	for _, c := range statusCases(t) {
		t.Run(c.Name, func(t *testing.T) {
			got := statusLoad(t, c)
			if _, err := core.ParseLoopState(string(got.State)); err != nil {
				t.Errorf("state = %q, which is not one of the five: %v", got.State, err)
			}
		})
	}
}

// TestStatusPassCounts pins the two numbers the LOOP section reads: the current
// review pass and the highest pass any marker mentions, refused passes included
// (lib/run.sh:3065-3066).
func TestStatusPassCounts(t *testing.T) {
	for _, c := range statusCases(t) {
		t.Run(c.Name, func(t *testing.T) {
			got := statusLoad(t, c)
			if got.Pass != c.Want.Pass {
				t.Errorf("pass = %d, want %d (%s)", got.Pass, c.Want.Pass, c.Shell)
			}
			if got.MaxPass != c.Want.MaxPass {
				t.Errorf("max pass = %d, want %d (%s)", got.MaxPass, c.Want.MaxPass, c.Shell)
			}
		})
	}
}

// TestStatusRows pins the glyph and the words on every leg row: the decision at
// lib/run.sh:3207-3267, with the absent and completed shapes at
// lib/run.sh:3354-3420.
func TestStatusRows(t *testing.T) {
	for _, c := range statusCases(t) {
		t.Run(c.Name, func(t *testing.T) {
			got := statusLoad(t, c)
			if len(got.Rows) != len(c.Want.Rows) {
				t.Fatalf("%d rows, want %d (%s)", len(got.Rows), len(c.Want.Rows), c.Shell)
			}
			for i, want := range c.Want.Rows {
				row := got.Rows[i]
				if row.Pass != want.Pass || string(row.Leg) != want.Leg {
					t.Errorf("row %d is pass %d %s, want pass %d %s",
						i, row.Pass, row.Leg, want.Pass, want.Leg)
				}
				if statusStepNames[row.Step] != want.Step {
					t.Errorf("row %d glyph is %q, want %q (%s)",
						i, statusStepNames[row.Step], want.Step, c.Shell)
				}
				if row.Description != want.Description {
					t.Errorf("row %d description =\n  %q\nwant\n  %q\n(%s)",
						i, row.Description, want.Description, c.Shell)
				}
			}
		})
	}
}

// TestStatusRowsMatchTheirRenderedText checks the fixture against itself: the
// description a row carries is the tail of the line the shell printed for it,
// so the verbatim text step 2 renders from cannot drift away from the decision
// step 1 makes.
func TestStatusRowsMatchTheirRenderedText(t *testing.T) {
	for _, c := range statusCases(t) {
		t.Run(c.Name, func(t *testing.T) {
			for i, want := range c.Want.Rows {
				if want.Description == "" {
					t.Errorf("row %d has no description at all", i)
					continue
				}
				if !strings.HasSuffix(want.Rendered, want.Description) {
					t.Errorf("row %d rendered as %q, which does not end in %q",
						i, want.Rendered, want.Description)
				}
			}
		})
	}
}

// TestStatusNext pins the NEXT section: which lines are prose and which are a
// command, and the exact text of each (lib/run.sh:3421-3665).
func TestStatusNext(t *testing.T) {
	for _, c := range statusCases(t) {
		t.Run(c.Name, func(t *testing.T) {
			got := statusLoad(t, c)
			if len(got.Next) != len(c.Want.Next) {
				t.Fatalf("%d NEXT lines, want %d (%s):\n got %v\nwant %v",
					len(got.Next), len(c.Want.Next), c.Shell, got.Next, c.Want.Next)
			}
			for i, want := range c.Want.Next {
				line := got.Next[i]
				wantCmd := want.Kind == "cmd"
				if line.Command != wantCmd || line.Text != want.Text {
					t.Errorf("NEXT line %d = {command:%v %q}, want {command:%v %q} (%s)",
						i, line.Command, line.Text, wantCmd, want.Text, c.Shell)
				}
			}
		})
	}
}

// TestStatusNextAlwaysOffersSomethingTypable pins the property the section
// exists for: never an empty section, never a bare dash, and never "nothing
// automatic" as the last word (lib/run.sh:3417-3420). Every case either carries
// a command or opens by saying there is nothing to run.
func TestStatusNextAlwaysOffersSomethingTypable(t *testing.T) {
	for _, c := range statusCases(t) {
		t.Run(c.Name, func(t *testing.T) {
			got := statusLoad(t, c)
			if len(got.Next) == 0 {
				t.Fatalf("NEXT is empty (%s)", c.Shell)
			}
			for _, line := range got.Next {
				if line.Command {
					return
				}
			}
			if !strings.HasPrefix(got.Next[0].Text, "nothing to run") {
				t.Errorf("NEXT offers no command and does not report nothing to run (%s): %v",
					c.Shell, got.Next)
			}
		})
	}
}

// TestStatusHeaderNote reads the watchdog qualifier straight off its label
// (lib/run.sh:3062-3063).
func TestStatusHeaderNote(t *testing.T) {
	for _, c := range statusCases(t) {
		t.Run(c.Name, func(t *testing.T) {
			want := ""
			for _, label := range c.Labels {
				if label == "crossrev/watchdog-retried" {
					want = "(retried once)"
				}
			}
			if got := statusLoad(t, c).Note; got != want {
				t.Errorf("note = %q, want %q", got, want)
			}
		})
	}
}

// TestStatusHeaderColour pins the four-way colour choice at lib/run.sh:3055-3060.
func TestStatusHeaderColour(t *testing.T) {
	want := map[string]ui.State{
		"converged":           ui.StateOK,
		"halted":              ui.StateWarn,
		"stopped":             ui.StateBad,
		"awaiting review":     ui.StateNeutral,
		"awaiting resolution": ui.StateNeutral,
	}
	for _, c := range statusCases(t) {
		t.Run(c.Name, func(t *testing.T) {
			got := statusLoad(t, c)
			if got.Colour != want[c.Want.State] {
				t.Errorf("colour for %q is %v, want %v", c.Want.State, got.Colour, want[c.Want.State])
			}
		})
	}
}

// TestStatusPullRequestFacts pins the fields the PULL REQUEST and LOOP sections
// print, which Load carries so step 2 does not read the forge again
// (lib/run.sh:3070-3084).
func TestStatusPullRequestFacts(t *testing.T) {
	c := statusCases(t)[0]
	got := statusLoad(t, c)
	if got.Title != "Add refresh" {
		t.Errorf("title = %q", got.Title)
	}
	if got.URL != "https://github.com/x" {
		t.Errorf("url = %q", got.URL)
	}
	if got.HeadSHA != c.HeadSHA {
		t.Errorf("head = %q, want %q", got.HeadSHA, c.HeadSHA)
	}
	if got.HeadBranch != "feature" {
		t.Errorf("head branch = %q", got.HeadBranch)
	}
	if got.ChangedFiles != 1 {
		t.Errorf("changed files = %d", got.ChangedFiles)
	}
	if got.Mode != "local" {
		t.Errorf("mode = %q", got.Mode)
	}
	if got.Author != statusAuthor {
		t.Errorf("author = %q", got.Author)
	}
	if got.MaxPassesPerCycle != c.MaxPassesPerCycle {
		t.Errorf("max passes = %d", got.MaxPassesPerCycle)
	}
	if string(got.MinFixSeverity) != c.MinFixSeverity {
		t.Errorf("min fix severity = %q", got.MinFixSeverity)
	}
	if got.Repo.String() != statusRepo || got.PR != statusPR {
		t.Errorf("subject = %s#%d", got.Repo, got.PR)
	}
}

// TestStatusRefusesAClosedPullRequest pins the one refusal cmd_status inherits
// from ctx_load: crossrev only runs on open pull requests (lib/run.sh:255-257).
func TestStatusRefusesAClosedPullRequest(t *testing.T) {
	c := statusCases(t)[0]
	head, err := core.NewRevision(c.HeadSHA)
	if err != nil {
		t.Fatalf("head revision: %v", err)
	}
	slug, err := core.ParseSlug(statusRepo)
	if err != nil {
		t.Fatalf("slug: %v", err)
	}
	s := &cycle.Status{
		Forge:    &statusForge{pr: forge.PullRequest{Number: statusPR, HeadRefOid: head, State: "CLOSED"}},
		Liveness: statusLife{},
		Now:      func() time.Time { return time.Unix(c.Now, 0) },
		Show:     statusShow(c),
	}
	_, err = s.Load(context.Background(), slug, statusPR)
	if err == nil {
		t.Fatal("a closed pull request loaded without a refusal")
	}
	if !strings.Contains(err.Error(), "is not open") {
		t.Errorf("refusal = %q, want it to say the pull request is not open", err)
	}
}

// TestStatusReadsOnlyTheTrustedAuthorsMarkers pins the filter markersFromComments
// applies (lib/state.sh:56-63): a marker somebody else wrote is not loop state.
func TestStatusReadsOnlyTheTrustedAuthorsMarkers(t *testing.T) {
	var c statusCase
	for _, candidate := range statusCases(t) {
		if candidate.Name == "converged-loop" {
			c = candidate
		}
	}
	if c.Name == "" {
		t.Fatal("the converged-loop fixture is missing")
	}
	head, err := core.NewRevision(c.HeadSHA)
	if err != nil {
		t.Fatalf("head revision: %v", err)
	}
	base, err := core.NewRevision(statusBase)
	if err != nil {
		t.Fatalf("base revision: %v", err)
	}
	slug, err := core.ParseSlug(statusRepo)
	if err != nil {
		t.Fatalf("slug: %v", err)
	}
	comments := make([]forge.IssueComment, 0, len(c.Markers))
	for i, raw := range c.Markers {
		body, err := prstate.EncodeMarker(raw)
		if err != nil {
			t.Fatalf("encoding marker %d: %v", i, err)
		}
		comments = append(comments, forge.IssueComment{
			ID: int64(9001 + i), AuthorLogin: "someone-else", Body: "Summary." + body,
		})
	}
	s := &cycle.Status{
		Forge: &statusForge{
			pr: forge.PullRequest{
				Number: statusPR, HeadRefOid: head, BaseRefOid: base, State: "OPEN",
			},
			comments: comments,
		},
		Liveness: statusLife{},
		Now:      func() time.Time { return time.Unix(c.Now, 0) },
		Show:     statusShow(c),
	}
	report, err := s.Load(context.Background(), slug, statusPR)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if report.Pass != 0 || report.MaxPass != 0 {
		t.Errorf("pass %d of max %d read off another author's markers", report.Pass, report.MaxPass)
	}
	if string(report.State) != "awaiting review" {
		t.Errorf("state = %q, want %q", report.State, "awaiting review")
	}
}

// statusLife is the injected liveness answer. Step 2 ports the real one; step 1
// only needs the seam, because the row wording is decided from the answer and
// not from how it was obtained.
type statusLife struct {
	life   cycle.Life
	detail string
}

func (l statusLife) Alive(context.Context, prstate.Marker) (cycle.Life, string) {
	return l.life, l.detail
}

// statusForge answers the three reads status makes and refuses everything else,
// so a read this command should not be making shows up as a panic rather than
// as a silently empty answer.
type statusForge struct {
	pr       forge.PullRequest
	prErr    error
	comments []forge.IssueComment
}

func (f *statusForge) RepoSlug(context.Context) (core.Slug, error) {
	return core.ParseSlug(statusRepo)
}

func (f *statusForge) PullRequest(context.Context, core.Slug, int) (forge.PullRequest, error) {
	if f.prErr != nil {
		return forge.PullRequest{}, f.prErr
	}
	return f.pr, nil
}

func (f *statusForge) IssueComments(context.Context, core.Slug, int) []forge.IssueComment {
	return f.comments
}

func (f *statusForge) ViewerLogin(context.Context) (string, error) { return statusAuthor, nil }
func (f *statusForge) AwaitingPullRequests(context.Context, core.Slug) []forge.AwaitingPullRequest {
	return nil
}

func (f *statusForge) DefaultBranch(context.Context, core.Slug) string { return "main" }

func (f *statusForge) PullRequestDiff(context.Context, core.Slug, core.Revision, core.Revision) ([]byte, error) {
	panic("status does not read the diff")
}

func (f *statusForge) PullRequestLabels(context.Context, core.Slug, int) []string {
	panic("status reads the labels off the pull request it already loaded")
}

func (f *statusForge) ReviewThreads(context.Context, core.Slug, int) []forge.ReviewThread {
	panic("status does not read review threads")
}

func (f *statusForge) ReviewComments(context.Context, core.Slug, int) []forge.IssueComment {
	panic("status does not read review comments")
}

func (f *statusForge) RepoIssueComments(context.Context, core.Slug, time.Time, int) ([]forge.IssueComment, error) {
	panic("status does not read the repository's comments")
}

func (f *statusForge) WorkflowRunStatus(context.Context, core.Slug, string) forge.RunStatus {
	panic("the liveness seam answers this, not the report")
}

func (f *statusForge) LabelColour(context.Context, core.Slug, string) string {
	panic("status mints no labels")
}

func (f *statusForge) IssueByFinding(context.Context, core.Slug, string, core.FindingID) (int, bool) {
	panic("status files no issues")
}

func (f *statusForge) IssueCandidates(context.Context, core.Slug, string, string) []forge.IssueCandidate {
	panic("status files no issues")
}

func (f *statusForge) CommentCreate(context.Context, core.Slug, int, string) (int64, error) {
	panic("status writes nothing")
}

func (f *statusForge) CommentEdit(context.Context, core.Slug, int64, string) error {
	panic("status writes nothing")
}

func (f *statusForge) ReviewCommentCreate(context.Context, forge.ReviewComment) (forge.Placement, error) {
	panic("status writes nothing")
}

func (f *statusForge) ReviewReply(context.Context, core.Slug, int, int64, string) error {
	panic("status writes nothing")
}

func (f *statusForge) ThreadResolve(context.Context, string) error {
	panic("status writes nothing")
}

func (f *statusForge) LabelEnsure(context.Context, core.Slug, forge.Label) (forge.LabelState, error) {
	panic("status writes nothing")
}

func (f *statusForge) IssueCreate(context.Context, core.Slug, string, string, []string) (int, error) {
	panic("status writes nothing")
}

func (f *statusForge) IssueCommentCreate(context.Context, core.Slug, int, string) {
	panic("status writes nothing")
}

func (f *statusForge) PullRequestLabelAdd(context.Context, core.Slug, int, string) error {
	panic("status writes nothing")
}

func (f *statusForge) PullRequestLabelRemove(context.Context, core.Slug, int, string) {
	panic("status writes nothing")
}

var _ forge.Forge = (*statusForge)(nil)
var _ cycle.Liveness = statusLife{}
