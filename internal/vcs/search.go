package vcs

import (
	"context"
	"fmt"
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
// carries their bytes, and a single in-memory walk matches the whole term
// set as fixed strings. Results sort by term, hits by path.
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
	contents, err := r.batchBlobs(ctx, entries)
	if err != nil {
		return nil, err
	}
	found := scanBlobs(unique, entries, contents, limit)
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

// batchBlobs streams every entry's bytes in one `git cat-file --batch` call,
// keyed by object id. Identical blobs stream once no matter how many paths
// name them.
func (r *Repository) batchBlobs(ctx context.Context, entries []treeBlob) (map[string][]byte, error) {
	var stdin strings.Builder
	ordered := make([]string, 0, len(entries))
	requested := make(map[string]bool, len(entries))
	for _, entry := range entries {
		if requested[entry.sha] {
			continue
		}
		requested[entry.sha] = true
		ordered = append(ordered, entry.sha)
		stdin.WriteString(entry.sha)
		stdin.WriteByte('\n')
	}
	output, err := r.git.Run(ctx, Call{Dir: r.dir, Args: []string{"cat-file", "--batch"}, Stdin: []byte(stdin.String())})
	if err != nil {
		return nil, err
	}
	if !output.OK() {
		return nil, fmt.Errorf("git cat-file --batch exited %d: %s", output.ExitCode, output.Stderr)
	}
	return parseBatch(output.Stdout, ordered)
}

// parseBatch reads one --batch stream: per requested object in order, either
// "<sha> missing" or "<sha> <type> <size>" followed by size bytes and one
// newline. A missing object degrades to absent bytes rather than failing the
// pass — the tree named it, so its absence is a corrupt object store the
// advisory rule routes around, never a reason to block the review.
func parseBatch(stream string, ordered []string) (map[string][]byte, error) {
	contents := make(map[string][]byte, len(ordered))
	rest := stream
	for _, sha := range ordered {
		line, after, ok := strings.Cut(rest, "\n")
		if !ok {
			return nil, fmt.Errorf("git cat-file --batch ended inside %s", shortSHA(sha))
		}
		rest = after
		fields := strings.Split(line, " ")
		if len(fields) == 2 && fields[0] == sha && fields[1] == "missing" {
			continue
		}
		if len(fields) != 3 || fields[0] != sha || fields[1] != "blob" {
			return nil, fmt.Errorf("git cat-file --batch answered %q, want the blob header for %s", line, shortSHA(sha))
		}
		var size int
		for i := 0; i < len(fields[2]); i++ {
			if fields[2][i] < '0' || fields[2][i] > '9' {
				return nil, fmt.Errorf("git cat-file --batch answered a non-numeric size for %s", shortSHA(sha))
			}
			size = size*10 + int(fields[2][i]-'0')
		}
		if len(rest) < size+1 {
			return nil, fmt.Errorf("git cat-file --batch ended inside the %d bytes of %s", size, shortSHA(sha))
		}
		contents[sha] = []byte(rest[:size])
		if rest[size] != '\n' {
			return nil, fmt.Errorf("git cat-file --batch missed the trailing newline of %s", shortSHA(sha))
		}
		rest = rest[size+1:]
	}
	return contents, nil
}

// scanBlobs matches the whole term set against every blob in one walk each
// and fans identical blobs out to every path naming them. Blobs stream in
// tree order, so the recorded holders are deterministic; per term, holders
// past limit+1 stop recording — the caller only needs to know the cap broke,
// and truncates the extra holder itself.
func scanBlobs(terms []string, entries []treeBlob, contents map[string][]byte, limit int) [][]SearchHit {
	paths := make(map[string][]string, len(entries))
	for _, entry := range entries {
		paths[entry.sha] = append(paths[entry.sha], entry.path)
	}
	found := make([][]SearchHit, len(terms))
	at := make(map[int]map[string]int)
	lastLine := make(map[int]map[string]int)
	holders := make([]int, len(terms))
	capped := make([]bool, len(terms))
	m := newMatcher(terms)
	streamed := make(map[string]bool, len(paths))
	for _, entry := range entries {
		if streamed[entry.sha] {
			continue
		}
		streamed[entry.sha] = true
		data, ok := contents[entry.sha]
		if !ok {
			continue
		}
		// Matches arrive in byte order, so the line cursor only walks
		// forward: count the newlines since the last match. A term never
		// holds a newline, so the byte a match ends on is never one.
		pos, line := 0, 1
		m.scan(data, func(term int, end int) {
			if capped[term] {
				return
			}
			for ; pos < end; pos++ {
				if data[pos] == '\n' {
					line++
				}
			}
			for _, path := range paths[entry.sha] {
				if capped[term] {
					return
				}
				if last, seen := lastLine[term][path]; seen && last == line {
					continue
				}
				slot, exists := at[term][path]
				if !exists {
					holders[term]++
					if at[term] == nil {
						at[term] = make(map[string]int)
					}
					slot = len(found[term])
					at[term][path] = slot
					found[term] = append(found[term], SearchHit{Path: path})
					if holders[term] > limit {
						capped[term] = true
					}
				}
				if lastLine[term] == nil {
					lastLine[term] = make(map[string]int)
				}
				lastLine[term][path] = line
				found[term][slot].Lines = append(found[term][slot].Lines, line)
			}
		})
	}
	return found
}

func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}
