package intel_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/intel"
)

// fakeSearcher is the advisory Searcher with scripted answers: one blob-pass
// result per term, existence per path, and an optional whole-call search
// error. It records the terms and limit each SearchAll call carried, so a
// test can pin that discovery searched the changed lines rather than the
// whole bodies.
type fakeSearcher struct {
	results   map[string]intel.TermResult
	searchErr error
	exists    map[string]bool
	existErr  map[string]error
	calls     int
	gotTerms  []string
	gotLimit  int
}

func (f *fakeSearcher) SearchAll(_ context.Context, _ core.Revision, terms []string, limit int) ([]intel.TermResult, error) {
	f.calls++
	f.gotTerms = append([]string(nil), terms...)
	f.gotLimit = limit
	if f.searchErr != nil {
		return nil, f.searchErr
	}
	var out []intel.TermResult
	for _, term := range terms {
		if res, ok := f.results[term]; ok {
			res.Term = term
			out = append(out, res)
		} else {
			out = append(out, intel.TermResult{Term: term})
		}
	}
	return out, nil
}

func (f *fakeSearcher) Exists(_ context.Context, _ core.Revision, path string) (bool, error) {
	if err, ok := f.existErr[path]; ok {
		return false, err
	}
	return f.exists[path], nil
}

// advisoryScope builds a two-file required scope through RequiredFiles so the
// test starts from the same shape production code consumes: src/app.go holds
// the shared identifier, src/other.go holds nothing searchable, and the vendor
// tree is visibly excluded.
func advisoryScope(t *testing.T, bodies map[string]map[string]oracleCase) (intel.Scope, core.Revision, core.Revision) {
	t.Helper()
	base, head := stubRevisions(t)
	changes := []core.FileChange{
		{Path: "src/app.go", Kind: core.ChangeModified},
		{Path: "src/other.go", Kind: core.ChangeModified},
		{Path: "vendor/lib.go", Kind: core.ChangeModified},
	}
	excluded := []intel.Exclusion{{Path: "vendor", Reason: "vendored code"}}
	scope, err := intel.RequiredFiles(context.Background(), changes, stubReader{bodies: bodies}, base, head, excluded, nil)
	if err != nil {
		t.Fatalf("RequiredFiles: %v", err)
	}
	return scope, base, head
}

func advisoryBodies() map[string]map[string]oracleCase {
	return map[string]map[string]oracleCase{
		stubBaseSHA: {},
		stubHeadSHA: {
			"src/app.go":   {Path: "src/app.go", Body: "package app\n\nfunc SharedThing() {}\n", Available: true},
			"src/other.go": {Path: "src/other.go", Body: "package other\n", Available: true},
		},
	}
}

// advisoryDiff is the -U0 change behind advisoryScope: src/app.go swaps one
// identifier line, src/other.go gains its package line. The searched terms
// are these lines' identifiers plus the two required paths — never the whole
// bodies above.
func advisoryDiff() []byte {
	return []byte("diff --git a/src/app.go b/src/app.go\n" +
		"--- a/src/app.go\n" +
		"+++ b/src/app.go\n" +
		"@@ -2 +2 @@\n" +
		"-func OldThing() {}\n" +
		"+func SharedThing() {}\n" +
		"diff --git a/src/other.go b/src/other.go\n" +
		"--- a/src/other.go\n" +
		"+++ b/src/other.go\n" +
		"@@ -0,0 +1 @@\n" +
		"+package other\n")
}

