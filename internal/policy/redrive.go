package policy

import "github.com/carlosboeing/crossrev/internal/core"

// ResolutionRecord is one entry of a resolve marker's `resolutions` array, cut
// down to the two fields lib/legs.sh reads off it.
type ResolutionRecord struct {
	Resolution core.Resolution
	Tracked    core.Tracked
}

// ResolveMarker is the part of a completed resolve marker the redrive and
// pass-label decisions read.
//
// Absent, null and empty `commit_sha` are one state here, because jq's
// `.commit_sha // ""` collapses all three (lib/legs.sh:117). An absent
// `resolutions` array and an empty one are likewise one state, for the same
// reason at lib/legs.sh:140.
type ResolveMarker struct {
	Blocked     bool
	CommitSHA   string
	Resolutions []ResolutionRecord
}

// ReviewMarker is the part of a completed review marker ReviewRedrivable reads.
type ReviewMarker struct {
	State   core.PassState
	Verdict core.Verdict
}

// ResolveUnpushedFix reports a fix the resolver claimed and never committed
// (lib/legs.sh:115-119).
//
// `gh_commit_and_push` produces no commit when the tree is unchanged, so a
// `fixed` resolution on a marker carrying no commit is a promise about the code
// that the diff does not keep. A marker with no resolutions claims no fix either
// way; that shape is ResolveUnrecorded's.
func ResolveUnpushedFix(m ResolveMarker) bool {
	if m.CommitSHA != "" {
		return false
	}
	return countResolution(m, core.ResolutionFixed) > 0
}

// ResolveUnrecorded reports a completed pass that recorded no resolutions and
// pushed no commit (lib/legs.sh:137-141).
//
// The leg returns before it writes a resolve marker when its review pass raised
// nothing, so a marker that reached `complete` answered at least one finding.
// One that records none of those answers is a legacy marker and says nothing
// about what happened to the findings. Silence is not a settle.
//
// The commit is read for the same reason ResolveUnpushedFix reads it: a legacy
// marker that pushed moved the head, so the reviewer has something new to see.
func ResolveUnrecorded(m ResolveMarker) bool {
	return m.CommitSHA == "" && len(m.Resolutions) == 0
}

// ResolveRedrivable reports whether a completed resolve pass may be driven again
// (lib/legs.sh:157-175).
//
// Escalating or reporting blocked completes a pass rather than abandoning it,
// and completion is what the re-run guard reads, so without this a pass that
// ended either way could never run again once whatever stopped it was fixed.
// Both endings leave something undecided, so both admit a re-drive. So does a
// deferral whose record never landed, a fix that reached no commit, and a pass
// that recorded no resolutions at all. A pass that settled every finding stays
// refused: running it again would re-decide work that is done.
//
// This reads current marker fields only. Recurrence, coverage and verification
// are Review Intelligence inputs and are deliberately absent.
func ResolveRedrivable(m ResolveMarker) bool {
	if m.Blocked {
		return true
	}
	if countResolution(m, core.ResolutionEscalated) > 0 {
		return true
	}
	if ResolveUnpushedFix(m) {
		return true
	}
	if ResolveUnrecorded(m) {
		return true
	}
	return UnfiledDeferrals(m) > 0
}

// ReviewRedrivable reports whether a completed review pass may be driven again
// (lib/legs.sh:187-192). A nil marker is the absent one, which lib/legs.sh:189
// spells as an empty string.
//
// Only `verdict: "blocked"` on a `complete` pass admits a re-drive: a review
// that recorded findings, or that converged, is settled for this revision, and
// an unfinished one is recovery rather than this re-drive.
func ReviewRedrivable(m *ReviewMarker) bool {
	if m == nil {
		return false
	}
	if m.State != core.PassComplete {
		return false
	}
	return m.Verdict == core.VerdictBlocked
}

// UnfiledDeferrals counts deferrals whose backlog record never landed
// (lib/legs.sh:174).
//
// jq's `.crossrev_tracked == ""` matches only a key that is present and empty. A
// deferral without the key is a legacy marker, or one written with no backlog
// configured, and both read as settled — which is why core.Tracked keeps absent
// and present-empty apart.
func UnfiledDeferrals(m ResolveMarker) int {
	n := 0
	for _, r := range m.Resolutions {
		if r.Resolution == core.ResolutionDeferred && r.Tracked.Unfiled() {
			n++
		}
	}
	return n
}

// countResolution counts entries carrying one resolution word.
//
// The comparison is on the word as written, matching jq's `select(.resolution
// == …)`. A resolution the schema does not list therefore matches no arm and
// settles nothing, which is the closed answer.
func countResolution(m ResolveMarker, want core.Resolution) int {
	n := 0
	for _, r := range m.Resolutions {
		if r.Resolution == want {
			n++
		}
	}
	return n
}
