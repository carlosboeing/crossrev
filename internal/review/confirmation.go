package review

import (
	"context"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/prstate"
)

// confirmationPair is the B-to-C repair the next review must confirm: B the
// head the review leg last judged, C the head the resolve leg pushed from it.
type confirmationPair struct {
	base core.Revision
	head core.Revision
	set  bool
}

// repairConfirmation reads the B-to-C pair off the last completed resolve
// marker that pushed from an earlier reviewed head. B is the resolve
// marker's head (the reviewed code), C its commit (the repaired code). The
// pair holds only when C is the current head: a push the resolve leg did
// not make is new work, not a repair to confirm.
func repairConfirmation(markers []prstate.Marker, currentHead core.Revision) confirmationPair {
	var best prstate.Marker
	found := false
	for _, m := range markers {
		if m.Leg != core.LegResolve {
			continue
		}
		commit, ok := m.CommitSHA.Get()
		if !ok || commit == "" {
			continue
		}
		if !found || m.Pass > best.Pass {
			best, found = m, true
		}
	}
	if !found {
		return confirmationPair{}
	}
	head, ok := best.HeadSHA.Get()
	if !ok || head == "" {
		return confirmationPair{}
	}
	commit, _ := best.CommitSHA.Get()
	if commit != currentHead.SHA() {
		return confirmationPair{}
	}
	base, err := core.NewRevision(head)
	if err != nil {
		return confirmationPair{}
	}
	headRev, err := core.NewRevision(commit)
	if err != nil {
		return confirmationPair{}
	}
	if base.SHA() == headRev.SHA() {
		return confirmationPair{}
	}
	return confirmationPair{base: base, head: headRev, set: true}
}

// confirmationDelta reads the B-to-C repair bytes for the prompt: the
// resolver-only delta ahead of the current full scope. Empty when no repair
// is under confirmation, so an initial clean review renders no delta. A
// retrieval failure is reported, so the caller can unset the pair rather
// than claim a confirmation the reviewer never saw.
func (l *Leg) confirmationDelta(ctx context.Context, pair confirmationPair) ([]byte, error) {
	if !pair.set || l.VCS == nil {
		return nil, nil
	}
	return l.VCS.RangeDiff(ctx, pair.base, pair.head)
}
