package review_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/exec"
	"github.com/carlosboeing/crossrev/internal/prstate"
	"github.com/carlosboeing/crossrev/internal/prstate/storetest"
	"github.com/carlosboeing/crossrev/internal/review"
)

// errCutoverTransient is the transient store failure the fail-closed cases
// read: neither lost nor corrupt, so nothing may treat it as absence.
var errCutoverTransient = errors.New("cutover test: the store cannot be read")

// cutoverPublish is one publication the leg handed the ledger store: the
// parent it resolved, the candidate it wrote, and the handle it got back.
type cutoverPublish struct {
	parent    prstate.Handle
	candidate prstate.Generation
	handle    prstate.Handle
}

// recordingLedger is a ledger store double that records every publication.
// It embeds the in-memory fake, so reads behave exactly as the store
// contract requires and only the recording is new.
type recordingLedger struct {
	*storetest.FakeStore
	published []cutoverPublish
	// refTarget is the commit the slot's ref currently names. The store
	// reads resolve the handle's commit and never consult it; a case
	// moves it to prove the parent does not follow.
	refTarget string
	// onPublish runs before each publish is recorded, so a case can move
	// the ref between batches the way another writer would.
	onPublish func(n int)
}

func (s *recordingLedger) PublishGeneration(ctx context.Context, ref prstate.SlotRef, parent prstate.Handle, candidate prstate.Generation) (prstate.Handle, error) {
	if s.onPublish != nil {
		s.onPublish(len(s.published))
	}
	handle, err := s.FakeStore.PublishGeneration(ctx, ref, parent, candidate)
	if err != nil {
		return handle, err
	}
	s.published = append(s.published, cutoverPublish{parent: parent, candidate: candidate, handle: handle})
	return handle, nil
}

var _ prstate.LedgerStore = (*recordingLedger)(nil)

// readFailingLedger is a recording ledger whose first read succeeds and
// whose later reads fail transiently, while its publishes succeed: the
// shape that proves a parent-read failure refuses rather than rooting a
// new chain. Resumption succeeds off the first read, so only the parent
// resolution can fail the pass; a store that failed the first read too
// would fail resumption instead, and a store that failed publishes would
// fail the publish — either way the test would pass for the wrong reason.
type readFailingLedger struct {
	*recordingLedger
	readErr error
	reads   int
}

func (s *readFailingLedger) ReadGeneration(ctx context.Context, ref prstate.SlotRef, handle prstate.Handle) (prstate.Generation, error) {
	s.reads++
	if s.reads == 1 {
		return s.recordingLedger.ReadGeneration(ctx, ref, handle)
	}
	return prstate.Generation{}, s.readErr
}

var _ prstate.LedgerStore = (*readFailingLedger)(nil)

// cutoverEnv writes the 41-file shape that packs into two batches (40 + 1),
// scripts one no_issue answer per batch, and hands the leg a recording
// ledger store. It returns the environment and the recorder.
func cutoverEnv(t *testing.T) (*env, *recordingLedger) {
	t.Helper()
	e := newEnv(t)
	var first, rest []string
	for i := 1; i <= 41; i++ {
		path := fmt.Sprintf("file%02d.go", i)
		writeRequiredHead(e, path, "package x\n")
		if i <= 40 {
			first = append(first, path)
		} else {
			rest = append(rest, path)
		}
	}
	e.runner.script = []exec.Result{
		{ExitCode: 0, Stdout: claudeStdout(batchAnswerFor(t, first))},
		{ExitCode: 0, Stdout: claudeStdout(batchAnswerFor(t, rest))},
	}
	recorder := &recordingLedger{FakeStore: storetest.NewFakeStore()}
	e.forge.store = recorder
	return e, recorder
}

// seedStartedClaim posts an open pass-1 review claim carrying the given
// coverage fields, so the run recovers the pass and resumes from the
// handle the marker names.
func seedStartedClaim(t *testing.T, e *env, marker prstate.Marker) {
	t.Helper()
	marker.Version = core.MarkerVersion
	marker.Leg = core.LegReview
	marker.Pass = 1
	marker.State = core.PassStarted
	marker.TS = frozenNow.Unix()
	marker.DoneTS = prstate.Null[int64]()
	marker.RunID = prstate.Some(runID)
	marker.HeadSHA = prstate.Some(headSHA)
	marker.Harness = prstate.Some("claude")
	marker.Findings = []byte("[]")
	e.forge.comments = append(e.forge.comments, commentWithMarker(t, 9001, marker))
}

// TestEachBatchParentsOnThePreviousBatch pins within-pass chaining: a pass
// publishes one generation per accepted batch plus the initial one, each
// parented on the previous publication. Parenting every batch on the
// pass's starting SHA would make them siblings, and every earlier batch
// would fall out of the retained ancestry and become collectable.
func TestEachBatchParentsOnThePreviousBatch(t *testing.T) {
	e, recorder := cutoverEnv(t)
	got := runLeg(t, e, e.request(t))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if e.runner.calls != 2 {
		t.Fatalf("harness calls = %d, want 2 (two batches)", e.runner.calls)
	}
	published := recorder.published // initial, b1, b2
	if len(published) != 3 {
		t.Fatalf("published %d generations through the ledger store, want 3 (initial plus one per accepted batch)", len(published))
	}
	for i := 1; i < len(published); i++ {
		if published[i].parent.Commit != published[i-1].handle.Commit {
			t.Fatalf("generation %d parents on %q, want the previous batch %q",
				i, published[i].parent.Commit, published[i-1].handle.Commit)
		}
	}
	commits := map[string]bool{}
	for _, p := range published {
		commits[p.handle.Commit] = true
	}
	// Every earlier generation is reachable from the tip by following
	// parents, so none falls out of the retained ancestry.
	reachable := map[string]bool{}
	next := published[len(published)-1].handle.Commit
	for next != "" {
		reachable[next] = true
		found := false
		for _, p := range published {
			if p.handle.Commit == next {
				next = p.parent.Commit
				found = true
				break
			}
		}
		if !found {
			break
		}
	}
	for commit := range commits {
		if !reachable[commit] {
			t.Fatalf("generation at %q fell out of the ancestry and is collectable", commit)
		}
	}
}