// TestAdvisoryDiscoveryNeverChangesTheRequiredSet runs discovery over a scope
// whose search hits name a required path, an excluded path and two untouched
// paths, and requires the required set to come back identical while only the
// untouched paths turn advisory.
func TestAdvisoryDiscoveryNeverChangesTheRequiredSet(t *testing.T) {
	scope, _, head := advisoryScope(t, advisoryBodies())
	_ = head
	beforeRequired := append([]intel.FileUnit(nil), scope.Required...)
	beforeExcluded := append([]intel.Exclusion(nil), scope.Excluded...)

	search := &fakeSearcher{
		results: map[string]intel.TermResult{
			"SharedThing": {Hits: []intel.SearchHit{
				{Path: "docs/notes.md", Lines: []int{2}},
				{Path: "src/app.go", Lines: []int{1}},
				{Path: "unrelated.md", Lines: []int{9}},
				{Path: "vendor/lib.go", Lines: []int{1}},
			}},
		},
		exists: map[string]bool{"src/app_test.go": true},
	}
	summary := intel.AdvisoryFiles(context.Background(), scope, advisoryDiff(), search)

	if !reflect.DeepEqual(scope.Required, beforeRequired) {
		t.Errorf("AdvisoryFiles changed the required set: was %v, now %v", pathsOf(beforeRequired), pathsOf(scope.Required))
	}
	if !reflect.DeepEqual(scope.Excluded, beforeExcluded) {
		t.Errorf("AdvisoryFiles changed the exclusions: was %v, now %v", beforeExcluded, scope.Excluded)
	}
	if len(scope.Required) != 2 {
		t.Fatalf("required set holds %d units, want 2", len(scope.Required))
	}
	got := map[string]intel.AdvisoryFile{}
	for _, f := range summary.Files {
		got[f.Path] = f
	}
	for _, want := range []string{"docs/notes.md", "unrelated.md", "src/app_test.go"} {
		if _, ok := got[want]; !ok {
			t.Errorf("advisory files = %v, want %q among them", advisoryPaths(summary), want)
		}
	}
	for _, forbidden := range []string{"src/app.go", "src/other.go", "vendor/lib.go"} {
		if _, ok := got[forbidden]; ok {
			t.Errorf("advisory files contain required or excluded path %q: %v", forbidden, advisoryPaths(summary))
		}
	}
	if summary.Count != len(summary.Files) {
		t.Errorf("advisory count = %d, want the %d files listed", summary.Count, len(summary.Files))
	}
	if search.calls != 1 {
		t.Errorf("SearchAll calls = %d, want 1 (one blob pass answers every term)", search.calls)
	}
}

func pathsOf(units []intel.FileUnit) []string {
	var out []string
	for _, u := range units {
		out = append(out, u.Path)
	}
	return out
}

func advisoryPaths(summary intel.AdvisorySummary) []string {
	var out []string
	for _, f := range summary.Files {
		out = append(out, f.Path)
	}
	return out
}

// TestAdvisoryTooCommonContributesNoUnits scripts a capped term and requires
// the visible too_common limit with no advisory file from that term, while an
// ordinary term beside it still contributes.
func TestAdvisoryTooCommonContributesNoUnits(t *testing.T) {
	scope, _, _ := advisoryScope(t, advisoryBodies())
	search := &fakeSearcher{
		results: map[string]intel.TermResult{
			"SharedThing": {
				Hits:      []intel.SearchHit{{Path: "docs/capped.md", Lines: []int{1}}},
				TooCommon: true,
			},
			"other": {Hits: []intel.SearchHit{{Path: "docs/plain.md", Lines: []int{4}}}},
		},
	}
	summary := intel.AdvisoryFiles(context.Background(), scope, advisoryDiff(), search)

	for _, f := range summary.Files {
		if f.Path == "docs/capped.md" || f.Term == "SharedThing" {
			t.Errorf("capped term contributed advisory file %+v, want none", f)
		}
	}
	if len(summary.Limits) != 1 {
		t.Fatalf("advisory limits = %v, want one too_common entry", summary.Limits)
	}
	limit := summary.Limits[0]
	if limit.Reason != "too_common" {
		t.Errorf("limit reason = %q, want too_common", limit.Reason)
	}
	if limit.Limit != 200 {
		t.Errorf("limit = %d, want 200", limit.Limit)
	}
	if limit.Rule != "search:SharedThing" {
		t.Errorf("limit rule = %q, want search:SharedThing", limit.Rule)
	}
	found := false
	for _, f := range summary.Files {
		if f.Path == "docs/plain.md" {
			found = true
		}
	}
	if !found {
		t.Errorf("ordinary term contributed nothing: files = %v", advisoryPaths(summary))
	}
}

