package resolve

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/prstate"
	"github.com/carlosboeing/crossrev/internal/vcs"
)

const (
	otherFinding = "bbbbbbbbbbbbbbbb"
	fixCommit    = "d00dfeedd00dfeedd00dfeedd00dfeedd00dfeed"
)

func finding(id, path string, line int, side, category string) string {
	return fmt.Sprintf(`{"id":%q,"path":%q,"line":%d,"side":%q,"severity":"high","category":%q,"pre_existing":false,"title":"t %s","why":"w","fix":"f"}`,
		id, path, line, side, category, id)
}

// priorFix lays down pass 1 as a finished review and a finished resolve that
// recorded resolution for oldID, and pass 2 as a finished review raising
// current. commit is the commit the resolve pass pushed; empty means none.
func (e *testEnv) priorFix(t *testing.T, old, current, resolution, commit string, oldID string) {
	t.Helper()
	e.addReviewPass(t, 1, json.RawMessage("["+old+"]"), "issues-remain", core.PassComplete)
	m := prstate.Marker{
		Leg:         core.LegResolve,
		Pass:        1,
		State:       core.PassComplete,
		Resolutions: json.RawMessage(fmt.Sprintf(`[{"finding_id":%q,"resolution":%q,"reply":"r"}]`, oldID, resolution)),
	}
	if commit != "" {
		m.CommitSHA = prstate.Some(commit)
	}
	e.addResolve(t, m, 9101)
	e.addReviewPass(t, 2, json.RawMessage("["+current+"]"), "issues-remain", core.PassComplete)
}

func (e *testEnv) promptText(t *testing.T) string {
	t.Helper()
	got := e.run(t)
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if len(e.adapter.invs) == 0 {
		t.Fatalf("the resolver never started: outcome %q, message %q", got.Outcome, got.Message)
	}
	return e.adapter.invs[0].Prompt.Text
}

func TestRecurrenceCandidateByFindingID(t *testing.T) {
	e := setup(t)
	e.priorFix(t,
		finding(testFinding, "app.ts", 2, "RIGHT", "correctness"),
		finding(testFinding, "app.ts", 40, "RIGHT", "correctness"),
		"fixed", fixCommit, testFinding)
	text := e.promptText(t)

	for _, want := range []string{"**Recurrence candidate.**", "pass 1", "`" + testFinding + "`", "`d00dfee`"} {
		if !strings.Contains(text, want) {
			t.Errorf("prompt is missing %q", want)
		}
	}
}

func TestRecurrenceCandidateByNearbyAnchor(t *testing.T) {
	cases := []struct {
		name string
		cur  string
		want bool
	}{
		{"ten lines away", finding(otherFinding, "app.ts", 12, "RIGHT", "correctness"), true},
		{"eleven lines away", finding(otherFinding, "app.ts", 13, "RIGHT", "correctness"), false},
		{"ten lines above", finding(otherFinding, "app.ts", 2, "RIGHT", "correctness"), true},
		{"other category", finding(otherFinding, "app.ts", 4, "RIGHT", "security"), false},
		{"other side", finding(otherFinding, "app.ts", 4, "LEFT", "correctness"), false},
		{"other path", finding(otherFinding, "lib.ts", 4, "RIGHT", "correctness"), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := setup(t)
			line := 2
			if c.name == "ten lines above" {
				line = 12
			}
			e.priorFix(t, finding(testFinding, "app.ts", line, "RIGHT", "correctness"), c.cur, "fixed", fixCommit, testFinding)
			text := e.promptText(t)
			if got := strings.Contains(text, "**Recurrence candidate.**"); got != c.want {
				t.Errorf("recurrence candidate = %v, want %v", got, c.want)
			}
		})
	}
}

func TestNoRecurrenceCandidateForAnUnpushedOrUnfixedEarlierPass(t *testing.T) {
	cases := []struct {
		name       string
		resolution string
		commit     string
	}{
		{"fixed with no pushed commit", "fixed", ""},
		{"pushed but skipped", "skipped", fixCommit},
		{"pushed but disputed", "disputed", fixCommit},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := setup(t)
			same := finding(testFinding, "app.ts", 2, "RIGHT", "correctness")
			e.priorFix(t, same, same, c.resolution, c.commit, testFinding)
			text := e.promptText(t)
			if strings.Contains(text, "**Recurrence candidate.**") {
				t.Error("a recurrence candidate was named without a pushed fix")
			}
		})
	}
}

