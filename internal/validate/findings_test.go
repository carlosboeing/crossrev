package validate_test

import (
	"errors"
	"testing"

	"github.com/carlosboeing/crossrev/internal/validate"
)

// code is the exit status the shell's validate_findings and validate_resolve
// return: 0 for nothing wrong, 1 for a shape problem, 2 for a semantic one
// (lib/validate.sh:4-25).
func code(err error) int {
	var shape *validate.ShapeError
	if errors.As(err, &shape) {
		return shape.Code()
	}
	var semantic *validate.SemanticError
	if errors.As(err, &semantic) {
		return semantic.Code()
	}
	if err != nil {
		return -1
	}
	return 0
}

func message(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func TestFindingsAcceptsAWellFormedPayload(t *testing.T) {
	payload := `{"verdict":"issues-remain","prior":null,"blocked_reason":null,
	  "findings":[{"path":"a.ts","line":1,"side":"RIGHT","severity":"high",
	               "category":"correctness","pre_existing":true,"title":"t",
	               "why":"w","fix":"f"}]}`
	if err := validate.Findings([]byte(payload)); err != nil {
		t.Fatalf("wanted the payload accepted, got %q", err)
	}
}

func TestFindingsShape(t *testing.T) {
	// Every want below is the exact byte string the shell prints, measured by
	// running lib/validate.sh's validate_findings on the same payload.
	cases := []struct {
		name    string
		payload string
		want    string
	}{
		{
			// jq's own parse failure, which the shell turns into this line
			// (lib/validate.sh:62).
			name:    "unparseable",
			payload: `not json`,
			want:    "the payload is not parseable JSON",
		},
		{
			// An empty input runs the filter zero times, so jq prints nothing
			// and exits 0. The shell reads that as no problem
			// (lib/validate.sh:35-62, the `<<<"$payload"` redirection).
			name:    "empty is accepted, because jq ran the filter on nothing",
			payload: ``,
			want:    "",
		},
		{
			name:    "whitespace only is accepted for the same reason",
			payload: `   `,
			want:    "",
		},
		{
			name:    "not an object",
			payload: `[]`,
			want:    "the payload is not a JSON object",
		},
		{
			name:    "no verdict key",
			payload: `{"findings":[]}`,
			want:    "no verdict key",
		},
		{
			name:    "a null verdict interpolates as null",
			payload: `{"verdict":null,"findings":[]}`,
			want:    `verdict is "null", which is not one of converged, issues-remain, blocked`,
		},
		{
			name:    "an unknown verdict word",
			payload: `{"verdict":"nope","findings":[]}`,
			want:    `verdict is "nope", which is not one of converged, issues-remain, blocked`,
		},
		{
			// jq interpolates a non-string as its compact JSON form, so the
			// message carries an object rather than a type name.
			name:    "an object verdict interpolates as compact JSON",
			payload: `{"verdict":{"a":1},"findings":[]}`,
			want:    `verdict is "{"a":1}", which is not one of converged, issues-remain, blocked`,
		},
		{
			name:    "findings absent",
			payload: `{"verdict":"converged"}`,
			want:    "findings is missing or not an array",
		},
		{
			name:    "findings is not an array",
			payload: `{"verdict":"converged","findings":{}}`,
			want:    "findings is missing or not an array",
		},
		{
			name: "an old severity word",
			payload: `{"verdict":"issues-remain","findings":[{"path":"a.ts","line":1,"side":"RIGHT",
			  "severity":"important","category":"correctness","pre_existing":false,"title":"t"}]}`,
			want: `1 finding(s) have a missing or out-of-range path, line, side, severity, category, pre_existing or title — first: {"path":"a.ts","line":1,"side":"RIGHT","severity":"important","category":"correctness","pre_existing":false,"title":"t"}`,
		},
		{
			name: "an invented category",
			payload: `{"verdict":"issues-remain","findings":[{"path":"a.ts","line":1,
			  "severity":"high","category":"refactoring","pre_existing":false,"title":"t"}]}`,
			want: `1 finding(s) have a missing or out-of-range path, line, side, severity, category, pre_existing or title — first: {"path":"a.ts","line":1,"severity":"high","category":"refactoring","pre_existing":false,"title":"t"}`,
		},
		{
			// Provenance has no safe default: absent must fail rather than fall
			// to false, which is the value that lets the resolve leg change code
			// (lib/validate.sh:50-56).
			name: "an absent pre_existing",
			payload: `{"verdict":"issues-remain","findings":[{"path":"a.ts","line":1,
			  "severity":"high","category":"docs","title":"t"}]}`,
			want: `1 finding(s) have a missing or out-of-range path, line, side, severity, category, pre_existing or title — first: {"path":"a.ts","line":1,"severity":"high","category":"docs","title":"t"}`,
		},
		{
			name: "a null pre_existing",
			payload: `{"verdict":"issues-remain","findings":[{"path":"a.ts","line":1,
			  "severity":"high","category":"docs","pre_existing":null,"title":"t"}]}`,
			want: `1 finding(s) have a missing or out-of-range path, line, side, severity, category, pre_existing or title — first: {"path":"a.ts","line":1,"severity":"high","category":"docs","pre_existing":null,"title":"t"}`,
		},
		{
			name: "an empty path",
			payload: `{"verdict":"issues-remain","findings":[
			  {"path":"a.ts","line":1,"severity":"high","category":"docs","pre_existing":false,"title":"t"},
			  {"path":"","line":2,"severity":"high","category":"docs","pre_existing":false,"title":"t"}]}`,
			want: `1 finding(s) have a missing or out-of-range path, line, side, severity, category, pre_existing or title — first: {"path":"","line":2,"severity":"high","category":"docs","pre_existing":false,"title":"t"}`,
		},
		{
			// `tojson` re-prints, so an escaped solidus reaches the message as a
			// bare one. Measured against jq 1.8.1.
			name: "tojson re-prints an escaped solidus",
			payload: `{"verdict":"issues-remain","findings":[{"path":"a\/b.ts","line":1,
			  "severity":"high","category":"docs","pre_existing":"no","title":"t"}]}`,
			want: `1 finding(s) have a missing or out-of-range path, line, side, severity, category, pre_existing or title — first: {"path":"a/b.ts","line":1,"severity":"high","category":"docs","pre_existing":"no","title":"t"}`,
		},
		{
			name: "the count names every bad finding, and the message quotes the first",
			payload: `{"verdict":"issues-remain","findings":[
			  {"path":"a.ts","line":1,"severity":"nope","category":"docs","pre_existing":false,"title":"t"},
			  {"path":"b.ts","line":2,"severity":"nope","category":"docs","pre_existing":false,"title":"t"}]}`,
			want: `2 finding(s) have a missing or out-of-range path, line, side, severity, category, pre_existing or title — first: {"path":"a.ts","line":1,"severity":"nope","category":"docs","pre_existing":false,"title":"t"}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validate.Findings([]byte(tc.payload))
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

// The alternative operator reads false as absent, so a finding whose `side` is
// the literal false takes the "RIGHT" default and passes (lib/validate.sh:47).
// Reproduced rather than fixed: this is a parity port.
func TestFindingsReadsFalseAsAbsentThroughTheAlternativeOperator(t *testing.T) {
	payload := `{"verdict":"converged","findings":[{"path":"a.ts","line":1,"side":false,
	  "severity":"high","category":"docs","pre_existing":false,"title":"t"}]}`
	if err := validate.Findings([]byte(payload)); err != nil {
		t.Fatalf("wanted the false side accepted the way jq accepts it, got %q", err)
	}
}

// `.path?` raises on a scalar and the `?` swallows it, so the whole `select`
// yields nothing and the element is never counted as bad (lib/validate.sh:44-58).
// A null element is different: `null.path` is null rather than an error.
func TestFindingsSkipsAnElementThatIsNotAnObject(t *testing.T) {
	for _, payload := range []string{
		`{"verdict":"converged","findings":[1,2]}`,
		`{"verdict":"converged","findings":["x"]}`,
		`{"verdict":"converged","findings":[true]}`,
		`{"verdict":"converged","findings":[[]]}`,
	} {
		if err := validate.Findings([]byte(payload)); err != nil {
			t.Errorf("%s: wanted acceptance, got %q", payload, err)
		}
	}

	err := validate.Findings([]byte(`{"verdict":"converged","findings":[null]}`))
	want := `1 finding(s) have a missing or out-of-range path, line, side, severity, category, pre_existing or title — first: null`
	if got := message(err); got != want {
		t.Fatalf("a null element:\n got %q\nwant %q", got, want)
	}
}
