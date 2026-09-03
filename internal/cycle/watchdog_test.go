package cycle_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/cycle"
	"github.com/carlosboeing/crossrev/internal/forge"
	"github.com/carlosboeing/crossrev/internal/ui"
)

// watchdogCase is one row of the table, read from internal/cycle/testdata/watchdog.
//
// Nothing under `want` is written by hand. Each fixture was recorded by running
// the shipped cmd_watchdog with lib/*.sh sourced, a frozen clock, and the two
// reads it makes answered with the payload GitHub would return — so the real
// --jq filters in lib/run.sh:3708-3709 and lib/state.sh:62 ran. `page` is the
// bytes it printed with stdout on a file, and `calls` is the four write
// functions and the comment read it called, in order, named for the forge
// operation each one is.
//
// `shell` names the case in tests/test-watchdog.sh the fixture stands for, and
// is empty for the fixtures that cover ground that suite does not.
type watchdogCase struct {
	Name     string            `json:"name"`
	Shell    string            `json:"shell"`
	Repo     string            `json:"repo"`
	Author   string            `json:"author"`
	Now      int64             `json:"now"`
	Timeout  int               `json:"timeout_seconds"`
	Waiting  []watchdogWaiting `json:"waiting"`
	Comments []watchdogComment `json:"comments"`
	Want     watchdogWant      `json:"want"`
}

type watchdogWaiting struct {
	PR     int      `json:"pr"`
	Labels []string `json:"labels"`
	Head   string   `json:"head"`
	Draft  bool     `json:"draft"`
}

type watchdogComment struct {
	ID     int64  `json:"id"`
	Author string `json:"author"`
	Body   string `json:"body"`
}

type watchdogWant struct {
	Page    string          `json:"page"`
	Calls   []string        `json:"calls"`
	Summary watchdogSummary `json:"summary"`
}

type watchdogSummary struct {
	Checked int `json:"checked"`
	Retried int `json:"retried"`
	Halted  int `json:"halted"`
	Drafts  int `json:"drafts"`
}

func watchdogCases(t *testing.T) []watchdogCase {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join("testdata", "watchdog", "*.json"))
	if err != nil {
		t.Fatalf("globbing the watchdog fixtures: %v", err)
	}
	if len(paths) == 0 {
		t.Fatal("no watchdog fixtures found")
	}
	cases := make([]watchdogCase, 0, len(paths))
	for _, path := range paths {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		var c watchdogCase
		if err := json.Unmarshal(body, &c); err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		cases = append(cases, c)
	}
	return cases
}

// watchdogRun drives the real sweep for one fixture. Nothing is substituted: the
// repository, the trusted author, the clock, the timeout, the pull requests and
// the comments all come off the fixture, and the forge answers from there.
func watchdogRun(t *testing.T, c watchdogCase) (*watchdogForge, *bytes.Buffer, cycle.Summary, error) {
	t.Helper()
	slug, err := core.ParseSlug(c.Repo)
	if err != nil {
		t.Fatalf("%s: slug: %v", c.Name, err)
	}
	comments := make([]forge.IssueComment, 0, len(c.Comments))
	for _, comment := range c.Comments {
		comments = append(comments, forge.IssueComment{
			ID:          comment.ID,
			Body:        comment.Body,
			AuthorLogin: comment.Author,
			CreatedAt:   "2026-08-11T00:00:00Z",
		})
	}
	f := &watchdogForge{comments: comments}
	var out bytes.Buffer
	w := &cycle.Watchdog{
		Forge:   f,
		Now:     func() time.Time { return time.Unix(c.Now, 0) },
		Out:     &ui.IO{Out: &out},
		Timeout: time.Duration(c.Timeout) * time.Second,
		Author:  c.Author,
	}
	waiting := make([]cycle.Waiting, 0, len(c.Waiting))
	for _, w := range c.Waiting {
		waiting = append(waiting, cycle.Waiting{PR: w.PR, Labels: w.Labels, HeadSHA: w.Head, Draft: w.Draft})
	}
	summary, err := w.Run(context.Background(), slug, waiting)
	return f, &out, summary, err
}

