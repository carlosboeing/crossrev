package validate_test

import (
	"testing"

	"github.com/carlosboeing/crossrev/internal/validate"
)

// The cases here are the ones the two tables in findings_test.go and
// resolve_test.go do not reach. Each was found by mutating the code and
// watching every existing test still pass, so each one names the mutation it
// refuses in its comment.
//
// Every `want` is the byte string lib/validate.sh prints for the same payload,
// measured by running validate_findings or validate_resolve on it rather than
// derived from the Go.

// finding wraps one findings element in a payload the rest of the check
// accepts, so a case says only what it is testing.
func finding(inner string) string {
	return `{"verdict":"issues-remain","findings":[{` + inner + `}]}`
}

const goodFinding = `"path":"a.ts","line":1,"severity":"high","category":"correctness",` +
	`"pre_existing":false,"title":"t"`

// `.line? | type != "number"` is the only thing said about a line, so a string
// that reads as one is still not one. Dropping the clause left every existing
// case passing.
func TestFindingsRefusesALineThatIsNotANumber(t *testing.T) {
	for _, line := range []string{`"2"`, `null`, `true`, `[2]`} {
		payload := finding(`"path":"a.ts","line":` + line +
			`,"severity":"high","category":"correctness","pre_existing":false,"title":"t"`)
		err := validate.Findings([]byte(payload))
		if code(err) != 1 {
			t.Errorf("line %s: got code %d (%v), want a shape failure", line, code(err), err)
		}
	}
}

// lib/validate.sh accepts a fractional line: it checks the type and nothing
// else. The resolve leg's finding_number is the one with a floor check, and
// the asymmetry is deliberate, so tightening this to whole numbers would put
// the port ahead of the shell rather than level with it.
func TestFindingsAcceptsAFractionalLine(t *testing.T) {
	payload := finding(`"path":"a.ts","line":1.5,"severity":"high","category":"correctness",` +
		`"pre_existing":false,"title":"t"`)
	if err := validate.Findings([]byte(payload)); err != nil {
		t.Fatalf("wanted 1.5 accepted the way the shell accepts it, got %q", err)
	}
}

// `(.side? // "RIGHT")` supplies the default, and `IN("LEFT","RIGHT")` is the
// range. Removing either left every existing case passing, because no case
// omitted side and no case supplied a wrong one.
func TestFindingsSideDefaultsToRightAndIsRangeChecked(t *testing.T) {
	if err := validate.Findings([]byte(finding(goodFinding))); err != nil {
		t.Fatalf("an absent side should take the RIGHT default, got %q", err)
	}
	for _, side := range []string{`"LEFT"`, `"RIGHT"`} {
		if err := validate.Findings([]byte(finding(goodFinding + `,"side":` + side))); err != nil {
			t.Errorf("side %s should pass, got %q", side, err)
		}
	}
	for _, side := range []string{`"MIDDLE"`, `"right"`, `2`} {
		err := validate.Findings([]byte(finding(goodFinding + `,"side":` + side)))
		if code(err) != 1 {
			t.Errorf("side %s: got code %d, want a shape failure", side, code(err))
		}
	}
}

// Every value in the two enums is named, so dropping one from either list
// fails here. Dropping "low" and dropping "testing" both survived before.
func TestFindingsAcceptsEverySeverityAndCategory(t *testing.T) {
	for _, severity := range []string{"high", "medium", "low"} {
		payload := finding(`"path":"a.ts","line":1,"severity":"` + severity +
			`","category":"correctness","pre_existing":false,"title":"t"`)
		if err := validate.Findings([]byte(payload)); err != nil {
			t.Errorf("severity %q should pass, got %q", severity, err)
		}
	}
	for _, category := range []string{
		"correctness", "security", "performance", "maintainability", "testing", "docs",
	} {
		payload := finding(`"path":"a.ts","line":1,"severity":"high","category":"` + category +
			`","pre_existing":false,"title":"t"`)
		if err := validate.Findings([]byte(payload)); err != nil {
			t.Errorf("category %q should pass, got %q", category, err)
		}
	}
}

