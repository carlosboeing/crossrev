package review_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/carlosboeing/crossrev/internal/config"
	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/exec"
	"github.com/carlosboeing/crossrev/internal/forge"
	"github.com/carlosboeing/crossrev/internal/harness"
	"github.com/carlosboeing/crossrev/internal/intel"
	"github.com/carlosboeing/crossrev/internal/prstate"
	"github.com/carlosboeing/crossrev/internal/prstate/storetest"
	"github.com/carlosboeing/crossrev/internal/readserve"
	"github.com/carlosboeing/crossrev/internal/review"
	"github.com/carlosboeing/crossrev/internal/runlog"
	"github.com/carlosboeing/crossrev/internal/validate"
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

// claudePackBytes is the per-call packing limit the tests run under: the
// claude harness window at 3.9 bytes per token. Every leg test drives the
// claude harness override, so packing budgets read from here rather than
// repeating the window math.
func claudePackBytes() int {
	return intel.ComputeLimits(200000, false).PackBytes
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

// claudeStdout is the harness answer the fixtures speak: the final result
// event of a served stream-json run, the shape a served claude review
// leg's envelope reads its answer and usage off. Usage travels absent the
// way it always did here; the envelope answers nil usage for both shapes.
func claudeStdout(payload string) []byte {
	raw, err := json.Marshal(map[string]any{
		"type":     "result",
		"result":   payload,
		"is_error": false,
	})
	if err != nil {
		panic(err)
	}
	return append(raw, '\n')
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
	// required marks head paths the coverage loop must account for. Paths
	// written by writeHead/writeBase (config fixtures, hijack cases) stay
	// invisible to ChangedFiles, so frozen-path tests keep zero required
	// units and stay on the single-prompt path.
	required map[string]bool
	// repair, when set, answers RangeDiff with the B-to-C delta.
	repair *fakeRepair
	// changedErr, when set, is the git failure ChangedFiles returns.
	changedErr error
	// attrs, when non-nil, is the base-tree linguist-generated answer per
	// current path. attrErr fails the attribute read; attrWarn rides with a
	// successful one, the old-git posture.
	attrs    map[string]vcs.AttributeDecision
	attrWarn *vcs.Warning
	attrErr  error
	// reads counts Show calls per path, so a test can prove an excluded
	// path's body was never read.
	reads map[string]int
	// symlinks marks head paths the served read server refuses with the
	// symlink reason: the blob holds the link target text, the way git
	// show reads a symlink, but the server never serves mode 120000.
	symlinks map[string]bool
	// searchResults scripts the blob-pass answers per term, searchTooCommon
	// caps per term, and searchErr fails the whole pass. gotTerms records
	// the last call's terms, and searchCalls counts SearchAll invocations,
	// so a test can pin that advisory discovery runs once per pass.
	searchResults   map[string][]vcs.SearchHit
	searchTooCommon map[string]bool
	searchErr       error
	gotTerms        []string
	searchCalls     int
	// changedLines answers ChangedLines with the stub -U0 diff the
	// advisory term walk reads; changedLinesErr fails it, and
	// changedLinesCalls counts invocations.
	changedLines      []byte
	changedLinesErr   error
	changedLinesCalls int
	// shapeFunc, when non-nil, answers ShapeFileDiff per unit with the
	// scripted hunk input; nil leaves every unit unshaped, so the batch
	// renders from its body the way it always did. shapeWarn rides with
	// the support answer the way an old git reports it, and shapeErr
	// fails the shaping step. shapeCalls counts ShapeFileDiff
	// invocations.
	shapeFunc  func(unit intel.FileUnit) (vcs.ShapedFile, error)
	shapeWarn  *vcs.Warning
	shapeErr   error
	shapeCalls int
	// removePersistedCalls counts RemovePersistedCredentials invocations, and
	// removePersistedErr is the failure it returns.
	removePersistedCalls int
	removePersistedErr   error
	// removed is what RemovePersistedCredentials answers.
	removed []vcs.RemovedCredential
	// heads answers HeadAt per directory. A directory with no entry reads
	// the pull request head, so the explicit-workdir default every existing
	// case passes keeps proving the head it always proved.
	heads map[string]string
	// headErr, when set, is the failure HeadAt returns.
	headErr error
	// hasCommit, when non-nil, answers HasCommit per SHA. Nil holds every
	// commit, so cases that never move the head fetch nothing.
	hasCommit map[string]bool
	// hasCommitErr, when set, is the failure HasCommit returns.
	hasCommitErr error
	// fetchCalls records Fetch invocations as "remote refspec", and onFetch
	// runs after each one, so a case can land the head on the fallback it
	// is proving.
	fetchCalls []string
	fetchErr   error
	onFetch    func(remote, refspec string)
	// config answers ConfigGet per key; the push-remote keys default to
	// origin, the way a checkout with no branch configuration reads.
	config map[string]string
	// reusable, when set, holds explicit WorktreeReusable answers per
	// directory. A directory the fake created through AddWorktree answers
	// true unless an explicit entry says otherwise, so the leg's
	// post-create ownership proof passes for trees it just made; every
	// other directory without an entry answers false.
	reusable map[string]bool
	// addErrs, when set, fails AddWorktree for the named directory,
	// simulating a creation race the leg must ride out by trying the next
	// path. The winner's tree now occupies the path, the way a real race
	// leaves it.
	addErrs map[string]error
	// clean, when set, is the answer WorktreeClean gives per directory.
	// Unset means every worktree is clean; a set map answers false for
	// directories with no entry, so unknown cleanliness never earns reuse.
	clean map[string]bool
	// worktrees records the directories AddWorktree created, addCalls counts
	// them, and onAddWorktree lays files into the fresh worktree.
	worktrees     []string
	addCalls      int
	onAddWorktree func(dir string) error
	// removedWorktrees records the directories RemoveWorktree took away,
	// and pruneCalls counts PruneWorktrees invocations.
	removedWorktrees []string
	pruneCalls       int
	// removeErr, when set, is the failure RemoveWorktree returns, leaving
	// the directory in place the way a failed removal does.
	removeErr error
}

func (f *fakeVCS) GeneratedAttributes(_ context.Context, _ core.Revision, paths []string) (map[string]vcs.AttributeDecision, *vcs.Warning, error) {
	if f.attrErr != nil {
		return nil, nil, f.attrErr
	}
	answers := make(map[string]vcs.AttributeDecision, len(paths))
	for _, path := range paths {
		answers[path] = f.attrs[path]
	}
	return answers, f.attrWarn, nil
}

func (f *fakeVCS) SearchAll(_ context.Context, _ core.Revision, terms []string, _ int) ([]vcs.TermResult, error) {
	f.searchCalls++
	f.gotTerms = append([]string(nil), terms...)
	if f.searchErr != nil {
		return nil, f.searchErr
	}
	var out []vcs.TermResult
	for _, term := range terms {
		out = append(out, vcs.TermResult{Term: term, Hits: f.searchResults[term], TooCommon: f.searchTooCommon[term]})
	}
	return out, nil
}

func (f *fakeVCS) ChangedLines(_ context.Context, _, _ core.Revision) ([]byte, error) {
	f.changedLinesCalls++
	if f.changedLinesErr != nil {
		return nil, f.changedLinesErr
	}
	return f.changedLines, nil
}

func (f *fakeVCS) HunkDiffSupport(context.Context) (bool, *vcs.Warning, error) {
	if f.shapeErr != nil {
		return false, nil, f.shapeErr
	}
	return true, f.shapeWarn, nil
}

func (f *fakeVCS) ShapeFileDiff(_ context.Context, _, _ core.Revision, change core.FileChange, body []byte, binary bool, unavailableReason string, _ bool) (vcs.ShapedFile, error) {
	f.shapeCalls++
	if f.shapeErr != nil {
		return vcs.ShapedFile{}, f.shapeErr
	}
	if f.shapeFunc == nil {
		return vcs.ShapedFile{}, nil
	}
	unit := intel.FileUnit{Path: change.Path, OldPath: change.OldPath, Change: change.Kind, Body: body, Available: unavailableReason == "", Binary: binary, Reason: unavailableReason}
	return f.shapeFunc(unit)
}

// repairDelta, when set, is the B-to-C delta RangeDiff answers: the bytes a
// repair changed between the reviewed head and the current head.
func (f *fakeVCS) RangeDiff(_ context.Context, base, head core.Revision) ([]byte, error) {
	if f.repair == nil {
		return nil, nil
	}
	return f.repair.at(base.SHA(), head.SHA())
}

type fakeRepair struct {
	base  string
	head  string
	bytes []byte
}

func (r *fakeRepair) at(base, head string) ([]byte, error) {
	if r == nil || base != r.base || head != r.head {
		return nil, nil
	}
	return r.bytes, nil
}

func (f *fakeVCS) ChangedFiles(_ context.Context, base, head core.Revision) ([]core.FileChange, error) {
	if f.changedErr != nil {
		return nil, f.changedErr
	}
	var changes []core.FileChange
	for path := range f.files[head.SHA()] {
		if !f.required[path] {
			continue
		}
		kind := core.ChangeAdded
		if _, ok := f.files[base.SHA()][path]; ok {
			kind = core.ChangeModified
		}
		changes = append(changes, core.FileChange{Path: path, Kind: kind})
	}
	for path := range f.files[base.SHA()] {
		if !f.required[path] {
			continue
		}
		if _, ok := f.files[head.SHA()][path]; !ok {
			changes = append(changes, core.FileChange{OldPath: path, Path: path, Kind: core.ChangeDeleted})
		}
	}
	return changes, nil
}

func (f *fakeVCS) Show(_ context.Context, revision core.Revision, path string) ([]byte, vcs.FileStatus, error) {
	if f.reads == nil {
		f.reads = map[string]int{}
	}
	f.reads[path]++
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

func (f *fakeVCS) RemovePersistedCredentials(context.Context) ([]vcs.RemovedCredential, error) {
	f.removePersistedCalls++
	if f.removePersistedErr != nil {
		return nil, f.removePersistedErr
	}
	return f.removed, nil
}

func (f *fakeVCS) headFor(dir string) core.Revision {
	if sha, ok := f.heads[dir]; ok {
		rev, err := core.NewRevision(sha)
		if err != nil {
			panic(err)
		}
		return rev
	}
	rev, err := core.NewRevision(headSHA)
	if err != nil {
		panic(err)
	}
	return rev
}

func (f *fakeVCS) HeadAt(_ context.Context, dir string) (core.Revision, error) {
	if f.headErr != nil {
		return core.Revision{}, f.headErr
	}
	return f.headFor(dir), nil
}

func (f *fakeVCS) HasCommit(_ context.Context, revision core.Revision) (bool, error) {
	if f.hasCommitErr != nil {
		return false, f.hasCommitErr
	}
	if f.hasCommit == nil {
		return true, nil
	}
	return f.hasCommit[revision.SHA()], nil
}

func (f *fakeVCS) ConfigGet(_ context.Context, key string) (string, error) {
	if val, ok := f.config[key]; ok {
		return val, nil
	}
	switch key {
	case "branch.feature.pushRemote", "branch.feature.remote", "remote.pushDefault":
		return "origin", nil
	}
	return "", nil
}

func (f *fakeVCS) Fetch(_ context.Context, remote, refspec string) error {
	f.fetchCalls = append(f.fetchCalls, remote+" "+refspec)
	if f.onFetch != nil {
		f.onFetch(remote, refspec)
	}
	return f.fetchErr
}

func (f *fakeVCS) WorktreeReusable(_ context.Context, dir string, _ core.Revision) (bool, error) {
	if f.reusable != nil {
		if answer, ok := f.reusable[dir]; ok {
			return answer, nil
		}
	}
	for _, created := range f.worktrees {
		if created == dir {
			return true, nil
		}
	}
	return false, nil
}

func (f *fakeVCS) WorktreeClean(_ context.Context, dir string) (bool, error) {
	if f.clean == nil {
		return true, nil
	}
	return f.clean[dir], nil
}

func (f *fakeVCS) AddWorktree(_ context.Context, dir string, revision core.Revision) error {
	f.addCalls++
	if err, ok := f.addErrs[dir]; ok && err != nil {
		_ = os.MkdirAll(dir, 0o755)
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if f.heads == nil {
		f.heads = map[string]string{}
	}
	f.heads[dir] = revision.SHA()
	if f.onAddWorktree != nil {
		if err := f.onAddWorktree(dir); err != nil {
			return err
		}
	}
	f.worktrees = append(f.worktrees, dir)
	return nil
}

func (f *fakeVCS) RemoveWorktree(_ context.Context, dir string) error {
	f.removedWorktrees = append(f.removedWorktrees, dir)
	if f.removeErr != nil {
		return f.removeErr
	}
	return os.RemoveAll(dir)
}

func (f *fakeVCS) PruneWorktrees(context.Context) { f.pruneCalls++ }

type fakeRunner struct {
	log    *eventLog
	mu     sync.Mutex
	specs  []exec.Spec
	script []exec.Result
	calls  int
	onSpec func(exec.Spec)
	// probes records `--version` children, and versions answers them by
	// binary: the installed CLI each probe reports. A probe is not a
	// session child: it neither advances the script nor counts in calls,
	// the same way tests/stub/opencode answers --version before it logs
	// anything. version keeps answering the opencode probe for the tests
	// that set it; probeFail answers every probe as a failure.
	probes    []exec.Spec
	versions  map[string]string
	version   string
	probeFail bool
	// vcs answers the served read server the leg-start self-test speaks to.
	// A `__read-server` session is not a harness child either: the fake
	// serves it from the same file map the fixture's VCS reads, renders
	// the answer the way the server renders it, and appends the call log
	// the post-call check reads — so the self-test byte-checks the
	// wiring, not canned bytes.
	vcs *fakeVCS
	// serveErr, when set, is the failure the served session answers: the
	// broken-tool posture the degrade and halt paths are proved against.
	serveErr error
}

func (r *fakeRunner) Run(_ context.Context, spec exec.Spec) exec.Result {
	served := false
	for _, arg := range spec.Args {
		if arg == "__read-server" {
			served = true
		}
	}
	// Neither the served session nor a version probe is a harness child,
	// so neither leaves a harness event: progress tests read the harness
	// boundary off these events.
	if r.log != nil && !served && !(len(spec.Args) == 1 && spec.Args[0] == "--version") {
		r.log.add("harness")
	}
	r.mu.Lock()
	if len(spec.Args) == 1 && spec.Args[0] == "--version" {
		r.probes = append(r.probes, spec)
		probeFail := r.probeFail
		version := r.versions[spec.Path]
		if version == "" {
			version = r.version
		}
		if version == "" {
			version = "1.18.21 (test stub)"
		}
		r.mu.Unlock()
		if probeFail {
			return exec.Result{ExitCode: 1}
		}
		return exec.Result{ExitCode: 0, Stdout: []byte(version + "\n")}
	}
	for _, arg := range spec.Args {
		if arg == "__read-server" {
			vcs := r.vcs
			serveErr := r.serveErr
			r.mu.Unlock()
			if serveErr != nil {
				return exec.Result{ExitCode: 1, Err: serveErr}
			}
			return serveFixtureSession(spec, vcs)
		}
	}
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

// serveFixtureSession answers a `__read-server` session from the fixture's
// own file map: the same bytes the fixture's VCS reads, rendered the way
// the server renders them. The self-test byte-checks the wiring against
// these bytes, and the call log appended beside them is what the post-call
// check reads.
func serveFixtureSession(spec exec.Spec, vcs *fakeVCS) exec.Result {
	flag := func(name string) string {
		for at := 0; at+1 < len(spec.Args); at++ {
			if spec.Args[at] == name {
				return spec.Args[at+1]
			}
		}
		return ""
	}
	base, head, logPath := flag("--base"), flag("--head"), flag("--log")
	appendLog := func(event, payload string) {
		if logPath == "" {
			return
		}
		line := `{"event":` + strconv.Quote(event)
		if payload != "" {
			line += `,"payload":` + payload
		}
		line += "}\n"
		f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return
		}
		_, _ = f.WriteString(line)
		_ = f.Close()
	}
	answer := func(id json.RawMessage, result string) string {
		return `{"jsonrpc":"2.0","id":` + string(id) + `,"result":` + result + "}\n"
	}
	refuse := func(id json.RawMessage, text string) string {
		raw, _ := json.Marshal(map[string]any{
			"isError": true,
			"content": []map[string]any{{"type": "text", "text": text}},
		})
		return `{"jsonrpc":"2.0","id":` + string(id) + `,"result":` + string(raw) + "}\n"
	}
	var out strings.Builder
	appendLog("start", "")
	for _, line := range strings.Split(strings.TrimRight(string(spec.Stdin), "\n"), "\n") {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				Name      string `json:"name"`
				Arguments struct {
					Path     string `json:"path"`
					Revision string `json:"revision"`
				} `json:"arguments"`
			} `json:"params"`
		}
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			continue
		}
		switch req.Method {
		case "initialize":
			appendLog("initialize", "")
			out.WriteString(answer(req.ID, `{"protocolVersion":"2025-06-18"}`))
		case "tools/list":
			appendLog("tools_list", "")
			out.WriteString(answer(req.ID, `{"tools":[{"name":"read_file"}]}`))
		case "tools/call":
			if req.Params.Name != "read_file" {
				appendLog("refused", `{"reason":"unknown_tool"}`)
				out.WriteString(refuse(req.ID, "Method not found: "+req.Params.Name))
				continue
			}
			if vcs != nil && vcs.symlinks[req.Params.Arguments.Path] {
				appendLog("refused", `{"reason":"symlink"}`)
				out.WriteString(refuse(req.ID, "symlink"))
				continue
			}
			sha := base
			if req.Params.Arguments.Revision == "head" {
				sha = head
			}
			var body []byte
			if vcs != nil {
				body = vcs.files[sha][req.Params.Arguments.Path]
			}
			if len(body) == 0 {
				appendLog("refused", `{"reason":"not_found"}`)
				out.WriteString(refuse(req.ID, "not_found"))
				continue
			}
			text := strings.TrimSuffix(string(body), "\n")
			lines := strings.Split(text, "\n")
			// The server cuts a result at DefaultMaxResultLines: render
			// the cut the way it renders it, so the byte-check meets the
			// same answer production meets.
			last := len(lines)
			nextStart := 0
			if len(lines) > readserve.DefaultMaxResultLines {
				last = readserve.DefaultMaxResultLines
				nextStart = last + 1
				lines = lines[:last]
			}
			var rendered strings.Builder
			rendered.WriteString(req.Params.Arguments.Path + "@" + req.Params.Arguments.Revision +
				" lines 1-" + strconv.Itoa(last) + "\n")
			for at, content := range lines {
				rendered.WriteString(strconv.Itoa(at+1) + ": " + content + "\n")
			}
			if nextStart > 0 {
				rendered.WriteString("(cut, next start_line=" + strconv.Itoa(nextStart) + ")\n")
			}
			payload, _ := json.Marshal(map[string]any{
				"path":       req.Params.Arguments.Path,
				"revision":   req.Params.Arguments.Revision,
				"start_line": 1,
				"end_line":   len(lines),
				"bytes":      len(rendered.String()),
			})
			appendLog("read", string(payload))
			content, _ := json.Marshal(map[string]any{
				"content": []map[string]any{{"type": "text", "text": rendered.String()}},
			})
			out.WriteString(answer(req.ID, string(content)))
		}
	}
	appendLog("end", "")
	return exec.Result{ExitCode: 0, Stdout: []byte(out.String())}
}

