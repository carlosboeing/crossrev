package vcs

import (
	"bytes"
	"io"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// chunkReader yields data in chunks of at most size bytes, so a test can
// prove the batch parser never assumes a buffered read.
type chunkReader struct {
	data []byte
	size int
}

func (c *chunkReader) Read(p []byte) (int, error) {
	if len(c.data) == 0 {
		return 0, io.EOF
	}
	n := c.size
	if n > len(c.data) {
		n = len(c.data)
	}
	if n > len(p) {
		n = len(p)
	}
	copy(p, c.data[:n])
	c.data = c.data[n:]
	return n, nil
}

// TestBlobScannerConsumeMatchesAcrossChunkings feeds one synthetic --batch
// stream whole and one byte at a time: a twin blob fanning out to two
// paths, a quiet blob, and a missing object. Both reads must answer the
// same holders with the same lines.
func TestBlobScannerConsumeMatchesAcrossChunkings(t *testing.T) {
	sha1 := strings.Repeat("1", 40)
	sha2 := strings.Repeat("2", 40)
	sha3 := strings.Repeat("3", 40)
	entries := []treeBlob{
		{sha: sha1, path: "a.go"},
		{sha: sha1, path: "b.go"},
		{sha: sha2, path: "c.go"},
		{sha: sha3, path: "gone.go"},
	}
	body1 := "line one alpha\nline two\n"
	body2 := "nothing here\n"
	var stream bytes.Buffer
	stream.WriteString(sha1 + " blob " + strconv.Itoa(len(body1)) + "\n" + body1 + "\n")
	stream.WriteString(sha2 + " blob " + strconv.Itoa(len(body2)) + "\n" + body2 + "\n")
	stream.WriteString(sha3 + " missing\n")
	raw := stream.Bytes()

	whole := newBlobScanner([]string{"alpha", "quiet"}, entries, 200)
	if err := whole.consume(bytes.NewReader(raw)); err != nil {
		t.Fatalf("consume whole: %v", err)
	}
	want := [][]SearchHit{
		{{Path: "a.go", Lines: []int{1}}, {Path: "b.go", Lines: []int{1}}},
		nil,
	}
	if !reflect.DeepEqual(whole.found, want) {
		t.Fatalf("consume whole found = %+v, want %+v", whole.found, want)
	}

	dribble := newBlobScanner([]string{"alpha", "quiet"}, entries, 200)
	if err := dribble.consume(&chunkReader{data: append([]byte(nil), raw...), size: 1}); err != nil {
		t.Fatalf("consume one byte at a time: %v", err)
	}
	if !reflect.DeepEqual(dribble.found, whole.found) {
		t.Errorf("one-byte reads found = %+v, want the whole-read %+v", dribble.found, whole.found)
	}
}

// TestBlobScannerConsumeRejectsCorruptStreams feeds each broken --batch
// shape and requires the error that names it. The missing object beside
// them is the positive control: absence degrades to skipped, never to an
// error.
func TestBlobScannerConsumeRejectsCorruptStreams(t *testing.T) {
	sha := strings.Repeat("a", 40)
	vectors := []struct {
		name    string
		stream  string
		wantErr string
	}{
		{"truncated header", "abc12", "ended inside"},
		{"wrong sha", strings.Repeat("b", 40) + " blob 3\nabc\n", "want the blob header"},
		{"non blob type", sha + " commit 3\nabc\n", "want the blob header"},
		{"non numeric size", sha + " blob 3x\nabc\n", "non-numeric size"},
		{"truncated body", sha + " blob 10\nshort\n", "ended inside the 10 bytes"},
		{"missing trailing newline", sha + " blob 3\nabc", "missed the trailing newline"},
		{"wrong trailing byte", sha + " blob 3\nabcX\n", "missed the trailing newline"},
		{"missing object skips", sha + " missing\n", ""},
	}
	for _, v := range vectors {
		t.Run(v.name, func(t *testing.T) {
			s := newBlobScanner([]string{"alpha"}, []treeBlob{{sha: sha, path: "a.go"}}, 200)
			err := s.consume(strings.NewReader(v.stream))
			if v.wantErr == "" {
				if err != nil {
					t.Errorf("consume %q: %v, want nil", v.stream, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("consume %q succeeded, want an error naming %q", v.stream, v.wantErr)
			}
			if !strings.Contains(err.Error(), v.wantErr) {
				t.Errorf("consume %q error = %q, want it to name %q", v.stream, err.Error(), v.wantErr)
			}
		})
	}
}

// TestBlobScannerStopsPastTheCap requires holders past limit+1 to stop
// recording: the caller only needs to know the cap broke, and truncates
// the extra holder itself.
func TestBlobScannerStopsPastTheCap(t *testing.T) {
	entries := []treeBlob{
		{sha: strings.Repeat("1", 40), path: "a.go"},
		{sha: strings.Repeat("2", 40), path: "b.go"},
		{sha: strings.Repeat("3", 40), path: "c.go"},
	}
	var stream bytes.Buffer
	for _, entry := range entries {
		stream.WriteString(entry.sha + " blob 6\nalpha\n\n")
	}
	s := newBlobScanner([]string{"alpha"}, entries, 1)
	if err := s.consume(&stream); err != nil {
		t.Fatalf("consume: %v", err)
	}
	if len(s.found) != 1 || len(s.found[0]) != 2 {
		t.Fatalf("found = %+v, want the first two holders (limit+1)", s.found)
	}
	if s.found[0][0].Path != "a.go" || s.found[0][1].Path != "b.go" {
		t.Errorf("found = %+v, want a.go then b.go in stream order", s.found[0])
	}
}

// TestBlobScannerCarriesMatchesAcrossWindows requires the 32KB read window
// to be invisible: a term split over the window edge still matches, and
// line numbers count newlines from every earlier window.
func TestBlobScannerCarriesMatchesAcrossWindows(t *testing.T) {
	shaSplit := strings.Repeat("1", 40)
	shaLines := strings.Repeat("2", 40)
	entries := []treeBlob{
		{sha: shaSplit, path: "split.go"},
		{sha: shaLines, path: "lines.go"},
	}
	// MARKER starts on the window's last byte, so all but one byte lands
	// in the next read.
	split := bytes.Repeat([]byte("x"), scanWindow-1)
	split = append(split, "MARKER\n"...)
	// Twenty thousand short lines put the marker on line 20001, past two
	// window edges.
	lines := bytes.Repeat([]byte("a\n"), 20000)
	lines = append(lines, "MARKER"...)
	var stream bytes.Buffer
	stream.WriteString(shaSplit + " blob " + strconv.Itoa(len(split)) + "\n")
	stream.Write(split)
	stream.WriteByte('\n')
	stream.WriteString(shaLines + " blob " + strconv.Itoa(len(lines)) + "\n")
	stream.Write(lines)
	stream.WriteByte('\n')
	s := newBlobScanner([]string{"MARKER"}, entries, 200)
	if err := s.consume(&stream); err != nil {
		t.Fatalf("consume: %v", err)
	}
	if len(s.found) != 1 || len(s.found[0]) != 2 {
		t.Fatalf("found = %+v, want both window-crossing holders", s.found)
	}
	byPath := map[string][]int{}
	for _, hit := range s.found[0] {
		byPath[hit.Path] = hit.Lines
	}
	if !reflect.DeepEqual(byPath["split.go"], []int{1}) {
		t.Errorf("split.go lines = %v, want [1] (the term split over the window edge)", byPath["split.go"])
	}
	if !reflect.DeepEqual(byPath["lines.go"], []int{20001}) {
		t.Errorf("lines.go lines = %v, want [20001] (newlines counted across windows)", byPath["lines.go"])
	}
}

// TestMatcherStreamFeedsAcrossBoundaries requires automaton state to carry
// across feeds: "swo" and "rd" arrive apart and still match sword, word and
// ord at absolute offsets.
func TestMatcherStreamFeedsAcrossBoundaries(t *testing.T) {
	m := newMatcher([]string{"sword", "word", "ord"})
	fed := m.stream()
	var got [][2]int
	emit := func(term, end int) { got = append(got, [2]int{term, end}) }
	fed.feed([]byte("a sw"), 0, emit)
	fed.feed([]byte("ord is a word"), 4, emit)
	want := [][2]int{{0, 6}, {1, 6}, {2, 6}, {1, 16}, {2, 16}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("feed matches = %v, want %v", got, want)
	}
}
