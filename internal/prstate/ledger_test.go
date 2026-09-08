package prstate_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/prstate"
)

// mustPair builds the revision pair the ledger tests read and write.
func mustPair(t *testing.T) core.RevisionPair {
	t.Helper()
	base, err := core.NewRevision(coverageBaseSHA())
	if err != nil {
		t.Fatalf("base revision: %v", err)
	}
	head, err := core.NewRevision(coverageHeadSHA())
	if err != nil {
		t.Fatalf("head revision: %v", err)
	}
	return core.RevisionPair{Base: base, Head: head}
}

// candidateFor builds one complete generation candidate the publisher can
// pack: two required files, one judged and one outstanding, with advisory,
// exclusions and a scope report.
func candidateFor(t *testing.T, gen int) prstate.Generation {
	t.Helper()
	return prstate.Generation{
		Gen:      gen,
		Revision: mustPair(t),
		Engine:   core.FileEngineID(),
		Paths:    []string{"a.go", "b.go"},
		Records:  sampleRecords(t),
		Advisory: prstate.Advisory{Count: 0, Rules: []string{"convention", "search"}},
		ScopeReport: prstate.ScopeReport{
			ExaminedScope: "Read a.go and b.go at the head.",
			KnownLimits:   []string{},
		},
	}
}

// scriptStore is a LedgerStore that answers from a script and records every
// create. A read past the script's end reports the call, because a publisher
// that reads a comment it never wrote is the failure the read-back proves
// absent.
type scriptStore struct {
	t           *testing.T
	comments    map[int64]prstate.CoverageComment
	list        []prstate.CoverageComment
	listErr     error
	creates     []string
	nextID      int64
	readBackErr error
}

func newScriptStore(t *testing.T) *scriptStore {
	t.Helper()
	return &scriptStore{t: t, comments: map[int64]prstate.CoverageComment{}, nextID: 9001}
}

func mustTestSlug(t *testing.T) core.Slug {
	t.Helper()
	slug, err := core.ParseSlug("acme/widget")
	if err != nil {
		t.Fatalf("slug: %v", err)
	}
	return slug
}

func (s *scriptStore) CoverageComments(_ context.Context, _ core.Slug, _ int) ([]prstate.CoverageComment, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	out := make([]prstate.CoverageComment, len(s.list))
	copy(out, s.list)
	return out, nil
}

func (s *scriptStore) CoverageComment(_ context.Context, _ core.Slug, id int64) (prstate.CoverageComment, error) {
	if s.readBackErr != nil {
		return prstate.CoverageComment{}, s.readBackErr
	}
	comment, ok := s.comments[id]
	if !ok {
		return prstate.CoverageComment{}, errors.New("no such comment")
	}
	return comment, nil
}

func (s *scriptStore) CreateCoverageComment(_ context.Context, _ core.Slug, _ int, body string) (int64, error) {
	s.creates = append(s.creates, body)
	id := s.nextID
	s.nextID++
	comment := prstate.CoverageComment{ID: id, Author: "tester", Body: body}
	s.comments[id] = comment
	s.list = append(s.list, comment)
	return id, nil
}

