package prstate

import (
	"context"

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

// LedgerStore is the coverage ledger's durable surface. It is separate from
// the general parity-era Forge interface on purpose: the parity reads
// collapse failure to absence, where the ledger must tell an unreadable
// comment list from an empty one. Every call reports its failure, and no
// call edits a comment.
type LedgerStore interface {
	CoverageComments(ctx context.Context, repo core.Slug, number int) ([]CoverageComment, error)
	CoverageComment(ctx context.Context, repo core.Slug, commentID int64) (CoverageComment, error)
	CreateCoverageComment(ctx context.Context, repo core.Slug, number int, body string) (int64, error)
}

// Generation is one complete coverage generation a publisher packs: the
// number, the revision pair and engine it was made at, the path table, every
// record, and the summaries the manifest carries.
type Generation struct {
	Gen         int
	Revision    core.RevisionPair
	Engine      string
	Paths       []string
	Records     []Record
	Advisory    Advisory
	Excluded    []CoverageExclusion
	ScopeReport ScopeReport
}
