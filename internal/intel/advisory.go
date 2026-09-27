package intel

import (
	"context"
	"sort"
	"strconv"
	"strings"

	"github.com/carlosboeing/crossrev/internal/core"
)

// MaxSearchHits is the per-term search cap: a blob-pass search returns no
// more than this many holders per term, and a term above it is recorded as
// too_common and contributes no advisory file. It names the same section 7.2
// budget as vcs.ExactSearchLimit from this side of the tier boundary — this
// tier-1 package cannot import the tier-2 vcs package, so the two constants
// state the same number twice rather than sharing one.
const MaxSearchHits = 200

// MaxPointersPerCall caps the advisory pointer lines one model call renders.
// The call's own pointers rank rarest first and the rest count as omitted,
// so a noisy term cannot page the whole untouched tree into one prompt.
const MaxPointersPerCall = 50

// TooCommonReason is the visible cap reason a capped term records.
const TooCommonReason = "too_common"

// Advisory rule names. Search covers fixed-string hits on changed-line
// identifiers and changed paths in untouched files; convention covers
// adjacent tests by naming convention. Both stay advisory: neither adds a
// required unit nor satisfies coverage.
const (
	AdvisoryRuleSearch     = "search"
	AdvisoryRuleConvention = "convention"
)

// SearchHit is one holder of a search term: the repository-relative path and
// the 1-based line numbers holding the term, ascending with no repeats.
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

// Searcher is the advisory discovery surface the orchestrator wires to git.
// SearchAll answers every term in one blob pass at revision, reporting per
// term whether more holders exist than limit; Exists reports whether path
// names a file at revision. The signatures differ from vcs.Repository on
// purpose: this tier-1 package cannot import that tier-2 type, so the tier-3
// wiring adapts vcs hits to these at the call site.
type Searcher interface {
	SearchAll(ctx context.Context, revision core.Revision, terms []string, limit int) ([]TermResult, error)
	Exists(ctx context.Context, revision core.Revision, path string) (bool, error)
}

// AdvisoryFile is one untouched pointer offered as uncertain context, with
// the rule that found it. A search pointer names its term and its 1-based
// holder line; a convention pointer names neither.
type AdvisoryFile struct {
	Path string
	Rule string
	Term string
	Line int
}

// AdvisoryLimit is one visible cap: a search term whose holders exceeded
// MaxSearchHits. Rule names the capped term as "search:<term>"; Observed is
// the holders returned up to the cap, so a capped term observed at least
// Limit holders.
type AdvisoryLimit struct {
	Rule     string
	Observed int
	Limit    int
	Reason   string
}

// AdvisorySummary is the whole advisory answer for one scope: the untouched
// context pointers, the caps hit along the way, their count, and the rules
// that ran. Per-call rendering selects from Files; the coverage ledger
// persists Count, Rules and Limits, so no compatibility layer sits between
// this shape and either consumer.
type AdvisorySummary struct {
	Files  []AdvisoryFile
	Limits []AdvisoryLimit
	Count  int
	Rules  []string
}

// SearchTerms extracts the language-neutral ASCII identifier set from data:
// runs of [A-Za-z_][A-Za-z0-9_]*, with terms shorter than four bytes dropped,
// then deduplicated and sorted. Keywords are terms too — the set is neutral
// about language, so it names no keyword list — and non-ASCII bytes break
// runs rather than joining them.
func SearchTerms(data []byte) []string {
	seen := make(map[string]bool)
	var out []string
	start := -1
	flush := func(end int) {
		if start < 0 {
			return
		}
		term := string(data[start:end])
		start = -1
		if len(term) < 4 {
			return
		}
		if !seen[term] {
			seen[term] = true
			out = append(out, term)
		}
	}
	isStart := func(c byte) bool {
		return c == '_' || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')
	}
	isContinue := func(c byte) bool {
		return isStart(c) || (c >= '0' && c <= '9')
	}
	for i := 0; i < len(data); i++ {
		if start < 0 {
			if isStart(data[i]) {
				start = i
			}
			continue
		}
		if !isContinue(data[i]) {
			flush(i)
		}
	}
	flush(len(data))
	sort.Strings(out)
	return out
}

