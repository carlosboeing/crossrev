package vcs

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/carlosboeing/crossrev/internal/core"
)

// ExactSearchLimit is the per-term search cap at the git layer: a blob-pass
// search returns no more than this many holders per term, and reports the
// term as too common when more exist. It names the same section 7.2 budget as
// intel.MaxSearchHits from the other side of the tier boundary — intel is tier
// 1 and cannot import this tier-2 package, so the two constants state the same
// number twice rather than sharing one.
const ExactSearchLimit = 200

// SearchHit is one holder of a fixed-string term at the searched revision:
// the repository-relative path and the 1-based line numbers holding the
// term, ascending with no repeats.
type SearchHit struct {
	Path  string
	Lines []int
}

// TermResult is one term's blob-pass answer: its holders by path, or the
// too-common cap when more holders exist than the limit allowed through.
type TermResult struct {
	Term      string
	Hits      []SearchHit
	TooCommon bool
}

// SearchAll answers every term in one blob pass at revision: one
// `git ls-tree -r -z` enumerates the blobs, one `git cat-file --batch` stream
// carries their bytes, and each blob is matched as it arrives in bounded
// windows — the pass retains hit data plus one read window, never the
// whole repository. Results sort by term, hits by path.
//
// At most limit holders return per term. When more paths hold the term, the
// first limit by scan order return with tooCommon set, and the caller
// records the cap rather than treating the truncated list as complete. A
// term with no holder answers an empty hit list. Empty terms are skipped —
// an empty pattern matches everywhere — and a call with no surviving term
// answers empty without reaching git.
func (r *Repository) SearchAll(ctx context.Context, revision core.Revision, terms []string, limit int) ([]TermResult, error) {
	if revision.IsZero() {
		return nil, fmt.Errorf("vcs: blob search needs a revision")
	}
	if limit <= 0 {
		return nil, fmt.Errorf("vcs: blob search needs a limit above zero, got %d", limit)
	}
	seen := make(map[string]bool)
	var unique []string
	for _, term := range terms {
		if term == "" || seen[term] {
			continue
		}
		seen[term] = true
		unique = append(unique, term)
	}
	if len(unique) == 0 {
		return nil, nil
	}
	entries, err := r.treeBlobs(ctx, revision)
	if err != nil {
		return nil, err
	}
	found, err := r.searchBlobs(ctx, entries, unique, limit)
	if err != nil {
		return nil, err
	}
	results := make([]TermResult, 0, len(unique))
	for i, term := range unique {
		hits := found[i]
		sort.Slice(hits, func(a, b int) bool { return hits[a].Path < hits[b].Path })
		tooCommon := false
		if len(hits) > limit {
			hits = hits[:limit]
			tooCommon = true
		}
		results = append(results, TermResult{Term: term, Hits: hits, TooCommon: tooCommon})
	}
	sort.Slice(results, func(i, j int) bool { return results[i].Term < results[j].Term })
	return results, nil
}

// treeBlob is one blob entry at the searched revision: the object to stream
// and the repository-relative path that names it.
type treeBlob struct {
	sha  string
	path string
}

// treeBlobs enumerates every blob at revision in one NUL-delimited
// `git ls-tree -r -z` call. Non-blob entries — submodule commits — carry no
// file bytes and are skipped.
func (r *Repository) treeBlobs(ctx context.Context, revision core.Revision) ([]treeBlob, error) {
	output, err := r.Run(ctx, "ls-tree", "-r", "-z", revision.SHA())
	if err != nil {
		return nil, err
	}
	if !output.OK() {
		return nil, fmt.Errorf("git ls-tree -r -z exited %d: %s", output.ExitCode, output.Stderr)
	}
	var entries []treeBlob
	for _, field := range strings.Split(output.Stdout, "\x00") {
		if field == "" {
			continue
		}
		tab := strings.IndexByte(field, '\t')
		if tab < 0 {
			return nil, fmt.Errorf("git ls-tree answered a record with no path: %q", field)
		}
		meta, path := field[:tab], field[tab+1:]
		parts := strings.Split(meta, " ")
		if len(parts) != 3 {
			return nil, fmt.Errorf("git ls-tree answered a record with no object: %q", field)
		}
		if parts[1] != "blob" {
			continue
		}
		entries = append(entries, treeBlob{sha: parts[2], path: path})
	}
	return entries, nil
}

// searchBlobs answers every term in one `git cat-file --batch` stream,
// parsing and matching each blob as it arrives in bounded windows: the
// pass retains hit data plus one read window, never the whole repository.
func (r *Repository) searchBlobs(ctx context.Context, entries []treeBlob, terms []string, limit int) ([][]SearchHit, error) {
	s := newBlobScanner(terms, entries, limit)
	output, err := r.git.RunStream(ctx, Call{Dir: r.dir, Args: []string{"cat-file", "--batch"}, Stdin: s.stdin()}, s.consume)
	if err != nil {
		return nil, err
	}
	if !output.OK() {
		return nil, fmt.Errorf("git cat-file --batch exited %d: %s", output.ExitCode, output.Stderr)
	}
	return s.found, nil
}

// blobScanner is one streaming blob pass: the term matcher, each blob's
// fan-out to every path naming it, and the running per-term holders.
type blobScanner struct {
	ordered []string
	paths   map[string][]string
	m       *matcher
	limit   int
	found   [][]SearchHit
	at      map[int]map[string]int
	last    map[int]map[string]int
	holders []int
	capped  []bool
}

