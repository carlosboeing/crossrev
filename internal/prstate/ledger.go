package prstate

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/carlosboeing/crossrev/internal/core"
)

// PublishGeneration packs one complete generation into the ledger store.
//
// Shards are created first, each new shard is read back by comment id and
// verified for position and digest, the revision pair is rechecked through
// stillCurrent, and the manifest comment is created last. A crash before the
// manifest leaves the previous manifest intact; nothing is ever edited, and
// no partial generation is exposed as complete.
//
// stillCurrent reports whether the base and head the candidate was built at
// still hold. A head that moved during publication retires the candidate
// rather than publishing stale coverage under the new revision.
//
// Crossing MaxCoverageShards stops with ledger stop diagnostics while the
// last complete generation stands. The stop carries the measured totals and
// the limit name; it never counts as a complete generation.
func PublishGeneration(ctx context.Context, store LedgerStore, repo core.Slug, number int, candidate Generation, stillCurrent func() error) (Manifest, CoverageStop, error) {
	if candidate.Revision.Incomplete() {
		return Manifest{}, CoverageStop{}, coverageErrorf("publishing a generation with no revision pair")
	}
	if candidate.Engine == "" {
		return Manifest{}, CoverageStop{}, coverageErrorf("publishing a generation with no engine")
	}
	if len(candidate.Paths) == 0 && len(candidate.Records) > 0 {
		return Manifest{}, CoverageStop{}, coverageErrorf("publishing %d records with no path table", len(candidate.Records))
	}

	packed, stop, err := packRecords(candidate)
	if err != nil {
		return Manifest{}, CoverageStop{}, err
	}
	if stop != nil {
		return Manifest{}, *stop, nil
	}

	shards := make([]ShardRef, 0, len(packed))
	for pos, records := range packed {
		body, err := EncodeCoverageShard(BuildShard(pos, records))
		if err != nil {
			return Manifest{}, CoverageStop{}, err
		}
		id, err := store.CreateCoverageComment(ctx, repo, number, body)
		if err != nil {
			return Manifest{}, CoverageStop{}, err
		}
		read, err := store.CoverageComment(ctx, repo, id)
		if err != nil {
			return Manifest{}, CoverageStop{}, err
		}
		shard, ok := DecodeCoverageShard(read.Body)
		if !ok {
			return Manifest{}, CoverageStop{}, coverageErrorf("the shard read back from comment %d does not decode", id)
		}
		if shard.Pos != pos {
			return Manifest{}, CoverageStop{}, coverageErrorf("the shard read back from comment %d sits at %d, want %d", id, shard.Pos, pos)
		}
		encoded, err := EncodeCoverageShard(shard)
		if err != nil {
			return Manifest{}, CoverageStop{}, err
		}
		again, ok := DecodeCoverageShard(encoded)
		if !ok || again.Digest != shard.Digest {
			return Manifest{}, CoverageStop{}, coverageErrorf("the shard read back from comment %d does not round-trip", id)
		}
		shards = append(shards, ShardRef{Pos: pos, ID: id, Digest: shard.Digest, N: len(records)})
	}

	if err := stillCurrent(); err != nil {
		return Manifest{}, CoverageStop{}, err
	}

	outstanding := 0
	for _, record := range candidate.Records {
		if record.Type == CoverageRecordOutstanding {
			outstanding++
		}
	}
	manifest := BuildManifest(candidate.Gen, candidate.Revision.Base.SHA(), candidate.Revision.Head.SHA(),
		candidate.Engine, candidate.Paths, shards, outstanding, len(candidate.Records),
		candidate.Advisory, candidate.Excluded, candidate.ScopeReport)
	body, err := EncodeCoverageManifest(manifest)
	if err != nil {
		return Manifest{}, CoverageStop{}, err
	}
	id, err := store.CreateCoverageComment(ctx, repo, number, body)
	if err != nil {
		return Manifest{}, CoverageStop{}, err
	}
	read, err := store.CoverageComment(ctx, repo, id)
	if err != nil {
		return Manifest{}, CoverageStop{}, err
	}
	published, ok := DecodeCoverageManifest(read.Body)
	if !ok {
		return Manifest{}, CoverageStop{}, coverageErrorf("the manifest read back from comment %d does not decode", id)
	}
	if published.Gen != candidate.Gen || published.Digest != manifestDigest(body) {
		return Manifest{}, CoverageStop{}, coverageErrorf("the manifest read back from comment %d is not the one written", id)
	}
	published.commentID = id
	published.raw = manifestRaw(body)
	return published, CoverageStop{}, nil
}