// TestWatchdogPrintsThePageTheShellPrints pins every printed byte of the sweep
// against the shell's own output (lib/run.sh:3711-3762): the section heading,
// the per-pull-request line and its glyph, the retry and halt lines indented
// under it, the blank rule, the counted summary and the closing line with the
// blank line ui_end leaves after it (lib/ui.sh:81).
func TestWatchdogPrintsThePageTheShellPrints(t *testing.T) {
	for _, c := range watchdogCases(t) {
		t.Run(c.Name, func(t *testing.T) {
			_, out, _, err := watchdogRun(t, c)
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if got := out.String(); got != c.Want.Page {
				t.Errorf("page mismatch\n--- got ---\n%q\n--- want ---\n%q", got, c.Want.Page)
			}
		})
	}
}

// TestWatchdogWritesWhatTheShellWrites pins the forge calls, in order. The order
// is the mechanism: re-applying a label GitHub already holds fires no event, so
// the retry removes it before adding it back (lib/run.sh:3787-3792), and the
// halt removes the awaiting label before it labels and comments
// (lib/run.sh:3771-3780).
func TestWatchdogWritesWhatTheShellWrites(t *testing.T) {
	for _, c := range watchdogCases(t) {
		t.Run(c.Name, func(t *testing.T) {
			f, _, _, err := watchdogRun(t, c)
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if got := strings.Join(f.calls, "\n"); got != strings.Join(c.Want.Calls, "\n") {
				t.Errorf("call transcript mismatch\n--- got ---\n%s\n--- want ---\n%s",
					got, strings.Join(c.Want.Calls, "\n"))
			}
		})
	}
}

// TestWatchdogCounts pins the three counters the summary line reports. A pull
// request carrying crossrev/stop is counted as checked and acted on in neither
// direction, because lib/run.sh:3720 increments before lib/run.sh:3722 skips.
func TestWatchdogCounts(t *testing.T) {
	for _, c := range watchdogCases(t) {
		t.Run(c.Name, func(t *testing.T) {
			_, _, summary, err := watchdogRun(t, c)
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			want := cycle.Summary{
				Checked: c.Want.Summary.Checked,
				Retried: c.Want.Summary.Retried,
				Halted:  c.Want.Summary.Halted,
				Drafts:  c.Want.Summary.Drafts,
			}
			if summary != want {
				t.Errorf("summary = %+v, want %+v", summary, want)
			}
		})
	}
}

// TestWatchdogRetriesOnceThenHalts is the whole mechanism in one sequence: the
// first sweep re-fires the label and records that it did, and the second sweep
// reads that record and halts instead of retrying again (lib/run.sh:3770).
//
// The second attempt writes no awaiting label back, which is what makes the halt
// terminal rather than another retry.
func TestWatchdogRetriesOnceThenHalts(t *testing.T) {
	const head = "d81a3f2abc0000000000000000000000000000ab"
	f := &watchdogForge{comments: []forge.IssueComment{
		watchdogMarker(t, 9001, watchdogAuthor, core.LegReview, watchdogNow-3600, head),
	}}
	var out bytes.Buffer
	w := watchdogSweeper(f, &out, 30*time.Minute)

	first := []cycle.Waiting{{PR: 42, Labels: []string{"crossrev/awaiting-review"}, HeadSHA: head}}
	if _, err := w.Run(context.Background(), watchdogSlug(t), first); err != nil {
		t.Fatalf("first sweep: %v", err)
	}
	wantFirst := []string{
		"issue-comments 42",
		"label-ensure crossrev/watchdog-retried fbca04 crossrev: the watchdog retried this leg once",
		"label-add 42 crossrev/watchdog-retried",
		"label-remove 42 crossrev/awaiting-review",
		"label-add 42 crossrev/awaiting-review",
	}
	if got := strings.Join(f.calls, "\n"); got != strings.Join(wantFirst, "\n") {
		t.Fatalf("first sweep transcript\n--- got ---\n%s\n--- want ---\n%s", got, strings.Join(wantFirst, "\n"))
	}

	// The bookkeeping label the first sweep applied is now on the pull
	// request, which is the only thing that differs on the second.
	f.calls = nil
	out.Reset()
	second := []cycle.Waiting{{PR: 42, Labels: []string{"crossrev/awaiting-review", "crossrev/watchdog-retried"}, HeadSHA: head}}
	summary, err := w.Run(context.Background(), watchdogSlug(t), second)
	if err != nil {
		t.Fatalf("second sweep: %v", err)
	}
	if summary != (cycle.Summary{Checked: 1, Retried: 0, Halted: 1}) {
		t.Errorf("second sweep summary = %+v, want one halt", summary)
	}
	for _, call := range f.calls {
		if call == "label-add 42 crossrev/awaiting-review" {
			t.Error("the halt re-applied the awaiting label, so the loop would keep firing")
		}
	}
	if got := f.calls[len(f.calls)-1]; !strings.HasPrefix(got, "comment-create 42 ") {
		t.Errorf("last write on the halt = %q, want the comment", got)
	}
}

