package vcs_test

import (
	"bytes"
	"context"
	"fmt"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/vcs"
)

// searchHead commits the staged fixture and answers its revision.
func searchHead(t *testing.T, repo *vcs.Repository) core.Revision {
	t.Helper()
	mustGit(t, repo, "add", "-A")
	mustGit(t, repo, append(testIdentity, "commit", "-qm", "head")...)
	headOut := mustGit(t, repo, "rev-parse", "HEAD")
	head, err := core.NewRevision(headOut.Text())
	if err != nil {
		t.Fatalf("head revision: %v", err)
	}
	return head
}

func searchResultsByTerm(t *testing.T, results []vcs.TermResult) map[string]vcs.TermResult {
	t.Helper()
	out := make(map[string]vcs.TermResult, len(results))
	for _, res := range results {
		if _, dup := out[res.Term]; dup {
			t.Fatalf("SearchAll answered term %q twice", res.Term)
		}
		out[res.Term] = res
	}
	return out
}

// TestSearchAllReportsTooCommonAtTwoHundred commits 205 files holding one
// fixed-string term and requires the 200-holder cap: the capped call reports
// too_common with exactly 200 hits, while a limit above the total does not.
func TestSearchAllReportsTooCommonAtTwoHundred(t *testing.T) {
	git := testGit(t)
	dir := realTempDir(t)
	repo := initRepo(t, git, dir)
	ctx := context.Background()

	const term = "sharedhelper_thing"
	const files = 205
	for i := 0; i < files; i++ {
		write(t, dir, fmt.Sprintf("pkg/file%03d.go", i), fmt.Sprintf("package pkg\n\n// %s filler %d\n", term, i))
	}
	write(t, dir, "pkg/rare.go", "package pkg\n\nfunc LoneUnrelatedSymbol() {}\n")
	head := searchHead(t, repo)

	results, err := repo.SearchAll(ctx, head, []string{term}, 200)
	if err != nil {
		t.Fatalf("SearchAll: %v", err)
	}
	byTerm := searchResultsByTerm(t, results)
	res := byTerm[term]
	if !res.TooCommon {
		t.Error("SearchAll over 205 holders with limit 200 reports too_common false, want true")
	}
	if len(res.Hits) != 200 {
		t.Errorf("SearchAll returned %d hits, want exactly 200", len(res.Hits))
	}
	if !sort.SliceIsSorted(res.Hits, func(i, j int) bool { return res.Hits[i].Path < res.Hits[j].Path }) {
		t.Error("SearchAll hits are not sorted by path")
	}
	for _, hit := range res.Hits {
		if len(hit.Lines) == 0 {
			t.Errorf("holder %q carries no line numbers", hit.Path)
		}
	}

	roomy, err := repo.SearchAll(ctx, head, []string{term}, 206)
	if err != nil {
		t.Fatalf("SearchAll: %v", err)
	}
	roomyRes := searchResultsByTerm(t, roomy)[term]
	if roomyRes.TooCommon {
		t.Error("SearchAll over 205 holders with limit 206 reports too_common true, want false")
	}
	if len(roomyRes.Hits) != files {
		t.Errorf("SearchAll with room returned %d hits, want %d", len(roomyRes.Hits), files)
	}

	rare, err := repo.SearchAll(ctx, head, []string{"LoneUnrelatedSymbol"}, 200)
	if err != nil {
		t.Fatalf("SearchAll: %v", err)
	}
	rareRes := searchResultsByTerm(t, rare)["LoneUnrelatedSymbol"]
	if rareRes.TooCommon {
		t.Error("SearchAll over one holder reports too_common true, want false")
	}
	if len(rareRes.Hits) != 1 || rareRes.Hits[0].Path != "pkg/rare.go" {
		t.Fatalf("SearchAll rare term = %v, want [pkg/rare.go]", rareRes.Hits)
	}
	if len(rareRes.Hits[0].Lines) != 1 || rareRes.Hits[0].Lines[0] != 3 {
		t.Errorf("rare holder lines = %v, want [3]", rareRes.Hits[0].Lines)
	}
}

