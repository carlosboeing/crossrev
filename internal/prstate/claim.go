package prstate

import (
	"fmt"
	"time"

	"github.com/carlosboeing/crossrev/internal/core"
)

// DefaultClaimWindow is how long an unfinished claim stays resumable
// (lib/state.sh:323, whose third parameter defaults to 3600 seconds).
const DefaultClaimWindow = time.Hour

// abbreviatedSHALength is the width the stale message quotes a revision at.
// Bash takes it with `${claim_sha:0:7}`, which counts bytes.
const abbreviatedSHALength = 7

// OpenClaim is an unfinished claim for the same (pass, leg), which means
// recovery rather than a fresh start (lib/state.sh:309-314).
//
// The order is take the newest marker for the pair, then ask whether it is
// still started. Asking first and taking the newest after would find a claim
// underneath a completed leg and drive a settled pass again.
func OpenClaim(markers []Marker, pass int, leg core.Leg) (Marker, bool) {
	newest, ok := MarkerFor(markers, pass, leg)
	if !ok || newest.State != core.PassStarted {
		return Marker{}, false
	}
	return newest, true
}

// ClaimIsStale reports whether a claim is too old, or against a revision that
// has since moved on, and gives the reason a person reads
// (lib/state.sh:322-339).
//
// Resuming either one is worse than abandoning it. Coming back a week later
// resumes into a pull request that has changed underneath, and the work is then
// reconciled against findings that no longer describe the code.
//
// The revision test runs first and both are guarded, because a marker written
// before either field existed carries neither: an absent head SHA is not a
// moved revision, and an absent timestamp is not an expired one.
func ClaimIsStale(claim Marker, headSHA string, now time.Time, window time.Duration) (string, bool) {
	if claim.HeadSHA != "" && claim.HeadSHA != headSHA {
		return fmt.Sprintf("it started against %s and the pull request is now at %s",
			abbreviate(claim.HeadSHA), abbreviate(headSHA)), true
	}
	age := now.Unix() - claim.TS
	if claim.TS > 0 && age > int64(window.Seconds()) {
		return fmt.Sprintf("it was made %d minutes ago, past the %d-minute window",
			age/60, int64(window.Minutes())), true
	}
	return "", false
}

// abbreviate takes the first seven bytes of a revision, and a shorter one
// whole. Bash's `${sha:0:7}` does exactly that rather than refusing.
func abbreviate(sha string) string {
	if len(sha) <= abbreviatedSHALength {
		return sha
	}
	return sha[:abbreviatedSHALength]
}
