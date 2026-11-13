package intel

import (
	"reflect"
	"testing"
)

func hit(path string, lines ...int) SearchHit { return SearchHit{Path: path, Lines: lines} }

func TestAnchorLineReadsOneBasedLines(t *testing.T) {
	body := []byte("first\nsecond line\r\nthird")
	cases := []struct {
		line int
		want string
	}{{1, "first"}, {2, "second line"}, {3, "third"}, {0, ""}, {4, ""}, {-1, ""}}
	for _, c := range cases {
		if got := string(AnchorLine(body, c.line)); got != c.want {
			t.Errorf("AnchorLine(%d) = %q, want %q", c.line, got, c.want)
		}
	}
}

func TestSiblingPointersIncludeTheFindingsOwnFile(t *testing.T) {
	results := []TermResult{{Term: "parseThing", Hits: []SearchHit{hit("a.go", 5, 40), hit("b.go", 7)}}}
	got := SiblingPointers([]string{"parseThing"}, results, "a.go", 5, MaxSiblings)
	want := []Sibling{
		{Path: "a.go", Line: 40, Term: "parseThing"},
		{Path: "b.go", Line: 7, Term: "parseThing"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestSiblingPointersExcludeTheAnchoredLine(t *testing.T) {
	results := []TermResult{{Term: "parseThing", Hits: []SearchHit{hit("a.go", 5)}}}
	if got := SiblingPointers([]string{"parseThing"}, results, "a.go", 5, MaxSiblings); len(got) != 0 {
		t.Fatalf("the anchored line listed itself: %v", got)
	}
	// Line 0 names no head line (a finding anchored on the removed side), so
	// nothing is excluded.
	if got := SiblingPointers([]string{"parseThing"}, results, "a.go", 0, MaxSiblings); len(got) != 1 {
		t.Fatalf("with no head line, got %v, want the one hit", got)
	}
}

func TestSiblingPointersRankByFewestHoldersThenTermPathLine(t *testing.T) {
	results := []TermResult{
		{Term: "common_name", Hits: []SearchHit{hit("a.go", 1), hit("b.go", 2), hit("c.go", 3)}},
		{Term: "rare_name", Hits: []SearchHit{hit("z.go", 9, 20)}},
		{Term: "mid_name", Hits: []SearchHit{hit("m.go", 4), hit("n.go", 6)}},
	}
	terms := []string{"common_name", "mid_name", "rare_name"}
	got := SiblingPointers(terms, results, "own.go", 1, MaxSiblings)
	want := []Sibling{
		{Path: "z.go", Line: 9, Term: "rare_name"},
		{Path: "z.go", Line: 20, Term: "rare_name"},
		{Path: "m.go", Line: 4, Term: "mid_name"},
		{Path: "n.go", Line: 6, Term: "mid_name"},
		{Path: "a.go", Line: 1, Term: "common_name"},
		{Path: "b.go", Line: 2, Term: "common_name"},
		{Path: "c.go", Line: 3, Term: "common_name"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v\nwant %v", got, want)
	}
}

func TestSiblingPointersListOneLocationOnceUnderItsRarestTerm(t *testing.T) {
	results := []TermResult{
		{Term: "common_name", Hits: []SearchHit{hit("a.go", 3), hit("b.go", 1)}},
		{Term: "rare_name", Hits: []SearchHit{hit("a.go", 3)}},
	}
	got := SiblingPointers([]string{"common_name", "rare_name"}, results, "own.go", 1, MaxSiblings)
	want := []Sibling{
		{Path: "a.go", Line: 3, Term: "rare_name"},
		{Path: "b.go", Line: 1, Term: "common_name"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestSiblingPointersSkipATooCommonTermAndAnyTermNotAsked(t *testing.T) {
	results := []TermResult{
		{Term: "everywhere", Hits: []SearchHit{hit("a.go", 1)}, TooCommon: true},
		{Term: "elsewhere", Hits: []SearchHit{hit("b.go", 2)}},
		{Term: "kept_name", Hits: []SearchHit{hit("c.go", 3)}},
	}
	got := SiblingPointers([]string{"everywhere", "kept_name"}, results, "own.go", 1, MaxSiblings)
	want := []Sibling{{Path: "c.go", Line: 3, Term: "kept_name"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestSiblingPointersStopAtTheLimit(t *testing.T) {
	var lines []int
	for i := 1; i <= 25; i++ {
		lines = append(lines, i)
	}
	results := []TermResult{{Term: "parseThing", Hits: []SearchHit{hit("a.go", lines...)}}}
	got := SiblingPointers([]string{"parseThing"}, results, "own.go", 1, MaxSiblings)
	if len(got) != MaxSiblings {
		t.Fatalf("got %d siblings, want the cap of %d", len(got), MaxSiblings)
	}
	if got[0].Line != 1 || got[MaxSiblings-1].Line != MaxSiblings {
		t.Fatalf("the cap kept the wrong end of the ranking: %v", got)
	}
}