// TestAdvisorySearchErrorDropsSearchKeepsConvention scripts a failing blob
// pass and requires discovery to survive it: no search file and no search
// limit, while the convention rule beside it still contributes.
func TestAdvisorySearchErrorDropsSearchKeepsConvention(t *testing.T) {
	scope, _, _ := advisoryScope(t, advisoryBodies())
	search := &fakeSearcher{
		searchErr: errors.New("index offline"),
		exists:    map[string]bool{"src/app_test.go": true},
	}
	summary := intel.AdvisoryFiles(context.Background(), scope, advisoryDiff(), search)
	for _, f := range summary.Files {
		if f.Rule == intel.AdvisoryRuleSearch {
			t.Errorf("failing search contributed advisory file %+v, want none", f)
		}
	}
	found := false
	for _, f := range summary.Files {
		if f.Path == "src/app_test.go" {
			found = true
		}
	}
	if !found {
		t.Errorf("failing search dropped the convention rule: files = %v", advisoryPaths(summary))
	}
	if len(summary.Limits) != 0 {
		t.Errorf("a search error recorded limits %v, want none", summary.Limits)
	}
}

// TestAdvisorySearchesChangedLinesNotWholeBodies is the fixture where the old
// whole-body terms would page noise: the required body holds a noisy
// identifier the changed lines never touch. Discovery must search the
// changed line's identifier plus the changed paths, and never the body-only
// token — so its holders stay unpaged whatever they hold.
func TestAdvisorySearchesChangedLinesNotWholeBodies(t *testing.T) {
	bodies := map[string]map[string]oracleCase{
		stubBaseSHA: {},
		stubHeadSHA: {
			"src/app.go":   {Path: "src/app.go", Body: "package app\n\nconst NoisyWholeBodyToken = 1\nfunc RareChangedToken() {}\n", Available: true},
			"src/other.go": {Path: "src/other.go", Body: "package other\n", Available: true},
		},
	}
	scope, _, _ := advisoryScope(t, bodies)
	diff := []byte("diff --git a/src/app.go b/src/app.go\n" +
		"--- a/src/app.go\n" +
		"+++ b/src/app.go\n" +
		"@@ -3 +3 @@\n" +
		"-func OldThing() {}\n" +
		"+func RareChangedToken() {}\n")
	search := &fakeSearcher{
		results: map[string]intel.TermResult{
			"RareChangedToken": {Hits: []intel.SearchHit{{Path: "docs/rare.md", Lines: []int{5}}}},
		},
	}
	summary := intel.AdvisoryFiles(context.Background(), scope, diff, search)

	wantTerms := []string{"OldThing", "RareChangedToken", "func", "src/app.go", "src/other.go"}
	sort.Strings(wantTerms)
	if !reflect.DeepEqual(search.gotTerms, wantTerms) {
		t.Errorf("searched terms = %q, want the changed lines' identifiers plus the changed paths %q", search.gotTerms, wantTerms)
	}
	for _, term := range search.gotTerms {
		if term == "NoisyWholeBodyToken" {
			t.Errorf("searched terms include the body-only token %q, which the changed lines never touch", term)
		}
	}
	for _, f := range summary.Files {
		if f.Term == "NoisyWholeBodyToken" {
			t.Errorf("body-only token contributed advisory file %+v, want none", f)
		}
	}
	found := false
	for _, f := range summary.Files {
		if f.Path == "docs/rare.md" && f.Line == 5 {
			found = true
		}
	}
	if !found {
		t.Errorf("changed-line term contributed nothing: files = %+v", summary.Files)
	}
}

