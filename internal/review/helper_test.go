package review_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/carlosboeing/crossrev/internal/config"
	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/exec"
	"github.com/carlosboeing/crossrev/internal/forge"
	"github.com/carlosboeing/crossrev/internal/harness"
	"github.com/carlosboeing/crossrev/internal/prstate"
	"github.com/carlosboeing/crossrev/internal/review"
	"github.com/carlosboeing/crossrev/internal/runlog"
	"github.com/carlosboeing/crossrev/internal/vcs"
)

const (
	repoRoot = "../.."
	baseSHA  = "0913bf7b99dcecf746d0e6fcef5a9c1d64aaf3b0"
	headSHA  = "2c4a46cb321db01826d116b5ef2add6b0284d68c"
	oldSHA   = "0000000000000000000000000000000000000000"
	author   = "tester"
	runID    = "local-test"
)

var frozenNow = time.Unix(1_700_000_000, 0)

func mustRev(t *testing.T, sha string) core.Revision {
	t.Helper()
	rev, err := core.NewRevision(sha)
	if err != nil {
		t.Fatalf("revision %s: %v", sha, err)
	}
	return rev
}

func mustSlug(t *testing.T) core.Slug {
	t.Helper()
	slug, err := core.ParseSlug("acme/widget")
	if err != nil {
		t.Fatalf("slug: %v", err)
	}
	return slug
}

func mustDoc(t *testing.T) harness.Document {
	t.Helper()
	doc, err := harness.Descriptors()
	if err != nil {
		t.Fatalf("harness descriptor: %v", err)
	}
	return doc
}

func mustConfig(t *testing.T, yaml string) *config.Config {
	t.Helper()
	base := mustRev(t, baseSHA)
	show := func(_ context.Context, rev core.Revision, path string) ([]byte, config.FileStatus, error) {
		if rev.SHA() == baseSHA && path == ".github/crossrev.yml" && yaml != "" {
			return []byte(yaml), config.IsFile, nil
		}
		return nil, config.NotFound, nil
	}
	cfg, err := config.Load(context.Background(), base, show)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	return cfg
}

func parseMarker(t *testing.T, raw string) prstate.Marker {
	t.Helper()
	marker, err := prstate.ParseMarker(json.RawMessage(raw))
	if err != nil {
		t.Fatalf("ParseMarker: %v", err)
	}
	return marker
}

func commentWithMarker(t *testing.T, id int64, marker prstate.Marker) forge.IssueComment {
	t.Helper()
	encoded, err := marker.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	return forge.IssueComment{
		ID:          id,
		AuthorLogin: author,
		Body:        "progress" + encoded,
		CreatedAt:   "2023-11-14T22:13:20Z",
		IssueURL:    "https://api.github.com/repos/acme/widget/issues/42",
	}
}

func claudeStdout(payload string) []byte {
	raw, err := json.Marshal(map[string]any{
		"result":   payload,
		"is_error": false,
	})
	if err != nil {
		panic(err)
	}
	return raw
}

func convergedPayload() string {
	return `{"verdict":"converged","findings":[]}`
}

type eventLog struct {
	mu     sync.Mutex
	events []string
}

func (e *eventLog) add(name string) {
	e.mu.Lock()
	e.events = append(e.events, name)
	e.mu.Unlock()
}

func (e *eventLog) all() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, len(e.events))
	copy(out, e.events)
	return out
}

type fakeVCS struct {
	files map[string]map[string][]byte
}

func (f *fakeVCS) Show(_ context.Context, revision core.Revision, path string) ([]byte, vcs.FileStatus, error) {
	tree, ok := f.files[revision.SHA()]
	if !ok {
		return nil, vcs.NotFound, nil
	}
	content, ok := tree[path]
	if !ok {
		return nil, vcs.NotFound, nil
	}
	return content, vcs.IsFile, nil
}

type fakeRunner struct {
	log    *eventLog
	mu     sync.Mutex
	specs  []exec.Spec
	script []exec.Result
	calls  int
	onSpec func(exec.Spec)
}