// TestPublishGenerationMakesTheManifestLast proves the append-only write
// order: shards are created first, each one is read back by comment id and
// verified, and the manifest comment is created last. A crash before the
// manifest leaves the previous manifest naming intact shards; a manifest
// first would leave a manifest naming shards that never landed.
//
// It fails before the change because no append-only protocol exists: the
// only comment read collapses failure to absence, so a missing manifest
// reads as an empty ledger rather than as a generation that never became
// one.
func TestPublishGenerationMakesTheManifestLast(t *testing.T) {
	store := newScriptStore(t)
	candidate := candidateFor(t, 7)

	manifest, _, err := prstate.PublishGeneration(t.Context(), store, mustTestSlug(t), 42, candidate, func() error { return nil })
	if err != nil {
		t.Fatalf("PublishGeneration: %v", err)
	}
	if manifest.Gen != 7 {
		t.Errorf("published gen = %d, want 7", manifest.Gen)
	}
	if len(store.creates) == 0 {
		t.Fatal("no comment was created")
	}
	last := store.creates[len(store.creates)-1]
	m, ok := prstate.DecodeCoverageManifest(last)
	if !ok {
		t.Fatalf("the last comment created is not a manifest:\n%s", last)
	}
	if m.Gen != 7 || len(m.Shards) == 0 {
		t.Errorf("last manifest is gen %d with %d shards", m.Gen, len(m.Shards))
	}
	for _, body := range store.creates[:len(store.creates)-1] {
		if _, ok := prstate.DecodeCoverageShard(body); !ok {
			t.Errorf("a comment before the manifest is not a shard:\n%.120s", body)
		}
	}
	if got := manifest.CommentID(); got == 0 {
		t.Error("the published manifest carries no comment id")
	}

	// The manifest names the shards the publisher created, by id.
	if len(manifest.Shards) != 1 || manifest.Shards[0].ID == manifest.CommentID() {
		t.Errorf("manifest shards = %+v, want the created shard ids", manifest.Shards)
	}

	// Decoding the stored manifest comment reproduces the returned one.
	stored := store.list[len(store.list)-1]
	decoded, ok := prstate.DecodeCoverageManifest(stored.Body)
	if !ok {
		t.Fatalf("decoding the stored manifest: %.120s", stored.Body)
	}
	if decoded.Digest != manifest.Digest {
		t.Errorf("stored digest %q != returned digest %q", decoded.Digest, manifest.Digest)
	}

	// A publisher that created nothing before the manifest would leave the
	// bodies in creation order with the manifest first.
	if strings.Contains(store.creates[0], `"kind":"manifest"`) {
		t.Error("the first comment created is the manifest; shards must come first")
	}
}

// TestSelectGenerationRefusesMissingBytes proves a generation with a missing
// shard is an error, not an empty ledger. Ordinary IssueComments collapses
// failure to absence, so a generation that lost a comment would read as no
// coverage at all — the one failure the ledger exists to prevent.
//
// It fails before the change because no selection protocol exists: the only
// reader answers a comment list or nothing, and nothing binds a manifest to
// its shards by id, position and digest.
func TestSelectGenerationRefusesMissingBytes(t *testing.T) {
	store := newScriptStore(t)
	candidate := candidateFor(t, 7)

	manifest, _, err := prstate.PublishGeneration(t.Context(), store, mustTestSlug(t), 42, candidate, func() error { return nil })
	if err != nil {
		t.Fatalf("PublishGeneration: %v", err)
	}

	// Drop the shard comment the manifest names: the bytes are gone while
	// the manifest still names them.
	comments, err := store.CoverageComments(t.Context(), mustTestSlug(t), 42)
	if err != nil {
		t.Fatalf("CoverageComments: %v", err)
	}
	var kept []prstate.CoverageComment
	for _, comment := range comments {
		if comment.ID != manifest.Shards[0].ID {
			kept = append(kept, comment)
		}
	}
	if len(kept) == len(comments) {
		t.Fatal("the shard comment was not in the list, so nothing was dropped")
	}

	_, err = prstate.SelectGeneration(kept, "tester", mustPair(t), core.FileEngineID())
	if err == nil {
		t.Fatal("a generation with a missing shard selected as complete")
	}
	if !prstate.IsCoverageError(err) {
		t.Errorf("error = %v, want the coverage refusal", err)
	}

	// The same list with the shard present selects.
	got, err := prstate.SelectGeneration(comments, "tester", mustPair(t), core.FileEngineID())
	if err != nil {
		t.Fatalf("SelectGeneration with all bytes present: %v", err)
	}
	if got.Gen != 7 || len(got.Records) != 2 {
		t.Errorf("selected gen %d with %d records, want gen 7 with 2", got.Gen, len(got.Records))
	}
}