// packRecords packs records into shards by measured bytes. One shard holds
// records until the next would cross the fill line; crossing MaxCoverageShards
// stops with diagnostics while the last complete generation stands.
func packRecords(candidate Generation) ([][]Record, *CoverageStop, error) {
	const (
		commentCap      = 65536
		envelopeReserve = 256
	)
	const fillBytes = (commentCap - envelopeReserve) * 9 / 10

	measured := func(records []Record) int {
		body, err := EncodeCoverageShard(BuildShard(0, records))
		if err != nil {
			return -1
		}
		return len(body)
	}

	var shards [][]Record
	var current []Record
	flush := func() {
		if len(current) > 0 {
			shards = append(shards, current)
			current = nil
		}
	}
	for _, record := range candidate.Records {
		candidate := append(append([]Record{}, current...), record)
		size := measured(candidate)
		if size < 0 {
			return nil, nil, coverageErrorf("packing a record that does not encode")
		}
		if size <= fillBytes {
			current = candidate
			continue
		}
		if len(current) == 0 {
			// One record over the fill line still ships alone, under the
			// cap: a record that cannot fit the cap alone is refused
			// rather than published truncated.
			alone := measured([]Record{record})
			if alone > commentCap-envelopeReserve {
				return nil, nil, coverageErrorf("a record larger than one comment cannot be published")
			}
			shards = append(shards, []Record{record})
			continue
		}
		flush()
		current = []Record{record}
	}
	flush()
	if len(shards) == 0 {
		shards = [][]Record{{}}
	}

	if len(shards) > MaxCoverageShards {
		covered := 0
		for _, records := range shards {
			covered += len(records)
		}
		outstanding := 0
		for _, record := range candidate.Records {
			if record.Type == CoverageRecordOutstanding {
				outstanding++
			}
		}
		manifest, err := EncodeCoverageManifest(BuildManifest(candidate.Gen,
			candidate.Revision.Base.SHA(), candidate.Revision.Head.SHA(), candidate.Engine,
			candidate.Paths, nil, outstanding, len(candidate.Records),
			candidate.Advisory, candidate.Excluded, candidate.ScopeReport))
		if err != nil {
			return nil, nil, err
		}
		return nil, &CoverageStop{
			RequiredCount:    len(candidate.Records),
			CoveredCount:     covered,
			OutstandingCount: outstanding,
			MeasuredBytes:    len(manifest),
			ShardCount:       len(shards),
			Limit:            CoverageStopLimit,
		}, nil
	}
	return shards, nil, nil
}

// manifestDigest reads the digest out of an encoded manifest body.
func manifestDigest(body string) string {
	m, ok := DecodeCoverageManifest(body)
	if !ok {
		return ""
	}
	return m.Digest
}

// manifestRaw reads the payload bytes out of an encoded manifest body.
func manifestRaw(body string) []byte {
	return []byte(extractCoveragePayload(body))
}

// SelectGeneration reads the highest complete generation out of the ledger
// comments.
//
// Only comments by the trusted author are read; anything else is another
// writer's bytes and contributes nothing. Manifests must carry the exact
// base, head and engine asked for — any revision or engine change retires
// every earlier disposition. The highest generation wins; two manifests at
// the same generation reconcile by lower manifest comment id. Missing,
// altered or reordered shards, an unreadable comment list and an unknown
// future schema are errors, not an empty ledger.
func SelectGeneration(comments []CoverageComment, trustedAuthor string, revision core.RevisionPair, engine string) (Generation, error) {
	if revision.Incomplete() {
		return Generation{}, coverageErrorf("selecting a generation with no revision pair")
	}
	if engine == "" {
		return Generation{}, coverageErrorf("selecting a generation with no engine")
	}

	byID := make(map[int64]CoverageComment, len(comments))
	for _, comment := range comments {
		if comment.Author != trustedAuthor {
			continue
		}
		byID[comment.ID] = comment
	}

	type manifestAt struct {
		manifest Manifest
		id       int64
	}
	var manifests []manifestAt
	for id, comment := range byID {
		manifest, ok := DecodeCoverageManifest(comment.Body)
		if !ok {
			// A comment by the trusted author carrying no coverage marker
			// is another write — a pass marker, a finding — and contributes
			// nothing. A comment carrying a coverage marker that does not
			// decode is corrupt state, and refuses.
			if hasCoverageMarker(comment.Body) {
				return Generation{}, coverageErrorf("comment %d carries coverage bytes no strict reader accepts", id)
			}
			continue
		}
		manifest.commentID = id
		manifest.raw = []byte(extractCoveragePayload(comment.Body))
		if manifest.BaseSHA != revision.Base.SHA() || manifest.HeadSHA != revision.Head.SHA() || manifest.Engine != engine {
			continue
		}
		manifests = append(manifests, manifestAt{manifest: manifest, id: id})
	}

	sort.Slice(manifests, func(i, j int) bool {
		if manifests[i].manifest.Gen != manifests[j].manifest.Gen {
			return manifests[i].manifest.Gen > manifests[j].manifest.Gen
		}
		return manifests[i].id < manifests[j].id
	})

	var lastErr error
	for _, candidate := range manifests {
		generation, err := assembleGeneration(candidate.manifest, byID)
		if err != nil {
			lastErr = err
			continue
		}
		return generation, nil
	}
	if lastErr != nil {
		return Generation{}, lastErr
	}
	return Generation{}, fmt.Errorf("%w: %w at %s...%s under %q", ErrCoverage, ErrNoCompleteGeneration, revision.Base.SHA(), revision.Head.SHA(), engine)
}