func (r *fakeRunner) Run(_ context.Context, spec exec.Spec) exec.Result {
	if r.log != nil {
		r.log.add("harness")
	}
	r.mu.Lock()
	r.specs = append(r.specs, spec)
	r.calls++
	call := r.calls
	script := r.script
	onSpec := r.onSpec
	r.mu.Unlock()
	if onSpec != nil {
		onSpec(spec)
	}
	if len(script) == 0 {
		return exec.Result{ExitCode: 0, Stdout: claudeStdout(convergedPayload())}
	}
	idx := call - 1
	if idx >= len(script) {
		idx = len(script) - 1
	}
	return script[idx]
}

func (r *fakeRunner) Specs() []exec.Spec {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]exec.Spec, len(r.specs))
	copy(out, r.specs)
	return out
}

type fakeForge struct {
	log             *eventLog
	pr              forge.PullRequest
	prErr           error
	prCalls         int
	comments        []forge.IssueComment
	createErr       error
	created         []string
	createdIDs      []int64
	zeroCreateID    bool
	edits           []string
	editIDs         []int64
	labelsAdded     []string
	labelsRemoved   []string
	labelAddErr     error
	threads         []forge.ReviewThread
	diff            []byte
	repoComments    []forge.IssueComment
	repoCommentsErr error
	nextID          int64
	ops             []string
	reviewPosted    []forge.ReviewComment
	reviewComments  []forge.IssueComment
	placements      []forge.Placement
	forceFallback   bool
}

func (f *fakeForge) RepoSlug(context.Context) (core.Slug, error) {
	return core.ParseSlug("acme/widget")
}

func (f *fakeForge) DefaultBranch(context.Context, core.Slug) string { return "main" }

func (f *fakeForge) PullRequest(context.Context, core.Slug, int) (forge.PullRequest, error) {
	f.prCalls++
	if f.prErr != nil {
		return forge.PullRequest{}, f.prErr
	}
	return f.pr, nil
}

func (f *fakeForge) PullRequestDiff(context.Context, core.Slug, core.Revision, core.Revision) ([]byte, error) {
	if f.diff != nil {
		return f.diff, nil
	}
	return []byte("diff --git a/app.go b/app.go\n--- a/app.go\n+++ b/app.go\n@@ -1,1 +1,2 @@\n context\n+added\n"), nil
}

func (f *fakeForge) PullRequestLabels(context.Context, core.Slug, int) []string {
	names := make([]string, 0, len(f.pr.Labels))
	for _, label := range f.pr.Labels {
		names = append(names, label.Name)
	}
	return names
}

func (f *fakeForge) ReviewThreads(context.Context, core.Slug, int) []forge.ReviewThread {
	return f.threads
}

func (f *fakeForge) IssueComments(context.Context, core.Slug, int) []forge.IssueComment {
	return f.comments
}

func (f *fakeForge) ReviewComments(context.Context, core.Slug, int) []forge.IssueComment {
	return f.reviewComments
}

func (f *fakeForge) RepoIssueComments(context.Context, core.Slug, time.Time, int) ([]forge.IssueComment, error) {
	return f.repoComments, f.repoCommentsErr
}

func (f *fakeForge) ViewerLogin(context.Context) (string, error) { return author, nil }
func (f *fakeForge) AwaitingPullRequests(context.Context, core.Slug) []forge.AwaitingPullRequest {
	return nil
}

func (f *fakeForge) WorkflowRunStatus(context.Context, core.Slug, string) forge.RunStatus {
	return ""
}

func (f *fakeForge) LabelColour(context.Context, core.Slug, string) string { return "" }

func (f *fakeForge) IssueByFinding(context.Context, core.Slug, string, core.FindingID) (int, bool) {
	return 0, false
}

func (f *fakeForge) IssueCandidates(context.Context, core.Slug, string, string) []forge.IssueCandidate {
	return nil
}