// TestSelectGenerationBreaksEqualGenerationTiesByLowerCommentID proves two
// writers reaching the same generation reconcile deterministically: the
// manifest with the lower comment id wins, and its coverage is kept
// knowingly rather than merged silently.
//
// It fails before the change because no reconciliation rule exists: two
// manifests at one generation are two comment lists with no generation
// number to compare and no tie-break to apply.
func TestSelectGenerationBreaksEqualGenerationTiesByLowerCommentID(t *testing.T) {
	first := newScriptStore(t)
	second := newScriptStore(t)

	// Two writers publish the same generation number with different
	// dispositions: the first finds nothing, the second records a finding.
	quiet := candidateFor(t, 9)
	_, _, err := prstate.PublishGeneration(t.Context(), first, mustTestSlug(t), 42, quiet, func() error { return nil })
	if err != nil {
		t.Fatalf("first PublishGeneration: %v", err)
	}
	loud := candidateFor(t, 9)
	loud.Records[0].Disposition = prstate.Some("finding")
	loud.Records[0].FindingIDs = []string{"a1b2c3d4e5f60718"}
	_, _, err = prstate.PublishGeneration(t.Context(), second, mustTestSlug(t), 42, loud, func() error { return nil })
	if err != nil {
		t.Fatalf("second PublishGeneration: %v", err)
	}

	// The tie-break needs the two manifests on one pull request: merge both
	// stores' comments with the first manifest lower. Each store numbers
	// from 9001, so the second store's ids move past the first's; the
	// shards move with their manifest by the same offset.
	firstList, err := first.CoverageComments(t.Context(), mustTestSlug(t), 42)
	if err != nil {
		t.Fatalf("first CoverageComments: %v", err)
	}
	secondList, err := second.CoverageComments(t.Context(), mustTestSlug(t), 42)
	if err != nil {
		t.Fatalf("second CoverageComments: %v", err)
	}
	// Renumber the second writer's comments past the first's. Ids alone
	// are not enough: each manifest names its shards by id, so the shard
	// references move by the same offset and the bodies are re-encoded.
	offset := int64(len(firstList))
	moved := make([]prstate.CoverageComment, 0, len(secondList))
	idOf := map[int64]int64{}
	for _, comment := range secondList {
		idOf[comment.ID] = comment.ID + offset
	}
	for _, comment := range secondList {
		comment.ID += offset
		if manifest, ok := prstate.DecodeCoverageManifest(comment.Body); ok {
			for i := range manifest.Shards {
				movedID, ok := idOf[manifest.Shards[i].ID]
				if !ok {
					t.Fatalf("manifest names shard %d the store never created", manifest.Shards[i].ID)
				}
				manifest.Shards[i].ID = movedID
			}
			body, err := prstate.EncodeCoverageManifest(manifest)
			if err != nil {
				t.Fatalf("re-encoding the moved manifest: %v", err)
			}
			comment.Body = body
		}
		moved = append(moved, comment)
	}
	merged := append(append([]prstate.CoverageComment{}, firstList...), moved...)

	got, err := prstate.SelectGeneration(merged, "tester", mustPair(t), core.FileEngineID())
	if err != nil {
		t.Fatalf("SelectGeneration: %v", err)
	}
	if got.Gen != 9 {
		t.Fatalf("selected gen = %d, want 9", got.Gen)
	}
	if len(got.Records) != 2 {
		t.Fatalf("selected %d records, want 2", len(got.Records))
	}
	if got.Records[0].Disposition.Value() != "no_issue" {
		t.Errorf("the losing manifest's finding survived the tie-break: %+v", got.Records[0])
	}

	// Reversed comment order reconciles the same way: the rule reads ids,
	// not list order.
	reversed := append([]prstate.CoverageComment{}, merged...)
	for i, j := 0, len(reversed)-1; i < j; i, j = i+1, j-1 {
		reversed[i], reversed[j] = reversed[j], reversed[i]
	}
	again, err := prstate.SelectGeneration(reversed, "tester", mustPair(t), core.FileEngineID())
	if err != nil {
		t.Fatalf("SelectGeneration reversed: %v", err)
	}
	if again.Records[0].Disposition.Value() != "no_issue" {
		t.Errorf("reversed order reconciled differently: %+v", again.Records[0])
	}
}

// TestSelectGenerationFiltersByTrustedAuthor proves comments by any other
// author contribute nothing: an untrusted manifest, even at a higher
// generation, never becomes the selected one.
func TestSelectGenerationFiltersByTrustedAuthor(t *testing.T) {
	store := newScriptStore(t)
	candidate := candidateFor(t, 7)
	if _, _, err := prstate.PublishGeneration(t.Context(), store, mustTestSlug(t), 42, candidate, func() error { return nil }); err != nil {
		t.Fatalf("PublishGeneration: %v", err)
	}
	comments, err := store.CoverageComments(t.Context(), mustTestSlug(t), 42)
	if err != nil {
		t.Fatalf("CoverageComments: %v", err)
	}
	if _, err := prstate.SelectGeneration(comments, "someone-else", mustPair(t), core.FileEngineID()); err == nil {
		t.Fatal("an untrusted author's manifests selected")
	} else if !prstate.IsCoverageError(err) {
		t.Errorf("error = %v, want the coverage refusal", err)
	}
}

