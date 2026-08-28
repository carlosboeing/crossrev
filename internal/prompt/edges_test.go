package prompt_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/prompt"
)

// The cases here are the ones the frozen oracles and the tables beside them do
// not reach. Each was found by mutating the code and watching every existing
// test still pass, so each names the mutation it refuses.
//
// Where a case has no parity vector behind it, the comment says which line of
// lib/prompt.sh it was measured against and how.

// tr's second range is `\013-\037`, and 0x1f is its upper bound. Every existing
// case used ESC (0x1b), which sits well inside it, so moving the bound one down
// changed nothing.
func TestQuoteBlockFlattensBothEndsOfEachControlRange(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want string
	}{
		{"the first range's floor, NUL", "a\x00b", "    a b"},
		{"the first range's ceiling, TAB", "a\x09b", "    a b"},
		{"the second range's floor, VT", "a\x0bb", "    a b"},
		{"the second range's ceiling, 0x1f", "a\x1fb", "    a b"},
		{"DEL, which is its own range", "a\x7fb", "    a b"},
		{"0x20 is not a control character", "a b", "    a b"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := string(prompt.QuoteBlock([]byte(tc.in))); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// The shell is `printf '%s' "$1" | tr … | sed 's/^/    /'` inside a command
// substitution, and sed writes no line for the text's own trailing newline
// while the substitution strips the one sed appends. Measured by running that
// pipeline on each input rather than read off the sed manual.
//
// Unreachable through today's two callers, which both trim first. It is an
// exported function, and the next caller is not bound by what these two do.
func TestQuoteBlockDoesNotInventALineForATrailingNewline(t *testing.T) {
	for _, tc := range []struct {
		in, want string
	}{
		{"a\n", "    a"},
		{"a\nb\n", "    a\n    b"},
		{"a\n\n", "    a\n    "},
		{"a\n\nb\n\n", "    a\n    \n    b\n    "},
		{"\n", "    "},
		{"a", "    a"},
	} {
		if got := string(prompt.QuoteBlock([]byte(tc.in))); got != tc.want {
			t.Errorf("QuoteBlock(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// A skill file that is nothing but the fence deletes to nothing, with or
// without a terminating newline.
func TestSkillBodyOnAFileThatIsOnlyTheFence(t *testing.T) {
	for _, in := range []string{"---", "---\n"} {
		if got := prompt.SkillBody([]byte(in)); len(got) != 0 {
			t.Errorf("SkillBody(%q) = %q, want nothing", in, got)
		}
	}
}

// The rubric is what the leg is judged against, and it is process-wide state.
// Writing through what SkillBody returned used to corrupt the embedded copy for
// every later prompt in the same process.
func TestTheEmbeddedSkillsCannotBeWrittenThrough(t *testing.T) {
	for _, tc := range []struct {
		name string
		read func() []byte
	}{
		{"pr-review", prompt.ReviewSkill},
		{"pr-resolve", prompt.ResolveSkill},
	} {
		got := tc.read()
		if len(got) == 0 {
			t.Fatalf("%s is empty", tc.name)
		}
		want := got[0]
		got[0] = 'X'
		if tc.read()[0] != want {
			t.Errorf("%s: writing through the accessor's result changed the embedded copy",
				tc.name)
		}
	}
}

// SkillBody answers a copy rather than a window onto its argument, so a caller
// holding the skill bytes itself is not writing into them either.
func TestSkillBodyDoesNotAliasItsArgument(t *testing.T) {
	for _, in := range []string{"---\nname: pr-review\n---\nbody\n", "no frontmatter here\n"} {
		skill := []byte(in)
		body := prompt.SkillBody(skill)
		if len(body) == 0 {
			t.Fatalf("SkillBody(%q) answered nothing", in)
		}
		body[0] = 'X'
		if string(skill) != in {
			t.Errorf("SkillBody(%q) handed back a window onto its argument", in)
		}
	}
}

// A comment left by a deleted account arrives as `author: null`, because
// lib/github.sh:133-139 projects `.author.login` and GitHub answers null for
// one. jq's interpolation prints the word; decoding into a Go string printed
// nothing, because json.Unmarshal of `null` into a string is a nil error that
// leaves the string empty.
func TestANullCommentAuthorRendersAsTheWordNull(t *testing.T) {
	o := loadReviewOracle(t)
	var author prompt.Author
	if err := json.Unmarshal([]byte("null"), &author); err != nil {
		t.Fatal(err)
	}
	got := string(prompt.Review{
		Skill: []byte(o.Inputs.Skill),
		Diff:  []byte(o.Inputs.Diff),
		Meta:  o.Inputs.Meta,
		Threads: []prompt.Thread{{
			Path: prompt.Str("a.ts"), Line: prompt.Num(4), IsResolved: prompt.Bool(false),
			Comments: []prompt.Comment{{Author: author, Body: prompt.Str("hi")}},
		}},
	}.Render())

	if !strings.Contains(got, "- **null**: hi\n") {
		t.Fatalf("wanted the word null; got:\n%s",
			got[strings.Index(got, "## Open review conversation"):])
	}
}

// `select(.isResolved == false)` is strict equality, so a thread carrying no
// isResolved is dropped. A Go bool made it show, which inverts a filter that
// decides what the reviewer reads.
func TestAThreadWithNoIsResolvedIsDroppedFromTheReviewPrompt(t *testing.T) {
	o := loadReviewOracle(t)
	got := string(prompt.Review{
		Skill: []byte(o.Inputs.Skill),
		Diff:  []byte(o.Inputs.Diff),
		Meta:  o.Inputs.Meta,
		Threads: []prompt.Thread{{
			Path: prompt.Str("a.ts"), Line: prompt.Num(4),
			Comments: []prompt.Comment{{Author: prompt.Login("carol"), Body: prompt.Str("hi")}},
		}},
	}.Render())

	if strings.Contains(got, "### a.ts:4") {
		t.Fatal("a thread carrying no isResolved was shown as open conversation")
	}
	// The heading is printed off the whole set rather than the filtered one, so
	// a set where nothing survives the filter still gets its bare heading.
	if !strings.Contains(got, "## Open review conversation") {
		t.Fatal("the heading is gated on the filtered set rather than the whole one")
	}
}

// Every thread resolved is the same shape: `[[ length != 0 ]]` reads the whole
// array, and the jq filter then yields nothing. The heading and its paragraph
// are printed with no thread under them.
func TestAllResolvedThreadsStillPrintTheBareHeading(t *testing.T) {
	o := loadReviewOracle(t)
	got := string(prompt.Review{
		Skill: []byte(o.Inputs.Skill),
		Diff:  []byte(o.Inputs.Diff),
		Meta:  o.Inputs.Meta,
		Threads: []prompt.Thread{
			{Path: prompt.Str("a.ts"), Line: prompt.Num(1), IsResolved: prompt.Bool(true)},
			{Path: prompt.Str("b.ts"), Line: prompt.Num(2), IsResolved: prompt.Bool(true)},
		},
	}.Render())

	if !strings.Contains(got, "## Open review conversation") {
		t.Fatal("the heading was dropped when every thread was resolved")
	}
	if strings.Contains(got, "### a.ts:1") || strings.Contains(got, "### b.ts:2") {
		t.Fatal("a resolved thread was shown as open conversation")
	}
}

// `.min_fix_severity // "medium"` falls through on null and on false and prints
// an empty string for "". Changing the default and removing it both survived,
// because every existing case set it to a real severity.
func TestMinFixSeverityDefaultsToMediumAndPrintsAnEmptyStringAsItself(t *testing.T) {
	o := loadReviewOracle(t)
	render := func(meta prompt.Meta) string {
		return string(prompt.Review{
			Skill: []byte(o.Inputs.Skill), Diff: []byte(o.Inputs.Diff), Meta: meta,
		}.Render())
	}
	meta := o.Inputs.Meta

	meta.MinFixSeverity = prompt.Value{}
	if !strings.Contains(render(meta), "in force this pass: **medium**.") {
		t.Error("an absent min_fix_severity did not take the medium default")
	}
	meta.MinFixSeverity = prompt.Str("high")
	if !strings.Contains(render(meta), "in force this pass: **high**.") {
		t.Error("a set min_fix_severity was not printed")
	}
	meta.MinFixSeverity = prompt.Str("")
	if !strings.Contains(render(meta), "in force this pass: ****.") {
		t.Error(`an empty min_fix_severity should print ****; jq counts "" as true`)
	}
	meta.MinFixSeverity = prompt.Bool(false)
	if !strings.Contains(render(meta), "in force this pass: **medium**.") {
		t.Error("a false min_fix_severity should fall through to the default")
	}
}

// `gsub("<!--[^>]*-->";"")` cannot cross a `>`, so a body carrying two markers
// loses two comments and keeps the text between them. A `.*` would take the
// lot, and every existing case had one marker.
func TestTwoMarkersInOneBodyAreStrippedSeparately(t *testing.T) {
	o := loadReviewOracle(t)
	got := string(prompt.Review{
		Skill: []byte(o.Inputs.Skill),
		Diff:  []byte(o.Inputs.Diff),
		Meta:  o.Inputs.Meta,
		Threads: []prompt.Thread{{
			Path: prompt.Str("a.ts"), Line: prompt.Num(4), IsResolved: prompt.Bool(false),
			Comments: []prompt.Comment{{
				Author: prompt.Login("carol"),
				Body: prompt.Str("one<!-- crossrev:f {\"id\":\"a\"} -->two" +
					"<!-- crossrev:f {\"id\":\"b\"} -->three"),
			}},
		}},
	}.Render())

	if !strings.Contains(got, "- **carol**: onetwothree\n") {
		t.Fatalf("wanted onetwothree; got:\n%s",
			got[strings.Index(got, "## Open review conversation"):])
	}
}

// The issue lines are joined with a newline. Every existing candidate set held
// one issue, so dropping the join left them all passing.
func TestTwoCandidateIssuesAreOnSeparateLines(t *testing.T) {
	o := loadResolveOracle(t)
	in := resolveFromOracle(o)
	in.Candidates = prompt.Candidates{{
		FindingID: "aaaa000000000001",
		Issues: []prompt.Issue{
			{Number: prompt.Num(17), State: prompt.Str("OPEN"), Title: prompt.Str("a")},
			{Number: prompt.Num(31), State: prompt.Str("CLOSED"), Title: prompt.Str("b")},
		},
	}}
	got := string(in.Render())

	if !strings.Contains(got, "- **#17** (OPEN) a\n- **#31** (CLOSED) b\n") {
		t.Fatalf("the two candidate lines ran together; got:\n%s",
			got[strings.Index(got, "## Issues that might already cover"):])
	}
}

// jq's `to_entries` walks an object in the order its keys were written, and
// lib/run.sh:2482 builds it one finding at a time. A Go map would reorder the
// blocks on every run, so the decoder keeps the order and the encoder writes it
// back.
func TestCandidatesRoundTripInTheOrderTheyWereWritten(t *testing.T) {
	const raw = `{"cccc000000000003":[{"number":31,"state":"CLOSED","title":"b"}],` +
		`"aaaa000000000001":[{"number":17,"state":"OPEN","title":"a"}]}`
	var c prompt.Candidates
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		t.Fatal(err)
	}
	if len(c) != 2 || c[0].FindingID != "cccc000000000003" || c[1].FindingID != "aaaa000000000001" {
		t.Fatalf("decoded in the wrong order: %+v", c)
	}
	// encoding/json compacts what a MarshalJSON answers, so whitespace is not
	// what this measures: the object form and the key order are.
	back, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if string(back) != raw {
		t.Fatalf("re-encoded as\n%s\nwant\n%s", back, raw)
	}

	// Nested inside a struct, which is how the orchestrator will write it back.
	nested, err := json.Marshal(struct {
		Candidates prompt.Candidates `json:"candidates"`
	}{c})
	if err != nil {
		t.Fatal(err)
	}
	if string(nested) != `{"candidates":`+raw+`}` {
		t.Fatalf("nested re-encoding is %s", nested)
	}
}

// An unretrieved backlog leaves a null candidates object, and the shell reads
// `(. // {}) | length`, so null is nothing rather than an error.
func TestCandidatesDecodeNullAsNothing(t *testing.T) {
	c := prompt.Candidates{{FindingID: "x"}}
	if err := json.Unmarshal([]byte("null"), &c); err != nil {
		t.Fatalf("a null candidates object should decode to nothing, got %v", err)
	}
	if len(c) != 0 {
		t.Fatalf("wanted nothing, got %+v", c)
	}
	if err := json.Unmarshal([]byte(`[1]`), &c); err == nil {
		t.Fatal("an array is not the object form and should be refused")
	}
}

// The findings heading is not one of the three optional sections: the shell
// prints it and its paragraph unconditionally, and a resolve prompt with no
// findings still has to say what shape the answer takes. Gating it on a
// non-empty findings list survived every existing case.
func TestTheFindingsHeadingIsPrintedEvenWithNoFindings(t *testing.T) {
	o := loadResolveOracle(t)
	in := resolveFromOracle(o)
	in.Findings = nil
	got := string(in.Render())

	if !strings.Contains(got, "## The findings to address\n\n") {
		t.Fatal("the findings heading was gated on there being findings")
	}
	if !strings.Contains(got, "Return exactly one entry in `resolutions` per finding here") {
		t.Fatal("the paragraph under the heading went with it")
	}
}

// `_sandbox_paths` already answers sorted and unique, so Render doing it again
// costs no parity and removes one thing the leg after this one can get wrong.
func TestQuarantinedPathsAreSortedAndMadeUniqueByRender(t *testing.T) {
	o := loadResolveOracle(t)
	in := resolveFromOracle(o)
	shuffled := append([]string(nil), quarantinedInTheFixture...)
	// Reversed, with one path repeated.
	for i, j := 0, len(shuffled)-1; i < j; i, j = i+1, j-1 {
		shuffled[i], shuffled[j] = shuffled[j], shuffled[i]
	}
	in.QuarantinedPaths = append(shuffled, "CLAUDE.md")

	got := string(in.Render())
	want := "- These paths are **deliberately not in the checkout**: " +
		".agents, .claude, .clauderc, .codex, .cursor, .gemini, " +
		".github/copilot-instructions.md, .grok, .mcp.json, .opencode, " +
		"AGENT.md, AGENTS.md, Agents.md, CLAUDE.local.md, CLAUDE.md, " +
		"Claude.md, GEMINI.md, agents.md, claude.md, opencode.json, opencode.jsonc. "
	if !strings.Contains(got, want) {
		t.Fatalf("the list was not sorted and made unique; got:\n%s",
			got[strings.Index(got, "- These paths are"):strings.Index(got, "- Deferred work")])
	}
}

// Every field the shell interpolates with no `//` default prints `null` for an
// absent key, and the four that do have one print an empty string for "". Both
// halves used to collapse to the Go zero value.
func TestAnAbsentPriorFieldPrintsNullAndAnEmptyOnePrintsEmpty(t *testing.T) {
	o := loadReviewOracle(t)
	render := func(p prompt.Prior) string {
		return string(prompt.Review{
			Skill: []byte(o.Inputs.Skill), Diff: []byte(o.Inputs.Diff), Meta: o.Inputs.Meta,
			Prior: []prompt.Prior{p},
		}.Render())
	}

	absent := render(prompt.Prior{ID: prompt.Str("aaaa000000000001"), Path: prompt.Str("a.ts")})
	if !strings.Contains(absent, "| 1 | aaaa000000000001 | a.ts:null | null | - | no | - | none | - |\n") {
		t.Errorf("absent fields did not print null and the four defaults; got:\n%s",
			row(absent))
	}

	empty := render(prompt.Prior{
		ID: prompt.Str("aaaa000000000001"), Path: prompt.Str("a.ts"), Line: prompt.Num(2),
		Severity: prompt.Str("low"), PreExisting: prompt.Bool(true),
		Category: prompt.Str(""), Title: prompt.Str(""),
		Resolution: prompt.Str(""), TrackedAs: prompt.Str(""),
	})
	if !strings.Contains(empty, "| 1 | aaaa000000000001 | a.ts:2 | low |  | yes |  |  |  |\n") {
		t.Errorf(`an empty string took a "//" default; jq counts "" as true. Got:\n%s`, row(empty))
	}
}

// The resolve leg's own three defaults, on the same rule.
func TestAnEmptyWhyFixOrPriorResolutionIsNotAbsent(t *testing.T) {
	o := loadResolveOracle(t)
	in := resolveFromOracle(o)
	in.Findings = []prompt.Finding{{
		Number: prompt.Num(1), ID: prompt.Str("aaaa000000000001"),
		Severity: prompt.Str("high"), Category: prompt.Str("security"),
		Path: prompt.Str("a.ts"), Line: prompt.Num(2), Title: prompt.Str("t"),
		MayFix: prompt.Bool(true),
		Why:    prompt.Str(""), Fix: prompt.Str(""), PriorResolution: prompt.Str(""),
	}}
	got := string(in.Render())

	if !strings.Contains(got, "- Why it matters: \n- Suggested fix: \n") {
		t.Error(`an empty why or fix printed the "-" default`)
	}
	if !strings.Contains(got, "- **You settled this `` in an earlier pass.**") {
		t.Error("an empty prior_resolution dropped the line the shell prints")
	}

	// false is absent to `//`, and to `(.prior_resolution // null) != null`.
	in.Findings[0].Why = prompt.Bool(false)
	in.Findings[0].PriorResolution = prompt.Bool(false)
	got = string(in.Render())
	if !strings.Contains(got, "- Why it matters: -\n") {
		t.Error("a false why did not fall through to the dash")
	}
	if strings.Contains(got, "You settled this") {
		t.Error("a false prior_resolution still printed the line")
	}
}

// row is the prior table's first data line, for a readable failure.
func row(prompt string) string {
	at := strings.Index(prompt, "\n| 1 | ")
	if at < 0 {
		return prompt
	}
	end := strings.IndexByte(prompt[at+1:], '\n')
	return prompt[at+1 : at+1+end]
}