func (f *fakeForge) CommentCreate(_ context.Context, _ core.Slug, _ int, body string) (int64, error) {
	if f.log != nil && (strings.Contains(body, "**crossrev — reviewing") || strings.Contains(body, "**crossrev stopped")) {
		f.log.add("claim")
	}
	if f.createErr != nil {
		return 0, f.createErr
	}
	if f.zeroCreateID {
		return 0, nil
	}
	f.created = append(f.created, body)
	id := f.nextID
	if id == 0 {
		id = 9001
	}
	f.nextID = id + 1
	f.createdIDs = append(f.createdIDs, id)
	f.comments = append(f.comments, forge.IssueComment{
		ID:          id,
		AuthorLogin: author,
		Body:        body,
	})
	f.ops = append(f.ops, "comment-create")
	return id, nil
}

func (f *fakeForge) CommentEdit(_ context.Context, _ core.Slug, commentID int64, body string) error {
	f.editIDs = append(f.editIDs, commentID)
	f.edits = append(f.edits, body)
	f.ops = append(f.ops, "comment-edit")
	return nil
}

func (f *fakeForge) ReviewCommentCreate(_ context.Context, comment forge.ReviewComment) (forge.Placement, error) {
	f.reviewPosted = append(f.reviewPosted, comment)
	f.ops = append(f.ops, "review-comment")
	if f.forceFallback {
		body := fmt.Sprintf("**%s:%d** (%s)\n\n%s", comment.Path, comment.Line, comment.Side, comment.Body)
		if _, err := f.CommentCreate(context.Background(), comment.Repo, comment.Number, body); err != nil {
			return "", err
		}
		f.placements = append(f.placements, forge.PlacementFallback)
		return forge.PlacementFallback, nil
	}
	id := f.nextID
	if id == 0 {
		id = 9001
	}
	f.nextID = id + 1
	f.reviewComments = append(f.reviewComments, forge.IssueComment{
		ID:          id,
		AuthorLogin: author,
		Body:        comment.Body,
	})
	ids := prstate.FindingIDs([]string{comment.Body}, core.LegReview, 0)
	f.threads = append(f.threads, forge.ReviewThread{
		ID:            fmt.Sprintf("PRRT_%d", id),
		Path:          comment.Path,
		Line:          comment.Line,
		RootCommentID: id,
		FindingIDs:    ids,
	})
	f.placements = append(f.placements, forge.PlacementInline)
	return forge.PlacementInline, nil
}

func (f *fakeForge) ReviewReply(context.Context, core.Slug, int, int64, string) error { return nil }

func (f *fakeForge) ThreadResolve(context.Context, string) error { return nil }

func (f *fakeForge) LabelEnsure(context.Context, core.Slug, forge.Label) (forge.LabelState, error) {
	return forge.LabelExists, nil
}

func (f *fakeForge) IssueCreate(context.Context, core.Slug, string, string, []string) (int, error) {
	return 0, nil
}

func (f *fakeForge) IssueCommentCreate(context.Context, core.Slug, int, string) {}

func (f *fakeForge) PullRequestLabelAdd(_ context.Context, _ core.Slug, _ int, label string) error {
	if f.labelAddErr != nil {
		return f.labelAddErr
	}
	f.labelsAdded = append(f.labelsAdded, label)
	f.ops = append(f.ops, "label-add")
	return nil
}

func (f *fakeForge) PullRequestLabelRemove(_ context.Context, _ core.Slug, _ int, label string) {
	f.labelsRemoved = append(f.labelsRemoved, label)
	f.ops = append(f.ops, "label-remove")
}

var _ forge.Forge = (*fakeForge)(nil)

type env struct {
	forge    *fakeForge
	vcs      *fakeVCS
	runner   *fakeRunner
	log      *eventLog
	cfg      *config.Config
	doc      harness.Document
	dir      string
	lookPath func(string) (string, error)
	// nilLookPath leaves review.Leg.LookPath nil so the case drives the
	// production PATH search rather than the substitute below. Without it no
	// case here reaches exec.LookPath at all, and the fallback the helper
	// fills in would hide whatever the real one does.
	nilLookPath bool
	// keepTranscripts is the --keep-transcripts posture, which the run log
	// carries rather than the leg.
	keepTranscripts bool
	// validate replaces validate.Findings, so a case can drive the retry
	// budgets without building a payload that fails for the right reason.
	validate func([]byte) error
}