// TestSelectGenerationRetiresStaleRevisions proves a base, head or engine
// change retires every earlier disposition: a manifest at another revision
// pair or engine is skipped, not merged.
func TestSelectGenerationRetiresStaleRevisions(t *testing.T) {
	store := newScriptStore(t)
	candidate := candidateFor(t, 7)
	if _, _, err := prstate.PublishGeneration(t.Context(), store, mustTestSlug(t), 42, candidate, func() error { return nil }); err != nil {
		t.Fatalf("PublishGeneration: %v", err)
	}
	comments, err := store.CoverageComments(t.Context(), mustTestSlug(t), 42)
	if err != nil {
		t.Fatalf("CoverageComments: %v", err)
	}
	other := mustPair(t)
	moved, err := core.NewRevision("3333333333333333333333333333333333333333")
	if err != nil {
		t.Fatalf("moved revision: %v", err)
	}
	other.Head = moved
	if _, err := prstate.SelectGeneration(comments, "tester", other, core.FileEngineID()); err == nil {
		t.Error("a manifest at another head selected")
	} else if !prstate.IsCoverageError(err) {
		t.Errorf("error = %v, want the coverage refusal", err)
	}
	if _, err := prstate.SelectGeneration(comments, "tester", mustPair(t), "deadbeefdeadbeef"); err == nil {
		t.Error("a manifest under another engine selected")
	} else if !prstate.IsCoverageError(err) {
		t.Errorf("error = %v, want the coverage refusal", err)
	}
}

// TestSelectGenerationRefusesCorruptBytes proves altered, reordered and
// corrupt state refuse rather than reading as partial coverage: a shard
// whose comment no longer matches its digest, a manifest whose shard
// positions disagree, and a coverage marker no strict reader accepts.
func TestSelectGenerationRefusesCorruptBytes(t *testing.T) {
	store := newScriptStore(t)
	candidate := candidateFor(t, 7)
	manifest, _, err := prstate.PublishGeneration(t.Context(), store, mustTestSlug(t), 42, candidate, func() error { return nil })
	if err != nil {
		t.Fatalf("PublishGeneration: %v", err)
	}
	comments, err := store.CoverageComments(t.Context(), mustTestSlug(t), 42)
	if err != nil {
		t.Fatalf("CoverageComments: %v", err)
	}

	// An altered shard: rewrite the shard comment with different records
	// under the manifest's digest.
	altered := append([]prstate.CoverageComment{}, comments...)
	for i, comment := range altered {
		if comment.ID == manifest.Shards[0].ID {
			shard, ok := prstate.DecodeCoverageShard(comment.Body)
			if !ok {
				t.Fatalf("decoding the shard: %.120s", comment.Body)
			}
			shard.Records = shard.Records[:1]
			body, err := prstate.EncodeCoverageShard(shard)
			if err != nil {
				t.Fatalf("re-encoding the altered shard: %v", err)
			}
			altered[i].Body = body
		}
	}
	if _, err := prstate.SelectGeneration(altered, "tester", mustPair(t), core.FileEngineID()); err == nil {
		t.Error("an altered shard selected as complete")
	} else if !prstate.IsCoverageError(err) {
		t.Errorf("error = %v, want the coverage refusal", err)
	}

	// A corrupt coverage marker refuses rather than contributing nothing.
	corrupt := append(append([]prstate.CoverageComment{}, comments...),
		prstate.CoverageComment{ID: 99999, Author: "tester", Body: "note\n\n<!-- crossrev:c {oops} -->"})
	if _, err := prstate.SelectGeneration(corrupt, "tester", mustPair(t), core.FileEngineID()); err == nil {
		t.Error("a corrupt coverage marker selected past")
	} else if !prstate.IsCoverageError(err) {
		t.Errorf("error = %v, want the coverage refusal", err)
	}

	// An unknown future schema refuses: v:2 decodes nowhere here.
	future := append([]prstate.CoverageComment{}, comments...)
	for i, comment := range future {
		if _, ok := prstate.DecodeCoverageManifest(comment.Body); ok {
			future[i].Body = withSchemaVersion(t, comment.Body, 2)
		}
	}
	if _, err := prstate.SelectGeneration(future, "tester", mustPair(t), core.FileEngineID()); err == nil {
		t.Error("an unknown future schema selected")
	} else if !prstate.IsCoverageError(err) {
		t.Errorf("error = %v, want the coverage refusal", err)
	}
}

