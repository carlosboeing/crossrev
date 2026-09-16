package intel

import (
	"context"
	"sort"

	"github.com/carlosboeing/crossrev/internal/core"
)

// MaxSearchHits is the per-term search cap: a fixed-string search returns no
// more than this many hits, and a term above it is recorded as too_common and
// contributes no advisory file. It names the same section 7.2 budget as
// vcs.ExactSearchLimit from this side of the tier boundary — this tier-1
// package cannot import the tier-2 vcs package, so the two constants state the
// same number twice rather than sharing one.
const MaxSearchHits = 200

// TooCommonReason is the visible cap reason a capped term records.
const TooCommonReason = "too_common"

// Advisory rule names. Search covers fixed-string hits on changed
// identifiers in untouched files; convention covers adjacent tests by naming
// convention. Both stay advisory: neither adds a required unit nor satisfies
// coverage.
const (
	AdvisoryRuleSearch     = "search"
	AdvisoryRuleConvention = "convention"
)

// Searcher is the advisory discovery surface the orchestrator wires to git.
// ExactSearch lists up to limit repository-relative paths holding term at
// revision, reporting whether more exist; Exists reports whether path names a
// file at revision. The signatures differ from vcs.Repository on purpose: this
// tier-1 package cannot import that tier-2 type, so the tier-3 wiring adapts
// vcs.SearchHit paths to strings at the call site.
type Searcher interface {
	ExactSearch(ctx context.Context, revision core.Revision, term string, limit int) ([]string, bool, error)
	Exists(ctx context.Context, revision core.Revision, path string) (bool, error)
}

// AdvisoryFile is one untouched path offered as uncertain context, with the
// rule that found it. Term names the search term for a search hit and is empty
// for a convention hit.
type AdvisoryFile struct {
	Path string
	Rule string
	Term string
}

// AdvisoryLimit is one visible cap: a search term whose holders exceeded
// MaxSearchHits. Rule names the capped term as "search:<term>"; Observed is
// the hits returned up to the cap, so a capped term observed at least Limit
// holders.
type AdvisoryLimit struct {
	Rule     string
	Observed int
	Limit    int
	Reason   string
}

// AdvisorySummary is the whole advisory answer for one scope: the untouched
// context files, the caps hit along the way, their count, and the rules that
// ran. The next prompt task renders Files directly; the later coverage ledger
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

// ScopeSearchTerms collects SearchTerms over every available required unit's
// evidence bytes, deduplicated and sorted. At file granularity there are no
// hunk lines yet, so the changed files' own evidence stands in for the changed
// lines; unavailable units carry no bytes and contribute no term.
func ScopeSearchTerms(scope Scope) []string {
	seen := make(map[string]bool)
	for _, unit := range scope.Required {
		if !unit.Available {
			continue
		}
		for _, term := range SearchTerms(unit.Body) {
			seen[term] = true
		}
	}
	out := make([]string, 0, len(seen))
	for term := range seen {
		out = append(out, term)
	}
	sort.Strings(out)
	return out
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
// required set: fixed-string hits on the scope's identifier set in untouched
// files, and existing adjacent tests by convention. Required and excluded
// paths never turn advisory, a capped term contributes no file but records its
// too_common limit, and a failed search or existence read drops that term or
// candidate rather than failing discovery — advisory context must never block
// a review pass. The scope argument is read, never written. Results sort by
// path, limits by rule.
func AdvisoryFiles(ctx context.Context, scope Scope, search Searcher) AdvisorySummary {
	required := make(map[string]bool, len(scope.Required))
	for _, unit := range scope.Required {
		required[unit.Path] = true
	}
	excluded := make(map[string]bool, len(scope.Excluded))
	for _, e := range scope.Excluded {
		excluded[e.Path] = true
	}
	summary := AdvisorySummary{Rules: []string{AdvisoryRuleConvention, AdvisoryRuleSearch}}
	seen := make(map[string]bool)

	for _, term := range ScopeSearchTerms(scope) {
		hits, tooCommon, err := search.ExactSearch(ctx, scope.Head, term, MaxSearchHits)
		if err != nil {
			continue
		}
		if tooCommon {
			summary.Limits = append(summary.Limits, AdvisoryLimit{
				Rule:     AdvisoryRuleSearch + ":" + term,
				Observed: len(hits),
				Limit:    MaxSearchHits,
				Reason:   TooCommonReason,
			})
			continue
		}
		for _, hit := range hits {
			if required[hit] || excluded[hit] || seen[hit] {
				continue
			}
			seen[hit] = true
			summary.Files = append(summary.Files, AdvisoryFile{Path: hit, Rule: AdvisoryRuleSearch, Term: term})
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

	sort.Slice(summary.Files, func(i, j int) bool { return summary.Files[i].Path < summary.Files[j].Path })
	sort.Slice(summary.Limits, func(i, j int) bool { return summary.Limits[i].Rule < summary.Limits[j].Rule })
	summary.Count = len(summary.Files)
	return summary
}
