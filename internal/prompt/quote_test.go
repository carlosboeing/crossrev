package prompt_test

import (
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/prompt"
)

// Quoting is indentation rather than a fence, because a fence can be closed by
// the very text it is quoting (lib/prompt.sh:18-33,
// tests/test-commit-convention.sh:124-157).
func TestQuoteBlockIndentsEveryLine(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "one line",
			in:   "feat(api): add the widget endpoint",
			want: "    feat(api): add the widget endpoint",
		},
		{
			name: "every line, including a blank one",
			in:   "first\n\nthird",
			want: "    first\n    \n    third",
		},
		{
			// No line of an indented block can end the block, whatever it says.
			name: "a fence cannot close the block",
			in:   "```\nA second line, which a closed fence would leave unquoted.",
			want: "    ```\n    A second line, which a closed fence would leave unquoted.",
		},
		{
			// The text reaches a terminal as well as a model, so an escape
			// sequence read from the repository cannot paint over the run.
			name: "an escape sequence is flattened to one space",
			in:   "fix(api): reset \x1b[2Jthe cache",
			want: "    fix(api): reset  [2Jthe cache",
		},
		{
			// tr's first range is \000-\011, which takes the tab with it.
			name: "a tab is a control character too",
			in:   "a\tb",
			want: "    a b",
		},
		{
			name: "and so is DEL",
			in:   "a\x7fb",
			want: "    a b",
		},
		{
			// \012 is the one byte the two ranges step over.
			name: "the newline survives, because it is what makes a line",
			in:   "a\nb",
			want: "    a\n    b",
		},
		{
			name: "nothing quotes to nothing",
			in:   "",
			want: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := prompt.QuoteBlock([]byte(tc.in)); string(got) != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// The multi-byte case tr is measured on. The oracle records that it ran under
// BSD tr in a UTF-8 locale, and a character whose UTF-8 encoding is valid
// passes through whole either way.
func TestQuoteBlockLeavesMultiByteCharactersAlone(t *testing.T) {
	if got := prompt.QuoteBlock([]byte("fix: café — reset")); string(got) != "    fix: café — reset" {
		t.Fatalf("got %q", got)
	}
}

// Where the two part company: invalid UTF-8.
//
// BSD tr in a UTF-8 locale is not byte-oriented, whatever the ranges look like.
// Measured on the platform the oracle records:
//
//	$ printf 'ab\xc3\x28cd' | tr '\000-\011\013-\037\177' ' '
//	ab
//	tr: Illegal byte sequence
//
// It writes what it had, reports the error and stops, so the shell truncates a
// `.gitmessage` at the first malformed byte and prints the rest of the section
// as though the file ended there. Go passes the bytes through and quotes the
// whole file.
//
// Go's answer is the better one, and it is the one this port keeps: a
// repository's commit template is not the orchestrator's to silently truncate,
// and the flattening exists to stop escape sequences reaching a terminal rather
// than to police encodings. The bytes are not control characters, so nothing
// the flattening is for is lost. Recorded here rather than closed.
func TestQuoteBlockPassesInvalidUTF8ThroughWhereBSDTrTruncates(t *testing.T) {
	got := string(prompt.QuoteBlock([]byte("ab\xc3(cd")))
	if got != "    ab\xc3(cd" {
		t.Fatalf("got %q, want the bytes passed through", got)
	}
}

// The skill's opening frontmatter fence is dropped, and only that one line.
// `sed '1{/^---$/,/^---$/d;}'` runs its block on line 1 alone, so the range
// never advances and the closing fence stays (lib/prompt.sh:147, 218).
func TestSkillBodyDropsOnlyTheFirstLine(t *testing.T) {
	in := "---\nname: pr-review\ndescription: d\n---\n\n# pr-review\n"
	want := "name: pr-review\ndescription: d\n---\n\n# pr-review\n"
	if got := prompt.SkillBody([]byte(in)); string(got) != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestSkillBodyLeavesAFileWithNoFrontmatterAlone(t *testing.T) {
	in := "# pr-review\n\n---\n"
	if got := prompt.SkillBody([]byte(in)); string(got) != in {
		t.Fatalf("got %q, want %q", got, in)
	}
	// A first line that only starts with three dashes is not the fence.
	dashes := "---- \n---\n"
	if got := prompt.SkillBody([]byte(dashes)); string(got) != dashes {
		t.Fatalf("got %q, want %q", got, dashes)
	}
}

// The embedded copies are what a compiled binary reproduces into the prompt, so
// they have to be the files a contributor edits (ADR 0007, design section 18.5).
func TestEmbeddedSkillsAreTheCanonicalFiles(t *testing.T) {
	for _, tc := range []struct {
		name     string
		embedded []byte
		path     string
	}{
		{"pr-review", prompt.ReviewSkill(), "../../skills/pr-review/SKILL.md"},
		{"pr-resolve", prompt.ResolveSkill(), "../../skills/pr-resolve/SKILL.md"},
	} {
		canonical := readFile(t, tc.path)
		if string(tc.embedded) != string(canonical) {
			t.Errorf("%s: the embedded copy differs from %s — run scripts/sync-embedded-assets.sh",
				tc.name, tc.path)
		}
		if !strings.HasPrefix(string(tc.embedded), "---\n") {
			t.Errorf("%s: the embedded copy lost its frontmatter fence", tc.name)
		}
	}
}