// TestSearchAllAnswersEveryTermWithLineNumbers requires one blob pass to
// answer two terms at once, each with its holder lines.
func TestSearchAllAnswersEveryTermWithLineNumbers(t *testing.T) {
	git := testGit(t)
	dir := realTempDir(t)
	repo := initRepo(t, git, dir)

	write(t, dir, "a.go", "package a\n\nconst AlphaMarker = 1\n\nconst BetaMarker = 2\n")
	write(t, dir, "b.go", "package b\n\nconst BetaMarker = 3\n")
	head := searchHead(t, repo)

	results, err := repo.SearchAll(context.Background(), head, []string{"BetaMarker", "AlphaMarker"}, 200)
	if err != nil {
		t.Fatalf("SearchAll: %v", err)
	}
	if len(results) != 2 || results[0].Term != "AlphaMarker" || results[1].Term != "BetaMarker" {
		t.Fatalf("SearchAll answered %d results, want AlphaMarker then BetaMarker in term order", len(results))
	}
	byTerm := searchResultsByTerm(t, results)
	alpha := byTerm["AlphaMarker"]
	if len(alpha.Hits) != 1 || alpha.Hits[0].Path != "a.go" {
		t.Fatalf("AlphaMarker holders = %v, want [a.go]", alpha.Hits)
	}
	if len(alpha.Hits[0].Lines) != 1 || alpha.Hits[0].Lines[0] != 3 {
		t.Errorf("AlphaMarker lines = %v, want [3]", alpha.Hits[0].Lines)
	}
	beta := byTerm["BetaMarker"]
	if len(beta.Hits) != 2 {
		t.Fatalf("BetaMarker holders = %v, want a.go and b.go", beta.Hits)
	}
	lines := map[string][]int{}
	for _, hit := range beta.Hits {
		lines[hit.Path] = hit.Lines
	}
	if len(lines["a.go"]) != 1 || lines["a.go"][0] != 5 {
		t.Errorf("BetaMarker a.go lines = %v, want [5]", lines["a.go"])
	}
	if len(lines["b.go"]) != 1 || lines["b.go"][0] != 3 {
		t.Errorf("BetaMarker b.go lines = %v, want [3]", lines["b.go"])
	}
}

// TestSearchAllMatchesOverlappingTerms requires substring semantics across
// terms that share bytes: every term matching inside "sword" reports it.
func TestSearchAllMatchesOverlappingTerms(t *testing.T) {
	git := testGit(t)
	dir := realTempDir(t)
	repo := initRepo(t, git, dir)

	write(t, dir, "words.txt", "a sword is a word\n")
	head := searchHead(t, repo)

	results, err := repo.SearchAll(context.Background(), head, []string{"sword", "word", "ord"}, 200)
	if err != nil {
		t.Fatalf("SearchAll: %v", err)
	}
	byTerm := searchResultsByTerm(t, results)
	for _, term := range []string{"sword", "word", "ord"} {
		hits := byTerm[term].Hits
		if len(hits) != 1 || hits[0].Path != "words.txt" || len(hits[0].Lines) != 1 || hits[0].Lines[0] != 1 {
			t.Errorf("%q holders = %+v, want words.txt line 1", term, hits)
		}
	}
}

