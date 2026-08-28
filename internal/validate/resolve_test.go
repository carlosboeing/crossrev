package validate_test

import (
	"testing"

	"github.com/carlosboeing/crossrev/internal/validate"
)

// threeFindings is what tests/test-schemas.sh:155 supplies: three findings were
// numbered, and two issues were offered as duplicate candidates.
var threeFindings = &validate.Expectations{Findings: 3, Candidates: []int{19, 31}}

// resolutions wraps a resolutions array the way tests/test-schemas.sh:157-160
// does.
func resolutions(inner string) string {
	return `{"blocked":false,"blocked_reason":null,"summary":"What happened.","resolutions":` + inner + `}`
}

const allThree = `[{"finding_number":1,"resolution":"fixed","reply":"r","persist":null,"duplicate_of":null},` +
	`{"finding_number":2,"resolution":"fixed","reply":"r","persist":null,"duplicate_of":null},` +
	`{"finding_number":3,"resolution":"fixed","reply":"r","persist":null,"duplicate_of":null}]`

func TestResolveShape(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		expect  *validate.Expectations
		want    string
	}{
		{
			name:    "three numbered resolutions for three findings pass",
			payload: resolutions(allThree),
			expect:  threeFindings,
		},
		{
			name:    "unparseable",
			payload: `nope`,
			want:    "the payload is not parseable JSON",
		},
		{
			name:    "not an object",
			payload: `[]`,
			want:    "the payload is not a JSON object",
		},
		{
			name:    "resolutions absent",
			payload: `{"summary":"s"}`,
			want:    "resolutions is missing or not an array",
		},
		{
			name:    "resolutions is not an array",
			payload: `{"resolutions":{},"summary":"s"}`,
			want:    "resolutions is missing or not an array",
		},
		{
			name:    "summary absent",
			payload: `{"resolutions":[]}`,
			want:    "summary is missing or empty",
		},
		{
			name:    "summary empty",
			payload: `{"resolutions":[],"summary":""}`,
			want:    "summary is missing or empty",
		},
		{
			name:    "summary is not a string",
			payload: `{"resolutions":[],"summary":3}`,
			want:    "summary is missing or empty",
		},
		{
			// The old field name. tests/test-schemas.sh:133 asserts the rename
			// through this route: a payload carrying wrap_up has no summary.
			name:    "the old wrap_up field is rejected",
			payload: `{"resolutions":[],"blocked":false,"wrap_up":"What happened this pass."}`,
			want:    "summary is missing or empty",
		},
		{
			name:    "blocked is not a boolean",
			payload: `{"resolutions":[],"summary":"s","blocked":"yes"}`,
			want:    "blocked is not a boolean",
		},
		{
			// `// false` reads a null as absent, so a null blocked takes the
			// default and passes (lib/validate.sh:80-81).
			name:    "a null blocked passes through the alternative operator",
			payload: `{"resolutions":[],"summary":"s","blocked":null}`,
		},
		{
			name:    "a numeric commit subject",
			payload: `{"resolutions":[],"summary":"s","commit_subject":3}`,
			want:    "commit_subject is number, and a commit subject is a string or null",
		},
		{
			// `jq -r` would read an empty array back as the string "[]" and
			// commit it (lib/validate.sh:82-91).
			name:    "an empty array commit subject",
			payload: `{"resolutions":[],"summary":"s","commit_subject":[]}`,
			want:    "commit_subject is array, and a commit subject is a string or null",
		},
		{
			name:    "a null commit subject passes, because a pass may fix nothing",
			payload: `{"resolutions":[],"summary":"s","commit_subject":null}`,
		},
		{
			name:    "an omitted commit subject is accepted, and the commit goes out generic",
			payload: resolutions(allThree),
			expect:  threeFindings,
		},
		{
			name: "the old finding_id field is a shape failure, not a semantic one",
			payload: resolutions(
				`[{"finding_id":"9e4f9ee1cbe25125","resolution":"fixed","reply":"r","persist":null,"duplicate_of":null}]`),
			expect: threeFindings,
			want: `1 resolution(s) have a missing or non-whole finding_number, a missing reply, a non-numeric duplicate_of, ` +
				`or a resolution outside the five allowed — first: {"finding_id":"9e4f9ee1cbe25125","resolution":"fixed","reply":"r","persist":null,"duplicate_of":null}`,
		},
		{
			name:    "a finding number that is not whole",
			payload: resolutions(`[{"finding_number":1.5,"resolution":"fixed","reply":"r"}]`),
			expect:  threeFindings,
			want: `1 resolution(s) have a missing or non-whole finding_number, a missing reply, a non-numeric duplicate_of, ` +
				`or a resolution outside the five allowed — first: {"finding_number":1.5,"resolution":"fixed","reply":"r"}`,
		},
		{
			name:    "a resolution word outside the five",
			payload: resolutions(`[{"finding_number":1,"resolution":"nope","reply":"r"}]`),
			expect:  threeFindings,
			want: `1 resolution(s) have a missing or non-whole finding_number, a missing reply, a non-numeric duplicate_of, ` +
				`or a resolution outside the five allowed — first: {"finding_number":1,"resolution":"nope","reply":"r"}`,
		},
		{
			name:    "an empty reply",
			payload: resolutions(`[{"finding_number":1,"resolution":"fixed","reply":""}]`),
			expect:  threeFindings,
			want: `1 resolution(s) have a missing or non-whole finding_number, a missing reply, a non-numeric duplicate_of, ` +
				`or a resolution outside the five allowed — first: {"finding_number":1,"resolution":"fixed","reply":""}`,
		},
		{
			name:    "a duplicate_of that is a string",
			payload: resolutions(`[{"finding_number":1,"resolution":"fixed","reply":"r","duplicate_of":"19"}]`),
			expect:  threeFindings,
			want: `1 resolution(s) have a missing or non-whole finding_number, a missing reply, a non-numeric duplicate_of, ` +
				`or a resolution outside the five allowed — first: {"finding_number":1,"resolution":"fixed","reply":"r","duplicate_of":"19"}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validate.Resolve([]byte(tc.payload), tc.expect)
			if got := message(err); got != tc.want {
				t.Fatalf("message:\n got %q\nwant %q", got, tc.want)
			}
			want := 0
			if tc.want != "" {
				want = 1
			}
			if got := code(err); got != want {
				t.Fatalf("exit code: got %d, want %d", got, want)
			}
		})
	}
}

func TestResolveSemantic(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		expect  *validate.Expectations
		want    string
	}{
		{
			name: "a finding number past the end",
			payload: resolutions(`[{"finding_number":1,"resolution":"fixed","reply":"r"},` +
				`{"finding_number":2,"resolution":"fixed","reply":"r"},` +
				`{"finding_number":7,"resolution":"fixed","reply":"r"}]`),
			expect: threeFindings,
			want:   "finding number(s) 7 do not exist — 3 finding(s) were supplied, numbered 1 to 3",
		},
		{
			// unique sorts, so the two out-of-range numbers are listed low to
			// high whatever order they arrived in.
			name: "a number below one, listed with one past the end in sorted order",
			payload: resolutions(`[{"finding_number":0,"resolution":"fixed","reply":"r"},` +
				`{"finding_number":2,"resolution":"fixed","reply":"r"},` +
				`{"finding_number":9,"resolution":"fixed","reply":"r"}]`),
			expect: threeFindings,
			want:   "finding number(s) 0, 9 do not exist — 3 finding(s) were supplied, numbered 1 to 3",
		},
		{
			name: "the same finding settled twice",
			payload: resolutions(`[{"finding_number":1,"resolution":"fixed","reply":"r"},` +
				`{"finding_number":2,"resolution":"fixed","reply":"r"},` +
				`{"finding_number":2,"resolution":"fixed","reply":"r"},` +
				`{"finding_number":1,"resolution":"fixed","reply":"r"}]`),
			expect: threeFindings,
			want:   "finding(s) 1, 2 were settled more than once",
		},
		{
			name:    "a finding with no resolution at all",
			payload: resolutions(`[{"finding_number":1,"resolution":"fixed","reply":"r"}]`),
			expect:  threeFindings,
			want: "finding(s) 2, 3 got no resolution at all, and a finding left out gets no reply " +
				"and no thread resolution",
		},
		{
			name: "a duplicate_of naming an issue nobody offered",
			payload: resolutions(`[{"finding_number":1,"resolution":"fixed","reply":"r","duplicate_of":404},` +
				`{"finding_number":2,"resolution":"fixed","reply":"r","duplicate_of":null},` +
				`{"finding_number":3,"resolution":"fixed","reply":"r","duplicate_of":null}]`),
			expect: threeFindings,
			want: "duplicate_of names issue(s) 404, which were not among the candidates " +
				"supplied in the prompt",
		},
		{
			name: "a duplicate_of naming a supplied candidate",
			payload: resolutions(`[{"finding_number":1,"resolution":"fixed","reply":"r","duplicate_of":19},` +
				`{"finding_number":2,"resolution":"fixed","reply":"r","duplicate_of":null},` +
				`{"finding_number":3,"resolution":"fixed","reply":"r","duplicate_of":null}]`),
			expect: threeFindings,
		},
		{
			// Candidates are supplied per finding, and the check is against all
			// of them (tests/test-schemas.sh:236-242).
			name: "a candidate offered for a different finding",
			payload: resolutions(`[{"finding_number":1,"resolution":"fixed","reply":"r","duplicate_of":null},` +
				`{"finding_number":2,"resolution":"fixed","reply":"r","duplicate_of":31},` +
				`{"finding_number":3,"resolution":"fixed","reply":"r","duplicate_of":null}]`),
			expect: threeFindings,
		},
		{
			name:    "no candidates offered, and none named",
			payload: resolutions(allThree),
			expect:  &validate.Expectations{Findings: 3},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validate.Resolve([]byte(tc.payload), tc.expect)
			if got := message(err); got != tc.want {
				t.Fatalf("message:\n got %q\nwant %q", got, tc.want)
			}
			want := 0
			if tc.want != "" {
				want = 2
			}
			if got := code(err); got != want {
				t.Fatalf("exit code: got %d, want %d", got, want)
			}
		})
	}
}

// A repository-backlog destination is offered no candidates at all, and
// duplicate_of still becomes the "tracked as" line on a deferred finding's reply
// (tests/test-schemas.sh:247-251).
func TestResolveRejectsADuplicateOfWhenNoCandidateWasOffered(t *testing.T) {
	payload := resolutions(`[{"finding_number":1,"resolution":"fixed","reply":"r","duplicate_of":19},` +
		`{"finding_number":2,"resolution":"fixed","reply":"r","duplicate_of":null},` +
		`{"finding_number":3,"resolution":"fixed","reply":"r","duplicate_of":null}]`)
	err := validate.Resolve([]byte(payload), &validate.Expectations{Findings: 3})
	want := "duplicate_of names issue(s) 19, which were not among the candidates supplied in the prompt"
	if got := message(err); got != want {
		t.Fatalf("message:\n got %q\nwant %q", got, want)
	}
	if got := code(err); got != 2 {
		t.Fatalf("exit code: got %d, want 2", got)
	}
}

// Nothing to compare against means the shape half is the whole check
// (lib/validate.sh:109-110).
func TestResolveWithNoExpectationsChecksShapeOnly(t *testing.T) {
	payload := resolutions(`[{"finding_number":9,"resolution":"fixed","reply":"r","persist":null,"duplicate_of":null}]`)
	if err := validate.Resolve([]byte(payload), nil); err != nil {
		t.Fatalf("wanted the shape half only, got %q", err)
	}
}

// The shape half skips a resolutions element that is not an object, the way
// `.finding_number?` skips it. The semantic half indexes without the `?`, so jq
// raises there and the shell reports the second parse (lib/validate.sh:112-130).
func TestResolveSemanticHalfRejectsWhatTheShapeHalfSkipped(t *testing.T) {
	payload := `{"summary":"s","resolutions":[1]}`
	if err := validate.Resolve([]byte(payload), nil); err != nil {
		t.Fatalf("shape half: wanted acceptance, got %q", err)
	}

	err := validate.Resolve([]byte(payload), &validate.Expectations{Findings: 1})
	want := "the payload stopped being parseable between the two checks"
	if got := message(err); got != want {
		t.Fatalf("semantic half:\n got %q\nwant %q", got, want)
	}
	if got := code(err); got != 2 {
		t.Fatalf("exit code: got %d, want 2", got)
	}
}