func TestRecurrenceDoesNotEscalateAndTheSummaryCountsIt(t *testing.T) {
	e := setup(t)
	same := finding(testFinding, "app.ts", 2, "RIGHT", "correctness")
	e.priorFix(t, same, same, "fixed", fixCommit, testFinding)
	got := e.run(t)
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	for _, label := range e.forge.addedLabels {
		if label == "crossrev/stop" {
			t.Fatal("a recurrence candidate escalated the loop on its own")
		}
	}
	var summary string
	for _, edit := range e.forge.edits {
		if strings.Contains(edit.Body, "crossrev resolved") {
			summary = edit.Body
		}
	}
	if !strings.Contains(summary, "1 finding was a recurrence candidate") {
		t.Errorf("the pass summary does not count the recurrence candidate:\n%s", summary)
	}
}

func TestSummaryHasNoRecurrenceLineWithNoCandidate(t *testing.T) {
	e := setup(t)
	e.addReview(t, defaultFindings(), "issues-remain")
	if got := e.run(t); got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	for _, edit := range e.forge.edits {
		if strings.Contains(edit.Body, "recurrence candidate") {
			t.Errorf("a pass with no candidate mentioned one:\n%s", edit.Body)
		}
	}
}

// ---------------------------------------------------------------------------
// Sibling pointers
// ---------------------------------------------------------------------------

// SearchAll over the committed blobs the fake serves: every Show entry keyed
// "<sha>:<path>" for the searched revision, matched as a fixed string.
func (g *fakeGit) SearchAll(_ context.Context, revision core.Revision, terms []string, limit int) ([]vcs.TermResult, error) {
	if g.searchErr != nil {
		return nil, g.searchErr
	}
	g.searchCalls++
	prefix := revision.SHA() + ":"
	var paths []string
	for key := range g.show {
		if strings.HasPrefix(key, prefix) {
			paths = append(paths, strings.TrimPrefix(key, prefix))
		}
	}
	sort.Strings(paths)
	var out []vcs.TermResult
	for _, term := range terms {
		res := vcs.TermResult{Term: term}
		for _, p := range paths {
			var hit vcs.SearchHit
			for i, line := range strings.Split(string(g.show[prefix+p]), "\n") {
				if strings.Contains(line, term) {
					hit.Path = p
					hit.Lines = append(hit.Lines, i+1)
				}
			}
			if hit.Path != "" {
				res.Hits = append(res.Hits, hit)
			}
		}
		if len(res.Hits) > limit {
			res.Hits, res.TooCommon = res.Hits[:limit], true
		}
		out = append(out, res)
	}
	return out, nil
}

func (e *testEnv) headBlob(path, body string) {
	if e.git.show == nil {
		e.git.show = map[string][]byte{}
	}
	e.git.show[e.head.SHA()+":"+path] = []byte(body)
}

func siblingSection(text string) string {
	i := strings.Index(text, "- Sibling locations")
	if i < 0 {
		return ""
	}
	rest := text[i:]
	if j := strings.Index(rest, "\n\n"); j >= 0 {
		rest = rest[:j]
	}
	return rest
}

func TestSiblingsListSameFileHitsAndLeaveOutTheAnchoredLine(t *testing.T) {
	e := setup(t)
	e.addReview(t, defaultFindings(), "issues-remain")
	e.headBlob("app.ts", "x\nconst got = loadWidget(a)\nloadWidget(b)\n")
	e.headBlob("lib.ts", "export function loadWidget() {}\n")
	section := siblingSection(e.promptText(t))

	if section == "" {
		t.Fatal("no sibling block in the prompt")
	}
	for _, want := range []string{"`app.ts:3` (`loadWidget`)", "`lib.ts:1` (`loadWidget`)", "partial"} {
		if !strings.Contains(section, want) {
			t.Errorf("sibling block is missing %q:\n%s", want, section)
		}
	}
	if strings.Contains(section, "`app.ts:2`") {
		t.Errorf("the anchored line listed itself:\n%s", section)
	}
}

func TestSiblingsRankTheRarestIdentifierFirstAndStopAtTen(t *testing.T) {
	e := setup(t)
	e.addReview(t, defaultFindings(), "issues-remain")
	// Line 2 of app.ts holds both terms. rare_symbol sits in one other file,
	// shared_symbol in many.
	body := "x\nrare_symbol(shared_symbol)\n"
	for i := 0; i < 12; i++ {
		body += "shared_symbol()\n"
	}
	e.headBlob("app.ts", body)
	e.headBlob("z_rare.ts", "rare_symbol()\n")
	for i := 0; i < 4; i++ {
		e.headBlob(fmt.Sprintf("other%d.ts", i), "shared_symbol()\n")
	}
	section := siblingSection(e.promptText(t))

	entries := strings.Count(section, "\n  - ")
	if entries != 10 {
		t.Fatalf("got %d entries, want the cap of 10:\n%s", entries, section)
	}
	first := strings.Index(section, "`z_rare.ts:1` (`rare_symbol`)")
	if first < 0 {
		t.Fatalf("the rarest identifier's location is missing:\n%s", section)
	}
	if shared := strings.Index(section, "(`shared_symbol`)"); shared < first {
		t.Errorf("a common identifier ranked ahead of the rare one:\n%s", section)
	}
}

