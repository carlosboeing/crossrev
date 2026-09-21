package prstate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// Shed records what ShedToFit had to discard so the caller can record it.
type Shed struct {
	DroppedPredecessor bool
	Degraded           bool
}

const (
	OverflowDegrade = "degrade"
	OverflowHalt    = "halt"
)

// ShedToFit applies the retention ladder to a marker until the rendered
// comment fits: shed the predecessor, then compact the current under
// OverflowDegrade, then halt. It returns what it had to give up so the
// caller can record it, and *LedgerExhausted when even one compact
// generation does not fit.
//
// The predecessor goes first because nothing consults it — it is a
// convenience the ref store gets free from its parent chain — and the
// current generation costs a re-review.
func ShedToFit(m *Marker, render func(Marker) (string, error), onOverflow string) (Shed, error) {
	rendered, err := render(*m)
	if err != nil {
		return Shed{}, err
	}
	if err := FitMarkerComment(rendered); err == nil {
		return Shed{}, nil
	}

	// 1. Shed the predecessor first.
	droppedPredecessor := false
	if len(m.CoveragePrevPayload) > 0 {
		m.CoveragePrevPayload = nil
		droppedPredecessor = true
		rendered, err = render(*m)
		if err != nil {
			return Shed{}, err
		}
		if err := FitMarkerComment(rendered); err == nil {
			return Shed{DroppedPredecessor: true, Degraded: false}, nil
		}
	}

	// 2. Compact the current under OverflowDegrade.
	if onOverflow == OverflowDegrade && len(m.CoveragePayload) > 0 {
		gen, err := decodeGenerationPayload(m.CoveragePayload)
		if err == nil && gen.Form != GenerationCompact {
			compactGen := CompactGeneration(gen)
			compactPayload, err := encodeGenerationPayload(compactGen)
			if err != nil {
				return Shed{}, err
			}
			m.CoveragePayload = compactPayload
			rendered, err = render(*m)
			if err != nil {
				return Shed{}, err
			}
			if err := FitMarkerComment(rendered); err == nil {
				return Shed{DroppedPredecessor: droppedPredecessor, Degraded: true}, nil
			}
		}
	}

	// 3. Halt with LedgerExhausted when even one compact generation does not fit
	// (or under OverflowHalt when shedding the predecessor was not enough).
	var stop CoverageStop
	if len(m.CoveragePayload) > 0 {
		if gen, err := decodeGenerationPayload(m.CoveragePayload); err == nil {
			outstanding, required := generationCounts(gen)
			stop = CoverageStop{
				RequiredCount:    required,
				CoveredCount:     required - outstanding,
				OutstandingCount: outstanding,
				MeasuredBytes:    len(rendered),
				Limit:            CoverageStopLimit,
			}
		}
	}
	if stop.Limit == "" {
		stop = CoverageStop{
			MeasuredBytes: len(rendered),
			Limit:         CoverageStopLimit,
		}
	}
	return Shed{DroppedPredecessor: droppedPredecessor}, &LedgerExhausted{Stop: stop}
}

func encodeGenerationPayload(g Generation) (json.RawMessage, error) {
	manifest, records, err := EncodeGenerationV2(g)
	if err != nil {
		return nil, err
	}
	obj := object{
		{key: "manifest", value: json.RawMessage(manifest)},
		{key: "records", value: json.RawMessage(records)},
	}
	return json.RawMessage(obj.marshal()), nil
}

