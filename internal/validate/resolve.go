// resolve.go — the resolve leg's payload, checked the way lib/validate.sh's
// validate_resolve checks it (lib/validate.sh:71-132).

package validate

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"math/big"
	"sort"
	"strconv"
	"strings"
)

// Expectations is what the orchestrator handed the resolve leg, which is the
// only thing the semantic half has to compare an answer against
// (lib/validate.sh:27-30, lib/run.sh:2070-2071).
//
// Findings is how many findings were numbered in the prompt, so a resolution may
// name 1 through Findings and nothing else. Candidates is every issue number
// offered as a duplicate candidate, flattened across findings: the check is
// against all of them rather than per finding, because a model that correctly
// notices one issue covers two findings is right and inventing a number is the
// failure being designed out (tests/test-schemas.sh:236-242).
type Expectations struct {
	Findings   int
	Candidates []int
}

// Resolve checks one resolve payload and returns nil, a ShapeError, or a
// SemanticError.
//
// A nil expect runs the shape half and stops, which is what the fenced-JSON
// fallback path does: comparing against an invented expectation would be worse
// than not comparing (lib/validate.sh:27-30, 109-110).
//
// The two halves disagree about a resolutions element that is not an object. The
// shape half indexes it through `?` and skips it; the semantic half indexes it
// without one, jq raises, and the shell reports that the payload stopped being
// parseable between the two checks. Both are reproduced.
func Resolve(payload []byte, expect *Expectations) error {
	if len(bytes.TrimSpace(payload)) == 0 {
		// jq ran the filter on no input at all, printed nothing and exited 0.
		return nil
	}
	top, ok := jqParse(payload)
	if !ok {
		return shapef("the payload is not parseable JSON")
	}
	if jqType(top) != "object" {
		return shapef("the payload is not a JSON object")
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(top, &doc); err != nil {
		return shapef("the payload is not parseable JSON")
	}

	rawResolutions, present := doc["resolutions"]
	if !present || jqType(rawResolutions) != "array" {
		return shapef("resolutions is missing or not an array")
	}
	var elements []json.RawMessage
	if err := json.Unmarshal(rawResolutions, &elements); err != nil {
		return shapef("resolutions is missing or not an array")
	}

	if summary, ok := jqString(doc["summary"]); !ok || summary == "" {
		return shapef("summary is missing or empty")
	}

	if jqType(jqAlt(doc["blocked"], "false")) != "boolean" {
		return shapef("blocked is not a boolean")
	}

	// Typed rather than required, which is the same call `blocked_reason` gets:
	// the schema lists both under "required" because one harness enforces strict
	// mode, not because a payload without them says anything false. An absent
	// subject is a resolver that wrote none, and the commit carries the generic
	// one. A subject of the wrong type is different, because `jq -r` turns a
	// number, a boolean or an empty array into a plausible-looking string and
	// commits it (lib/validate.sh:82-91).
	if subject, has := doc["commit_subject"]; has && jqType(subject) != "null" &&
		jqType(subject) != "string" {
		return shapef("commit_subject is %s, and a commit subject is a string or null",
			jqType(subject))
	}

	bad := 0
	var first json.RawMessage
	for _, element := range elements {
		if !resolutionIsBad(element) {
			continue
		}
		if bad == 0 {
			first = element
		}
		bad++
	}
	if bad > 0 {
		return shapef("%d resolution(s) have a missing or non-whole finding_number, a missing reply, "+
			"a non-numeric duplicate_of, or a resolution outside the five allowed — first: %s",
			bad, jqCompact(first))
	}

	if expect == nil {
		return nil
	}
	return resolveSemantic(elements, *expect)
}

// resolutionIsBad is the `select` at lib/validate.sh:93-102.
//
// jq's `or` short-circuits, and that matters here rather than only for reading:
// `(.finding_number | floor)` carries no `?`, so on an absent finding_number it
// would raise — the clause before it is already true, so jq never evaluates it.
func resolutionIsBad(element json.RawMessage) bool {
	kind := jqType(element)
	if kind != "object" && kind != "null" {
		// `.finding_number?` raises on a scalar or an array and the `?` swallows
		// it, so the select produces nothing.
		return false
	}
	var r map[string]json.RawMessage
	if kind == "object" {
		if err := json.Unmarshal(element, &r); err != nil {
			return false
		}
	}

	if jqType(r["finding_number"]) != "number" {
		return true
	}
	// jq has one number type, so whole-ness is checked rather than assumed. A
	// harness that constrains output cannot return 1.5 here, and the
	// fenced-JSON path has no such guarantee (lib/validate.sh:94-98).
	// `floor` is arithmetic, and jq's arithmetic is double precision: a
	// literal beyond a double collapses first, so 1.0000000000000001 and 1E+19
	// are both whole to jq and only 1.5 is not. Measured against jq 1.8.1
	// rather than read off the filter, because `(.n | floor) == .n` answers
	// true for 1.0000000000000001 while `.n == 1` answers false.
	//
	// float64(int64(n)) was the earlier spelling and is wrong twice over: Go's
	// spec makes an out-of-range float-to-int conversion
	// implementation-defined, so 1e30 took an undefined path, and the
	// saturation at 2^63 called 1E+19 non-whole — a shape failure where the
	// shell reports a semantic one, and the two carry different retry budgets.
	n, ok := jqFloat(r["finding_number"])
	if !ok || math.Floor(n) != n {
		return true
	}
	if !jqIn(jqAlt(r["resolution"], `""`), "fixed", "skipped", "deferred", "disputed", "escalated") {
		return true
	}
	if reply, ok := jqString(r["reply"]); !ok || reply == "" {
		return true
	}
	if jqType(jqAlt(r["duplicate_of"], "0")) != "number" {
		return true
	}
	return false
}

// resolveSemantic is the second jq program at lib/validate.sh:112-130, in its
// order: out of range, settled twice, left out, then an invented duplicate.
//
// The order is the answer, not just the reading. A payload that is both out of
// range and names an invented duplicate gets the range message, and a caller
// deciding what to tell the model reads the first problem rather than a set.
func resolveSemantic(elements []json.RawMessage, expect Expectations) error {
	got := make([]number, 0, len(elements))
	duplicates := make([]number, 0, len(elements))
	for _, element := range elements {
		// `.resolutions[].finding_number` carries no `?` here, so jq raises on
		// an element the shape half skipped and the shell reports the failure
		// against the second read rather than the first.
		if jqType(element) != "object" && jqType(element) != "null" {
			return semanticf("the payload stopped being parseable between the two checks")
		}
		var r map[string]json.RawMessage
		if jqType(element) == "object" {
			if err := json.Unmarshal(element, &r); err != nil {
				return semanticf("the payload stopped being parseable between the two checks")
			}
		}
		n, ok := asNumber(r["finding_number"])
		if !ok {
			// Unreachable behind the shape half, which has already refused every
			// object whose finding_number is not a number.
			return semanticf("the payload stopped being parseable between the two checks")
		}
		got = append(got, n)

		if jqType(r["duplicate_of"]) == "null" {
			continue
		}
		duplicates = append(duplicates, asScalar(r["duplicate_of"]))
	}

	low := exactInt(1)
	high := exactInt(expect.Findings)
	var outOfRange []number
	for _, n := range got {
		if n.exact.Cmp(low) < 0 || n.exact.Cmp(high) > 0 {
			outOfRange = append(outOfRange, n)
		}
	}
	if list := unique(outOfRange); len(list) > 0 {
		return semanticf("finding number(s) %s do not exist — %d finding(s) were supplied, "+
			"numbered 1 to %d", join(list), expect.Findings, expect.Findings)
	}

	var twice []number
	for i, n := range got {
		for j, m := range got {
			if i != j && n.eq(m) {
				twice = append(twice, n)
				break
			}
		}
	}
	if list := unique(twice); len(list) > 0 {
		return semanticf("finding(s) %s were settled more than once", join(list))
	}

	var missing []number
	for i := 1; i <= expect.Findings; i++ {
		// jq built these with `range`, which produces plain integers rather
		// than anything the payload wrote, so the literal is the integer
		// itself.
		want := number{literal: strconv.Itoa(i), f: float64(i), exact: exactInt(i), numeric: true}
		settled := false
		for _, n := range got {
			if n.eq(want) {
				settled = true
				break
			}
		}
		if !settled {
			missing = append(missing, want)
		}
	}
	if len(missing) > 0 {
		return semanticf("finding(s) %s got no resolution at all, and a finding left out gets "+
			"no reply and no thread resolution", join(missing))
	}

	offered := make([]number, 0, len(expect.Candidates))
	for _, c := range expect.Candidates {
		offered = append(offered, number{
			literal: strconv.Itoa(c), f: float64(c), exact: exactInt(c), numeric: true,
		})
	}
	var invented []number
	for _, d := range duplicates {
		match := false
		for _, c := range offered {
			if d.eq(c) {
				match = true
				break
			}
		}
		if match {
			continue
		}
		invented = append(invented, d)
	}
	if list := unique(invented); len(list) > 0 {
		return semanticf("duplicate_of names issue(s) %s, which were not among the candidates "+
			"supplied in the prompt", join(list))
	}
	return nil
}

// jqPrecision is how many bits of mantissa a payload's number is compared at.
//
// jq keeps a number's literal and compares two literals at decNumber's
// precision rather than a double's, which is why 1.0000000000000001 is greater
// than 1 there and equal to it in float64. 1024 bits is about 308 decimal
// digits, past anything a payload plausibly writes and past the 78 digits jq
// was measured distinguishing.
const jqPrecision = 1024

// number is one JSON number as jq holds it: the literal it was written as, the
// double its arithmetic collapses to, and the full-precision value its
// comparisons use.
//
// The three are not interchangeable. `join` writes the literal back, the shape
// half's `floor` reads the double, and the range, duplicate and candidate
// checks read the exact value — a finding_number of 1.0000000000000001 is whole
// to `floor` and greater than 1 to `>`, so it passes the shape half and fails
// the range check, which is exactly what the shell does.
type number struct {
	literal string
	f       float64
	exact   *big.Float
	// numeric is false for the one non-number that reaches the semantic half:
	// `duplicate_of: false`, which the shape half accepts because `// 0` reads
	// false as absent. jq's own `join` renders it "false", `-` never matches it
	// against a candidate however the candidate is written, and jq's total
	// order sorts it below every number.
	numeric bool
}

// eq is jq's `==` between two payload numbers: exact, so 17 and
// 17.0000000000000001 are different issues and false is neither.
func (n number) eq(m number) bool {
	if n.numeric != m.numeric {
		return false
	}
	if !n.numeric {
		return true
	}
	return n.exact.Cmp(m.exact) == 0
}

// less is jq's sort order over the two kinds that reach here: false below every
// number, and numbers by value.
func (n number) less(m number) bool {
	if n.numeric != m.numeric {
		return !n.numeric
	}
	if !n.numeric {
		return false
	}
	return n.exact.Cmp(m.exact) < 0
}

func exactInt(i int) *big.Float {
	return new(big.Float).SetPrec(jqPrecision).SetInt64(int64(i))
}

func asNumber(raw json.RawMessage) (number, bool) {
	if jqType(raw) != "number" {
		return number{}, false
	}
	literal := string(bytes.TrimSpace(raw))
	f, err := strconv.ParseFloat(literal, 64)
	if err != nil && !errors.Is(err, strconv.ErrRange) {
		// Not reachable through a document encoding/json has parsed, which
		// admits only JSON number syntax.
		return number{}, false
	}
	// A literal whose exponent is past big.Float's own range comes back as an
	// infinity with ErrRange, which compares the way jq's does.
	exact, _, perr := big.ParseFloat(literal, 10, jqPrecision, big.ToNearestEven)
	if perr != nil && !errors.Is(perr, strconv.ErrRange) {
		return number{}, false
	}
	return number{literal: literal, f: f, exact: exact, numeric: true}, true
}

// asScalar is asNumber with jq's sort and join behaviour for the non-number
// that can reach it.
func asScalar(raw json.RawMessage) number {
	if n, ok := asNumber(raw); ok {
		return n
	}
	return number{literal: jqCompact(raw), exact: exactInt(0)}
}

// jqFloat is the double jq's arithmetic works in. A literal past a double's
// range comes back as an infinity rather than an error, which is what jq's own
// `floor` answers for it.
func jqFloat(raw json.RawMessage) (float64, bool) {
	if jqType(raw) != "number" {
		return 0, false
	}
	f, err := strconv.ParseFloat(string(bytes.TrimSpace(raw)), 64)
	if err != nil && !errors.Is(err, strconv.ErrRange) {
		return 0, false
	}
	return f, true
}

// unique is jq's `unique`: sorted, then deduplicated.
func unique(in []number) []number {
	if len(in) == 0 {
		return nil
	}
	sorted := append([]number(nil), in...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].less(sorted[j]) })
	out := sorted[:1]
	for _, n := range sorted[1:] {
		if !n.eq(out[len(out)-1]) {
			out = append(out, n)
		}
	}
	return out
}

// join is jq's `join(", ")` over numbers.
func join(in []number) string {
	parts := make([]string, len(in))
	for i, n := range in {
		parts[i] = n.literal
	}
	return strings.Join(parts, ", ")
}