// assembleGeneration binds one manifest to its shards by id, position and
// digest, in manifest order. A missing, altered or reordered shard refuses
// the generation rather than producing partial coverage.
func assembleGeneration(manifest Manifest, byID map[int64]CoverageComment) (Generation, error) {
	refs := append([]ShardRef{}, manifest.Shards...)
	sort.Slice(refs, func(i, j int) bool { return refs[i].Pos < refs[j].Pos })
	for pos, ref := range refs {
		if ref.Pos != pos {
			return Generation{}, coverageErrorf("manifest gen %d names shard %d at position %d", manifest.Gen, ref.ID, ref.Pos)
		}
	}
	var records []Record
	for _, ref := range refs {
		comment, ok := byID[ref.ID]
		if !ok {
			return Generation{}, coverageErrorf("manifest gen %d names shard comment %d, which is missing", manifest.Gen, ref.ID)
		}
		shard, ok := DecodeCoverageShard(comment.Body)
		if !ok {
			return Generation{}, coverageErrorf("shard comment %d does not decode", ref.ID)
		}
		if shard.Pos != ref.Pos {
			return Generation{}, coverageErrorf("shard comment %d sits at %d, want %d", ref.ID, shard.Pos, ref.Pos)
		}
		if shard.Digest != ref.Digest {
			return Generation{}, coverageErrorf("shard comment %d is altered", ref.ID)
		}
		if len(shard.Records) != ref.N {
			return Generation{}, coverageErrorf("shard comment %d holds %d records, want %d", ref.ID, len(shard.Records), ref.N)
		}
		records = append(records, shard.Records...)
	}
	return Generation{
		Gen:         manifest.Gen,
		Revision:    core.RevisionPair{Base: mustRevision(manifest.BaseSHA), Head: mustRevision(manifest.HeadSHA)},
		Engine:      manifest.Engine,
		Paths:       manifest.Paths,
		Records:     records,
		Advisory:    manifest.Advisory,
		Excluded:    manifest.Excluded,
		ScopeReport: manifest.ScopeReport,
	}, nil
}

// mustRevision rebuilds a revision the strict decoder already validated.
func mustRevision(sha string) core.Revision {
	revision, err := core.NewRevision(sha)
	if err != nil {
		panic(fmt.Sprintf("a decoded revision is not one: %v", err))
	}
	return revision
}

// hasCoverageMarker reports whether a body carries a coverage marker that
// is not a decodable manifest or shard: an altered payload, a foreign kind,
// or bytes no strict reader accepts. A body with no coverage marker at all
// is another write and contributes nothing.
func hasCoverageMarker(body string) bool {
	if !containsCoverageOpen(body) {
		return false
	}
	if _, ok := DecodeCoverageManifest(body); ok {
		return false
	}
	if _, ok := DecodeCoverageShard(body); ok {
		return false
	}
	return true
}

// containsCoverageOpen is the delimiter half of the check above: a body
// whose marker opens but never closes still names coverage bytes.
func containsCoverageOpen(body string) bool {
	for line := range strings.Lines(body) {
		if strings.Contains(line, coverageMarkerOpen) {
			return true
		}
	}
	return false
}