func decodeGenerationPayload(raw json.RawMessage) (Generation, error) {
	if len(raw) == 0 {
		return Generation{}, ErrLedgerLost
	}
	var p struct {
		Manifest json.RawMessage `json:"manifest"`
		Records  json.RawMessage `json:"records"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return Generation{}, fmt.Errorf("%w: %v", ErrLedgerCorrupt, err)
	}
	if len(p.Manifest) == 0 || len(p.Records) == 0 {
		return Generation{}, fmt.Errorf("%w: missing manifest or records", ErrLedgerCorrupt)
	}
	gen, err := DecodeGenerationV2(p.Manifest, p.Records)
	if err != nil {
		return Generation{}, fmt.Errorf("%w: %v", ErrLedgerCorrupt, err)
	}
	return gen, nil
}

type markerStore struct {
	render     func(payload json.RawMessage) (string, error)
	onOverflow string
	filter     func(string) (string, error)
}

// ErrNoPublishFilter is what the marker store refuses a publication with
// when it was built without the publication filter: the comment writer
// would redact the payload after its digests were computed, so the
// persisted generation would never verify. Reads need no filter.
var ErrNoPublishFilter = errors.New("no publish filter is installed, so nothing was published")

// NewMarkerStore returns the fallback store, which carries its payload inside
// the pass marker comment. render composes the whole comment the leg will
// send, so the bound measures the bytes that will actually be written.
// filter is the same publication filter the comment writer applies, and the
// store runs it before encoding: the digests must describe the persisted
// bytes, not the pre-redaction ones. A nil filter serves reads only.
func NewMarkerStore(render func(payload json.RawMessage) (string, error), onOverflow string, filter func(string) (string, error)) LedgerStore {
	return &markerStore{
		render:     render,
		onOverflow: onOverflow,
		filter:     filter,
	}
}

func (s *markerStore) ReadGeneration(ctx context.Context, ref SlotRef, handle Handle) (Generation, error) {
	if err := ctx.Err(); err != nil {
		return Generation{}, err
	}
	raw, ok := handle.Payload.Get()
	if !ok || len(raw) == 0 {
		return Generation{}, ErrLedgerLost
	}
	return decodeGenerationPayload(raw)
}

func (s *markerStore) PublishGeneration(ctx context.Context, ref SlotRef, parent Handle, candidate Generation) (Handle, error) {
	if err := ctx.Err(); err != nil {
		return Handle{}, err
	}
	if s.filter == nil {
		return Handle{}, ErrNoPublishFilter
	}
	// Filter before digesting, the way the ref store does: the comment
	// writer filters the whole comment again on the way out, and the
	// digests must already describe the redacted bytes. Abort immediately
	// on filter failure!
	if err := FilterGeneration(s.filter, &candidate); err != nil {
		return Handle{}, fmt.Errorf("filtering generation: %w", err)
	}
	render := s.render
	if render == nil {
		render = func(p json.RawMessage) (string, error) { return string(p), nil }
	}

	genNum := candidate.Gen
	if genNum <= 0 {
		if parent.Gen > 0 {
			genNum = parent.Gen + 1
		} else {
			genNum = 1
		}
		candidate.Gen = genNum
	}

	payload, err := encodeGenerationPayload(candidate)
	if err != nil {
		return Handle{}, err
	}

	rendered, err := render(payload)
	if err != nil {
		return Handle{}, err
	}

	if err := FitMarkerComment(rendered); err == nil {
		return Handle{
			Gen:      genNum,
			Location: HandleMarker,
			Degraded: candidate.Form == GenerationCompact,
			Payload:  Some(payload),
		}, nil
	}

	// Does not fit rendered comment: apply ladder
	if candidate.Form != GenerationCompact && s.onOverflow == OverflowDegrade {
		compactGen := CompactGeneration(candidate)
		compactPayload, err := encodeGenerationPayload(compactGen)
		if err != nil {
			return Handle{}, err
		}
		renderedCompact, err := render(compactPayload)
		if err != nil {
			return Handle{}, err
		}
		if err := FitMarkerComment(renderedCompact); err == nil {
			return Handle{
				Gen:      genNum,
				Location: HandleMarker,
				Degraded: true,
				Payload:  Some(compactPayload),
			}, nil
		}
		// Even compact does not fit
		outstanding, required := generationCounts(compactGen)
		stop := CoverageStop{
			RequiredCount:    required,
			CoveredCount:     required - outstanding,
			OutstandingCount: outstanding,
			MeasuredBytes:    len(renderedCompact),
			Limit:            CoverageStopLimit,
		}
		return Handle{}, &LedgerExhausted{Stop: stop}
	}

	// Full does not fit and onOverflow is halt (or candidate was already compact).
	outstanding, required := generationCounts(candidate)
	stop := CoverageStop{
		RequiredCount:    required,
		CoveredCount:     required - outstanding,
		OutstandingCount: outstanding,
		MeasuredBytes:    len(rendered),
		Limit:            CoverageStopLimit,
	}
	return Handle{}, &LedgerExhausted{Stop: stop}
}