func (r *fakeRunner) Specs() []exec.Spec {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]exec.Spec, len(r.specs))
	copy(out, r.specs)
	return out
}

// specPrompt is the prompt a child process was handed: stdin where the
// adapter's transport is stdin, otherwise the last argument.
func specPrompt(spec exec.Spec) string {
	if spec.Stdin != nil {
		return string(spec.Stdin)
	}
	if len(spec.Args) > 0 {
		return spec.Args[len(spec.Args)-1]
	}
	return ""
}

type fakeForge struct {
	// store serves the ledger reads and writes. Nil means this fixture
	// has no ledger store, so an `auto` leg falls back to the marker.
	store   prstate.LedgerStore
	log     *eventLog
	pr      forge.PullRequest
	prErr   error
	prCalls int
	// onPullRequest, when set, runs after each PullRequest call with the
	// running call count, so a case can move the pull request's head or base
	// mid-run the way a push during the review would.
	onPullRequest func(calls int)
	comments      []forge.IssueComment
	createErr     error
	created       []string
	createdIDs    []int64
	zeroCreateID  bool
	edits         []string
	editIDs       []int64
	labelsAdded   []string
	labelsRemoved []string
	labelAddErr   error
	threads       []forge.ReviewThread
	// threadCalls counts ReviewThreads invocations, so a test can pin how
	// often the open conversation is fetched.
	threadCalls int
	diff        []byte
	// diffCalls counts PullRequestDiff invocations, so a test can pin how
	// often the diff is read.
	diffCalls int
	// checks is what CheckRuns answers: the required-check evidence for
	// the head. onCheckRuns, when set, runs after each CheckRuns call
	// with the running call count, so a case can move a run from pending
	// to passed mid-wait the way a finishing check would.
	checks          []forge.CheckRun
	checksErr       error
	checksTruncated bool
	checksCalls     int
	onCheckRuns     func(calls int)
	repoComments    []forge.IssueComment
	repoCommentsErr error
	nextID          int64
	ops             []string
	reviewPosted    []forge.ReviewComment
	filePosted      []forge.ReviewComment
	reviewComments  []forge.IssueComment
	placements      []forge.Placement
	forceFallback   bool
}

