package prstate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/carlosboeing/crossrev/internal/core"
)

// Ledger limits.
//
// A generation holds at most MaxCoverageShards shards. The design's §10.6
// fires its halt bound there; this release keeps the last complete generation
// and records stop diagnostics rather than publishing a partial one as
// complete.
const MaxCoverageShards = 32

// CoverageStopLimit is the recorded limit name when packing crosses the
// shard bound.
const CoverageStopLimit = "ledger_exhausted"

// CoverageComment is one comment as the ledger reads it: the id GitHub
// assigned, who wrote it, and the body it carries.
type CoverageComment struct {
	ID     int64
	Author string
	Body   string
}

// CommentStore is the comment-backed coverage ledger's durable surface. It is
// separate from the general parity-era Forge interface on purpose: the parity
// reads collapse failure to absence, where the ledger must tell an unreadable
// comment list from an empty one. Every call reports its failure, and no call
// edits a comment.
type CommentStore interface {
	CoverageComments(ctx context.Context, repo core.Slug, number int) ([]CoverageComment, error)
	CoverageComment(ctx context.Context, repo core.Slug, commentID int64) (CoverageComment, error)
	CreateCoverageComment(ctx context.Context, repo core.Slug, number int, body string) (int64, error)
}

const DefaultSlot = "reviewer1"

// Generation forms.
const (
	GenerationFull    = "full"
	GenerationCompact = "compact"
)

// SlotRef addresses one reviewer's ledger on one pull request.
type SlotRef struct {
	Repo   core.Slug
	Number int
	Slot   string // DefaultSlot unless the operator named it
}

// Producer is the configuration that produced a verdict. A change to any
// member retires the verdict, the way a revision change does.
type Producer struct {
	Harness  string `json:"harness"`
	Model    string `json:"model"`
	Effort   string `json:"effort"`
	Endpoint string `json:"endpoint"`
}

// Generation is one slot's complete coverage at one revision under one
// producer. Slot, Producer and Form live on the envelope rather than on every
// record: one generation is one slot's work under one configuration, so
// per-record copies would be redundant, and the envelope travels whatever the
// records are reduced to — which is what makes the compact form correct by
// construction rather than by special case (D3).
type Generation struct {
	Gen         int
	Revision    core.RevisionPair
	Engine      string
	Slot        string
	Producer    Producer
	Form        string
	Paths       []string
	Records     []Record
	Advisory    Advisory
	Excluded    []CoverageExclusion
	ScopeReport ScopeReport
}

// Handle is what a marker records to name a published generation, and what a
// later reader is given back.
type Handle struct {
	Gen      int
	Commit   string // the commit SHA; empty for the marker store
	Location string // a ref name, or HandleMarker
	Degraded bool   // published in the compact form
	// Payload is the encoded generation the marker store carries inline,
	// absent for the ref store. The marker is the fallback's authority and
	// its transport both.
	Payload Opt[json.RawMessage]
}

const HandleMarker = "marker"

// Present reports a handle naming a generation at all.
func (h Handle) Present() bool {
	return h.Gen > 0 || h.Commit != "" || h.Location != "" || h.Payload.Present()
}

// Valid reports a handle whose shape matches its location: a ref handle
// carries a commit and no payload, a marker handle carries a payload and no
// commit. A malformed combination is corrupt state, never "no coverage".
func (h Handle) Valid() error {
	if h.Location == "" {
		return errors.New("handle location must be non-empty")
	}
	if h.Location == HandleMarker {
		if h.Commit != "" {
			return errors.New("marker handle must not carry a commit SHA")
		}
		if !h.Payload.Present() {
			return errors.New("marker handle must carry an inline payload")
		}
		return nil
	}
	if h.Commit == "" {
		return errors.New("ref handle must carry a commit SHA")
	}
	if h.Payload.Present() {
		return errors.New("ref handle must not carry an inline payload")
	}
	return nil
}

type LedgerStore interface {
	// ReadGeneration returns the generation this handle names. It takes no
	// revision or engine: the generation carries its own in the envelope, and
	// judging them here would make a legitimately older generation
	// indistinguishable from corruption. Retirement is GenerationCurrent's
	// question, above the store.
	ReadGeneration(ctx context.Context, ref SlotRef, handle Handle) (Generation, error)

	// PublishGeneration writes one complete generation and returns the handle
	// a marker records. parent is the handle this slot's marker currently
	// records — never the current ref target, which another writer can move.
	// A zero parent starts a new chain.
	//
	// This is not the commit point. The caller recording the returned handle
	// in the pass marker is, because the marker is the only thing a reader
	// trusts.
	PublishGeneration(ctx context.Context, ref SlotRef, parent Handle, candidate Generation) (Handle, error)
}

// The store's three named failures. Anything else is transient.
var (
	// ErrLedgerLost is a handle whose objects are gone. It re-reviews: losing
	// the ledger must cost repeated work, never a wrong answer.
	ErrLedgerLost = errors.New("the generation this handle names is gone")
	// ErrLedgerCorrupt is a handle whose objects read and do not decode or do
	// not verify. A failure, not a transient error, and never absence.
	ErrLedgerCorrupt = errors.New("the generation this handle names does not verify")
	// ErrNoCompleteGeneration reports that no complete generation exists at a
	// revision pair: absence, not refusal.
	ErrNoCompleteGeneration = errors.New("no complete generation at this revision")
)

// LedgerExhausted carries the stop a halted publication records.
type LedgerExhausted struct{ Stop CoverageStop }

func (e *LedgerExhausted) Error() string {
	if e == nil {
		return "coverage ledger exhausted"
	}
	return fmt.Sprintf("coverage ledger exhausted: limit %q reached (%d measured bytes, %d required, %d covered, %d outstanding)",
		e.Stop.Limit, e.Stop.MeasuredBytes, e.Stop.RequiredCount, e.Stop.CoveredCount, e.Stop.OutstandingCount)
}

// GenerationCurrent reports whether a generation may still be reused: the
// revision pair, the engine and the producer must all match what is in force
// now. One predicate, so a fourth reason to retire cannot be added to three
// readers and missed on the fourth.
func GenerationCurrent(g Generation, revision core.RevisionPair, engine string, producer Producer) bool {
	if revision.Incomplete() || g.Revision.Incomplete() {
		return false
	}
	if !g.Revision.Equal(revision) {
		return false
	}
	if engine == "" || g.Engine != engine {
		return false
	}
	if g.Producer != producer {
		return false
	}
	return true
}
