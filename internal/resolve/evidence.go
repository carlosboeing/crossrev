package resolve

import (
	"context"
	"fmt"
	"sort"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/harness"
	"github.com/carlosboeing/crossrev/internal/intel"
	"github.com/carlosboeing/crossrev/internal/prompt"
	"github.com/carlosboeing/crossrev/internal/prstate"
	"github.com/carlosboeing/crossrev/internal/vcs"
)

// recurrenceWindow is how many lines apart two anchors may sit and still read
// as the same place.
const recurrenceWindow = 10

// earlierFix is one finding an earlier resolve pass settled `fixed` with a
// commit it pushed, as that pass's review recorded it.
type earlierFix struct {
	pass     int
	id       string
	path     string
	side     string
	category string
	line     int64
	hasLine  bool
	commit   string
}

// earlierFixes reads every fix an earlier pass pushed. A resolve marker with no
// commit_sha pushed nothing, so whatever it called fixed is not evidence that a
// fix reached the branch. The earlier finding's path, side, category and line
// come from the review record of the same pass, which is where its anchor was
// at that time.
func earlierFixes(markers []prstate.Marker, pass int) []earlierFix {
	var out []earlierFix
	for _, m := range markers {
		if m.Leg != core.LegResolve || m.Pass >= pass {
			continue
		}
		commit, ok := m.CommitSHA.Get()
		if !ok || commit == "" {
			continue
		}
		review, ok := prstate.MarkerFor(markers, m.Pass, core.LegReview)
		if !ok {
			continue
		}
		reviewed := unmarshalFindings(review.Findings)
		var recs []struct {
			FindingID  string `json:"finding_id"`
			Resolution string `json:"resolution"`
		}
		_ = m.DecodeResolutions(&recs)
		for _, r := range recs {
			if r.Resolution != string(core.ResolutionFixed) {
				continue
			}
			fix := earlierFix{pass: m.Pass, id: r.FindingID, commit: commit}
			for _, f := range reviewed {
				if f.Member("id").StringVal() != r.FindingID {
					continue
				}
				fix.path = f.Member("path").StringVal()
				fix.side = f.Member("side").StringVal()
				fix.category = f.Member("category").StringVal()
				fix.line, fix.hasLine = f.Member("line").AsInt()
				break
			}
			out = append(out, fix)
		}
	}
	return out
}

// recurrenceCandidates answers, per finding, the earlier fix it may be
// reopening, or nil. A finding is a candidate when an earlier pass fixed and
// pushed one with the same id, or with the same path, side and category and an
// anchor within recurrenceWindow lines of the finding's. The same id wins over
// a nearby anchor, then the latest pass, then the closest line.
//
// Only a signal: nothing here changes a resolution or escalates.
func recurrenceCandidates(markers []prstate.Marker, pass int, findings []harness.Node) []*prompt.Recurrence {
	out := make([]*prompt.Recurrence, len(findings))
	fixes := earlierFixes(markers, pass)
	if len(fixes) == 0 {
		return out
	}
	for i, f := range findings {
		id := f.Member("id").StringVal()
		path := f.Member("path").StringVal()
		side := f.Member("side").StringVal()
		category := f.Member("category").StringVal()
		line, hasLine := f.Member("line").AsInt()

		type match struct {
			fix      earlierFix
			sameID   bool
			distance int64
		}
		var best *match
		for _, fix := range fixes {
			m := match{fix: fix, sameID: fix.id == id}
			if !m.sameID {
				if !hasLine || !fix.hasLine || fix.path != path || fix.side != side || fix.category != category {
					continue
				}
				m.distance = line - fix.line
				if m.distance < 0 {
					m.distance = -m.distance
				}
				if m.distance > recurrenceWindow {
					continue
				}
			}
			switch {
			case best == nil,
				m.sameID && !best.sameID,
				m.sameID == best.sameID && m.fix.pass > best.fix.pass,
				m.sameID == best.sameID && m.fix.pass == best.fix.pass && m.distance < best.distance:
				c := m
				best = &c
			}
		}
		if best != nil {
			out[i] = &prompt.Recurrence{Pass: best.fix.pass, FindingID: best.fix.id, Commit: best.fix.commit}
		}
	}
	return out
}

