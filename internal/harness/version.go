// version.go — the recorded version span each harness CLI has on record.
//
// The lower bound of every span is the descriptor's own install pin
// (internal/harness/assets/harnesses.json), read where the span is built,
// so a pin bump moves the span with it. recordedRuns holds only what
// recorded runs proved past the pin, and only Claude Code has any: its pin
// is 2.1.237 and recorded runs reach 2.1.281.
//
// Both `crossrev doctor` and the review leg's installed-version gate read
// this: doctor reports where an install falls, and the leg refuses a
// served-or-tripwire review on an install outside it.

package harness

import (
	"strconv"
	"strings"
)

// recordedRuns is the recorded-run evidence above a descriptor pin.
var recordedRuns = map[string]string{
	"claude": "2.1.281",
}

// VersionSpan is the inclusive span of one harness CLI's versions on
// record: its install pin up to whatever recorded runs proved above it.
type VersionSpan struct {
	lo, hi [3]int
	upper  string
}

// RecordedSpan answers the span for one harness pin. No pin is no record —
// agy pins nothing — and a pin past every recorded run narrows back to the
// pin itself.
func RecordedSpan(name, pin string) (VersionSpan, bool) {
	if pin == "" {
		return VersionSpan{}, false
	}
	lo := versionNumbers(pin)
	hi, upper := lo, pin
	if recorded, ok := recordedRuns[name]; ok && compareVersions(versionNumbers(recorded), hi) > 0 {
		hi = versionNumbers(recorded)
		upper = recorded
	}
	return VersionSpan{lo: lo, hi: hi, upper: upper}, true
}

// ContainsToken reports whether a reported version token falls in the span.
func (s VersionSpan) ContainsToken(token string) bool {
	return s.contains(versionNumbers(token))
}

// Label prints the span the way reports do: the pin alone, or pin-upper
// where recorded runs proved past it.
func (s VersionSpan) Label(pin string) string {
	if s.upper == pin {
		return pin
	}
	return pin + "-" + s.upper
}

// contains is the inclusive recorded span.
func (s VersionSpan) contains(version [3]int) bool {
	return compareVersions(s.lo, version) <= 0 && compareVersions(version, s.hi) <= 0
}

// compareVersions orders two version triples component by component.
func compareVersions(a, b [3]int) int {
	for at := range a {
		if a[at] != b[at] {
			if a[at] < b[at] {
				return -1
			}
			return 1
		}
	}
	return 0
}

// versionNumbers reads up to three numeric components of a version token.
// "v2.1.281" and "2.1.281-beta" both read as 2.1.281: the digits before any
// suffix, with a missing component counting as zero.
func versionNumbers(token string) [3]int {
	var numbers [3]int
	for at, part := range strings.SplitN(strings.TrimPrefix(token, "v"), ".", 3) {
		digits := part
		for index, r := range part {
			if r < '0' || r > '9' {
				digits = part[:index]
				break
			}
		}
		number, _ := strconv.Atoi(digits)
		numbers[at] = number
	}
	return numbers
}