// TestWatchdogHaltComment pins the comment body byte for byte
// (lib/run.sh:3775-3780). It names the leg, refuses to read as a verdict on the
// code, and gives both the command to look and the labels to remove to restart.
func TestWatchdogHaltComment(t *testing.T) {
	const want = "**crossrev halted** — the resolve leg was already retried once and is still not finishing.\n" +
		"\n" +
		"The last marker on this pull request records how far it got. Nothing here is a judgement about the code: the loop stopped, it did not converge.\n" +
		"\n" +
		"To look yourself: `crossrev status --pr 42`. To restart it, remove `crossrev/halted` and `crossrev/watchdog-retried`, then apply `crossrev/awaiting-resolution`."

	var found string
	var seen bool
	for _, c := range watchdogCases(t) {
		if c.Name != "already-retried-halts" {
			continue
		}
		seen = true
		f, _, _, err := watchdogRun(t, c)
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		for _, call := range f.calls {
			if strings.HasPrefix(call, "comment-create 42 ") {
				found = strings.TrimPrefix(call, "comment-create 42 ")
			}
		}
	}
	if !seen {
		t.Fatal("the already-retried-halts fixture is gone")
	}
	if found == "" {
		t.Fatal("the halt posted no comment")
	}
	if found != want {
		t.Errorf("halt comment\n--- got ---\n%s\n--- want ---\n%s", found, want)
	}
}

// TestWatchdogWithoutATrustedAuthorRefuses pins that a sweep with no trusted
// author stops rather than running. With no author every marker filters out, so
// a sweep that carried on would read every pull request as never started and
// re-fire the whole repository. Bash cannot reach that state: it resolves the
// author inside the loop at lib/run.sh:3737 and dies there (lib/state.sh:37-39).
// Here the author is an input, so the refusal is the same words, earlier.
func TestWatchdogWithoutATrustedAuthorRefuses(t *testing.T) {
	const head = "d81a3f2abc0000000000000000000000000000ab"
	f := &watchdogForge{comments: []forge.IssueComment{
		watchdogMarker(t, 9001, watchdogAuthor, core.LegReview, watchdogNow, head),
	}}
	var out, errOut bytes.Buffer
	w := &cycle.Watchdog{
		Forge:   f,
		Now:     func() time.Time { return time.Unix(watchdogNow, 0) },
		Out:     &ui.IO{Out: &out, Err: &errOut},
		Timeout: 30 * time.Minute,
	}
	_, err := w.Run(context.Background(), watchdogSlug(t), []cycle.Waiting{
		{PR: 42, Labels: []string{"crossrev/awaiting-review"}, HeadSHA: head},
	})
	var fatal *ui.FatalError
	if !errors.As(err, &fatal) {
		t.Fatalf("err = %v, want a ui.FatalError", err)
	}
	if fatal.Reason != "cannot determine which App's markers to trust" {
		t.Errorf("reason = %q", fatal.Reason)
	}
	if len(f.calls) != 0 {
		t.Errorf("the refused sweep still called the forge: %v", f.calls)
	}
}