// TestAdvisoryConventionSurvivesAnotherTermsSearchHit requires the
// convention entry to survive a search hit on the same path: batch A owns
// the hitting term while batch B owns the adjacent source file, so batch B
// must still render its own adjacent test. A batch owning both renders the
// search pointer once, never both entries for one path.
func TestAdvisoryConventionSurvivesAnotherTermsSearchHit(t *testing.T) {
	scope, _, _ := advisoryScope(t, advisoryBodies())
	diff := advisoryDiff()
	search := &fakeSearcher{
		results: map[string]intel.TermResult{
			"SharedThing": {Hits: []intel.SearchHit{{Path: "src/other_test.go", Lines: []int{5}}}},
		},
		exists: map[string]bool{"src/other_test.go": true},
	}
	summary := intel.AdvisoryFiles(context.Background(), scope, diff, search)

	perFile := intel.FileChangedTerms(diff, []core.FileChange{
		{Path: "src/app.go", Kind: core.ChangeModified},
		{Path: "src/other.go", Kind: core.ChangeModified},
	})
	pointers, _ := intel.BatchPointers(summary, perFile["src/other.go"], []string{"src/other.go"})
	found := false
	for _, p := range pointers {
		if p.Path == "src/other_test.go" {
			found = true
		}
	}
	if !found {
		t.Errorf("batch pointers for src/other.go = %+v, want the src/other_test.go convention neighbour", pointers)
	}

	own, _ := intel.BatchPointers(summary, perFile["src/app.go"], []string{"src/app.go"})
	held := 0
	for _, p := range own {
		if p.Path == "src/other_test.go" {
			held++
			if p.Rule != intel.AdvisoryRuleSearch {
				t.Errorf("shared pointer rule = %q, want the search entry to win", p.Rule)
			}
		}
	}
	if held != 1 {
		t.Errorf("batch pointers for src/app.go hold src/other_test.go %d times, want once", held)
	}
}

// TestAdvisorySearchHitsCarryLineNumbers requires one advisory file per
// holder line: a hit on lines 3 and 7 renders two pointers, not one.
func TestAdvisorySearchHitsCarryLineNumbers(t *testing.T) {
	scope, _, _ := advisoryScope(t, advisoryBodies())
	diff := []byte("diff --git a/src/app.go b/src/app.go\n" +
		"--- a/src/app.go\n" +
		"+++ b/src/app.go\n" +
		"@@ -0,0 +1 @@\n" +
		"+// LoneMarkerAlpha\n")
	search := &fakeSearcher{
		results: map[string]intel.TermResult{
			"LoneMarkerAlpha": {Hits: []intel.SearchHit{{Path: "docs/lines.md", Lines: []int{3, 7}}}},
		},
	}
	summary := intel.AdvisoryFiles(context.Background(), scope, diff, search)

	var lines []int
	for _, f := range summary.Files {
		if f.Path == "docs/lines.md" {
			if f.Term != "LoneMarkerAlpha" {
				t.Errorf("holder term = %q, want LoneMarkerAlpha", f.Term)
			}
			lines = append(lines, f.Line)
		}
	}
	if !reflect.DeepEqual(lines, []int{3, 7}) {
		t.Errorf("holder lines = %v, want [3 7] (one pointer per holder line)", lines)
	}
}