// ChangedTerms collects the whole-pass search set: the identifiers on the
// -U0 diff's added and removed lines via SearchTerms, plus each changed path
// verbatim, deduplicated and sorted. A path joins as written — slashes,
// dots and all — because it is searched as a fixed string, not tokenised:
// a file that names a changed path is a holder of that path.
func ChangedTerms(diff []byte, changes []core.FileChange) []string {
	perFile := FileChangedTerms(diff, changes)
	seen := make(map[string]bool)
	for _, terms := range perFile {
		for _, term := range terms {
			seen[term] = true
		}
	}
	for _, term := range SearchTerms(unmatchedChangedLines(diff, changes)) {
		seen[term] = true
	}
	out := make([]string, 0, len(seen))
	for term := range seen {
		out = append(out, term)
	}
	sort.Strings(out)
	return out
}

// FileChangedTerms maps each changed path to its own search set: the
// identifiers on its own added and removed -U0 lines, plus the path itself
// and, for a rename, the old path. Per-call rendering reads this map, so one
// call's pointers come from its own files' lines rather than the pass's.
// Every change holds an entry even when the diff names no line for it — the
// paths alone still search.
func FileChangedTerms(diff []byte, changes []core.FileChange) map[string][]string {
	sections := changedSections(diff)
	out := make(map[string][]string, len(changes))
	for _, change := range changes {
		lines := sections[change.Path]
		if len(lines) == 0 && change.OldPath != "" && change.OldPath != change.Path {
			lines = sections[change.OldPath]
		}
		seen := make(map[string]bool)
		for _, term := range SearchTerms(lines) {
			seen[term] = true
		}
		seen[change.Path] = true
		if change.OldPath != "" {
			seen[change.OldPath] = true
		}
		terms := make([]string, 0, len(seen))
		for term := range seen {
			terms = append(terms, term)
		}
		sort.Strings(terms)
		out[change.Path] = terms
	}
	return out
}

// changedSections reads one -U0 diff into per-path added and removed line
// bytes, keyed by the current path. A deletion's section keys by its old
// path, where +++ names /dev/null and --- names the file; every other kind
// keys by the +++ path. Headers, hunk markers, binary announcements and
// gitlink Subproject lines are not code and contribute nothing.
func changedSections(diff []byte) map[string][]byte {
	sections := make(map[string][]byte)
	var current string
	var haveCurrent bool
	flush := func(path string, content []byte) {
		if path == "" {
			return
		}
		sections[path] = append(sections[path], content...)
	}
	for _, rec := range strings.Split(string(diff), "\n") {
		switch {
		case strings.HasPrefix(rec, "diff --git "):
			current, haveCurrent = "", false
		case strings.HasPrefix(rec, "--- "):
			if path := changedHeaderPath(rec[4:], "a/"); path != "" {
				current, haveCurrent = path, true
			}
		case strings.HasPrefix(rec, "+++ "):
			if path := changedHeaderPath(rec[4:], "b/"); path != "" {
				current, haveCurrent = path, true
			} else {
				// +++ /dev/null is a deletion: the --- path above stands.
			}
		case strings.HasPrefix(rec, "+") && !strings.HasPrefix(rec, "+++"):
			if haveCurrent && !isSubprojectLine(rec[1:]) {
				flush(current, append([]byte(rec[1:]), '\n'))
			}
		case strings.HasPrefix(rec, "-") && !strings.HasPrefix(rec, "---"):
			if haveCurrent && !isSubprojectLine(rec[1:]) {
				flush(current, append([]byte(rec[1:]), '\n'))
			}
		}
	}
	return sections
}

// unmatchedChangedLines gathers the added and removed line bytes under diff
// sections no change claims — a quoted path the walk could not match, or a
// section the enumeration never listed. ChangedTerms searches these
// globally: the lines changed, so their identifiers stay searchable for the
// ledger, even though no call can attribute them to its own files.
func unmatchedChangedLines(diff []byte, changes []core.FileChange) []byte {
	known := make(map[string]bool, 2*len(changes))
	for _, change := range changes {
		known[change.Path] = true
		if change.OldPath != "" {
			known[change.OldPath] = true
		}
	}
	var out []byte
	for path, lines := range changedSections(diff) {
		if !known[path] {
			out = append(out, lines...)
		}
	}
	return out
}

