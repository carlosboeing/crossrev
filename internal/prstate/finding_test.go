package prstate_test

import (
	"testing"

	"github.com/carlosboeing/crossrev/internal/prstate"
)

// TestParityFindingID drives lib/state.sh:242's state_finding_id from the
// frozen oracle. Every id in it was minted by the Bash function under
// LC_ALL=C, so a Unicode-aware fold or a Unicode-aware whitespace class in Go
// shows up here as a changed hash.
func TestParityFindingID(t *testing.T) {
	var f struct {
		Cases []struct {
			Name   string `json:"name"`
			Path   string `json:"path"`
			Title  string `json:"title"`
			Anchor string `json:"anchor"`
			ID     string `json:"id"`
		} `json:"cases"`
	}
	loadFixture(t, "state_finding_id.json", &f)

	for _, c := range f.Cases {
		t.Run(c.Name, func(t *testing.T) {
			recordCase("state_finding_id.json", c.Name)
			got := prstate.NewFindingID(c.Path, c.Title, prstate.Anchor(c.Anchor))
			if string(got) != c.ID {
				t.Errorf("id for %q is %q, want %q", c.Title, string(got), c.ID)
			}
		})
	}
}

// The fold is byte-oriented. strings.ToLower would fold the Greek capital
// sigma and change an id that automated mode has already minted and threaded.
func TestFindingIDFoldsASCIIOnly(t *testing.T) {
	upper := prstate.NewFindingID("x.ts", "ΣIGMA", prstate.Anchor(""))
	lower := prstate.NewFindingID("x.ts", "σigma", prstate.Anchor(""))
	if string(upper) == string(lower) {
		t.Error("the Greek capital sigma folded, so the fold is not byte-oriented")
	}
}

// Only the six ASCII whitespace bytes are squeezed. unicode.IsSpace counts
// U+00A0, and folding it into a space would change an already-minted id.
func TestFindingIDSqueezesASCIIWhitespaceOnly(t *testing.T) {
	nbsp := prstate.NewFindingID("x.ts", "token\u00a0refresh", prstate.Anchor(""))
	plain := prstate.NewFindingID("x.ts", "token refresh", prstate.Anchor(""))
	if string(nbsp) == string(plain) {
		t.Error("a non-breaking space was treated as whitespace")
	}
}

// The preimage is three fields joined by newlines with no trailing newline
// (lib/state.sh:245). A trailing newline changes every id ever minted.
func TestFindingIDHashesThreeFieldsWithNoTrailingNewline(t *testing.T) {
	// sha256("lib/auth.ts\ntoken refresh races with logout\nabcd1234"), first
	// 16 hexadecimal characters, taken from the frozen oracle's ascii-basic
	// case. Recomputed here from an independently built preimage so a change
	// to the join would fail even if the oracle were regenerated.
	got := prstate.NewFindingID("lib/auth.ts", "Token refresh races with logout", prstate.Anchor("abcd1234"))
	if string(got) != "1f3b64041e298591" {
		t.Errorf("got %q", string(got))
	}
}

func TestFindingIDIsSixteenLowercaseHex(t *testing.T) {
	got := prstate.NewFindingID("a", "b", prstate.Anchor("c"))
	if !got.Valid() {
		t.Errorf("%q is not a valid finding id", string(got))
	}
}

func TestParseFindingIDAcceptsOnlyTheMintedShape(t *testing.T) {
	good, err := prstate.ParseFindingID("1f3b64041e298591")
	if err != nil {
		t.Fatalf("a minted id was refused: %v", err)
	}
	if string(good) != "1f3b64041e298591" {
		t.Errorf("got %q", string(good))
	}
	for _, bad := range []string{"", "1F3B64041E298591", "1f3b64041e29859", "1f3b64041e2985911", "zzzzzzzzzzzzzzzz"} {
		if _, err := prstate.ParseFindingID(bad); err == nil {
			t.Errorf("%q was accepted as a finding id", bad)
		}
	}
}
