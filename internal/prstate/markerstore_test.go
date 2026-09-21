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
	"github.com/carlosboeing/crossrev/internal/runlog"
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

	store := prstate.NewMarkerStore(func(p json.RawMessage) (string, error) { return bulk + string(p), nil }, prstate.OverflowHalt, passthroughFilter)
	var exhausted *prstate.LedgerExhausted
	if _, err := store.PublishGeneration(ctx, ref, prstate.Handle{}, gen); !errors.As(err, &exhausted) {
		t.Fatalf("a generation that fits alone but not in the comment was accepted: %v", err)
	}

	lone := prstate.NewMarkerStore(func(p json.RawMessage) (string, error) { return string(p), nil }, prstate.OverflowHalt, passthroughFilter)
	if _, err := lone.PublishGeneration(ctx, ref, prstate.Handle{}, gen); err != nil {
		t.Fatalf("the generation does not fit a comment of its own: %v", err)
	}
}

func TestTheLadderShedsThePredecessorBeforeDegrading(t *testing.T) {
	// Two full generations do not fit; one does. The predecessor goes and the
	// current generation stays full.
	ctx := context.Background()
	ref := storetest.FixtureSlotRef(t)
	store := prstate.NewMarkerStore(nil, prstate.OverflowDegrade, passthroughFilter)

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
	store := prstate.NewMarkerStore(nil, prstate.OverflowDegrade, passthroughFilter)

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
	store := prstate.NewMarkerStore(nil, prstate.OverflowDegrade, passthroughFilter)

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
	store := prstate.NewMarkerStore(nil, prstate.OverflowHalt, passthroughFilter)

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

// TestMarkerStoreFiltersBeforeDigesting pins the encode → publication
// filter → persisted-marker read round trip: the comment writer filters
// the whole comment after the payload is embedded, so the store must
// digest the filtered bytes. Refiltering the persisted payload the way
// that pass would must leave a generation that still verifies.
func TestMarkerStoreFiltersBeforeDigesting(t *testing.T) {
	ctx := context.Background()
	ref := storetest.FixtureSlotRef(t)
	rewrite := func(s string) (string, error) {
		return strings.ReplaceAll(s, "sensitive-data", "redacted-info"), nil
	}
	store := prstate.NewMarkerStore(nil, prstate.OverflowDegrade, rewrite)

	gen := storetest.FixtureGeneration(t, prstate.GenerationFull)
	gen.ScopeReport.ExaminedScope = "sensitive-data"
	gen.Records[0].Reason = prstate.Some("sensitive-data")

	handle, err := store.PublishGeneration(ctx, ref, prstate.Handle{}, gen)
	if err != nil {
		t.Fatalf("PublishGeneration: %v", err)
	}
	raw, ok := handle.Payload.Get()
	if !ok {
		t.Fatal("the marker handle carries no payload")
	}
	if strings.Contains(string(raw), "sensitive-data") {
		t.Fatal("the persisted payload still carries the unfiltered text")
	}
	refiltered, err := rewrite(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	readBack, err := store.ReadGeneration(ctx, ref, prstate.Handle{Location: prstate.HandleMarker, Payload: prstate.Some(json.RawMessage(refiltered))})
	if err != nil {
		t.Fatalf("the persisted generation does not verify after the publication filter: %v", err)
	}
	if reason, _ := readBack.Records[0].Reason.Get(); reason != "redacted-info" {
		t.Fatalf("reason = %q, want the filtered text", reason)
	}
	if readBack.ScopeReport.ExaminedScope != "redacted-info" {
		t.Fatalf("examined scope = %q, want the filtered text", readBack.ScopeReport.ExaminedScope)
	}
}

// TestMarkerStorePersistsWhatTheProductionFilterLeaves replays the
// production redaction over the round trip: a credential-shaped string in
// the examined scope is masked before digesting, and the whole-body pass
// the comment writer applies on the way out changes nothing further.
func TestMarkerStorePersistsWhatTheProductionFilterLeaves(t *testing.T) {
	ctx := context.Background()
	ref := storetest.FixtureSlotRef(t)
	var noDirLog *runlog.Log // nil: filtering without a run directory, the way comment publication does
	store := prstate.NewMarkerStore(nil, prstate.OverflowDegrade, noDirLog.Publish)

	secret := "ghp_ABCDEF1234567890abcdef"
	gen := storetest.FixtureGeneration(t, prstate.GenerationFull)
	gen.ScopeReport.ExaminedScope = "compared the helper against " + secret

	handle, err := store.PublishGeneration(ctx, ref, prstate.Handle{}, gen)
	if err != nil {
		t.Fatalf("PublishGeneration: %v", err)
	}
	raw, ok := handle.Payload.Get()
	if !ok {
		t.Fatal("the marker handle carries no payload")
	}
	if strings.Contains(string(raw), secret) {
		t.Fatal("the persisted payload still carries the credential-shaped string")
	}
	refiltered, err := noDirLog.Publish(string(raw))
	if err != nil {
		t.Fatalf("the publication filter failed on its own output: %v", err)
	}
	if _, err := store.ReadGeneration(ctx, ref, prstate.Handle{Location: prstate.HandleMarker, Payload: prstate.Some(json.RawMessage(refiltered))}); err != nil {
		t.Fatalf("the persisted generation does not verify after the publication filter: %v", err)
	}
}

// TestMarkerStoreRefusesToPublishWithoutTheFilter pins the wiring guard: a
// store built without the publication filter refuses to publish, because
// the comment writer would redact the payload after its digests were
// computed. Reads need no filter.
func TestMarkerStoreRefusesToPublishWithoutTheFilter(t *testing.T) {
	ctx := context.Background()
	ref := storetest.FixtureSlotRef(t)
	filtered := prstate.NewMarkerStore(nil, prstate.OverflowDegrade, passthroughFilter)
	handle, err := filtered.PublishGeneration(ctx, ref, prstate.Handle{}, storetest.FixtureGeneration(t, prstate.GenerationFull))
	if err != nil {
		t.Fatalf("PublishGeneration: %v", err)
	}

	unfiltered := prstate.NewMarkerStore(nil, prstate.OverflowDegrade, nil)
	if _, err := unfiltered.PublishGeneration(ctx, ref, prstate.Handle{}, storetest.FixtureGeneration(t, prstate.GenerationFull)); !errors.Is(err, prstate.ErrNoPublishFilter) {
		t.Fatalf("publishing without a filter answered %v, want %v", err, prstate.ErrNoPublishFilter)
	}
	if _, err := unfiltered.ReadGeneration(ctx, ref, handle); err != nil {
		t.Fatalf("reading without a filter answered %v, want success", err)
	}
}

// TestFilterGenerationCoversEveryTextField pins the filter's reach: every
// free-text field a generation carries passes through it, so no field can
// smuggle a credential-shaped string past the digests. It runs against the
// function directly rather than through a codec the test would have to
// satisfy field by field.
func TestFilterGenerationCoversEveryTextField(t *testing.T) {
	rewrite := func(s string) (string, error) {
		return strings.ReplaceAll(s, "sensitive-data", "redacted-info"), nil
	}
	gen := prstate.Generation{
		ScopeReport: prstate.ScopeReport{ExaminedScope: "sensitive-data", KnownLimits: []string{"sensitive-data"}},
		Records: []prstate.Record{{
			Reason:     prstate.Some("sensitive-data"),
			FindingIDs: []string{"sensitive-data"},
			Evidence:   []prstate.Evidence{{Source: "sensitive-data", Note: prstate.Some("sensitive-data")}},
		}},
		Advisory: prstate.Advisory{
			Rules:  []string{"sensitive-data"},
			Limits: []prstate.AdvisoryLimit{{Reason: "sensitive-data"}},
		},
		Excluded: []prstate.CoverageExclusion{{Reason: "sensitive-data"}},
	}
	if err := prstate.FilterGeneration(rewrite, &gen); err != nil {
		t.Fatalf("FilterGeneration: %v", err)
	}
	checks := map[string]string{
		"examined scope":  gen.ScopeReport.ExaminedScope,
		"known limit":     gen.ScopeReport.KnownLimits[0],
		"reason":          gen.Records[0].Reason.Value(),
		"finding id":      gen.Records[0].FindingIDs[0],
		"evidence source": gen.Records[0].Evidence[0].Source,
		"evidence note":   gen.Records[0].Evidence[0].Note.Value(),
		"advisory rule":   gen.Advisory.Rules[0],
		"advisory limit":  gen.Advisory.Limits[0].Reason,
		"exclusion":       gen.Excluded[0].Reason,
	}
	for field, got := range checks {
		if got != "redacted-info" {
			t.Errorf("%s = %q, want the filtered text", field, got)
		}
	}
}

// TestMarkerStoreAbortsOnFilterFailure mirrors the ref store's abort: a
// generation the filter could not process stops the write.
func TestMarkerStoreAbortsOnFilterFailure(t *testing.T) {
	ctx := context.Background()
	ref := storetest.FixtureSlotRef(t)
	failing := func(string) (string, error) { return "", errors.New("the credential filter failed") }
	store := prstate.NewMarkerStore(nil, prstate.OverflowDegrade, failing)
	if _, err := store.PublishGeneration(ctx, ref, prstate.Handle{}, storetest.FixtureGeneration(t, prstate.GenerationFull)); err == nil {
		t.Fatal("a ledger write survived a filter failure")
	} else if !strings.Contains(err.Error(), "filtering generation") {
		t.Fatalf("error %q does not name the filtering failure", err)
	}
}