// TestAdvisoryChangedTermsWalkMinusUZeroLines checks the term walk over -U0
// shapes: added and removed lines contribute identifiers, headers and context
// never do, and every changed path joins verbatim.
func TestAdvisoryChangedTermsWalkMinusUZeroLines(t *testing.T) {
	changes := []core.FileChange{
		{Path: "src/app.go", Kind: core.ChangeModified},
		{Path: "src/new.go", Kind: core.ChangeAdded},
		{Path: "src/gone.go", OldPath: "src/gone.go", Kind: core.ChangeDeleted},
		{Path: "src/renamed.go", OldPath: "src/oldname.go", Kind: core.ChangeRenamed},
		{Path: "sp ace.go", Kind: core.ChangeModified},
	}
	diff := []byte("diff --git a/src/app.go b/src/app.go\n" +
		"index 1111111..2222222 100644\n" +
		"--- a/src/app.go\n" +
		"+++ b/src/app.go\n" +
		"@@ -1 +1 @@\n" +
		"-func OldThing() {}\n" +
		"+func SharedThing() {}\n" +
		"diff --git a/src/new.go b/src/new.go\n" +
		"new file mode 100644\n" +
		"--- /dev/null\n" +
		"+++ b/src/new.go\n" +
		"@@ -0,0 +1 @@\n" +
		"+package fresh\n" +
		"diff --git a/src/gone.go b/src/gone.go\n" +
		"deleted file mode 100644\n" +
		"--- a/src/gone.go\n" +
		"+++ /dev/null\n" +
		"@@ -1 +0,0 @@\n" +
		"-package doomed\n" +
		"diff --git a/src/oldname.go b/src/renamed.go\n" +
		"similarity index 90%\n" +
		"rename from src/oldname.go\n" +
		"rename to src/renamed.go\n" +
		"--- a/src/oldname.go\n" +
		"+++ b/src/renamed.go\n" +
		"@@ -1 +1 @@\n" +
		"-package stale\n" +
		"+package moved\n" +
		"diff --git \"a/sp ace.go\" \"b/sp ace.go\"\n" +
		"--- \"a/sp ace.go\"\n" +
		"+++ \"b/sp ace.go\"\n" +
		"@@ -1 +1 @@\n" +
		"-package cramped\n" +
		"+package roomy\n")

	perFile := intel.FileChangedTerms(diff, changes)
	wantPerFile := map[string][]string{
		"src/app.go":   {"OldThing", "SharedThing", "func", "src/app.go"},
		"src/new.go":   {"fresh", "package", "src/new.go"},
		"src/gone.go":  {"doomed", "package", "src/gone.go"},
		"src/renamed.go": {"moved", "package", "src/oldname.go", "src/renamed.go", "stale"},
		"sp ace.go":    {"cramped", "package", "roomy", "sp ace.go"},
	}
	for path, want := range wantPerFile {
		sort.Strings(want)
		if got := perFile[path]; !reflect.DeepEqual(got, want) {
			t.Errorf("FileChangedTerms[%q] = %q, want %q", path, got, want)
		}
	}
	if len(perFile) != len(wantPerFile) {
		t.Errorf("FileChangedTerms holds %d paths, want %d", len(perFile), len(wantPerFile))
	}

	global := intel.ChangedTerms(diff, changes)
	for _, term := range []string{"SharedThing", "fresh", "doomed", "moved", "roomy", "src/oldname.go", "sp ace.go"} {
		if !sort.StringsAreSorted(global) {
			t.Fatal("ChangedTerms is not sorted")
		}
		if idx := sort.SearchStrings(global, term); idx >= len(global) || global[idx] != term {
			t.Errorf("ChangedTerms = %q, want %q among the terms", global, term)
		}
	}
	for _, header := range []string{"diff", "index", "rename", "similarity", "No", "newline"} {
		if idx := sort.SearchStrings(global, header); idx < len(global) && global[idx] == header {
			t.Errorf("ChangedTerms = %q, want no header word %q", global, header)
		}
	}
}

// TestAdvisoryChangedTermsKeepRepeatedPrefixContentLines requires content
// lines starting with -- or ++ to contribute identifiers: git emits them as
// --- and +++ records, which are content inside a hunk, not file headers.
func TestAdvisoryChangedTermsKeepRepeatedPrefixContentLines(t *testing.T) {
	changes := []core.FileChange{
		{Path: "notes.txt", Kind: core.ChangeModified},
	}
	diff := []byte("diff --git a/notes.txt b/notes.txt\n" +
		"--- a/notes.txt\n" +
		"+++ b/notes.txt\n" +
		"@@ -1,2 +1,2 @@\n" +
		"--- OldMarker stays\n" +
		"-plain gone\n" +
		"+++ NewMarker arrives\n" +
		"+plain added\n")
	perFile := intel.FileChangedTerms(diff, changes)
	got := perFile["notes.txt"]
	for _, want := range []string{"OldMarker", "NewMarker"} {
		if idx := sort.SearchStrings(got, want); idx >= len(got) || got[idx] != want {
			t.Errorf("FileChangedTerms[notes.txt] = %q, want %q among the terms", got, want)
		}
	}
}