// RefLedger answers the ledger store this fixture carries, the way the
// production client answers its ref store under the configured namespace.
func (f *fakeForge) RefLedger(string) prstate.LedgerStore { return f.store }

func (f *fakeForge) RepoSlug(context.Context) (core.Slug, error) {
	return core.ParseSlug("acme/widget")
}

func (f *fakeForge) DefaultBranch(context.Context, core.Slug) string { return "main" }

func (f *fakeForge) PullRequest(context.Context, core.Slug, int) (forge.PullRequest, error) {
	f.prCalls++
	if f.onPullRequest != nil {
		f.onPullRequest(f.prCalls)
	}
	if f.prErr != nil {
		return forge.PullRequest{}, f.prErr
	}
	return f.pr, nil
}

func (f *fakeForge) PullRequestDiff(context.Context, core.Slug, core.Revision, core.Revision) ([]byte, error) {
	f.diffCalls++
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
	f.threadCalls++
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

func (f *fakeForge) CheckRuns(context.Context, core.Slug, core.Revision) (forge.CheckRuns, error) {
	f.checksCalls++
	if f.onCheckRuns != nil {
		f.onCheckRuns(f.checksCalls)
	}
	if f.checksErr != nil {
		return forge.CheckRuns{}, f.checksErr
	}
	return forge.CheckRuns{Runs: f.checks, Truncated: f.checksTruncated}, nil
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
	// Only the redrive claim reads as a claim event. The claim heading alone
	// would also match the mid-pass "Findings recorded" edit, which is a
	// claim-comment write but not the claim post the order tests pin.
	if f.log != nil && strings.Contains(body, "Driving the pass again") {
		f.log.add("claim")
	}
	f.editIDs = append(f.editIDs, commentID)
	f.edits = append(f.edits, body)
	// An edit rewrites the comment, the way GitHub does: a later read — a
	// resumed leg loading its markers — sees the new body, not the old one.
	for i, c := range f.comments {
		if c.ID == commentID {
			f.comments[i].Body = body
		}
	}
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

func (f *fakeForge) ReviewFileComment(_ context.Context, comment forge.ReviewComment) (forge.Placement, error) {
	f.filePosted = append(f.filePosted, comment)
	f.ops = append(f.ops, "review-file-comment")
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
	// validate replaces the leg's validator seam, so a case can drive the
	// retry budgets without building a payload that fails for the right
	// reason. It takes the same ReviewExpectations the production seam
	// takes; cases that do not care about the batch ignore the second
	// argument.
	validate func([]byte, validate.ReviewExpectations) error
	// legEnv is what the leg hands a child. Nil is the default pair below.
	legEnv []string

	concernsOverride string
}

func newEnv(t *testing.T, concerns ...string) *env {
	t.Helper()
	// cred.Prepare reads process RUNNER_ENVIRONMENT. GitHub-hosted runners set
	// it to github-hosted, and a missing harness secret then stops the leg.
	// Tests are not that runner: isolate them the way cred treats self-hosted.
	t.Setenv("RUNNER_ENVIRONMENT", "self-hosted")
	dir := t.TempDir()
	events := &eventLog{}
	head := mustRev(t, headSHA)
	base := mustRev(t, baseSHA)
	vcs := &fakeVCS{files: map[string]map[string][]byte{
		baseSHA: {},
		"":      {},
	}}
	doc := mustDoc(t)
	// Probes report the descriptor's own pins unless a test says
	// otherwise: the installed CLI matches the verified pin, so the
	// installed gate passes and only the tests that move the install
	// exercise the refusal.
	versions := map[string]string{}
	for name, banner := range map[string]string{
		"codex":  "codex-cli %s",
		"claude": "%s (Claude Code)",
		"grok":   "grok %s (test stub)",
	} {
		if entry, found := doc.For(name); found {
			versions[entry.Binary] = fmt.Sprintf(banner, entry.Install.PinnedVersion)
		}
	}
	concern := ""
	if len(concerns) > 0 {
		concern = concerns[0]
	}
	return &env{
		concernsOverride: concern,
		log:              events,
		forge: &fakeForge{
			store: storetest.NewFakeStore(),
			log:   events,
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
		vcs:    vcs,
		runner: &fakeRunner{log: events, vcs: vcs, versions: versions},
		cfg:    mustConfig(t, ""),
		doc:    doc,
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
		Env:      e.env(),
		LookPath: look,
	}
}

func (e *env) request(t *testing.T) review.Request {
	t.Helper()
	return review.Request{
		PR:               42,
		Repo:             mustSlug(t),
		Trigger:          review.TriggerHuman,
		HarnessOverride:  "claude",
		ConcernsOverride: e.concernsOverride,
		Author:           author,
		Workdir:          e.dir,
		RunID:            runID,
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

// writeRequiredHead writes one required head file the coverage loop must
// account for. Frozen-path tests that stub no head files keep zero required
// units and stay on the single-prompt path; batch tests write head files
// and drive the coverage loop.
func writeRequiredHead(e *env, path, content string) {
	writeHead(e, path, content)
	if e.vcs.required == nil {
		e.vcs.required = map[string]bool{}
	}
	e.vcs.required[path] = true
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

// env is what the leg hands a child.
func (e *env) env() []string {
	if e.legEnv != nil {
		return e.legEnv
	}
	return []string{"PATH=/usr/bin:/bin", "HOME=" + e.dir}
}
