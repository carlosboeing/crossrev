package resolve

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/exec"
	"github.com/carlosboeing/crossrev/internal/forge"
	"github.com/carlosboeing/crossrev/internal/harness"
	"github.com/carlosboeing/crossrev/internal/prstate"
	"github.com/carlosboeing/crossrev/internal/runlog"
	"github.com/carlosboeing/crossrev/internal/vcs"
)

const (
	testHeadSHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testBaseSHA = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	testFinding = "aaaaaaaaaaaaaaaa"
)

func mustSlug(t *testing.T) core.Slug {
	t.Helper()
	s, err := core.ParseSlug("acme/widget")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func mustRev(t *testing.T, sha string) core.Revision {
	t.Helper()
	r, err := core.NewRevision(sha)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func mustHarness(t *testing.T) harness.Document {
	t.Helper()
	doc, err := harness.Load(harness.DescriptorJSON())
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

type testEnv struct {
	forge   *fakeForge
	git     *fakeGit
	runner  *recordingRunner
	adapter *stubAdapter
	log     *runlog.Log
	now     time.Time
	slug    core.Slug
	head    core.Revision
	base    core.Revision
	workdir string
}

func setup(t *testing.T) *testEnv {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("GITHUB_RUN_ID", "test-run")

	slug := mustSlug(t)
	head := mustRev(t, testHeadSHA)
	base := mustRev(t, testBaseSHA)
	workdir := t.TempDir()
	now := time.Unix(1_700_000_000, 0).UTC()

	log, err := runlog.Open(runlog.Options{
		Dir: filepath.Join(t.TempDir(), "run"),
		Now: func() time.Time { return now },
		Leg: "resolve",
		PR:  "42",
	})
	if err != nil {
		t.Fatal(err)
	}

	pr := forge.PullRequest{
		Number:      42,
		Title:       "widget",
		HeadRefName: "feature",
		HeadRefOid:  head,
		BaseRefName: "main",
		BaseRefOid:  base,
		State:       "OPEN",
		Labels:      []forge.Label{{Name: "crossrev/awaiting-resolution"}},
	}

	env := &testEnv{
		forge: &fakeForge{
			slug:   slug,
			pr:     pr,
			viewer: "tester",
		},
		git: &fakeGit{
			dir:          workdir,
			head:         head,
			worktrees:    new([]string),
			captureCalls: new(int),
			restoreCalls: new(int),
		},
		runner:  &recordingRunner{},
		adapter: &stubAdapter{payloads: []json.RawMessage{oneFindingPayload()}},
		log:     log,
		now:     now,
		slug:    slug,
		head:    head,
		base:    base,
		workdir: workdir,
	}
	env.git.env = env
	env.forge.env = env
	return env
}

func (e *testEnv) run(t *testing.T) Result {
	t.Helper()
	leg := &Leg{
		Forge:   e.forge,
		Git:     e.git,
		Runner:  e.runner,
		Log:     e.log,
		Clock:   func() time.Time { return e.now },
		Env:     []string{"PATH=/usr/bin", "HOME=/tmp", "GH_TOKEN=should-not-leak"},
		Harness: mustHarness(t),
		Adapter: e.adapter,
	}
	return leg.Run(context.Background(), Request{
		PR:      42,
		Repo:    e.slug,
		Trigger: "human",
	})
}

func (e *testEnv) addReview(t *testing.T, findings json.RawMessage, verdict string) {
	t.Helper()
	if findings == nil {
		findings = json.RawMessage("[]")
	}
	m := prstate.Marker{
		Version:  core.MarkerVersion,
		Leg:      core.LegReview,
		Pass:     1,
		State:    core.PassComplete,
		TS:       e.now.Unix() - 60,
		RunID:    prstate.Some("review-run"),
		HeadSHA:  prstate.Some(e.head.SHA()),
		Harness:  prstate.Some("codex"),
		Verdict:  prstate.Some(verdict),
		Findings: findings,
	}
	e.postMarker(t, 9001, m)
}

func (e *testEnv) addResolve(t *testing.T, m prstate.Marker, id int64) {
	t.Helper()
	if m.Pass == 0 {
		m.Pass = 1
	}
	if m.Version == 0 {
		m.Version = core.MarkerVersion
	}
	if m.Leg == "" {
		m.Leg = core.LegResolve
	}
	e.postMarker(t, id, m)
}

func (e *testEnv) postMarker(t *testing.T, id int64, m prstate.Marker) {
	t.Helper()
	encoded, err := m.Encode()
	if err != nil {
		t.Fatal(err)
	}
	e.forge.comments = append(e.forge.comments, forge.IssueComment{
		ID:          id,
		Body:        "pass comment" + encoded,
		AuthorLogin: e.forge.viewer,
		CreatedAt:   e.now.Format(time.RFC3339),
	})
}

func defaultFindings() json.RawMessage {
	return json.RawMessage(`[
  {"id":"` + testFinding + `","path":"app.ts","line":2,"side":"RIGHT","severity":"high","category":"correctness","pre_existing":false,"title":"nil deref","why":"crash","fix":"check"}
]`)
}

func oneFindingPayload() json.RawMessage {
	return json.RawMessage(`{"blocked":false,"blocked_reason":null,"summary":"Fixed it.","commit_subject":null,"resolutions":[{"finding_number":1,"resolution":"fixed","reply":"done","persist":null,"duplicate_of":null}]}`)
}

func missingPayload() json.RawMessage {
	return json.RawMessage(`{"blocked":false,"blocked_reason":null,"summary":"Oops.","resolutions":[]}`)
}

func duplicatePayload() json.RawMessage {
	return json.RawMessage(`{"blocked":false,"blocked_reason":null,"summary":"Twice.","resolutions":[{"finding_number":1,"resolution":"fixed","reply":"a","persist":null,"duplicate_of":null},{"finding_number":1,"resolution":"skipped","reply":"b","persist":null,"duplicate_of":null}]}`)
}

func shapePayload() json.RawMessage {
	return json.RawMessage(`{"blocked":false,"blocked_reason":null,"summary":"Old field.","resolutions":[{"finding_id":"` + testFinding + `","resolution":"fixed","reply":"r","persist":null,"duplicate_of":null}]}`)
}

// ---------------------------------------------------------------------------
// fakeForge
// ---------------------------------------------------------------------------

type fakeForge struct {
	env        *testEnv
	slug       core.Slug
	pr         forge.PullRequest
	viewer     string
	comments   []forge.IssueComment
	threads    []forge.ReviewThread
	candidates []forge.IssueCandidate
	created    []createdComment
	edits      []editedComment
	createErr  error
	order      []string
}

type createdComment struct {
	Repo core.Slug
	PR   int
	Body string
}

type editedComment struct {
	Repo      core.Slug
	CommentID int64
	Body      string
}

func (f *fakeForge) note(op string) { f.order = append(f.order, op) }

func (f *fakeForge) RepoSlug(context.Context) (core.Slug, error) { return f.slug, nil }
func (f *fakeForge) DefaultBranch(context.Context, core.Slug) string {
	return "main"
}
func (f *fakeForge) PullRequest(context.Context, core.Slug, int) (forge.PullRequest, error) {
	f.note("PullRequest")
	return f.pr, nil
}
func (f *fakeForge) PullRequestDiff(context.Context, core.Slug, core.Revision, core.Revision) ([]byte, error) {
	f.note("PullRequestDiff")
	return []byte("diff --git a/app.ts b/app.ts\n--- a/app.ts\n+++ b/app.ts\n@@ -1 +1 @@\n-old\n+new\n"), nil
}
func (f *fakeForge) PullRequestLabels(context.Context, core.Slug, int) []string {
	names := make([]string, len(f.pr.Labels))
	for i, l := range f.pr.Labels {
		names[i] = l.Name
	}
	return names
}
func (f *fakeForge) ReviewThreads(context.Context, core.Slug, int) []forge.ReviewThread {
	f.note("ReviewThreads")
	return f.threads
}
func (f *fakeForge) IssueComments(context.Context, core.Slug, int) []forge.IssueComment {
	f.note("IssueComments")
	return f.comments
}
func (f *fakeForge) ReviewComments(context.Context, core.Slug, int) []forge.IssueComment {
	return nil
}
func (f *fakeForge) RepoIssueComments(context.Context, core.Slug, time.Time, int) ([]forge.IssueComment, error) {
	return nil, nil
}
func (f *fakeForge) ViewerLogin(context.Context) (string, error) { return f.viewer, nil }
func (f *fakeForge) WorkflowRunStatus(context.Context, core.Slug, string) forge.RunStatus {
	return ""
}
func (f *fakeForge) LabelColour(context.Context, core.Slug, string) string { return "" }
func (f *fakeForge) IssueByFinding(context.Context, core.Slug, string, core.FindingID) (int, bool) {
	return 0, false
}
func (f *fakeForge) IssueCandidates(_ context.Context, _ core.Slug, path, _ string) []forge.IssueCandidate {
	f.note("IssueCandidates:" + path)
	return f.candidates
}
func (f *fakeForge) CommentCreate(_ context.Context, repo core.Slug, number int, body string) (int64, error) {
	f.note("CommentCreate")
	if f.createErr != nil {
		return 0, f.createErr
	}
	f.created = append(f.created, createdComment{Repo: repo, PR: number, Body: body})
	id := int64(9100 + len(f.created))
	f.comments = append(f.comments, forge.IssueComment{
		ID:          id,
		Body:        body,
		AuthorLogin: f.viewer,
	})
	return id, nil
}
func (f *fakeForge) CommentEdit(_ context.Context, repo core.Slug, commentID int64, body string) error {
	f.note("CommentEdit")
	f.edits = append(f.edits, editedComment{Repo: repo, CommentID: commentID, Body: body})
	return nil
}
func (f *fakeForge) ReviewCommentCreate(context.Context, forge.ReviewComment) (forge.Placement, error) {
	return forge.PlacementInline, nil
}
func (f *fakeForge) ReviewReply(context.Context, core.Slug, int, int64, string) error {
	return nil
}
func (f *fakeForge) ThreadResolve(context.Context, string) error { return nil }
func (f *fakeForge) LabelEnsure(context.Context, core.Slug, forge.Label) (forge.LabelState, error) {
	return forge.LabelExists, nil
}
func (f *fakeForge) IssueCreate(context.Context, core.Slug, string, string, []string) (int, error) {
	return 0, nil
}
func (f *fakeForge) IssueCommentCreate(context.Context, core.Slug, int, string) {}
func (f *fakeForge) PullRequestLabelAdd(context.Context, core.Slug, int, string) error {
	return nil
}
func (f *fakeForge) PullRequestLabelRemove(context.Context, core.Slug, int, string) {}

var _ forge.Forge = (*fakeForge)(nil)

// ---------------------------------------------------------------------------
// fakeGit
// ---------------------------------------------------------------------------

type showCall struct {
	Revision core.Revision
	Path     string
}

type fakeGit struct {
	env          *testEnv
	dir          string
	head         core.Revision
	show         map[string][]byte
	showCalls    []showCall
	worktrees    *[]string
	fetchCalls   []string
	captureCalls *int
	restoreCalls *int
	runAt        []string
}

func (g *fakeGit) Dir() string { return g.dir }
func (g *fakeGit) WithDir(dir string) Git {
	clone := *g
	clone.dir = dir
	g.runAt = append(g.runAt, dir)
	return &clone
}
func (g *fakeGit) Show(_ context.Context, revision core.Revision, path string) ([]byte, vcs.FileStatus, error) {
	g.showCalls = append(g.showCalls, showCall{Revision: revision, Path: path})
	key := revision.SHA() + ":" + path
	if g.show != nil {
		if b, ok := g.show[key]; ok {
			return b, vcs.IsFile, nil
		}
		if b, ok := g.show[path]; ok {
			return b, vcs.IsFile, nil
		}
	}
	return nil, vcs.NotFound, nil
}
func (g *fakeGit) HasCommit(context.Context, core.Revision) (bool, error) { return true, nil }
func (g *fakeGit) Head(context.Context) (core.Revision, error)            { return g.head, nil }
func (g *fakeGit) ConfigGet(_ context.Context, key string) (string, error) {
	switch key {
	case "branch.feature.pushRemote", "branch.feature.remote", "remote.pushDefault":
		return "origin", nil
	}
	return "", nil
}
func (g *fakeGit) AddWorktree(_ context.Context, dir string, _ core.Revision) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte("injected\n"), 0o644); err != nil {
		return err
	}
	*g.worktrees = append(*g.worktrees, dir)
	return nil
}
func (g *fakeGit) WorktreeReusable(context.Context, string, core.Revision) (bool, error) {
	return false, nil
}
func (g *fakeGit) PruneWorktrees(context.Context) {}
func (g *fakeGit) Fetch(_ context.Context, remote, refspec string) error {
	g.fetchCalls = append(g.fetchCalls, remote+" "+refspec)
	return nil
}
func (g *fakeGit) ResolvePushRepo(context.Context, string) (vcs.PushTarget, error) {
	return vcs.PushTarget{Repo: g.env.slug}, nil
}
func (g *fakeGit) CaptureTree(context.Context, string) (string, error) {
	*g.captureCalls++
	return "snap-tree", nil
}
func (g *fakeGit) RestoreTree(context.Context, string, string) error {
	*g.restoreCalls++
	return nil
}
func (g *fakeGit) LogSubjects(context.Context, core.Revision) ([]byte, error) {
	return nil, nil
}

var _ Git = (*fakeGit)(nil)

// ---------------------------------------------------------------------------
// recordingRunner + stubAdapter
// ---------------------------------------------------------------------------

type recordingRunner struct {
	specs []exec.Spec
	onRun func(exec.Spec)
}

func (r *recordingRunner) Run(_ context.Context, spec exec.Spec) exec.Result {
	r.specs = append(r.specs, spec)
	if r.onRun != nil {
		r.onRun(spec)
	}
	return exec.Result{ExitCode: 0, Stdout: []byte(`{"result":"{}"}`)}
}

type stubAdapter struct {
	invs     []harness.Invocation
	payloads []json.RawMessage
	calls    int
}

func (a *stubAdapter) Name() string { return "claude" }
func (a *stubAdapter) NotInstalled() *harness.Refusal {
	return nil
}
func (a *stubAdapter) Spec(inv harness.Invocation) (exec.Spec, error) {
	a.invs = append(a.invs, inv)
	return exec.Spec{
		Path: "claude",
		Args: []string{"-p", "--output-format", "json"},
		Dir:  inv.Workdir,
		Env:  inv.Env,
	}, nil
}
func (a *stubAdapter) Envelope(_ harness.Invocation, _ exec.Result) harness.Envelope {
	i := a.calls
	a.calls++
	if i >= len(a.payloads) {
		msg := "no canned payload"
		return harness.Envelope{Harness: "claude", Error: &msg}
	}
	return harness.Envelope{OK: true, Payload: a.payloads[i], Harness: "claude"}
}

var _ harness.Adapter = (*stubAdapter)(nil)

func firstCreateBefore(order []string, later string) bool {
	create, run := -1, -1
	for i, op := range order {
		if op == "CommentCreate" && create < 0 {
			create = i
		}
		if op == later && run < 0 {
			run = i
		}
	}
	return create >= 0 && run >= 0 && create < run
}

func containsAny(args []string, needles ...string) bool {
	joined := strings.Join(args, " ")
	for _, n := range needles {
		if strings.Contains(joined, n) {
			return true
		}
	}
	return false
}

func envHas(env []string, name string) bool {
	prefix := name + "="
	for _, e := range env {
		if strings.HasPrefix(e, prefix) {
			return true
		}
	}
	return false
}