// isSubprojectLine reports a gitlink pointer line, which names two commits
// rather than changed code.
func isSubprojectLine(content string) bool {
	return strings.HasPrefix(content, "Subproject commit ")
}

// changedHeaderPath reads one path off a --- or +++ line: git's trailing-tab
// delimiter off, C-style quoting undone, the side prefix off. /dev/null
// answers empty, because it names no file.
func changedHeaderPath(rest, prefix string) string {
	if i := strings.IndexByte(rest, '\t'); i >= 0 {
		rest = rest[:i]
	}
	path := rest
	if strings.HasPrefix(path, `"`) {
		if unquoted, err := strconv.Unquote(path); err == nil {
			path = unquoted
		} else if len(path) >= 2 {
			path = path[1 : len(path)-1]
		}
	}
	if path == "/dev/null" {
		return ""
	}
	return strings.TrimPrefix(path, prefix)
}

// AdjacentTestCandidates derives the closed, language-neutral convention table
// for path: stem.test.ext and stem.spec.ext before the extension, stem_test.ext
// and test_stem.ext around the stem, and the same relative path below test/ or
// tests/. The table names shapes, not languages — no suffix is tied to one —
// and stays closed: nothing outside these six forms is a convention hit. The
// path itself is never a candidate. Results sort by path.
func AdjacentTestCandidates(path string) []string {
	if path == "" {
		return nil
	}
	dir, base := splitDir(path)
	stem, ext := splitExt(base)
	seen := make(map[string]bool)
	var out []string
	add := func(candidate string) {
		if candidate == "" || candidate == path || seen[candidate] {
			return
		}
		seen[candidate] = true
		out = append(out, candidate)
	}
	join := func(name string) string {
		if dir == "" {
			return name
		}
		return dir + "/" + name
	}
	if ext != "" {
		add(join(stem + ".test" + ext))
		add(join(stem + ".spec" + ext))
	}
	add(join(stem + "_test" + ext))
	add(join("test_" + stem + ext))
	add("test/" + path)
	add("tests/" + path)
	sort.Strings(out)
	return out
}

// splitDir cuts path at its last slash. The directory carries no trailing
// slash; a bare filename has none.
func splitDir(path string) (dir, base string) {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			return path[:i], path[i+1:]
		}
	}
	return "", path
}

// splitExt cuts base at its last dot, which must not be the first byte: a
// leading dot names a dotfile rather than an extension. The extension keeps
// its dot; a name without one has none.
func splitExt(base string) (stem, ext string) {
	for i := len(base) - 1; i > 0; i-- {
		if base[i] == '.' {
			return base[:i], base[i:]
		}
	}
	return base, ""
}

