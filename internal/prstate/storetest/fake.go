package storetest

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/prstate"
)

// FakeStore is an in-memory implementation of prstate.LedgerStore for testing.
type FakeStore struct {
	mu             sync.Mutex
	generations    map[string]prstate.Generation
	published      []prstate.Generation
	handles        []prstate.Handle
	goneCommits    map[string]bool
	corruptCommits map[string]bool
	unreadable     bool
	unreadableErr  error
	isMarker       bool
}

// NewFakeStore returns an in-memory FakeStore producing ref handles by default.
func NewFakeStore() *FakeStore {
	return &FakeStore{
		generations:    make(map[string]prstate.Generation),
		goneCommits:    make(map[string]bool),
		corruptCommits: make(map[string]bool),
	}
}

// NewFakeMarkerStore returns an in-memory FakeStore producing marker handles.
func NewFakeMarkerStore() *FakeStore {
	return &FakeStore{
		generations:    make(map[string]prstate.Generation),
		goneCommits:    make(map[string]bool),
		corruptCommits: make(map[string]bool),
		isMarker:       true,
	}
}

// SetGone marks a commit SHA as gone, causing ReadGeneration to return ErrLedgerLost.
func (f *FakeStore) SetGone(commit string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.goneCommits[commit] = true
}

// SetCorrupt marks a commit SHA as corrupt, causing ReadGeneration to return ErrLedgerCorrupt.
func (f *FakeStore) SetCorrupt(commit string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.corruptCommits[commit] = true
}

// SetUnreadable configures the store to fail with a transient error on reads and publishes.
func (f *FakeStore) SetUnreadable(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.unreadable = true
	f.unreadableErr = err
}

// Published returns the history of published generations.
func (f *FakeStore) Published() []prstate.Generation {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]prstate.Generation, len(f.published))
	copy(out, f.published)
	return out
}

func (f *FakeStore) ReadGeneration(ctx context.Context, ref prstate.SlotRef, handle prstate.Handle) (prstate.Generation, error) {
	if err := ctx.Err(); err != nil {
		return prstate.Generation{}, err
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	if f.unreadable {
		if f.unreadableErr != nil {
			return prstate.Generation{}, f.unreadableErr
		}
		return prstate.Generation{}, fmt.Errorf("transient store read failure on %s#%d", ref.Repo, ref.Number)
	}

	if handle.Location == prstate.HandleMarker {
		raw, ok := handle.Payload.Get()
		if !ok || len(raw) == 0 {
			return prstate.Generation{}, prstate.ErrLedgerLost
		}
		var gen prstate.Generation
		if err := json.Unmarshal(raw, &gen); err != nil {
			return prstate.Generation{}, prstate.ErrLedgerCorrupt
		}
		return gen, nil
	}

	// Ref handle:
	if f.goneCommits[handle.Commit] {
		return prstate.Generation{}, prstate.ErrLedgerLost
	}
	if f.corruptCommits[handle.Commit] {
		return prstate.Generation{}, prstate.ErrLedgerCorrupt
	}
	gen, ok := f.generations[handle.Commit]
	if !ok {
		return prstate.Generation{}, prstate.ErrLedgerLost
	}
	return gen, nil
}

func (f *FakeStore) PublishGeneration(ctx context.Context, ref prstate.SlotRef, parent prstate.Handle, candidate prstate.Generation) (prstate.Handle, error) {
	if err := ctx.Err(); err != nil {
		return prstate.Handle{}, err
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	if f.unreadable {
		if f.unreadableErr != nil {
			return prstate.Handle{}, f.unreadableErr
		}
		return prstate.Handle{}, fmt.Errorf("transient store publish failure on %s#%d", ref.Repo, ref.Number)
	}

	genNum := candidate.Gen
	if genNum <= 0 {
		genNum = len(f.published) + 1
		candidate.Gen = genNum
	}
	f.published = append(f.published, candidate)

	if f.isMarker {
		data, err := json.Marshal(candidate)
		if err != nil {
			return prstate.Handle{}, err
		}
		h := prstate.Handle{
			Gen:      genNum,
			Location: prstate.HandleMarker,
			Degraded: candidate.Form == prstate.GenerationCompact,
			Payload:  prstate.Some(json.RawMessage(data)),
		}
		f.handles = append(f.handles, h)
		return h, nil
	}

	// Ref handle:
	commit := fmt.Sprintf("%040x", len(f.published))
	location := fmt.Sprintf("refs/crossrev/pr/%d/%s/coverage", ref.Number, ref.Slot)
	f.generations[commit] = candidate
	h := prstate.Handle{
		Gen:      genNum,
		Commit:   commit,
		Location: location,
		Degraded: candidate.Form == prstate.GenerationCompact,
	}
	f.handles = append(f.handles, h)
	return h, nil
}

var _ prstate.LedgerStore = (*FakeStore)(nil)

// FixtureGeneration creates a valid Generation for testing.
func FixtureGeneration(t interface{ Fatal(...any); Helper() }, form string) prstate.Generation {
	t.Helper()
	base, err := core.NewRevision("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	if err != nil {
		t.Fatal(err)
	}
	head, err := core.NewRevision("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	if err != nil {
		t.Fatal(err)
	}
	return prstate.Generation{
		Gen:      1,
		Revision: core.RevisionPair{Base: base, Head: head},
		Engine:   core.FileEngineVersion,
		Slot:     prstate.DefaultSlot,
		Producer: prstate.Producer{
			Harness:  "codex",
			Model:    "o3-mini",
			Effort:   "medium",
			Endpoint: "https://api.openai.com/v1",
		},
		Form:  form,
		Paths: []string{"file1.go", "file2.go"},
		Records: []prstate.Record{
			{PathIndex: 0, Type: prstate.CoverageRecordUnit, Verdict: prstate.Some("no_issue")},
		},
	}
}

// FixtureSlotRef creates a valid SlotRef for testing.
func FixtureSlotRef(t interface{ Fatal(...any); Helper() }) prstate.SlotRef {
	t.Helper()
	slug, err := core.NewSlug("carlosboeing", "crossrev")
	if err != nil {
		t.Fatal(err)
	}
	return prstate.SlotRef{
		Repo:   slug,
		Number: 42,
		Slot:   prstate.DefaultSlot,
	}
}
