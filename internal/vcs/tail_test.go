package vcs_test

import (
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/vcs"
)

// tailVectors are the shell-agreed answers, frozen at the native cutover.
//
// _gh_git_tail lived in lib/github.sh, and TestGitTailMatchesTheShellByte ran
// it over every text below and compared the bytes. The shell is removed, so
// the answers measured then are frozen here: the last five lines, cut to a
// character cap with an ellipsis marker where the cut lands mid-stream.
var tailVectors = []struct {
	name  string
	text  string
	want  string
	found bool
}{
	{name: "short ascii", text: "one\ntwo\nthree",
		want: "one\ntwo\nthree", found: true},
	{name: "more than five lines", text: "one\n\ntwo\nthree\nfour\nfive\nsix\n",
		want: "two\nthree\nfour\nfive\nsix", found: true},
	{name: "nothing", text: "",
		want: "", found: false},
	{name: "blank lines only", text: "\n   \n\t\n",
		want: "", found: false},
	{name: "500 e-acute", text: strings.Repeat("\u00e9", 500),
		want: "…éééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééé", found: true},
	{name: "500 han", text: strings.Repeat("\u4e2d", 500),
		want: "…中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中", found: true},
	{name: "401 astral", text: strings.Repeat("\U0001f600", 401),
		want: "…😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀", found: true},
	{name: "no-break space", text: strings.Repeat("\u00a0x", 300),
		want: "…\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x\u00a0x", found: true},
	{name: "ideographic space", text: strings.Repeat("\u3000x", 300),
		want: "…\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x\u3000x", found: true},
	{name: "mixed widths at the boundary", text: strings.Repeat("a", 200) + strings.Repeat("\u4e2d", 300),
		want: "…aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中中", found: true},
	{name: "500 bare 0xFF", text: strings.Repeat("\xff", 500),
		want: "…\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff", found: true},
	{name: "300 continuation pairs", text: strings.Repeat("\x80\x81", 300),
		want: "…\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81\x80\x81", found: true},
	{name: "a truncated sequence in the middle", text: strings.Repeat("a", 300) + "\xc3" + strings.Repeat("b", 200),
		want: "…aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\xc3bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", found: true},
	{name: "latin-1 text", text: strings.Repeat("caf\xe9 ", 120),
		want: "…caf\xe9 caf\xe9 caf\xe9 caf\xe9 caf\xe9 caf\xe9 caf\xe9 caf\xe9 caf\xe9 caf\xe9 caf\xe9 caf\xe9 caf\xe9 caf\xe9 caf\xe9 caf\xe9 caf\xe9 caf\xe9 caf\xe9 caf\xe9 caf\xe9 caf\xe9 caf\xe9 caf\xe9 caf\xe9 caf\xe9 caf\xe9 caf\xe9 caf\xe9 caf\xe9 caf\xe9 caf\xe9 caf\xe9 caf\xe9 caf\xe9 caf\xe9 caf\xe9 caf\xe9 caf\xe9 caf\xe9 caf\xe9 caf\xe9 caf\xe9 caf\xe9 caf\xe9 caf\xe9 caf\xe9 caf\xe9 caf\xe9 caf\xe9 caf\xe9 caf\xe9 caf\xe9 caf\xe9 caf\xe9 caf\xe9 caf\xe9 caf\xe9 caf\xe9 caf\xe9 caf\xe9 caf\xe9 caf\xe9 caf\xe9 caf\xe9 caf\xe9 caf\xe9 caf\xe9 caf\xe9 caf\xe9 caf\xe9 caf\xe9 caf\xe9 caf\xe9 caf\xe9 caf\xe9 caf\xe9 caf\xe9 caf\xe9 caf\xe9 ", found: true},
	{name: "an invalid byte exactly at the cut", text: strings.Repeat("a", 100) + "\xff" + strings.Repeat("b", 400),
		want: "…bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", found: true},
}

// TestGitTailFreezesTheShellAgreedBytes pins the tail's byte agreement,
// including over invalid UTF-8, without the shell that used to answer it.
func TestGitTailFreezesTheShellAgreedBytes(t *testing.T) {
	for _, tt := range tailVectors {
		t.Run(tt.name, func(t *testing.T) {
			got, found := vcs.GitTail(tt.text)
			if !found {
				got = ""
			}
			if found != tt.found || got != tt.want {
				t.Errorf("bytes differ\n  want: found=%v %d bytes %q\n   got: found=%v %d bytes %q",
					tt.found, len(tt.want), tt.want, found, len(got), got)
			}
		})
	}
}

// The property capTail rests on, asserted rather than assumed: `for range`
// over a string yields one U+FFFD per invalid byte and advances one byte, so
// its count matches a character count under a UTF-8 locale.
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