// newBlobScanner builds the pass over entries, which arrive in tree order so
// the recorded holders are deterministic. Identical blobs stream once no
// matter how many paths name them.
func newBlobScanner(terms []string, entries []treeBlob, limit int) *blobScanner {
	s := &blobScanner{
		paths:   make(map[string][]string, len(entries)),
		m:       newMatcher(terms),
		limit:   limit,
		found:   make([][]SearchHit, len(terms)),
		at:      make(map[int]map[string]int),
		last:    make(map[int]map[string]int),
		holders: make([]int, len(terms)),
		capped:  make([]bool, len(terms)),
	}
	requested := make(map[string]bool, len(entries))
	for _, entry := range entries {
		s.paths[entry.sha] = append(s.paths[entry.sha], entry.path)
		if requested[entry.sha] {
			continue
		}
		requested[entry.sha] = true
		s.ordered = append(s.ordered, entry.sha)
	}
	return s
}

// stdin lists the requested objects, one SHA per line.
func (s *blobScanner) stdin() []byte {
	var stdin strings.Builder
	for _, sha := range s.ordered {
		stdin.WriteString(sha)
		stdin.WriteByte('\n')
	}
	return []byte(stdin.String())
}

// consume reads one --batch stream: per requested object in order, either
// "<sha> missing" or "<sha> <type> <size>" followed by size bytes and one
// newline. Each blob is matched the moment its bytes arrive and released
// before the next header is read. A missing object degrades to skipped
// rather than failing the pass — the tree named it, so its absence is a
// corrupt object store the advisory rule routes around, never a reason to
// block the review.
func (s *blobScanner) consume(stream io.Reader) error {
	in := bufio.NewReader(stream)
	for _, sha := range s.ordered {
		line, err := in.ReadString('\n')
		if err != nil {
			return fmt.Errorf("git cat-file --batch ended inside %s", shortSHA(sha))
		}
		header := strings.TrimSuffix(line, "\n")
		fields := strings.Split(header, " ")
		if len(fields) == 2 && fields[0] == sha && fields[1] == "missing" {
			continue
		}
		if len(fields) != 3 || fields[0] != sha || fields[1] != "blob" {
			return fmt.Errorf("git cat-file --batch answered %q, want the blob header for %s", header, shortSHA(sha))
		}
		var size int
		for i := 0; i < len(fields[2]); i++ {
			if fields[2][i] < '0' || fields[2][i] > '9' {
				return fmt.Errorf("git cat-file --batch answered a non-numeric size for %s", shortSHA(sha))
			}
			size = size*10 + int(fields[2][i]-'0')
		}
		if err := s.scanStreamed(sha, in, size); err != nil {
			return fmt.Errorf("git cat-file --batch ended inside the %d bytes of %s", size, shortSHA(sha))
		}
		trail, err := in.ReadByte()
		if err != nil || trail != '\n' {
			return fmt.Errorf("git cat-file --batch missed the trailing newline of %s", shortSHA(sha))
		}
	}
	return nil
}

// scanWindow is the read window one blob is matched in: the pass retains
// this many bytes plus the bounded hit data, however large the blob.
const scanWindow = 32 * 1024

// scanStreamed matches size bytes from in against every term, in scanWindow
// reads. The header's size bounds how many bytes are consumed, never an
// allocation: a corrupt header claiming gigabytes ends in an error once the
// stream runs out, not in a slice that size.
func (s *blobScanner) scanStreamed(sha string, in io.Reader, size int) error {
	fed := s.m.stream()
	line := 1
	base := 0
	scratch := make([]byte, scanWindow)
	for remaining := size; remaining > 0; {
		n, err := io.ReadFull(in, scratch[:min(len(scratch), remaining)])
		if err != nil {
			return err
		}
		chunk := scratch[:n]
		// Matches arrive in byte order, so the line cursor only walks
		// forward: count the newlines since the last match. A term never
		// holds a newline, so the byte a match ends on is never one. The
		// cursor crosses chunk edges by counting each chunk's tail once
		// its matches are filed.
		pos := base
		fed.feed(chunk, base, func(term, end int) {
			for ; pos < end; pos++ {
				if chunk[pos-base] == '\n' {
					line++
				}
			}
			s.record(term, sha, line)
		})
		for ; pos < base+len(chunk); pos++ {
			if chunk[pos-base] == '\n' {
				line++
			}
		}
		base += len(chunk)
		remaining -= n
	}
	return nil
}

// record files one match of term in the blob sha names, on 1-based line,
// fanning identical blobs out to every path naming them. Per term, holders
// past limit+1 stop recording — the caller only needs to know the cap
// broke, and truncates the extra holder itself.
func (s *blobScanner) record(term int, sha string, line int) {
	if s.capped[term] {
		return
	}
	for _, path := range s.paths[sha] {
		if s.capped[term] {
			return
		}
		if last, seen := s.last[term][path]; seen && last == line {
			continue
		}
		slot, exists := s.at[term][path]
		if !exists {
			s.holders[term]++
			if s.at[term] == nil {
				s.at[term] = make(map[string]int)
			}
			slot = len(s.found[term])
			s.at[term][path] = slot
			s.found[term] = append(s.found[term], SearchHit{Path: path})
			if s.holders[term] > s.limit {
				s.capped[term] = true
			}
		}
		if s.last[term] == nil {
			s.last[term] = make(map[string]int)
		}
		s.last[term][path] = line
		s.found[term][slot].Lines = append(s.found[term][slot].Lines, line)
	}
}

func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}