// `(.title? | type != "string") or (.title == "")` is two clauses, and only the
// first had a case. A title of "" anchors a comment nobody can read.
func TestFindingsRefusesAnEmptyTitle(t *testing.T) {
	payload := finding(`"path":"a.ts","line":1,"severity":"high","category":"correctness",` +
		`"pre_existing":false,"title":""`)
	err := validate.Findings([]byte(payload))
	want := `1 finding(s) have a missing or out-of-range path, line, side, severity, category, ` +
		`pre_existing or title — first: {"path":"a.ts","line":1,"severity":"high",` +
		`"category":"correctness","pre_existing":false,"title":""}`
	if code(err) != 1 || message(err) != want {
		t.Fatalf("got code %d %q, want 1 %q", code(err), message(err), want)
	}
}

// jq writes `<`, `>` and `&` as themselves, where encoding/json escapes all
// three by default; it writes a tab as its short escape and every other C0
// control and DEL as `\u00xx`. The first bad element is quoted with `tojson`,
// so a title carrying all three rules reaches the message.
//
// Dropping the short escape for a tab, escaping the three HTML code points and
// leaving control characters raw were three separate mutations, and every
// existing case survived all three.
func TestFindingsQuotesTheFirstBadElementTheWayJQDoes(t *testing.T) {
	// Bad for its missing line, so the title survives into the message.
	const title = `a<b>c&d\tE\u0001F\u007fG`
	payload := finding(`"path":"a.ts","severity":"high","category":"correctness",` +
		`"pre_existing":false,"title":"` + title + `"`)
	err := validate.Findings([]byte(payload))
	want := `1 finding(s) have a missing or out-of-range path, line, side, severity, category, ` +
		`pre_existing or title — first: {"path":"a.ts","severity":"high",` +
		`"category":"correctness","pre_existing":false,"title":"` + title + `"}`
	if message(err) != want {
		t.Fatalf("got  %s\nwant %s", message(err), want)
	}
}

// jq refuses a document carrying a `\uXXXX` escape that is half a surrogate
// pair, and the shell reports that as one line. Go's encoding/json accepts it
// and substitutes U+FFFD, which carried a mangled title into a posted comment
// where the shell's run had stopped.
func TestBothValidatorsRefuseALoneSurrogateEscape(t *testing.T) {
	for _, escape := range []string{
		`\ud800`, `\udc00`, `\ud800x`, `\ud800\ud800`, `\udfff\udfff`,
	} {
		findings := finding(`"path":"a.ts","line":1,"severity":"high","category":"correctness",` +
			`"pre_existing":false,"title":"t","why":"` + escape + `"`)
		err := validate.Findings([]byte(findings))
		if code(err) != 1 || message(err) != "the payload is not parseable JSON" {
			t.Errorf("findings %q: got code %d %q", escape, code(err), message(err))
		}

		resolve := `{"summary":"` + escape + `","resolutions":[]}`
		err = validate.Resolve([]byte(resolve), nil)
		if code(err) != 1 || message(err) != "the payload is not parseable JSON" {
			t.Errorf("resolve %q: got code %d %q", escape, code(err), message(err))
		}
	}
}

// A well-formed pair is not the refused case, and neither is a backslash
// followed by the letter u, which is an escaped backslash rather than an
// escape.
func TestBothValidatorsAcceptAWellFormedSurrogatePair(t *testing.T) {
	payload := finding(`"path":"a.ts","line":1,"severity":"high","category":"correctness",` +
		`"pre_existing":false,"title":"\ud83d\ude00","why":"\\ud800"`)
	if err := validate.Findings([]byte(payload)); err != nil {
		t.Fatalf("a well-formed pair should pass, got %q", err)
	}
}

