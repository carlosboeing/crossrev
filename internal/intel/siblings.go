package intel

import (
	"bytes"
	"sort"
)

// MaxSiblings is the most sibling locations one finding is given. The list is
// advisory and partial by design, so a tight cap keeps it from crowding the
// prompt it travels in.
const MaxSiblings = 10

// Sibling is one location at head where an identifier from a finding's
// anchored line also occurs: the repository-relative path, the 1-based line,
// and the identifier that matched.
type Sibling struct {
	Path string
	Line int
	Term string
}

// AnchorLine returns the 1-based line of body without its line ending, or nil
// when the body has no such line. The terms of that line are what a finding's
// siblings are searched for.
func AnchorLine(body []byte, line int) []byte {
	if line < 1 {
		return nil
	}
	for i := 1; ; i++ {
		end := bytes.IndexByte(body, '\n')
		if end < 0 {
			end = len(body)
		}
		if i == line {
			return bytes.TrimSuffix(body[:end], []byte("\r"))
		}
		if end == len(body) {
			return nil
		}
		body = body[end+1:]
	}
}

// SiblingPointers turns one blob pass's answers into a finding's siblings.
// terms are the identifiers of the finding's anchored line and results is the
// pass's answer for at least those terms; a result for any other term is
// ignored, so one pass can serve every finding.
//
// The finding's own file stays in: a repeated pattern in the same file is the
// likeliest sibling. Only the anchored line itself, ownPath at ownLine, is
// left out. A ownLine of zero names no head line, so nothing is left out.
// A term capped as too common holds too many places to point at and
// contributes none.
//
// Locations rank by fewest holders of the term first, since a rare
// identifier is the stronger sign of shared code, then by term, path and line.
// A location several terms found is listed once, under the rarest. At most
// limit return.
func SiblingPointers(terms []string, results []TermResult, ownPath string, ownLine int, limit int) []Sibling {
	asked := make(map[string]bool, len(terms))
	for _, term := range terms {
		asked[term] = true
	}
	type ranked struct {
		Sibling
		holders int
	}
	best := make(map[string]map[int]ranked)
	for _, res := range results {
		if !asked[res.Term] || res.TooCommon {
			continue
		}
		holders := len(res.Hits)
		for _, h := range res.Hits {
			for _, line := range h.Lines {
				if h.Path == ownPath && line == ownLine {
					continue
				}
				if best[h.Path] == nil {
					best[h.Path] = make(map[int]ranked)
				}
				cand := ranked{Sibling{Path: h.Path, Line: line, Term: res.Term}, holders}
				if cur, ok := best[h.Path][line]; !ok || cand.holders < cur.holders ||
					(cand.holders == cur.holders && cand.Term < cur.Term) {
					best[h.Path][line] = cand
				}
			}
		}
	}
	var all []ranked
	for _, byLine := range best {
		for _, r := range byLine {
			all = append(all, r)
		}
	}
	sort.Slice(all, func(i, j int) bool {
		a, b := all[i], all[j]
		if a.holders != b.holders {
			return a.holders < b.holders
		}
		if a.Term != b.Term {
			return a.Term < b.Term
		}
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		return a.Line < b.Line
	})
	if limit < 0 {
		limit = 0
	}
	if len(all) > limit {
		all = all[:limit]
	}
	out := make([]Sibling, len(all))
	for i, r := range all {
		out[i] = r.Sibling
	}
	return out
}
