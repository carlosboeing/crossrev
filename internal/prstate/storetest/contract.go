package storetest

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/prstate"
)

// Contract runs one store through the whole publication and read contract.
// Both backends run it, so the two implementations are held to identical
// behaviour rather than each to its own tests.
func Contract(t *testing.T, name string, newStore func(t *testing.T) prstate.LedgerStore) {
	t.Run(name+"/publish then read returns what was published", func(t *testing.T) {
		ctx := context.Background()
		store := newStore(t)
		ref := FixtureSlotRef(t)
		candidate := FixtureGeneration(t, prstate.GenerationFull)

		handle, err := store.PublishGeneration(ctx, ref, prstate.Handle{}, candidate)
		if err != nil {
			t.Fatalf("PublishGeneration failed: %v", err)
		}
		read, err := store.ReadGeneration(ctx, ref, handle)
		if err != nil {
			t.Fatalf("ReadGeneration failed: %v", err)
		}
		if read.Gen != candidate.Gen {
			t.Errorf("read Gen = %d, want %d", read.Gen, candidate.Gen)
		}
		if !read.Revision.Equal(candidate.Revision) {
			t.Errorf("read Revision = %v, want %v", read.Revision, candidate.Revision)
		}
		if read.Engine != candidate.Engine {
			t.Errorf("read Engine = %q, want %q", read.Engine, candidate.Engine)
		}
		if len(read.Paths) != len(candidate.Paths) {
			t.Errorf("read %d paths, want %d", len(read.Paths), len(candidate.Paths))
		}
		if len(read.Records) != len(candidate.Records) {
			t.Errorf("read %d records, want %d", len(read.Records), len(candidate.Records))
		}
	})

	t.Run(name+"/a handle naming gone objects is ErrLedgerLost", func(t *testing.T) {
		ctx := context.Background()
		store := newStore(t)
		ref := FixtureSlotRef(t)

		var goneHandle prstate.Handle
		if name == "marker" {
			goneHandle = prstate.Handle{
				Gen:      1,
				Location: prstate.HandleMarker,
				Payload:  prstate.Opt[json.RawMessage]{},
			}
		} else {
			goneHandle = prstate.Handle{
				Gen:      1,
				Commit:   "0000000000000000000000000000000000000000",
				Location: "refs/crossrev/pr/42/reviewer1/coverage",
			}
		}
		_, err := store.ReadGeneration(ctx, ref, goneHandle)
		if !errors.Is(err, prstate.ErrLedgerLost) {
			t.Fatalf("reading gone handle answered %v, want %v", err, prstate.ErrLedgerLost)
		}
	})

	t.Run(name+"/a handle naming unverifiable bytes is ErrLedgerCorrupt", func(t *testing.T) {
		ctx := context.Background()
		store := newStore(t)
		ref := FixtureSlotRef(t)

		var corruptHandle prstate.Handle
		if name == "marker" {
			corruptHandle = prstate.Handle{
				Gen:      1,
				Location: prstate.HandleMarker,
				Payload:  prstate.Some(json.RawMessage(`{invalid-json`)),
			}
		} else {
			if fake, ok := store.(*FakeStore); ok {
				fake.SetCorrupt("corrupt00000000000000000000000000000000")
			}
			corruptHandle = prstate.Handle{
				Gen:      1,
				Commit:   "corrupt00000000000000000000000000000000",
				Location: "refs/crossrev/pr/42/reviewer1/coverage",
			}
		}
		_, err := store.ReadGeneration(ctx, ref, corruptHandle)
		if !errors.Is(err, prstate.ErrLedgerCorrupt) {
			t.Fatalf("reading corrupt handle answered %v, want %v", err, prstate.ErrLedgerCorrupt)
		}
	})

	t.Run(name+"/an unreadable store is neither sentinel", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		store := newStore(t)
		ref := FixtureSlotRef(t)

		handle := prstate.Handle{
			Gen:      1,
			Commit:   "1111111111111111111111111111111111111111",
			Location: "refs/crossrev/pr/42/reviewer1/coverage",
		}
		if name == "marker" {
			handle = prstate.Handle{
				Gen:      1,
				Location: prstate.HandleMarker,
				Payload:  prstate.Some(json.RawMessage(`{}`)),
			}
		}
		_, err := store.ReadGeneration(ctx, ref, handle)
		if err == nil {
			t.Fatal("read with canceled context succeeded")
		}
		if errors.Is(err, prstate.ErrLedgerLost) {
			t.Fatalf("an unreadable store was reported as ErrLedgerLost: %v", err)
		}
		if errors.Is(err, prstate.ErrLedgerCorrupt) {
			t.Fatalf("an unreadable store was reported as ErrLedgerCorrupt: %v", err)
		}
	})

	t.Run(name+"/a published handle is Valid for this backend", func(t *testing.T) {
		ctx := context.Background()
		store := newStore(t)
		ref := FixtureSlotRef(t)
		candidate := FixtureGeneration(t, prstate.GenerationFull)

		handle, err := store.PublishGeneration(ctx, ref, prstate.Handle{}, candidate)
		if err != nil {
			t.Fatalf("PublishGeneration failed: %v", err)
		}
		if err := handle.Valid(); err != nil {
			t.Fatalf("published handle is not valid: %v", err)
		}
		if name == "refs" {
			if handle.Commit == "" {
				t.Fatal("ref store published handle has no commit")
			}
			if handle.Payload.Present() {
				t.Fatal("ref store published handle carried an inline payload")
			}
		}
		if name == "marker" {
			if handle.Commit != "" {
				t.Fatal("marker store published handle carried a commit")
			}
			if !handle.Payload.Present() {
				t.Fatal("marker store published handle carried no inline payload")
			}
		}
	})

	t.Run(name+"/an older revision reads back rather than refusing", func(t *testing.T) {
		ctx := context.Background()
		store := newStore(t)
		ref := FixtureSlotRef(t)

		oldBase, err := core.NewRevision("1111111111111111111111111111111111111111")
		if err != nil {
			t.Fatal(err)
		}
		oldHead, err := core.NewRevision("2222222222222222222222222222222222222222")
		if err != nil {
			t.Fatal(err)
		}
		candidate := FixtureGeneration(t, prstate.GenerationFull)
		candidate.Revision = core.RevisionPair{Base: oldBase, Head: oldHead}

		handle, err := store.PublishGeneration(ctx, ref, prstate.Handle{}, candidate)
		if err != nil {
			t.Fatalf("PublishGeneration failed: %v", err)
		}
		read, err := store.ReadGeneration(ctx, ref, handle)
		if err != nil {
			t.Fatalf("ReadGeneration refused an older revision: %v", err)
		}
		if !read.Revision.Equal(candidate.Revision) {
			t.Fatalf("read revision mismatch: got %v, want %v", read.Revision, candidate.Revision)
		}
	})
}