// The five resolutions are named, so dropping one from the list fails here.
// Dropping "escalated" and dropping "deferred" both survived before.
func TestResolveAcceptsEveryResolution(t *testing.T) {
	one := &validate.Expectations{Findings: 1}
	for _, resolution := range []string{"fixed", "skipped", "deferred", "disputed", "escalated"} {
		payload := `{"summary":"s","resolutions":[{"finding_number":1,"resolution":"` +
			resolution + `","reply":"r"}]}`
		if err := validate.Resolve([]byte(payload), one); err != nil {
			t.Errorf("resolution %q should pass, got %q", resolution, err)
		}
	}
}

// The upper bound is `. > $n`, and every existing case was far enough past it
// that `> $n + 1` would have caught them too. One past the end is the number a
// model actually returns.
func TestResolveRefusesTheFindingOnePastTheEnd(t *testing.T) {
	payload := resolutions(`[{"finding_number":1,"resolution":"fixed","reply":"r"},` +
		`{"finding_number":2,"resolution":"fixed","reply":"r"},` +
		`{"finding_number":3,"resolution":"fixed","reply":"r"},` +
		`{"finding_number":4,"resolution":"fixed","reply":"r"}]`)
	err := validate.Resolve([]byte(payload), threeFindings)
	want := "finding number(s) 4 do not exist — 3 finding(s) were supplied, numbered 1 to 3"
	if code(err) != 2 || message(err) != want {
		t.Fatalf("got code %d %q, want 2 %q", code(err), message(err), want)
	}
}

// The four semantic checks are an if/elif chain, and the order is the answer
// rather than a detail: a payload that is both out of range and names an
// invented duplicate gets the range message. Moving the invented check to the
// front left every existing case passing.
func TestResolveReportsTheRangeFailureBeforeTheInventedDuplicate(t *testing.T) {
	payload := resolutions(`[{"finding_number":1,"resolution":"fixed","reply":"r"},` +
		`{"finding_number":2,"resolution":"fixed","reply":"r"},` +
		`{"finding_number":9,"resolution":"fixed","reply":"r","duplicate_of":404}]`)
	err := validate.Resolve([]byte(payload), threeFindings)
	want := "finding number(s) 9 do not exist — 3 finding(s) were supplied, numbered 1 to 3"
	if message(err) != want {
		t.Fatalf("got %q, want %q", message(err), want)
	}
}

// `duplicate_of: false` reaches the semantic half because the shape half reads
// it through `// 0`, and jq's array subtraction never matches it against a
// number however the number is written. Comparing on the value alone matched
// it against a candidate of 0, which is the whole reason the kind is carried
// beside the value.
func TestResolveNeverMatchesAFalseDuplicateAgainstACandidate(t *testing.T) {
	payload := `{"summary":"s","resolutions":[` +
		`{"finding_number":1,"resolution":"fixed","reply":"r","duplicate_of":false}]}`
	expect := &validate.Expectations{Findings: 1, Candidates: []int{0}}
	err := validate.Resolve([]byte(payload), expect)
	want := "duplicate_of names issue(s) false, which were not among the candidates " +
		"supplied in the prompt"
	if code(err) != 2 || message(err) != want {
		t.Fatalf("got code %d %q, want 2 %q", code(err), message(err), want)
	}
}

// jq's total order puts false below every number, and `unique` sorts before it
// deduplicates, so false is listed first however it arrived. The number here is
// negative on purpose: false carries no value of its own, and a sort that
// compared it as one would put -5 in front of it.
func TestResolveListsAFalseDuplicateBeforeAnInventedNumber(t *testing.T) {
	payload := `{"summary":"s","resolutions":[` +
		`{"finding_number":1,"resolution":"fixed","reply":"r","duplicate_of":-5},` +
		`{"finding_number":2,"resolution":"fixed","reply":"r","duplicate_of":false}]}`
	expect := &validate.Expectations{Findings: 2, Candidates: []int{17}}
	err := validate.Resolve([]byte(payload), expect)
	want := "duplicate_of names issue(s) false, -5, which were not among the candidates " +
		"supplied in the prompt"
	if message(err) != want {
		t.Fatalf("got %q, want %q", message(err), want)
	}
}