// TestWatchdogWriteFailureStopsTheSweep pins that a write that did not land
// stops the sweep where it failed, rather than carrying on to the next one.
// state_label_add dies at lib/state.sh:429-434 and gh_comment_create at
// lib/github.sh:192, and ui_die exits the process (lib/ui.sh:118). The chain is
// label-driven, so carrying on would report a retry that fired no event, or a
// halt whose comment nobody can read.
func TestWatchdogWriteFailureStopsTheSweep(t *testing.T) {
	const head = "d81a3f2abc0000000000000000000000000000ab"
	cases := []struct {
		name string
		// neverStarted drops the marker, so the sweep reaches retry down
		// the "no marker at all" arm rather than the past-timeout one.
		neverStarted bool
		labels       []string
		failAdd      string
		failCreate   bool
		want         []string
	}{
		{
			name:         "the leg never started and the bookkeeping label does not land",
			neverStarted: true,
			labels:       []string{"crossrev/awaiting-review"},
			failAdd:      "crossrev/watchdog-retried",
			want: []string{
				"issue-comments 42",
				"label-ensure crossrev/watchdog-retried fbca04 crossrev: the watchdog retried this leg once",
			},
		},
		{
			name:    "the bookkeeping label does not land",
			labels:  []string{"crossrev/awaiting-review"},
			failAdd: "crossrev/watchdog-retried",
			want: []string{
				"issue-comments 42",
				"label-ensure crossrev/watchdog-retried fbca04 crossrev: the watchdog retried this leg once",
			},
		},
		{
			name:    "the re-fired awaiting label does not land",
			labels:  []string{"crossrev/awaiting-review"},
			failAdd: "crossrev/awaiting-review",
			want: []string{
				"issue-comments 42",
				"label-ensure crossrev/watchdog-retried fbca04 crossrev: the watchdog retried this leg once",
				"label-add 42 crossrev/watchdog-retried",
				"label-remove 42 crossrev/awaiting-review",
			},
		},
		{
			name:    "the halted label does not land, so no comment is posted",
			labels:  []string{"crossrev/awaiting-review", "crossrev/watchdog-retried"},
			failAdd: "crossrev/halted",
			want: []string{
				"issue-comments 42",
				"label-remove 42 crossrev/awaiting-review",
				"label-ensure crossrev/halted bc4c00 crossrev: stopped short, a human is needed",
			},
		},
		{
			name:       "the halt comment does not post",
			labels:     []string{"crossrev/awaiting-review", "crossrev/watchdog-retried"},
			failCreate: true,
			want: []string{
				"issue-comments 42",
				"label-remove 42 crossrev/awaiting-review",
				"label-ensure crossrev/halted bc4c00 crossrev: stopped short, a human is needed",
				"label-add 42 crossrev/halted",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &watchdogForge{failAdd: tc.failAdd, failCreate: tc.failCreate}
			if !tc.neverStarted {
				f.comments = []forge.IssueComment{
					watchdogMarker(t, 9001, watchdogAuthor, core.LegReview, watchdogNow-3600, head),
				}
			}
			var out bytes.Buffer
			w := watchdogSweeper(f, &out, 30*time.Minute)
			summary, err := w.Run(context.Background(), watchdogSlug(t), []cycle.Waiting{
				{PR: 42, Labels: tc.labels, HeadSHA: head},
			})
			if err == nil {
				t.Fatal("a failed write was not reported")
			}
			if got := strings.Join(f.calls, "\n"); got != strings.Join(tc.want, "\n") {
				t.Errorf("the sweep kept writing\n--- got ---\n%s\n--- want ---\n%s",
					got, strings.Join(tc.want, "\n"))
			}
			if summary.Retried != 0 || summary.Halted != 0 {
				t.Errorf("summary = %+v, want neither counted", summary)
			}
			if strings.Contains(out.String(), "retried by re-firing") {
				t.Error("the sweep claimed a retry it did not make")
			}
			if strings.Contains(out.String(), "halted — it had already been retried once") {
				t.Error("the sweep claimed a halt it did not complete")
			}
		})
	}
}