func TestSearchAllIsFixedString(t *testing.T) {
	git := testGit(t)
	dir := realTempDir(t)
	repo := initRepo(t, git, dir)
	ctx := context.Background()

	write(t, dir, "literal.go", "package p\n\nvar s = \"a.b\"\n")
	write(t, dir, "lookalike.go", "package p\n\nvar s = \"aXb\"\n")
	write(t, dir, "flag.go", "package p\n\n// -flag literal\n")
	head := searchHead(t, repo)

	results, err := repo.SearchAll(ctx, head, []string{"a.b"}, 200)
	if err != nil {
		t.Fatalf("SearchAll: %v", err)
	}
	res := searchResultsByTerm(t, results)["a.b"]
	if res.TooCommon {
		t.Error("SearchAll reports too_common over one holder, want false")
	}
	if len(res.Hits) != 1 || res.Hits[0].Path != "literal.go" {
		t.Errorf("SearchAll fixed-string a.b = %v, want [literal.go]", res.Hits)
	}

	flag, err := repo.SearchAll(ctx, head, []string{"-flag"}, 200)
	if err != nil {
		t.Fatalf("SearchAll: %v", err)
	}
	flagRes := searchResultsByTerm(t, flag)["-flag"]
	if len(flagRes.Hits) != 1 || flagRes.Hits[0].Path != "flag.go" {
		t.Errorf("SearchAll leading-dash term = %v, want [flag.go]", flagRes.Hits)
	}
}

func TestSearchAllNoMatchesIsEmpty(t *testing.T) {
	git := testGit(t)
	dir := realTempDir(t)
	repo := initRepo(t, git, dir)

	write(t, dir, "keep.go", "package keep\n")
	head := searchHead(t, repo)

	results, err := repo.SearchAll(context.Background(), head, []string{"nothing_names_this"}, 200)
	if err != nil {
		t.Fatalf("SearchAll: %v", err)
	}
	res := searchResultsByTerm(t, results)["nothing_names_this"]
	if res.TooCommon {
		t.Error("SearchAll with no matches reports too_common true, want false")
	}
	if len(res.Hits) != 0 {
		t.Errorf("SearchAll with no matches = %v, want empty", res.Hits)
	}
}

