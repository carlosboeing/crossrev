package prstate_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/prstate"
	"github.com/carlosboeing/crossrev/internal/prstate/storetest"
)

func generationWithRecords(t *testing.T, count int, paddingPerRecord int) prstate.Generation {
	t.Helper()
	gen := storetest.FixtureGeneration(t, prstate.GenerationFull)
	template := gen.Records[0]
	paths := make([]string, count)
	records := make([]prstate.Record, count)
	for i := range count {
		paths[i] = fmt.Sprintf("dir/file_%04d.go", i)
		rec := template
		rec.PathIndex = i
		rec.UnitID = fmt.Sprintf("%016x", i)
		if paddingPerRecord > 0 {
			rec.Reason = prstate.Some(strings.Repeat("r", paddingPerRecord))
		}
		records[i] = rec
	}
	gen.Paths = paths
	gen.Records = records
	return gen
}

func generationOfSize(t *testing.T, targetBytes int) prstate.Generation {
	t.Helper()
	const approxBytesPerRecord = 650
	count := targetBytes / approxBytesPerRecord
	if count < 1 {
		count = 1
	}
	return generationWithRecords(t, count, 200)
}

// The bound measures the rendered comment: prose, findings, metadata and the
// retained generations. A generation that fits alone and does not fit beside
// the rest of the marker must be refused, because reporting success is the
// failure this check exists to prevent.
func TestTheBoundMeasuresTheRenderedCommentNotTheGeneration(t *testing.T) {
	ctx := context.Background()
	ref := storetest.FixtureSlotRef(t)
	gen := generationOfSize(t, 40*1024)
	bulk := strings.Repeat("x", 30*1024) // the summary, findings and metadata beside it

	store := prstate.NewMarkerStore(func(p json.RawMessage) (string, error) { return bulk + string(p), nil }, prstate.OverflowHalt)
	var exhausted *prstate.LedgerExhausted
	if _, err := store.PublishGeneration(ctx, ref, prstate.Handle{}, gen); !errors.As(err, &exhausted) {
		t.Fatalf("a generation that fits alone but not in the comment was accepted: %v", err)
	}

	lone := prstate.NewMarkerStore(func(p json.RawMessage) (string, error) { return string(p), nil }, prstate.OverflowHalt)
	if _, err := lone.PublishGeneration(ctx, ref, prstate.Handle{}, gen); err != nil {
		t.Fatalf("the generation does not fit a comment of its own: %v", err)
	}
}

func TestTheLadderShedsThePredecessorBeforeDegrading(t *testing.T) {
	// Two full generations do not fit; one does. The predecessor goes and the
	// current generation stays full.
	ctx := context.Background()
	ref := storetest.FixtureSlotRef(t)
	store := prstate.NewMarkerStore(nil, prstate.OverflowDegrade)

	gen1 := generationOfSize(t, 25*1024)
	gen2 := generationOfSize(t, 25*1024)
	h1, err := store.PublishGeneration(ctx, ref, prstate.Handle{}, gen1)
	if err != nil {
		t.Fatal(err)
	}
	h2, err := store.PublishGeneration(ctx, ref, h1, gen2)
	if err != nil {
		t.Fatal(err)
	}

	bulk := strings.Repeat("x", 20*1024)
	m := prstate.Marker{
		Version:             2,
		CoveragePayload:     h2.Payload.Value(),
		CoveragePrevPayload: h1.Payload.Value(),
	}
	render := func(cur prstate.Marker) (string, error) {
		return bulk + string(cur.CoveragePayload) + string(cur.CoveragePrevPayload), nil
	}

	shed, err := prstate.ShedToFit(&m, render, prstate.OverflowDegrade)
	if err != nil {
		t.Fatalf("ShedToFit failed: %v", err)
	}
	if !shed.DroppedPredecessor {
		t.Fatal("predecessor was not dropped")
	}
	if shed.Degraded {
		t.Fatal("the ladder degraded before shedding the predecessor")
	}
	if len(m.CoveragePrevPayload) != 0 {
		t.Fatal("CoveragePrevPayload was not cleared")
	}
	readGen, err := store.ReadGeneration(ctx, ref, prstate.Handle{Location: prstate.HandleMarker, Payload: prstate.Some(m.CoveragePayload)})
	if err != nil {
		t.Fatal(err)
	}
	if readGen.Form != prstate.GenerationFull {
		t.Fatalf("current generation form = %q, want %q", readGen.Form, prstate.GenerationFull)
	}
}