// TestWatchdogDefaultTimeout pins the 1800 seconds lib/run.sh:3682 defaults the
// flag to. A zero Timeout must not read as "everything is past its timeout".
func TestWatchdogDefaultTimeout(t *testing.T) {
	const head = "d81a3f2abc0000000000000000000000000000ab"
	f := &watchdogForge{comments: []forge.IssueComment{
		watchdogMarker(t, 9001, watchdogAuthor, core.LegReview, watchdogNow-60, head),
	}}
	var out bytes.Buffer
	w := watchdogSweeper(f, &out, 0)
	if _, err := w.Run(context.Background(), watchdogSlug(t), []cycle.Waiting{
		{PR: 42, Labels: []string{"crossrev/awaiting-review"}, HeadSHA: head},
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	const want = "│  ○ #42 — waiting on the review leg, 1 minute(s) in, inside the 30-minute timeout\n"
	if got := watchdogBodyLine(out.String()); got != want {
		t.Errorf("line = %q, want %q", got, want)
	}
	if len(f.calls) != 1 {
		t.Errorf("a pull request inside its timeout was written to: %v", f.calls)
	}
}

// TestWatchdogTimeoutRefusalWaitsForTheComparison pins where a `--timeout` that
// is not a number stops the sweep.
//
// Bash keeps the flag as a string (lib/run.sh:3686) and reads it only at
// `(( age < timeout ))` (lib/run.sh:3747), which three shapes never reach: no
// pull request waiting, one carrying crossrev/stop, and one with no marker
// behind its label. Measured against bin/crossrev with `--timeout abc`: exit 0
// for each of those, exit 1 for a pull request that is waiting with a marker.
// Raising the refusal any earlier turns all four into exit 1.
func TestWatchdogTimeoutRefusalWaitsForTheComparison(t *testing.T) {
	const head = "d81a3f2abc0000000000000000000000000000ab"
	refusal := errors.New("--timeout must be a number of seconds, and it was: abc")

	marker := watchdogMarker(t, 9001, watchdogAuthor, core.LegReview, watchdogNow-60, head)

	for _, tc := range []struct {
		name     string
		comments []forge.IssueComment
		waiting  []cycle.Waiting
		wantErr  bool
	}{
		{
			name:    "nothing waiting never reads it",
			waiting: nil,
		},
		{
			name:     "a stopped pull request never reads it",
			comments: []forge.IssueComment{marker},
			waiting: []cycle.Waiting{
				{PR: 42, Labels: []string{"crossrev/awaiting-review", "crossrev/stop"}, HeadSHA: head},
			},
		},
		{
			name:    "a label with no marker behind it never reads it",
			waiting: []cycle.Waiting{{PR: 42, Labels: []string{"crossrev/awaiting-review"}, HeadSHA: head}},
		},
		{
			name:     "a waiting pull request with a marker does",
			comments: []forge.IssueComment{marker},
			waiting:  []cycle.Waiting{{PR: 42, Labels: []string{"crossrev/awaiting-review"}, HeadSHA: head}},
			wantErr:  true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &watchdogForge{comments: tc.comments}
			var out bytes.Buffer
			w := watchdogSweeper(f, &out, 0)
			w.TimeoutRefusal = refusal

			_, err := w.Run(context.Background(), watchdogSlug(t), tc.waiting)

			switch {
			case tc.wantErr && !errors.Is(err, refusal):
				t.Errorf("Run = %v, want the held refusal", err)
			case !tc.wantErr && err != nil:
				t.Errorf("Run = %v, want the sweep to finish without reading the timeout", err)
			}
			if !tc.wantErr && !strings.Contains(out.String(), "waiting on a leg — retried") {
				t.Errorf("output = %q, want the closing summary", out.String())
			}
		})
	}
}

// TestWatchdogEveryShellCaseIsCovered guards against a fixture that loads and
// asserts nothing: every case in tests/test-watchdog.sh has to be represented.
func TestWatchdogEveryShellCaseIsCovered(t *testing.T) {
	want := []string{
		"a leg still inside its timeout is left alone",
		"a leg past its timeout is retried exactly once",
		"a second failure halts and says why",
		"a leg that never started at all",
		"crossrev/stop is honoured here too",
		"a forged marker does not fool the watchdog",
	}
	seen := map[string]bool{}
	for _, c := range watchdogCases(t) {
		seen[c.Shell] = true
	}
	for _, name := range want {
		if !seen[name] {
			t.Errorf("no fixture stands for the shell case %q", name)
		}
	}
}

// --- helpers ---------------------------------------------------------------

const (
	watchdogRepo   = "acme/widget"
	watchdogAuthor = "crossrev-acme[bot]"
	watchdogNow    = int64(1755400000)
)

func watchdogSlug(t *testing.T) core.Slug {
	t.Helper()
	slug, err := core.ParseSlug(watchdogRepo)
	if err != nil {
		t.Fatalf("slug: %v", err)
	}
	return slug
}

func watchdogSweeper(f *watchdogForge, out *bytes.Buffer, timeout time.Duration) *cycle.Watchdog {
	return &cycle.Watchdog{
		Forge:   f,
		Now:     func() time.Time { return time.Unix(watchdogNow, 0) },
		Out:     &ui.IO{Out: out},
		Timeout: timeout,
		Author:  watchdogAuthor,
	}
}

// watchdogMarker is one started marker on a comment, encoded the way the loop
// writes it. Nothing here is defaulted: every field the watchdog reads is an
// argument.
func watchdogMarker(t *testing.T, id int64, author string, leg core.Leg, ts int64, head string) forge.IssueComment {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"v": 1, "leg": string(leg), "pass": 1, "state": "started",
		"ts": ts, "run_id": "1", "head_sha": head,
		"harness": "claude", "model": "m", "model_reported": "m",
	})
	if err != nil {
		t.Fatalf("marker: %v", err)
	}
	return forge.IssueComment{
		ID:          id,
		Body:        "Summary.\n\n<!-- crossrev: " + string(raw) + " -->",
		AuthorLogin: author,
		CreatedAt:   "2026-08-11T00:00:00Z",
	}
}