// TestAdvisoryChangedTermsIgnoreBinaryAndSubprojectSections requires two -U0
// shapes to contribute their paths alone: a binary pair with no content
// lines, and a gitlink whose Subproject lines are not code.
func TestAdvisoryChangedTermsIgnoreBinaryAndSubprojectSections(t *testing.T) {
	changes := []core.FileChange{
		{Path: "assets/logo.png", Kind: core.ChangeModified},
		{Path: "vendor/dep", Kind: core.ChangeModified},
	}
	diff := []byte("diff --git a/assets/logo.png b/assets/logo.png\n" +
		"index 1111111..2222222 100644\n" +
		"Binary files a/assets/logo.png and b/assets/logo.png differ\n" +
		"diff --git a/vendor/dep b/vendor/dep\n" +
		"--- a/vendor/dep\n" +
		"+++ b/vendor/dep\n" +
		"@@ -1 +1 @@\n" +
		"-Subproject commit 1111111111111111111111111111111111111111\n" +
		"+Subproject commit 2222222222222222222222222222222222222222\n")
	perFile := intel.FileChangedTerms(diff, changes)
	if got := perFile["assets/logo.png"]; !reflect.DeepEqual(got, []string{"assets/logo.png"}) {
		t.Errorf("binary terms = %q, want the path alone", got)
	}
	if got := perFile["vendor/dep"]; !reflect.DeepEqual(got, []string{"vendor/dep"}) {
		t.Errorf("gitlink terms = %q, want the path alone", got)
	}
}

// TestAdvisoryPointersRankFewestHoldersCapFiftyAtTheCall requires the
// per-call rendering: pointers from the call's own changed terms, rarest
// term first, fifty lines at most with the rest counted — and convention
// files only when they neighbour the call's own paths.
func TestAdvisoryPointersRankFewestHoldersCapFiftyAtTheCall(t *testing.T) {
	files := []intel.AdvisoryFile{{Path: "r.md", Rule: intel.AdvisoryRuleSearch, Term: "rareTerm", Line: 1}}
	for i := 0; i < 60; i++ {
		files = append(files, intel.AdvisoryFile{
			Path: string(rune('a'+i/26)) + string(rune('a'+i%26)) + ".md",
			Rule: intel.AdvisoryRuleSearch, Term: "busyTerm", Line: 1,
		})
	}
	files = append(files,
		intel.AdvisoryFile{Path: "src/app_test.go", Rule: intel.AdvisoryRuleConvention},
		intel.AdvisoryFile{Path: "zzz/lone_test.go", Rule: intel.AdvisoryRuleConvention},
	)
	summary := intel.AdvisorySummary{Files: files, Count: len(files), Rules: []string{"convention", "search"}}

	pointers, omitted := intel.BatchPointers(summary, []string{"rareTerm", "busyTerm"}, []string{"src/app.go"})
	if len(pointers) != 50 {
		t.Fatalf("pointers = %d lines, want the 50-line cap", len(pointers))
	}
	if omitted != 12 {
		t.Errorf("omitted = %d, want 12 (11 busy holders plus the neighbouring convention file)", omitted)
	}
	if pointers[0].Term != "rareTerm" {
		t.Errorf("first pointer term = %q, want rareTerm (fewest holders first)", pointers[0].Term)
	}
	for _, p := range pointers {
		if p.Path == "zzz/lone_test.go" {
			t.Errorf("pointers neighbour %q, which is adjacent to no batch path", p.Path)
		}
		if p.Path == "src/app_test.go" {
			t.Errorf("pointers hold the convention file %q inside the 50-line search cap", p.Path)
		}
	}

	// A quiet call renders every pointer with nothing omitted, convention
	// neighbours included.
	quiet, omitted := intel.BatchPointers(summary, []string{"rareTerm"}, []string{"src/app.go"})
	if omitted != 0 {
		t.Errorf("quiet omitted = %d, want 0", omitted)
	}
	if len(quiet) != 2 {
		t.Fatalf("quiet pointers = %d, want the rare pointer plus its convention neighbour", len(quiet))
	}
	if quiet[0].Term != "rareTerm" || quiet[1].Path != "src/app_test.go" {
		t.Errorf("quiet pointers = %+v, want search first and convention after", quiet)
	}
}