func TestSiblingsAreOneBlobPassAndNeverBlockThePass(t *testing.T) {
	e := setup(t)
	e.addReview(t, json.RawMessage(`[`+
		finding(testFinding, "app.ts", 2, "RIGHT", "correctness")+`,`+
		finding(otherFinding, "app.ts", 3, "RIGHT", "testing")+`]`), "issues-remain")
	e.headBlob("app.ts", "x\nloadWidget()\nrenderWidget()\n")
	e.adapter.payloads = []json.RawMessage{json.RawMessage(`{"blocked":false,"blocked_reason":null,"summary":"Done.","commit_subject":null,"resolutions":[` +
		`{"finding_number":1,"resolution":"skipped","reply":"a","persist":null,"duplicate_of":null},` +
		`{"finding_number":2,"resolution":"skipped","reply":"b","persist":null,"duplicate_of":null}]}`)}
	_ = e.promptText(t)
	if e.git.searchCalls != 1 {
		t.Errorf("blob passes = %d, want one pass for both findings", e.git.searchCalls)
	}

	failing := setup(t)
	failing.addReview(t, defaultFindings(), "issues-remain")
	failing.headBlob("app.ts", "x\nloadWidget()\nloadWidget()\n")
	failing.git.searchErr = fmt.Errorf("git cat-file died")
	text := failing.promptText(t)
	if strings.Contains(text, "- Sibling locations") {
		t.Error("a failed search still printed a sibling block")
	}
}

func TestSiblingsReadAnAnchorOnTheRemovedSideFromTheBase(t *testing.T) {
	e := setup(t)
	e.addReview(t, json.RawMessage(`[`+finding(testFinding, "app.ts", 2, "LEFT", "correctness")+`]`), "issues-remain")
	e.git.show = map[string][]byte{
		e.base.SHA() + ":app.ts":       []byte("x\noldHelper(a)\n"),
		e.head.SHA() + ":app.ts":       []byte("x\nnewCode()\nlater oldHelper(b)\n"),
		e.head.SHA() + ":elsewhere.ts": []byte("oldHelper()\n"),
	}
	section := siblingSection(e.promptText(t))
	for _, want := range []string{"`app.ts:3` (`oldHelper`)", "`elsewhere.ts:1` (`oldHelper`)"} {
		if !strings.Contains(section, want) {
			t.Errorf("sibling block is missing %q:\n%s", want, section)
		}
	}
}

func TestSiblingsShrinkBeforeTheHardLimit(t *testing.T) {
	e := setup(t)
	hard, ok := mustHarness(t).InputBudget(mustHarness(t).DefaultPairing().Resolver, "")
	if !ok {
		t.Fatal("no input budget for the default resolver")
	}
	body := "x\nrare_symbol()\n"
	for i := 0; i < 30; i++ {
		body += "rare_symbol()\n"
	}
	e.headBlob("app.ts", body)

	// Pad the diff, which the prompt carries and no marker does,
	// so the sibling-free prompt fits and the full one does not.
	bare := len(func() string {
		w := setup(t)
		w.addReview(t, defaultFindings(), "issues-remain")
		return w.promptText(t)
	}())
	e.addReview(t, defaultFindings(), "issues-remain")
	e.forge.diff = []byte("diff --git a/pad.txt b/pad.txt\n--- a/pad.txt\n+++ b/pad.txt\n@@ -0,0 +1 @@\n+" + strings.Repeat("a", hard.HardBytes-bare-400) + "\n")

	got := e.run(t)
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	if got.Message == "resolve_prompt_exceeds_limit" || len(e.adapter.invs) == 0 {
		t.Fatalf("siblings pushed the prompt past the limit instead of shrinking: %q", got.Message)
	}
	text := e.adapter.invs[0].Prompt.Text
	if len(text) > hard.HardBytes {
		t.Fatalf("prompt is %d bytes, over the %d hard limit", len(text), hard.HardBytes)
	}
	if n := strings.Count(siblingSection(text), "\n  - "); n >= 10 {
		t.Errorf("sibling list kept %d entries with room for fewer", n)
	}
}