// watchdogBodyLine is the first per-pull-request line of a page: the one after
// the section heading and before the summary rule.
func watchdogBodyLine(page string) string {
	for _, line := range strings.SplitAfter(page, "\n") {
		if strings.HasPrefix(line, "│  ○ ") || strings.HasPrefix(line, "│  ✗ ") {
			return line
		}
	}
	return ""
}

// watchdogForge records every call in the order it was made, and answers reads
// from one fixed comment list. The names match the forge operations rather than
// the gh argv, and the fixtures were recorded under the same names.
type watchdogForge struct {
	comments []forge.IssueComment
	calls    []string
	// failAdd is the one label whose add is refused, and failCreate refuses
	// the comment. Neither is recorded: a write that failed is a write the
	// forge did not make.
	failAdd    string
	failCreate bool
}

func (f *watchdogForge) record(format string, args ...any) {
	f.calls = append(f.calls, fmt.Sprintf(format, args...))
}

func (f *watchdogForge) IssueComments(_ context.Context, _ core.Slug, pr int) []forge.IssueComment {
	f.record("issue-comments %d", pr)
	return f.comments
}

func (f *watchdogForge) LabelEnsure(_ context.Context, _ core.Slug, label forge.Label) (forge.LabelState, error) {
	f.record("label-ensure %s %s %s", label.Name, label.Colour, label.Description)
	return forge.LabelCreated, nil
}