// AdvisoryFiles discovers uncertain context for scope without touching the
// required set: one blob pass over the changed-line terms plus existence
// reads for the adjacent-test candidates. Required and excluded paths never
// turn advisory, a capped term contributes no file but records its
// too_common limit, and a failed search drops the search rule rather than
// failing discovery — advisory context must never block a review pass. The
// scope argument is read, never written. Results sort by path, limits by
// rule.
func AdvisoryFiles(ctx context.Context, scope Scope, diff []byte, search Searcher) AdvisorySummary {
	required := make(map[string]bool, len(scope.Required))
	changes := make([]core.FileChange, 0, len(scope.Required))
	for _, unit := range scope.Required {
		required[unit.Path] = true
		changes = append(changes, core.FileChange{OldPath: unit.OldPath, Path: unit.Path, Kind: unit.Change})
	}
	excluded := make(map[string]bool, len(scope.Excluded))
	for _, e := range scope.Excluded {
		excluded[e.Path] = true
	}
	summary := AdvisorySummary{Rules: []string{AdvisoryRuleConvention, AdvisoryRuleSearch}}
	seen := make(map[string]bool)

	terms := ChangedTerms(diff, changes)
	if results, err := search.SearchAll(ctx, scope.Head, terms, MaxSearchHits); err == nil {
		byTerm := make(map[string]TermResult, len(results))
		for _, res := range results {
			byTerm[res.Term] = res
		}
		for _, term := range terms {
			res := byTerm[term]
			if res.TooCommon {
				summary.Limits = append(summary.Limits, AdvisoryLimit{
					Rule:     AdvisoryRuleSearch + ":" + term,
					Observed: len(res.Hits),
					Limit:    MaxSearchHits,
					Reason:   TooCommonReason,
				})
				continue
			}
			for _, hit := range res.Hits {
				if required[hit.Path] || excluded[hit.Path] {
					continue
				}
				for _, line := range hit.Lines {
					seen[hit.Path] = true
					summary.Files = append(summary.Files, AdvisoryFile{Path: hit.Path, Rule: AdvisoryRuleSearch, Term: term, Line: line})
				}
			}
		}
	}

	candidates := make(map[string]bool)
	for _, unit := range scope.Required {
		for _, candidate := range AdjacentTestCandidates(unit.Path) {
			if required[candidate] || excluded[candidate] || seen[candidate] || candidates[candidate] {
				continue
			}
			candidates[candidate] = true
		}
	}
	ordered := make([]string, 0, len(candidates))
	for candidate := range candidates {
		ordered = append(ordered, candidate)
	}
	sort.Strings(ordered)
	for _, candidate := range ordered {
		ok, err := search.Exists(ctx, scope.Head, candidate)
		if err != nil || !ok {
			continue
		}
		seen[candidate] = true
		summary.Files = append(summary.Files, AdvisoryFile{Path: candidate, Rule: AdvisoryRuleConvention})
	}

	sort.Slice(summary.Files, func(i, j int) bool {
		if summary.Files[i].Path != summary.Files[j].Path {
			return summary.Files[i].Path < summary.Files[j].Path
		}
		if summary.Files[i].Term != summary.Files[j].Term {
			return summary.Files[i].Term < summary.Files[j].Term
		}
		return summary.Files[i].Line < summary.Files[j].Line
	})
	sort.Slice(summary.Limits, func(i, j int) bool { return summary.Limits[i].Rule < summary.Limits[j].Rule })
	summary.Count = len(summary.Files)
	return summary
}

// BatchPointers selects one call's advisory pointers from the whole-pass
// summary: the search pointers whose term is one of the call's own changed
// terms, and the convention files neighbouring the call's own paths. Search
// pointers rank by fewest holders, then term, path and line; convention
// neighbours follow by path. At most MaxPointersPerCall render; omitted
// counts the rest.
func BatchPointers(summary AdvisorySummary, batchTerms []string, batchPaths []string) (pointers []AdvisoryFile, omitted int) {
	terms := make(map[string]bool, len(batchTerms))
	for _, term := range batchTerms {
		terms[term] = true
	}
	neighbours := make(map[string]bool)
	for _, path := range batchPaths {
		for _, candidate := range AdjacentTestCandidates(path) {
			neighbours[candidate] = true
		}
	}
	var search, convention []AdvisoryFile
	holders := make(map[string]map[string]bool)
	for _, file := range summary.Files {
		switch file.Rule {
		case AdvisoryRuleSearch:
			if !terms[file.Term] {
				continue
			}
			search = append(search, file)
			if holders[file.Term] == nil {
				holders[file.Term] = make(map[string]bool)
			}
			holders[file.Term][file.Path] = true
		case AdvisoryRuleConvention:
			if !neighbours[file.Path] {
				continue
			}
			convention = append(convention, file)
		}
	}
	sort.Slice(search, func(i, j int) bool {
		if len(holders[search[i].Term]) != len(holders[search[j].Term]) {
			return len(holders[search[i].Term]) < len(holders[search[j].Term])
		}
		if search[i].Term != search[j].Term {
			return search[i].Term < search[j].Term
		}
		if search[i].Path != search[j].Path {
			return search[i].Path < search[j].Path
		}
		return search[i].Line < search[j].Line
	})
	sort.Slice(convention, func(i, j int) bool { return convention[i].Path < convention[j].Path })
	selected := append(search, convention...)
	if len(selected) <= MaxPointersPerCall {
		return selected, 0
	}
	return selected[:MaxPointersPerCall], len(selected) - MaxPointersPerCall
}
