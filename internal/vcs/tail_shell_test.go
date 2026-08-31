package vcs_test

import (
	"bytes"
	osexec "os/exec"
	"strconv"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/vcs"
)

// shellGitTail runs the shipped _gh_git_tail over text and returns its exact
// bytes.
//
// The oracle is the shell itself rather than a table, because what is under
// test is a byte-for-byte agreement with it and a table would only record
// somebody's reading of the shell. internal/config and internal/prompt start
// bash for the same reason.
func shellGitTail(t *testing.T, text string) string {
	t.Helper()
	root := repoRoot(t)
	script := `set -uo pipefail
source "$1/lib/ui.sh"
source "$1/lib/github.sh"
_gh_git_tail "$2" || true`

	cmd := osexec.Command("bash", "-c", script, "_", root, text)
	cmd.Dir = root
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("bash _gh_git_tail: %v: %s", err, stderr.String())
	}
	return stdout.String()
}

// The cap counts characters, and the shell proves it: `${#picked}` and
// `${picked: -cap}` both count characters under bash. What the port must not do
// is reach that count by decoding and re-encoding, because every byte that is
// not valid UTF-8 becomes U+FFFD on the way in and three bytes on the way out.
//
// The same text is published, not merely printed: lib/github.sh:513 makes it
// the ui_die reason, lib/ui.sh:115 stores it, and lib/run.sh:144-146 writes it
// into a pull request comment.
func TestGitTailMatchesTheShellByte(t *testing.T) {
	tests := []struct {
		name string
		text string
	}{
		{name: "short ascii", text: "one\ntwo\nthree"},
		{name: "more than five lines", text: "one\n\ntwo\nthree\nfour\nfive\nsix\n"},
		{name: "nothing", text: ""},
		{name: "blank lines only", text: "\n   \n\t\n"},

		// Valid UTF-8 over the cap. These already agreed, and they are here so
		// a fix for the invalid cases cannot break the valid ones.
		{name: "500 e-acute", text: strings.Repeat("é", 500)},
		{name: "500 han", text: strings.Repeat("中", 500)},
		{name: "401 astral", text: strings.Repeat("😀", 401)},
		{name: "no-break space", text: strings.Repeat(" x", 300)},
		{name: "ideographic space", text: strings.Repeat("　x", 300)},
		{name: "mixed widths at the boundary", text: strings.Repeat("a", 200) + strings.Repeat("中", 300)},

		// Invalid UTF-8 over the cap. A hook printing Latin-1, or a filename in
		// a non-UTF-8 encoding, produces exactly this.
		{name: "500 bare 0xFF", text: strings.Repeat("\xff", 500)},
		{name: "300 continuation pairs", text: strings.Repeat("\x80\x81", 300)},
		{name: "a truncated sequence in the middle", text: strings.Repeat("a", 300) + "\xc3" + strings.Repeat("b", 200)},
		{name: "latin-1 text", text: strings.Repeat("caf\xe9 ", 120)},
		{name: "an invalid byte exactly at the cut", text: strings.Repeat("a", 100) + "\xff" + strings.Repeat("b", 400)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			want := shellGitTail(t, tt.text)
			got, found := vcs.GitTail(tt.text)
			if !found {
				got = ""
			}
			if got != want {
				t.Errorf("bytes differ\n shell: %d bytes %q\n    go: %d bytes %q",
					len(want), want, len(got), got)
			}
		})
	}
}

// repoRootHasTheShell guards the oracle: a differential that silently stopped
// finding the shell would pass by comparing nothing.
func TestTheShellOracleIsThere(t *testing.T) {
	if got := shellGitTail(t, "one\ntwo"); got != "one\ntwo" {
		t.Fatalf("the shell oracle answered %q; the harness is not running _gh_git_tail", got)
	}
}

// The property capTail rests on, asserted rather than assumed: `for range` over
// a string yields one U+FFFD per invalid byte and advances one byte, so its
// count matches what bash's `${#s}` counts under a UTF-8 locale.
func TestGitTailCountsAnInvalidByteAsOneCharacter(t *testing.T) {
	tests := []struct {
		name string
		text string
		want int
	}{
		{name: "bare 0xFF", text: "\xff\xff\xff", want: 3},
		{name: "continuation bytes", text: "\x80\x81", want: 2},
		{name: "a lead byte with no continuation", text: "a\xc3b", want: 3},
		{name: "a valid two-byte character", text: "é", want: 1},
		{name: "a valid astral character", text: "😀", want: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			count := 0
			for index, r := range tt.text {
				if r == '�' {
					// The width is what matters: an invalid byte must advance
					// one, or the count and the slice offset disagree.
					if _, next := nextIndex(tt.text, index); next != index+1 {
						t.Errorf("an invalid byte at %d advanced to %d, want %d", index, next, index+1)
					}
				}
				count++
			}
			if count != tt.want {
				t.Errorf("characters = %d, want %d", count, tt.want)
			}
			// And bash agrees, which is the whole point of the count.
			if got := shellCharacterCount(t, tt.text); got != tt.want {
				t.Errorf("bash counts %d characters, this counts %d", got, tt.want)
			}
		})
	}
}

// nextIndex reports the byte index after the character starting at index.
func nextIndex(s string, index int) (rune, int) {
	for next := range s[index+1:] {
		return 0, index + 1 + next
	}
	return 0, len(s)
}

// shellCharacterCount is bash's own `${#s}` over the same bytes.
func shellCharacterCount(t *testing.T, text string) int {
	t.Helper()
	cmd := osexec.Command("bash", "-c", `printf '%s' "${#1}"`, "_", text)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("bash ${#s}: %v", err)
	}
	count, err := strconv.Atoi(string(out))
	if err != nil {
		t.Fatalf("bash answered %q: %v", string(out), err)
	}
	return count
}
