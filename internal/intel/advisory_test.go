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

// fakeSearcher is the advisory Searcher with scripted answers: hits and caps
// per term, existence per path, and optional errors for either call.
type fakeSearcher struct {
	hits      map[string][]string
	tooCommon map[string]bool
	searchErr map[string]error
	exists    map[string]bool
	existErr  map[string]error
}

func (f *fakeSearcher) ExactSearch(_ context.Context, _ core.Revision, term string, _ int) ([]string, bool, error) {
	if err, ok := f.searchErr[term]; ok {
		return nil, false, err
	}
	return f.hits[term], f.tooCommon[term], nil
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
	scope, err := intel.RequiredFiles(context.Background(), changes, stubReader{bodies: bodies}, base, head, excluded)
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
		hits: map[string][]string{
			"SharedThing": {"docs/notes.md", "src/app.go", "unrelated.md", "vendor/lib.go"},
		},
		exists: map[string]bool{"src/app_test.go": true},
	}
	summary := intel.AdvisoryFiles(context.Background(), scope, search)

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
		hits: map[string][]string{
			"SharedThing": {"docs/capped.md"},
			"other":       {"docs/plain.md"},
		},
		tooCommon: map[string]bool{"SharedThing": true},
	}
	summary := intel.AdvisoryFiles(context.Background(), scope, search)

	for _, f := range summary.Files {
		if f.Path == "docs/capped.md" {
			t.Errorf("capped term contributed advisory file %q, want none", f.Path)
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

// TestAdvisorySearchErrorDegradesOneTerm scripts a failing term beside a good
// one and requires the good term to survive without failing discovery.
func TestAdvisorySearchErrorDegradesOneTerm(t *testing.T) {
	scope, _, _ := advisoryScope(t, advisoryBodies())
	search := &fakeSearcher{
		hits:      map[string][]string{"other": {"docs/plain.md"}},
		searchErr: map[string]error{"SharedThing": errors.New("index offline")},
	}
	summary := intel.AdvisoryFiles(context.Background(), scope, search)
	found := false
	for _, f := range summary.Files {
		if f.Path == "docs/plain.md" {
			found = true
		}
	}
	if !found {
		t.Errorf("failing term dropped the good term: files = %v", advisoryPaths(summary))
	}
	if len(summary.Limits) != 0 {
		t.Errorf("a search error recorded limits %v, want none", summary.Limits)
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