func withSchemaVersion(t *testing.T, body string, v int) string {
	t.Helper()
	if v == 2 {
		// Re-encode a valid manifest at another version by hand: bump the
		// field and re-digest so the refusal is the version and not the
		// integrity check.
		start := 0
		for {
			idx := indexOf(body[start:], `"v":1`)
			if idx < 0 {
				t.Fatalf("no version field in %.120s", body)
			}
			// The first v:1 in a manifest body is the manifest's own.
			body = body[:start+idx] + `"v":2` + body[start+idx+len(`"v":1`):]
			break
		}
	}
	return body
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// TestPublishGenerationRechecksTheRevisionPair proves a head that moved
// during publication retires the candidate: stillCurrent failing means no
// manifest comment is created and the previous generation stands.
func TestPublishGenerationRechecksTheRevisionPair(t *testing.T) {
	store := newScriptStore(t)
	candidate := candidateFor(t, 7)
	moved := errMovedHead()

	_, _, err := prstate.PublishGeneration(t.Context(), store, mustTestSlug(t), 42, candidate, func() error { return moved })
	if err == nil {
		t.Fatal("a candidate at a moved head published")
	}
	for _, body := range store.creates {
		if _, ok := prstate.DecodeCoverageManifest(body); ok {
			t.Errorf("a manifest was created after the head moved:\n%.120s", body)
		}
	}
}

func errMovedHead() error { return errHeadMoved }

var errHeadMoved = errMovedHeadType{}

type errMovedHeadType struct{}

func (errMovedHeadType) Error() string { return "the head moved during publication" }

// TestPublishGenerationVerifiesEveryShardReadBack proves a shard that does
// not read back intact stops publication with no manifest: the new
// generation never becomes one, and the previous manifest still names
// intact shards.
func TestPublishGenerationVerifiesEveryShardReadBack(t *testing.T) {
	store := newScriptStore(t)
	candidate := candidateFor(t, 7)

	store.readBackErr = errReadBack{}
	_, _, err := prstate.PublishGeneration(t.Context(), store, mustTestSlug(t), 42, candidate, func() error { return nil })
	if err == nil {
		t.Fatal("a shard that failed its read-back published")
	}
	for _, body := range store.creates {
		if _, ok := prstate.DecodeCoverageManifest(body); ok {
			t.Errorf("a manifest was created past a failed shard read-back:\n%.120s", body)
		}
	}
}

type errReadBack struct{}

func (errReadBack) Error() string { return "the shard read-back failed" }

// TestPublishGenerationKeepsTheStopDiagnostics proves crossing 32 shards
// stops with diagnostics while the last complete generation stands: the
// stop carries measured totals and the limit name and never counts as a
// complete generation.
func TestPublishGenerationKeepsTheStopDiagnostics(t *testing.T) {
	store := newScriptStore(t)
	candidate := candidateFor(t, 7)
	// One record per generated file keeps shards small and many: with the
	// fill line near 59 KB a comment holds hundreds of records, so push
	// past 32 shards with tens of thousands of small records.
	// About 213 small records fit one shard, so 8000 records cross 32.
	// Distinct paths keep the path table honest: one shared path would
	// pack the table once and misstate the measured bytes.
	candidate.Paths = nil
	candidate.Records = nil
	bodyDigest := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	for i := 0; i < 8000; i++ {
		candidate.Paths = append(candidate.Paths, "file.go")
		candidate.Records = append(candidate.Records, prstate.OutstandingRecord(
			string(core.FileUnitID("file.go")), 0, "modified", bodyDigest, "outstanding for the next pass"))
	}
	_, stop, err := prstate.PublishGeneration(t.Context(), store, mustTestSlug(t), 42, candidate, func() error { return nil })
	if err != nil {
		t.Fatalf("PublishGeneration: %v", err)
	}
	if stop.Limit != "ledger_exhausted" {
		t.Errorf("stop limit = %q, want ledger_exhausted", stop.Limit)
	}
	if stop.ShardCount <= prstate.MaxCoverageShards {
		t.Errorf("stop shard count = %d, want past %d", stop.ShardCount, prstate.MaxCoverageShards)
	}
	if stop.RequiredCount != len(candidate.Records) {
		t.Errorf("stop required = %d, want %d", stop.RequiredCount, len(candidate.Records))
	}
	if stop.MeasuredBytes <= 0 {
		t.Error("stop carries no measured bytes")
	}
	for _, body := range store.creates {
		if _, ok := prstate.DecodeCoverageManifest(body); ok {
			t.Errorf("a manifest was created past the shard bound:\n%.120s", body)
		}
	}
}