// TestAMovedRefIsNeverAdoptedAsAParent pins that the parent comes from the
// marker as it stands, never from the ref's current target: another writer
// moves the ref to a foreign commit between batches, and no publication
// parents onto it. Content addressing stops a commit being swapped after
// CrossRev names it; it does not vouch for what CrossRev parented onto.
func TestAMovedRefIsNeverAdoptedAsAParent(t *testing.T) {
	e, recorder := cutoverEnv(t)
	const foreign = "ffffffffffffffffffffffffffffffffffffffff"
	recorder.onPublish = func(n int) {
		if n == 1 {
			// Between the first and second publication another writer
			// moves the ref. The parent must still come from the
			// marker, so the foreign commit is never adopted.
			recorder.refTarget = foreign
		}
	}
	got := runLeg(t, e, e.request(t))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	published := recorder.published
	if len(published) != 3 {
		t.Fatalf("published %d generations through the ledger store, want 3", len(published))
	}
	for _, p := range published {
		if p.parent.Commit == foreign {
			t.Fatal("a foreign commit was adopted as a parent and its tip would land in a trusted marker")
		}
	}
	for i := 1; i < len(published); i++ {
		if published[i].parent.Commit != published[i-1].handle.Commit {
			t.Fatalf("generation %d parents on %q, want the previous batch %q",
				i, published[i].parent.Commit, published[i-1].handle.Commit)
		}
	}
}

// TestPublicationStartsANewChainWhenTheParentIsGone pins the lost-parent
// answer: the marker names a commit whose objects are gone, so the first
// publication roots a new chain with no parent. The lost history stays
// lost, which is what the re-review path already assumes, and a publish
// must never fail because its predecessor was deleted.
func TestPublicationStartsANewChainWhenTheParentIsGone(t *testing.T) {
	e := newEnv(t)
	writeRequiredHead(e, "a.go", "package a\n")
	e.runner.script = []exec.Result{
		{ExitCode: 0, Stdout: claudeStdout(batchAnswer(t, 1))},
	}
	recorder := &recordingLedger{FakeStore: storetest.NewFakeStore()}
	e.forge.store = recorder
	const lost = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	seedStartedClaim(t, e, prstate.Marker{
		CoverageGen:      prstate.Some(7),
		CoverageRef:      prstate.Some("refs/crossrev/pr/42/reviewer1/coverage"),
		CoverageCommit:   prstate.Some(lost),
		CoverageDegraded: prstate.Some(false),
	})

	got := runLeg(t, e, e.request(t))
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if len(recorder.published) != 2 {
		t.Fatalf("published %d generations through the ledger store, want 2 (initial plus one batch)", len(recorder.published))
	}
	if parent := recorder.published[0].parent; parent.Commit != "" || parent.Present() {
		t.Fatalf("the first publication after a lost parent rooted on %+v, want a new chain with no parent", parent)
	}
	if got.Outcome != review.OutcomeInvoked {
		t.Fatalf("Outcome = %q, want invoked (the lost history re-reviews)", got.Outcome)
	}
}

// TestPublicationFailsClosedWhenTheParentCannotBeRead pins the refusal
// answer: a transient read failure on the parent must fail the pass, not
// root a new chain — that would orphan a real history on a network blip.
func TestPublicationFailsClosedWhenTheParentCannotBeRead(t *testing.T) {
	e := newEnv(t)
	writeRequiredHead(e, "a.go", "package a\n")
	e.runner.script = []exec.Result{
		{ExitCode: 0, Stdout: claudeStdout(batchAnswer(t, 1))},
	}
	recorder := &recordingLedger{FakeStore: storetest.NewFakeStore()}
	e.forge.store = &readFailingLedger{recordingLedger: recorder, readErr: errCutoverTransient}
	// A readable generation behind the marker, retired by revision: the
	// first read (resumption) succeeds and retires it, so the pass
	// reaches publication and only the parent read can fail it.
	published := storetest.FixtureGeneration(t, prstate.GenerationFull)
	slug, err := core.ParseSlug("acme/widget")
	if err != nil {
		t.Fatal(err)
	}
	handle, err := recorder.FakeStore.PublishGeneration(context.Background(),
		prstate.SlotRef{Repo: slug, Number: 42, Slot: prstate.DefaultSlot},
		prstate.Handle{}, published)
	if err != nil {
		t.Fatal(err)
	}
	var checkpoint prstate.Marker
	checkpoint.RecordCoverage(handle)
	seedStartedClaim(t, e, checkpoint)

	got := runLeg(t, e, e.request(t))
	if got.Err == nil {
		t.Fatal("a pass whose parent could not be read published anyway; want the pass to fail closed")
	}
	if len(recorder.published) != 0 {
		t.Fatalf("published %d generations past an unreadable parent, want 0", len(recorder.published))
	}
	for _, label := range e.forge.labelsAdded {
		if strings.Contains(label, "converged") {
			t.Fatalf("converged label applied past an unreadable parent: %v", e.forge.labelsAdded)
		}
	}
}