func TestTheLadderDegradesOnlyWhenSheddingIsNotEnough(t *testing.T) {
	ctx := context.Background()
	ref := storetest.FixtureSlotRef(t)
	store := prstate.NewMarkerStore(nil, prstate.OverflowDegrade)

	gen1 := generationOfSize(t, 10*1024)
	h1, err := store.PublishGeneration(ctx, ref, prstate.Handle{}, gen1)
	if err != nil {
		t.Fatal(err)
	}

	// 40KB full generation.
	gen2 := generationOfSize(t, 40*1024)
	h2, err := store.PublishGeneration(ctx, ref, h1, gen2)
	if err != nil {
		t.Fatal(err)
	}

	// 30KB bulk prose.
	// Both: ~10KB + ~41KB + 30KB = ~81KB > 65536
	// Current full alone: ~41KB + 30KB = ~71KB > 65536 (shedding predecessor is not enough!)
	// Compact current: ~3KB + 30KB = ~33KB <= 65536 (fits!)
	bulk := strings.Repeat("x", 30*1024)
	m := prstate.Marker{
		Version:             2,
		CoveragePayload:     h2.Payload.Value(),
		CoveragePrevPayload: h1.Payload.Value(),
	}
	render := func(cur prstate.Marker) (string, error) {
		return bulk + string(cur.CoveragePayload) + string(cur.CoveragePrevPayload), nil
	}

	shed, err := prstate.ShedToFit(&m, render, prstate.OverflowDegrade)
	if err != nil {
		t.Fatalf("ShedToFit failed: %v", err)
	}
	if !shed.DroppedPredecessor {
		t.Fatal("predecessor was not dropped")
	}
	if !shed.Degraded {
		t.Fatal("current generation was not degraded when shedding alone was not enough")
	}
	if len(m.CoveragePrevPayload) != 0 {
		t.Fatal("CoveragePrevPayload was not cleared")
	}
	readGen, err := store.ReadGeneration(ctx, ref, prstate.Handle{Location: prstate.HandleMarker, Payload: prstate.Some(m.CoveragePayload)})
	if err != nil {
		t.Fatal(err)
	}
	if readGen.Form != prstate.GenerationCompact {
		t.Fatalf("current generation form = %q, want %q", readGen.Form, prstate.GenerationCompact)
	}
}

func TestTheLadderHaltsWhenEvenOneCompactGenerationDoesNotFit(t *testing.T) {
	ctx := context.Background()
	ref := storetest.FixtureSlotRef(t)
	store := prstate.NewMarkerStore(nil, prstate.OverflowDegrade)

	gen := generationOfSize(t, 20*1024)
	h, err := store.PublishGeneration(ctx, ref, prstate.Handle{}, gen)
	if err != nil {
		t.Fatal(err)
	}
	// 65KB bulk prose alone is near cap; with generation it exceeds 65536
	bulk := strings.Repeat("x", 65*1024)
	m := prstate.Marker{
		Version:         2,
		CoveragePayload: h.Payload.Value(),
	}
	render := func(cur prstate.Marker) (string, error) {
		return bulk + string(cur.CoveragePayload) + string(cur.CoveragePrevPayload), nil
	}

	_, err = prstate.ShedToFit(&m, render, prstate.OverflowDegrade)
	if err == nil {
		t.Fatal("expected halt with LedgerExhausted")
	}
	var exhausted *prstate.LedgerExhausted
	if !errors.As(err, &exhausted) {
		t.Fatalf("expected *LedgerExhausted, got %v", err)
	}
	if exhausted.Stop.Limit != "ledger_exhausted" {
		t.Fatalf("stop limit = %q, want %q", exhausted.Stop.Limit, "ledger_exhausted")
	}
	if exhausted.Stop.MeasuredBytes <= prstate.CommentCap {
		t.Fatalf("measured bytes = %d, want > %d", exhausted.Stop.MeasuredBytes, prstate.CommentCap)
	}
}

func TestTheLadderHaltUnderOverflowHaltSkipsDegradation(t *testing.T) {
	ctx := context.Background()
	ref := storetest.FixtureSlotRef(t)
	store := prstate.NewMarkerStore(nil, prstate.OverflowHalt)

	// Case 1: Two full generations do not fit, but shedding the predecessor fits.
	// The predecessor goes first even under OverflowHalt.
	gen1 := generationOfSize(t, 25*1024)
	gen2 := generationOfSize(t, 25*1024)
	h1, err := store.PublishGeneration(ctx, ref, prstate.Handle{}, gen1)
	if err != nil {
		t.Fatal(err)
	}
	h2, err := store.PublishGeneration(ctx, ref, h1, gen2)
	if err != nil {
		t.Fatal(err)
	}
	bulk := strings.Repeat("x", 20*1024)
	m := prstate.Marker{
		Version:             2,
		CoveragePayload:     h2.Payload.Value(),
		CoveragePrevPayload: h1.Payload.Value(),
	}
	render := func(cur prstate.Marker) (string, error) {
		return bulk + string(cur.CoveragePayload) + string(cur.CoveragePrevPayload), nil
	}

	shed, err := prstate.ShedToFit(&m, render, prstate.OverflowHalt)
	if err != nil {
		t.Fatalf("ShedToFit failed: %v", err)
	}
	if !shed.DroppedPredecessor {
		t.Fatal("predecessor was not shed under OverflowHalt")
	}
	if shed.Degraded {
		t.Fatal("degraded under OverflowHalt")
	}

	// Case 2: One full generation does not fit. Under OverflowHalt, degradation is skipped and it halts immediately.
	genSingle := generationOfSize(t, 40*1024)
	hSingle, err := store.PublishGeneration(ctx, ref, prstate.Handle{}, genSingle)
	if err != nil {
		t.Fatal(err)
	}
	bulkSingle := strings.Repeat("x", 30*1024)
	mSingle := prstate.Marker{
		Version:         2,
		CoveragePayload: hSingle.Payload.Value(),
	}
	renderSingle := func(cur prstate.Marker) (string, error) {
		return bulkSingle + string(cur.CoveragePayload), nil
	}
	_, err = prstate.ShedToFit(&mSingle, renderSingle, prstate.OverflowHalt)
	if err == nil {
		t.Fatal("expected halt under OverflowHalt when full generation does not fit")
	}
	var exhausted *prstate.LedgerExhausted
	if !errors.As(err, &exhausted) {
		t.Fatalf("expected *LedgerExhausted, got %v", err)
	}
}

func TestHaltUnderOverflowHaltSkipsDegradation(t *testing.T) {
	TestTheLadderHaltUnderOverflowHaltSkipsDegradation(t)
}