// jq prints a number as the literal the payload wrote, so 4.0 is reported as
// 4.0 rather than as 4. Replacing the literal with the float's own formatting
// left every existing case passing, because every one of them wrote a plain
// integer.
func TestResolveReportsTheLiteralThePayloadWrote(t *testing.T) {
	payload := `{"summary":"s","resolutions":[` +
		`{"finding_number":1,"resolution":"fixed","reply":"r","duplicate_of":4.0}]}`
	expect := &validate.Expectations{Findings: 1, Candidates: []int{17}}
	err := validate.Resolve([]byte(payload), expect)
	want := "duplicate_of names issue(s) 4.0, which were not among the candidates " +
		"supplied in the prompt"
	if message(err) != want {
		t.Fatalf("got %q, want %q", message(err), want)
	}
}

// jq keeps a number's literal and compares two literals at more than double
// precision, so 1.0000000000000001 is whole to `floor` — which is arithmetic,
// and collapses to a double — and greater than 1 to `>`, which is not.
// Deciding the range on the float64 accepted a payload the shell refuses.
func TestResolveComparesPastDoublePrecision(t *testing.T) {
	one := &validate.Expectations{Findings: 1, Candidates: []int{17}}

	payload := `{"summary":"s","resolutions":[` +
		`{"finding_number":1.0000000000000001,"resolution":"fixed","reply":"r"}]}`
	err := validate.Resolve([]byte(payload), one)
	want := "finding number(s) 1.0000000000000001 do not exist — 1 finding(s) were supplied, " +
		"numbered 1 to 1"
	if code(err) != 2 || message(err) != want {
		t.Fatalf("finding_number: got code %d %q, want 2 %q", code(err), message(err), want)
	}

	payload = `{"summary":"s","resolutions":[` +
		`{"finding_number":1,"resolution":"fixed","reply":"r",` +
		`"duplicate_of":17.0000000000000001}]}`
	err = validate.Resolve([]byte(payload), one)
	want = "duplicate_of names issue(s) 17.0000000000000001, which were not among the " +
		"candidates supplied in the prompt"
	if code(err) != 2 || message(err) != want {
		t.Fatalf("duplicate_of: got code %d %q, want 2 %q", code(err), message(err), want)
	}
}

// A whole number past 2^63 is whole, and the failure it earns is semantic. The
// earlier `n != float64(int64(n))` saturated and called it non-whole, which is
// a shape failure — a different exit code, and lib/run.sh:810-811 spends a
// different retry budget on each.
func TestResolveCallsAHugeWholeNumberSemanticRatherThanMalformed(t *testing.T) {
	one := &validate.Expectations{Findings: 1}
	for _, literal := range []string{
		"1E+19", "1e20", "1.7976931348623157e308", "12345678901234567890",
		"9223372036854775807", "9223372036854775808",
	} {
		payload := `{"summary":"s","resolutions":[{"finding_number":` + literal +
			`,"resolution":"fixed","reply":"r"}]}`
		err := validate.Resolve([]byte(payload), one)
		want := "finding number(s) " + literal +
			" do not exist — 1 finding(s) were supplied, numbered 1 to 1"
		if code(err) != 2 || message(err) != want {
			t.Errorf("%s: got code %d %q, want 2 %q", literal, code(err), message(err), want)
		}
	}
}

// 1.5 is the case `floor` exists for, and it stays a shape failure.
func TestResolveCallsAFractionalFindingNumberMalformed(t *testing.T) {
	payload := `{"summary":"s","resolutions":[` +
		`{"finding_number":1.5,"resolution":"fixed","reply":"r"}]}`
	err := validate.Resolve([]byte(payload), &validate.Expectations{Findings: 1})
	if code(err) != 1 {
		t.Fatalf("got code %d %q, want a shape failure", code(err), message(err))
	}
}