func TestSearchAllRejectsBadInputAndSkipsEmptyTerms(t *testing.T) {
	git := testGit(t)
	dir := realTempDir(t)
	repo := initRepo(t, git, dir)

	write(t, dir, "keep.go", "package keep\n")
	head := searchHead(t, repo)
	ctx := context.Background()

	if _, err := repo.SearchAll(ctx, core.Revision{}, []string{"keep"}, 200); err == nil {
		t.Error("SearchAll with a zero revision succeeded, want an error")
	}
	if _, err := repo.SearchAll(ctx, head, []string{"keep"}, 0); err == nil {
		t.Error("SearchAll with limit 0 succeeded, want an error")
	}
	if _, err := repo.SearchAll(ctx, head, []string{"keep"}, -1); err == nil {
		t.Error("SearchAll with a negative limit succeeded, want an error")
	}
	empty, err := repo.SearchAll(ctx, head, nil, 200)
	if err != nil {
		t.Fatalf("SearchAll with no terms: %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("SearchAll with no terms = %v, want empty", empty)
	}
	skipped, err := repo.SearchAll(ctx, head, []string{"", "keep"}, 200)
	if err != nil {
		t.Fatalf("SearchAll with an empty term: %v", err)
	}
	if len(skipped) != 1 || skipped[0].Term != "keep" {
		t.Errorf("SearchAll with an empty term answered %v, want keep alone", skipped)
	}
}

func TestSearchAllFindsAPathWithASpace(t *testing.T) {
	git := testGit(t)
	dir := realTempDir(t)
	repo := initRepo(t, git, dir)

	write(t, dir, "dir/space name.go", "package spaced\n\nfunc SpacedHelper() {}\n")
	head := searchHead(t, repo)

	results, err := repo.SearchAll(context.Background(), head, []string{"SpacedHelper"}, 200)
	if err != nil {
		t.Fatalf("SearchAll: %v", err)
	}
	res := searchResultsByTerm(t, results)["SpacedHelper"]
	if len(res.Hits) != 1 || res.Hits[0].Path != "dir/space name.go" {
		t.Errorf("SearchAll spaced path = %v, want [dir/space name.go]", res.Hits)
	}
}

// TestSearchAllStreamsBlobsWithinOneBlobOfMemory requires the blob pass to
// parse and match as the cat-file stream arrives: thirty 1 MB blobs must
// cost well under their total in allocation. Buffering the whole stream
// plus a per-blob copy costs several times it, so the bound fails there
// and passes here with an order of magnitude to spare either way.
func TestSearchAllStreamsBlobsWithinOneBlobOfMemory(t *testing.T) {
	git := testGit(t)
	dir := realTempDir(t)
	repo := initRepo(t, git, dir)

	const blobs = 30
	const blobBytes = 1 << 20
	const term = "StreamedRareMarker"
	filler := bytes.Repeat([]byte("x"), blobBytes-len(term))
	for i := 0; i < blobs; i++ {
		body := append(append([]byte(nil), filler...), term...)
		if i > 0 {
			// A distinct last byte per blob: identical blobs would
			// stream once, and the pass must face all thirty megabytes.
			body[len(body)-1] = byte(i)
		}
		write(t, dir, fmt.Sprintf("big/blob%02d.bin", i), string(body))
	}
	head := searchHead(t, repo)

	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	results, err := repo.SearchAll(context.Background(), head, []string{term}, 200)
	runtime.ReadMemStats(&after)
	if err != nil {
		t.Fatalf("SearchAll: %v", err)
	}
	res := searchResultsByTerm(t, results)[term]
	if len(res.Hits) != 1 || res.Hits[0].Path != "big/blob00.bin" {
		t.Fatalf("SearchAll large-blob holders = %v, want big/blob00.bin alone", res.Hits)
	}
	if len(res.Hits[0].Lines) != 1 || res.Hits[0].Lines[0] != 1 {
		t.Errorf("SearchAll large-blob lines = %v, want [1] (one unbroken line)", res.Hits[0].Lines)
	}
	allocated := after.TotalAlloc - before.TotalAlloc
	if budget := uint64(blobs*blobBytes) * 3 / 2; allocated > budget {
		t.Errorf("SearchAll allocated %d bytes over %d bytes of blobs, want under %d (stream, do not buffer)", allocated, blobs*blobBytes, budget)
	}
}

// TestSearchAllNeedsAStreamingRunner requires an honest error when the git
// runner cannot stream: a silent buffered fallback would reintroduce the
// peak the streaming pass exists to avoid.
func TestSearchAllNeedsAStreamingRunner(t *testing.T) {
	repo := vcs.New(&recorder{}, nil).At(t.TempDir())
	head, err := core.NewRevision(strings.Repeat("a", 40))
	if err != nil {
		t.Fatalf("head revision: %v", err)
	}
	_, err = repo.SearchAll(context.Background(), head, []string{"term"}, 1)
	if err == nil {
		t.Fatal("SearchAll over a non-streaming runner succeeded, want an error")
	}
	if !strings.Contains(err.Error(), "stream") {
		t.Errorf("SearchAll error = %q, want it to name streaming", err.Error())
	}
}

// TestSearchAllReportsEveryPathSharingABlob requires identical files to each
// report: two paths over one blob are two holders, not one.
func TestSearchAllReportsEveryPathSharingABlob(t *testing.T) {
	git := testGit(t)
	dir := realTempDir(t)
	repo := initRepo(t, git, dir)

	write(t, dir, "one.go", "package twins\n\nconst TwinMarker = 1\n")
	write(t, dir, "two.go", "package twins\n\nconst TwinMarker = 1\n")
	head := searchHead(t, repo)

	results, err := repo.SearchAll(context.Background(), head, []string{"TwinMarker"}, 200)
	if err != nil {
		t.Fatalf("SearchAll: %v", err)
	}
	res := searchResultsByTerm(t, results)["TwinMarker"]
	if len(res.Hits) != 2 || res.Hits[0].Path != "one.go" || res.Hits[1].Path != "two.go" {
		t.Errorf("SearchAll twin holders = %v, want one.go and two.go", res.Hits)
	}
}