func (f *watchdogForge) PullRequestLabelAdd(_ context.Context, _ core.Slug, pr int, label string) error {
	if f.failAdd != "" && f.failAdd == label {
		return errors.New("labels: 403")
	}
	f.record("label-add %d %s", pr, label)
	return nil
}

func (f *watchdogForge) PullRequestLabelRemove(_ context.Context, _ core.Slug, pr int, label string) {
	f.record("label-remove %d %s", pr, label)
}

func (f *watchdogForge) CommentCreate(_ context.Context, _ core.Slug, pr int, body string) (int64, error) {
	if f.failCreate {
		return 0, errors.New("comments: 403")
	}
	f.record("comment-create %d %s", pr, body)
	return 1, nil
}

func (f *watchdogForge) RepoSlug(context.Context) (core.Slug, error) {
	return core.ParseSlug(watchdogRepo)
}
func (f *watchdogForge) DefaultBranch(context.Context, core.Slug) string { return "main" }
func (f *watchdogForge) PullRequest(context.Context, core.Slug, int) (forge.PullRequest, error) {
	return forge.PullRequest{}, errors.New("the watchdog reads no pull request")
}
func (f *watchdogForge) PullRequestDiff(context.Context, core.Slug, core.Revision, core.Revision) ([]byte, error) {
	return nil, nil
}
func (f *watchdogForge) PullRequestLabels(context.Context, core.Slug, int) []string { return nil }
func (f *watchdogForge) ReviewThreads(context.Context, core.Slug, int) []forge.ReviewThread {
	return nil
}
func (f *watchdogForge) ReviewComments(context.Context, core.Slug, int) []forge.IssueComment {
	return nil
}
func (f *watchdogForge) RepoIssueComments(context.Context, core.Slug, time.Time, int) ([]forge.IssueComment, error) {
	return nil, nil
}
func (f *watchdogForge) ViewerLogin(context.Context) (string, error) { return watchdogAuthor, nil }
func (f *watchdogForge) AwaitingPullRequests(context.Context, core.Slug) []forge.AwaitingPullRequest {
	return nil
}
func (f *watchdogForge) WorkflowRunStatus(context.Context, core.Slug, string) forge.RunStatus {
	return ""
}
func (f *watchdogForge) LabelColour(context.Context, core.Slug, string) string { return "" }
func (f *watchdogForge) IssueByFinding(context.Context, core.Slug, string, core.FindingID) (int, bool) {
	return 0, false
}
func (f *watchdogForge) IssueCandidates(context.Context, core.Slug, string, string) []forge.IssueCandidate {
	return nil
}
func (f *watchdogForge) CommentEdit(context.Context, core.Slug, int64, string) error { return nil }
func (f *watchdogForge) ReviewCommentCreate(context.Context, forge.ReviewComment) (forge.Placement, error) {
	return "", nil
}
func (f *watchdogForge) ReviewReply(context.Context, core.Slug, int, int64, string) error { return nil }
func (f *watchdogForge) ThreadResolve(context.Context, string) error                      { return nil }
func (f *watchdogForge) IssueCreate(context.Context, core.Slug, string, string, []string) (int, error) {
	return 0, nil
}
func (f *watchdogForge) IssueCommentCreate(context.Context, core.Slug, int, string) {}

var _ forge.Forge = (*watchdogForge)(nil)