func newEnv(t *testing.T) *env {
	t.Helper()
	// cred.Prepare reads process RUNNER_ENVIRONMENT. GitHub-hosted runners set
	// it to github-hosted, and a missing harness secret then stops the leg.
	// Tests are not that runner: isolate them the way cred treats self-hosted.
	t.Setenv("RUNNER_ENVIRONMENT", "self-hosted")
	dir := t.TempDir()
	events := &eventLog{}
	head := mustRev(t, headSHA)
	base := mustRev(t, baseSHA)
	return &env{
		log: events,
		forge: &fakeForge{
			log: events,
			pr: forge.PullRequest{
				Number:       42,
				Title:        "t",
				Body:         "",
				URL:          "https://github.com/acme/widget/pull/42",
				HeadRefName:  "feature",
				HeadRefOid:   head,
				BaseRefName:  "main",
				BaseRefOid:   base,
				ChangedFiles: 1,
				State:        "OPEN",
			},
		},
		vcs: &fakeVCS{files: map[string]map[string][]byte{
			baseSHA: {},
			"":      {},
		}},
		runner: &fakeRunner{log: events},
		cfg:    mustConfig(t, ""),
		doc:    mustDoc(t),
		dir:    dir,
	}
}

func (e *env) leg(t *testing.T) review.Leg {
	t.Helper()
	run, err := runlog.Open(runlog.Options{
		Dir:             filepath.Join(e.dir, "run"),
		Now:             func() time.Time { return frozenNow },
		Leg:             "review",
		Repo:            "acme/widget",
		PR:              "42",
		KeepTranscripts: e.keepTranscripts,
	})
	if err != nil {
		t.Fatalf("runlog.Open: %v", err)
	}
	look := e.lookPath
	if look == nil && !e.nilLookPath {
		look = func(name string) (string, error) { return "/usr/bin/" + name, nil }
	}
	return review.Leg{
		Validate: e.validate,
		Forge:    e.forge,
		VCS:      e.vcs,
		Config:   e.cfg,
		Harness:  e.doc,
		Log:      run,
		Now:      func() time.Time { return frozenNow },
		Runner:   e.runner,
		Env:      []string{"PATH=/usr/bin:/bin", "HOME=" + e.dir},
		LookPath: look,
	}
}

func (e *env) request(t *testing.T) review.Request {
	t.Helper()
	return review.Request{
		PR:              42,
		Repo:            mustSlug(t),
		Trigger:         review.TriggerHuman,
		HarnessOverride: "claude",
		Author:          author,
		Workdir:         e.dir,
		RunID:           runID,
	}
}

func runLeg(t *testing.T, e *env, req review.Request) review.Result {
	t.Helper()
	if req.Repo.Incomplete() {
		req.Repo = mustSlug(t)
	}
	if req.Workdir == "" {
		req.Workdir = e.dir
	}
	if req.Author == "" {
		req.Author = author
	}
	if req.RunID == "" {
		req.RunID = runID
	}
	if req.HarnessOverride == "" {
		req.HarnessOverride = "claude"
	}
	leg := e.leg(t)
	return leg.Run(context.Background(), req)
}

func writeBase(e *env, path, content string) {
	if e.vcs.files[baseSHA] == nil {
		e.vcs.files[baseSHA] = map[string][]byte{}
	}
	e.vcs.files[baseSHA][path] = []byte(content)
}

func writeHead(e *env, path, content string) {
	if e.vcs.files[headSHA] == nil {
		e.vcs.files[headSHA] = map[string][]byte{}
	}
	e.vcs.files[headSHA][path] = []byte(content)
}

func repoRootPath(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(repoRoot)
	if err != nil {
		t.Fatalf("repo root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "lib", "run.sh")); err != nil {
		t.Fatalf("lib/run.sh missing under %s: %v", root, err)
	}
	return root
}

func bashOutput(t *testing.T, script string, args ...string) string {
	t.Helper()
	cmdArgs := append([]string{"-c", script, "bash"}, args...)
	cmd := osexec.Command("bash", cmdArgs...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("bash: %v\nstderr: %s", err, stderr.String())
	}
	return string(out)
}