// TestSearchTermsExtractsIdentifiers checks the language-neutral identifier
// set, including the four-byte floor, digit-leading splits and ASCII-only
// runs. The return-common-word case is the hard negative: shared keywords are
// still terms, and stay advisory rather than required.
func TestSearchTermsExtractsIdentifiers(t *testing.T) {
	vectors := []struct {
		name string
		body string
		want []string
	}{
		{"four byte floor keeps func", "func ab Foo Bar Qux\n", []string{"func"}},
		{"drops short terms", "ab := Foo(2fast, _x1)\n", []string{"fast"}},
		{"dedupes and sorts", "zebra apple zebra mango\n", []string{"apple", "mango", "zebra"}},
		{"non ascii breaks runs", "caf\u00e9 au_lait return\n", []string{"au_lait", "return"}},
		{"common words stay terms", "return return\n", []string{"return"}},
		{"empty body has no terms", "", nil},
	}
	for _, v := range vectors {
		t.Run(v.name, func(t *testing.T) {
			if got := intel.SearchTerms([]byte(v.body)); !reflect.DeepEqual(got, v.want) {
				t.Errorf("SearchTerms(%q) = %q, want %q", v.body, got, v.want)
			}
		})
	}
}

// TestAdjacentTestCandidatesUsesTheClosedTable checks every row of the
// convention table and one extensionless path.
func TestAdjacentTestCandidatesUsesTheClosedTable(t *testing.T) {
	vectors := []struct {
		path string
		want []string
	}{
		{"src/store.go", []string{"src/store.spec.go", "src/store.test.go", "src/store_test.go", "src/test_store.go", "test/src/store.go", "tests/src/store.go"}},
		{"src/app.js", []string{"src/app.spec.js", "src/app.test.js", "src/app_test.js", "src/test_app.js", "test/src/app.js", "tests/src/app.js"}},
		{"Makefile", []string{"Makefile_test", "test/Makefile", "test_Makefile", "tests/Makefile"}},
	}
	for _, v := range vectors {
		t.Run(v.path, func(t *testing.T) {
			if got := intel.AdjacentTestCandidates(v.path); !reflect.DeepEqual(got, v.want) {
				t.Errorf("AdjacentTestCandidates(%q) = %q, want %q", v.path, got, v.want)
			}
		})
	}
}

// TestAdvisoryMatchesTheFrozenFixture replays the frozen term and convention
// vectors and requires the production functions to return them exactly.
func TestAdvisoryMatchesTheFrozenFixture(t *testing.T) {
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("no go.mod above the test directory")
		}
		dir = parent
	}
	raw, err := os.ReadFile(filepath.Join(dir, "tests", "fixtures", "intelligence", "file-units.json"))
	if err != nil {
		t.Fatalf("read the frozen oracle: %v", err)
	}
	var fixture struct {
		Advisory struct {
			MaxSearchHits int `json:"max_search_hits"`
			TermVectors   []struct {
				Body  string   `json:"body"`
				Terms []string `json:"terms"`
			} `json:"term_vectors"`
			Conventions []struct {
				Path       string   `json:"path"`
				Candidates []string `json:"candidates"`
			} `json:"conventions"`
		} `json:"advisory"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatalf("decode the frozen oracle: %v", err)
	}
	if intel.MaxSearchHits != fixture.Advisory.MaxSearchHits {
		t.Errorf("MaxSearchHits = %d, want frozen %d", intel.MaxSearchHits, fixture.Advisory.MaxSearchHits)
	}
	for _, v := range fixture.Advisory.TermVectors {
		if got := intel.SearchTerms([]byte(v.Body)); !reflect.DeepEqual(got, v.Terms) {
			t.Errorf("SearchTerms(%q) = %q, want frozen %q", v.Body, got, v.Terms)
		}
	}
	for _, v := range fixture.Advisory.Conventions {
		if got := intel.AdjacentTestCandidates(v.Path); !reflect.DeepEqual(got, v.Candidates) {
			t.Errorf("AdjacentTestCandidates(%q) = %q, want frozen %q", v.Path, got, v.Candidates)
		}
	}
	if !sort.StringsAreSorted([]string{"convention", "search"}) {
		t.Error("fixture check needs sorted rule names")
	}
}