// TestWatchdogReportsADraftRatherThanRetryingIt pins the branch at
// lib/run.sh:3755-3763.
//
// A draft is waiting on its author, not on a leg: every automatic invocation
// refuses it and the review workflow's job condition skips before that runs, so
// re-firing the awaiting label meets the same refusal and the sweep after it
// would report a leg that "is still not finishing" — a leg that never started.
// Both legs are covered because the resolve half is the one that pushes, and so
// is a pull request an older version already retried, whose watchdog-retried
// label must not turn the report into a halt.
func TestWatchdogReportsADraftRatherThanRetryingIt(t *testing.T) {
	const head = "d81a3f2abc0000000000000000000000000000ab"
	for _, tc := range []struct {
		name    string
		labels  []string
		wantOpt string
		wantCmd string
	}{
		{
			name:    "awaiting review",
			labels:  []string{"crossrev/awaiting-review"},
			wantOpt: "│  ○ #42 — a draft pull request, so no automatic review leg runs on it\n",
			wantCmd: "│     mark it ready for review, or run `crossrev review --pr 42` yourself\n",
		},
		{
			name:    "awaiting resolution",
			labels:  []string{"crossrev/awaiting-resolution"},
			wantOpt: "│  ○ #42 — a draft pull request, so no automatic resolve leg runs on it\n",
			wantCmd: "│     mark it ready for review, or run `crossrev resolve --pr 42` yourself\n",
		},
		{
			name:    "already retried once",
			labels:  []string{"crossrev/awaiting-review", "crossrev/watchdog-retried"},
			wantOpt: "│  ○ #42 — a draft pull request, so no automatic review leg runs on it\n",
			wantCmd: "│     mark it ready for review, or run `crossrev review --pr 42` yourself\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &watchdogForge{comments: []forge.IssueComment{
				watchdogMarker(t, 9001, watchdogAuthor, core.LegReview, watchdogNow-3600, head),
			}}
			var out bytes.Buffer
			w := watchdogSweeper(f, &out, 30*time.Minute)

			summary, err := w.Run(context.Background(), watchdogSlug(t), []cycle.Waiting{
				{PR: 42, Labels: tc.labels, HeadSHA: head, Draft: true},
			})
			if err != nil {
				t.Fatalf("Run: %v", err)
			}

			page := out.String()
			if !strings.Contains(page, tc.wantOpt) {
				t.Errorf("page = %q, want the draft line %q", page, tc.wantOpt)
			}
			if !strings.Contains(page, tc.wantCmd) {
				t.Errorf("page = %q, want the recovery line %q", page, tc.wantCmd)
			}
			if want := "checked 1 pull request(s) waiting on a leg — retried 0, halted 0, 1 in draft\n"; !strings.Contains(page, want) {
				t.Errorf("page = %q, want the summary %q", page, want)
			}
			if summary != (cycle.Summary{Checked: 1, Drafts: 1}) {
				t.Errorf("summary = %+v, want one checked and one draft", summary)
			}
			// The draft is decided before the markers are read, so the
			// sweep makes no forge call at all for it — not the comment
			// read, and none of the label writes a retry or a halt makes.
			if len(f.calls) != 0 {
				t.Errorf("a draft was written to or read for: %v", f.calls)
			}
			if strings.Contains(page, "retried by re-firing") || strings.Contains(page, "halted — it had already been retried once") {
				t.Errorf("page = %q, want neither a retry nor a halt", page)
			}
		})
	}
}

// TestWatchdogCountsDraftsApartFromTheRest pins the third counter: a sweep over
// one draft and one stuck pull request retries the second and reports the first,
// and the closing line names all three numbers (lib/run.sh:3761).
func TestWatchdogCountsDraftsApartFromTheRest(t *testing.T) {
	const head = "d81a3f2abc0000000000000000000000000000ab"
	f := &watchdogForge{comments: []forge.IssueComment{
		watchdogMarker(t, 9001, watchdogAuthor, core.LegReview, watchdogNow-3600, head),
	}}
	var out bytes.Buffer
	w := watchdogSweeper(f, &out, 30*time.Minute)

	summary, err := w.Run(context.Background(), watchdogSlug(t), []cycle.Waiting{
		{PR: 42, Labels: []string{"crossrev/awaiting-review"}, HeadSHA: head, Draft: true},
		{PR: 43, Labels: []string{"crossrev/awaiting-review"}, HeadSHA: head},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if summary != (cycle.Summary{Checked: 2, Retried: 1, Drafts: 1}) {
		t.Errorf("summary = %+v", summary)
	}
	want := "checked 2 pull request(s) waiting on a leg — retried 1, halted 0, 1 in draft\n"
	if !strings.Contains(out.String(), want) {
		t.Errorf("page = %q, want the summary %q", out.String(), want)
	}
}
