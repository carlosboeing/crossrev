package core_test

import (
	"reflect"
	"testing"

	"github.com/carlosboeing/crossrev/internal/core"
)

func TestMergeLineSpansFoldsContiguousAndOverlapping(t *testing.T) {
	got := core.MergeLineSpans([]core.LineSpan{{Start: 8, End: 10}, {Start: 1, End: 3}, {Start: 3, End: 5}, {Start: 20, End: 22}})
	want := []core.LineSpan{{Start: 1, End: 5}, {Start: 8, End: 10}, {Start: 20, End: 22}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("merged = %+v, want %+v", got, want)
	}
}

func TestMergeLineSpansDropsDegenerateAndEmptiesToNil(t *testing.T) {
	if got := core.MergeLineSpans(nil); got != nil {
		t.Fatalf("merged nil = %+v, want nil", got)
	}
	got := core.MergeLineSpans([]core.LineSpan{{Start: 5, End: 3}, {Start: 0, End: 0}, {Start: 2, End: 2}})
	want := []core.LineSpan{{Start: 2, End: 2}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("merged = %+v, want %+v", got, want)
	}
}

func TestSuppliedRangesCoverOneSpanOnly(t *testing.T) {
	ranges := core.SuppliedRanges{
		Base: []core.LineSpan{{Start: 1, End: 10}},
		Head: []core.LineSpan{{Start: 1, End: 3}, {Start: 8, End: 10}},
	}
	if !ranges.CoversBase(5, 6) {
		t.Error("base 5-6 inside 1-10 does not cover")
	}
	if ranges.CoversHead(5, 6) {
		t.Error("head 5-6 across the unseen gap covers")
	}
	if !ranges.CoversHead(8, 10) {
		t.Error("head 8-10 inside 8-10 does not cover")
	}
	if ranges.CoversHead(0, 1) || ranges.CoversBase(4, 3) {
		t.Error("a degenerate span covers")
	}
	empty := core.SuppliedRanges{}
	if empty.CoversHead(1, 1) || empty.CoversBase(1, 1) {
		t.Error("an empty range set covers")
	}
}

func TestSuppliedRangesUnionMergesBothSides(t *testing.T) {
	first := core.SuppliedRanges{
		Base: []core.LineSpan{{Start: 1, End: 3}},
		Head: []core.LineSpan{{Start: 1, End: 4}},
	}
	second := core.SuppliedRanges{
		Base: []core.LineSpan{{Start: 4, End: 6}},
		Head: []core.LineSpan{{Start: 50, End: 52}},
	}
	got := first.Union(second)
	want := core.SuppliedRanges{
		Base: []core.LineSpan{{Start: 1, End: 6}},
		Head: []core.LineSpan{{Start: 1, End: 4}, {Start: 50, End: 52}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("union = %+v, want %+v", got, want)
	}
}