// recurrenceCount is how many of the pass's posted findings are recurrence
// candidates. It reads the full review record rather than what the resolver
// was shown, so a pass that resumes from recorded resolutions, and never
// renders a prompt, still counts them.
func recurrenceCount(markers []prstate.Marker, pass int, findings []harness.Node) int {
	n := 0
	for _, r := range recurrenceCandidates(markers, pass, postedOnly(findings)) {
		if r != nil {
			n++
		}
	}
	return n
}

// siblingPointers answers, per finding in findings, the advisory list of
// places at head where an identifier from its anchored line also occurs. One
// blob pass over head serves every finding. The anchored line is read from the
// committed blob, at head for a finding on the added side and at base for one
// on the removed side, so the pull request's own checkout is never opened.
//
// The list is advisory, so every failure leaves it empty rather than failing
// the pass: a finding whose line cannot be read has none, and a failed search
// gives every finding none.
func (l *Leg) siblingPointers(ctx context.Context, s *session, findings []harness.Node, quarantined []string) [][]prompt.Sibling {
	out := make([][]prompt.Sibling, len(findings))

	type anchor struct {
		terms []string
		path  string
		line  int
	}
	anchors := make([]*anchor, len(findings))
	seen := map[string]bool{}
	var terms []string
	for i, f := range findings {
		path := f.Member("path").StringVal()
		line, ok := f.Member("line").AsInt()
		if path == "" || !ok || line < 1 {
			continue
		}
		revision, own := s.pr.HeadRefOid, int(line)
		if f.Member("side").StringVal() == "LEFT" {
			// The line exists at base, so no line at head is the finding's own.
			revision, own = s.pr.BaseRefOid, 0
		}
		body, err := l.blobAt(ctx, revision, path)
		if err != nil || body == nil {
			continue
		}
		found := intel.SearchTerms(intel.AnchorLine(body, int(line)))
		if len(found) == 0 {
			continue
		}
		anchors[i] = &anchor{terms: found, path: path, line: own}
		for _, term := range found {
			if !seen[term] {
				seen[term] = true
				terms = append(terms, term)
			}
		}
	}
	if len(terms) == 0 {
		return out
	}
	sort.Strings(terms)

	found, err := l.Git.SearchAll(ctx, s.pr.HeadRefOid, terms, intel.MaxSearchHits)
	if err != nil {
		if l.Log != nil {
			l.Log.Event("siblings", fmt.Sprintf("search failed, so no sibling locations this pass: %v", err))
		}
		return out
	}
	results := dropQuarantined(found, quarantined)
	for i, a := range anchors {
		if a == nil {
			continue
		}
		for _, sib := range intel.SiblingPointers(a.terms, results, a.path, a.line, intel.MaxSiblings) {
			out[i] = append(out[i], prompt.Sibling{Path: sib.Path, Line: sib.Line, Term: sib.Term})
		}
	}
	return out
}

// dropQuarantined converts the blob pass's answer and removes every holder the
// sandbox keeps out of the checkout: the resolver cannot open those paths, so
// a pointer to one is a dead end.
func dropQuarantined(in []vcs.TermResult, quarantined []string) []intel.TermResult {
	out := make([]intel.TermResult, 0, len(in))
	for _, res := range in {
		kept := intel.TermResult{Term: res.Term, TooCommon: res.TooCommon}
		for _, h := range res.Hits {
			if isQuarantined(h.Path, quarantined) {
				continue
			}
			kept.Hits = append(kept.Hits, intel.SearchHit{Path: h.Path, Lines: append([]int(nil), h.Lines...), OmittedLines: h.OmittedLines})
		}
		out = append(out, kept)
	}
	return out
}

func isQuarantined(path string, quarantined []string) bool {
	for _, q := range quarantined {
		if path == q || (len(path) > len(q) && path[:len(q)] == q && path[len(q)] == '/') {
			return true
		}
	}
	return false
}
