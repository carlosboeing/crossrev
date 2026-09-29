package intel

import (
	"fmt"
	"strconv"
	"strings"
)

// partContent is one split call's content: the diff header lines every
// part repeats, and the hunk blocks it carries. Each block starts with
// its own @@ line, so a part renders with its header and gutter numbers
// the same way a whole file does.
type partContent struct {
	header []string
	blocks [][]string
}

// bytes renders the part's diff: the header and its hunk blocks.
func (p partContent) bytes() []byte {
	var b strings.Builder
	for _, line := range p.header {
		b.WriteString(line)
	}
	for _, block := range p.blocks {
		for _, line := range block {
			b.WriteString(line)
		}
	}
	return []byte(b.String())
}

// contentBytes is the hunk bytes without the repeated header: the budget
// the splitter packs against.
func (p partContent) contentBytes() int {
	n := 0
	for _, block := range p.blocks {
		for _, line := range block {
			n += len(line)
		}
	}
	return n
}

// contentLines counts the hunk body lines, excluding @@ headers.
func (p partContent) contentLines() int {
	n := 0
	for _, block := range p.blocks {
		n += len(block) - 1
	}
	return n
}

// splitDiffContent cuts a per-file diff into its header lines and its
// hunk blocks. The header is every line before the first @@ line; each
// block starts at an @@ line and runs to the next one. A diff with no
// hunk boundary answers no blocks, and the caller synthesises hunks from
// the body instead.
func splitDiffContent(diffBytes []byte) (header []string, blocks [][]string) {
	var current []string
	seenHunk := false
	for _, line := range strings.SplitAfter(string(diffBytes), "\n") {
		if line == "" {
			continue
		}
		// A unified diff prefixes every content line with ' ', '+' or
		// '-', so a line starting at column zero with @@ is a hunk
		// header rather than file content.
		if strings.HasPrefix(line, "@@") {
			seenHunk = true
			if current != nil {
				blocks = append(blocks, current)
			}
			current = []string{line}
			continue
		}
		if !seenHunk {
			header = append(header, line)
			continue
		}
		current = append(current, line)
	}
	if current != nil {
		blocks = append(blocks, current)
	}
	return header, blocks
}

// synthBlocks shapes an unshaped unit's body into one hunk, so a file
// with no diff structure still splits: every body line reads as context,
// and the hunk header names the true line span. Gutter numbers stay
// truthful because the lines are the file's own in order.
func synthBlocks(path string, body []byte) (header []string, blocks [][]string) {
	lines := strings.SplitAfter(string(body), "\n")
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	header = []string{
		"diff --git a/" + path + " b/" + path + "\n",
		"--- a/" + path + "\n",
		"+++ b/" + path + "\n",
	}
	if len(lines) == 0 {
		return header, nil
	}
	hunk := []string{"@@ -1," + strconv.Itoa(len(lines)) + " +1," + strconv.Itoa(len(lines)) + " @@\n"}
	for _, line := range lines {
		hunk = append(hunk, " "+line)
	}
	return header, [][]string{hunk}
}

// hunkRange parses one @@ header into its old and new starts, lengths
// and trailing suffix. ok is false for a line that is not a hunk header,
// and the caller keeps such a block whole rather than misnumbering it.
func hunkRange(header string) (oldStart, oldLen, newStart, newLen int, suffix string, ok bool) {
	rest, found := strings.CutPrefix(header, "@@")
	if !found {
		return 0, 0, 0, 0, "", false
	}
	middle, suffix, found := strings.Cut(rest, "@@")
	if !found {
		return 0, 0, 0, 0, "", false
	}
	fields := strings.Fields(middle)
	if len(fields) != 2 {
		return 0, 0, 0, 0, "", false
	}
	var good bool
	if oldStart, oldLen, good = hunkField(fields[0], '-'); !good {
		return 0, 0, 0, 0, "", false
	}
	if newStart, newLen, good = hunkField(fields[1], '+'); !good {
		return 0, 0, 0, 0, "", false
	}
	return oldStart, oldLen, newStart, newLen, suffix, true
}

// hunkField parses one @@ range field: the sign byte, then the start,
// then the length after a comma, defaulting to a single line.
func hunkField(field string, sign byte) (start, length int, ok bool) {
	if len(field) == 0 || field[0] != sign {
		return 0, 0, false
	}
	text := field[1:]
	count := "1"
	if before, after, found := strings.Cut(text, ","); found {
		text, count = before, after
	}
	start, err := strconv.Atoi(text)
	if err != nil || start < 0 {
		return 0, 0, false
	}
	length, err = strconv.Atoi(count)
	if err != nil || length < 0 {
		return 0, 0, false
	}
	return start, length, true
}

// chunkHunk cuts one oversized hunk into line chunks of at most maxBytes,
// each carrying an adjusted @@ header so gutter numbers stay truthful
// across the split. A "\ No newline" marker glues to its line and never
// strands a chunk boundary. A hunk whose header does not parse, or a
// single line past the budget, is kept whole: the verifier loop upstairs
// reports what it cannot shrink.
func chunkHunk(block []string, maxBytes int) [][]string {
	if len(block) == 0 {
		return nil
	}
	oldStart, _, newStart, _, suffix, ok := hunkRange(block[0])
	if !ok {
		return [][]string{block}
	}
	// Glue each "\ No newline" marker to its line: the pair moves as one
	// and measures as one.
	type cell struct {
		lines []string
		size  int
		old   int
		new   int
	}
	var cells []cell
	for _, line := range block[1:] {
		size := len(line)
		old, new := 0, 0
		if len(line) > 0 {
			switch line[0] {
			case ' ', '+':
				if line[0] == ' ' {
					old = 1
				}
				new = 1
			case '-':
				old = 1
			}
		}
		if strings.HasPrefix(line, "\\") && len(cells) > 0 {
			last := &cells[len(cells)-1]
			last.lines = append(last.lines, line)
			last.size += size
			continue
		}
		cells = append(cells, cell{lines: []string{line}, size: size, old: old, new: new})
	}
	var out [][]string
	old, new := oldStart, newStart
	start := 0
	for start < len(cells) {
		end := start
		size := 0
		for end < len(cells) && size+cells[end].size <= maxBytes {
			size += cells[end].size
			end++
		}
		if end == start {
			end = start + 1
		}
		oldLen, newLen := 0, 0
		for _, c := range cells[start:end] {
			oldLen += c.old
			newLen += c.new
		}
		head := fmt.Sprintf("@@ -%d,%d +%d,%d @@%s\n", old, oldLen, new, newLen, suffix)
		chunk := []string{head}
		for _, c := range cells[start:end] {
			chunk = append(chunk, c.lines...)
			old += c.old
			new += c.new
		}
		out = append(out, chunk)
		start = end
	}
	return out
}

// repackContent packs hunk blocks into parts of at most maxBytes of hunk
// content, splitting at hunk boundaries and cutting one oversized hunk
// into line chunks. The repeated header is not charged to the budget:
// the verifier loop upstairs measures the whole rendered prompt and
// shrinks the budget until each part's call fits.
func repackContent(header []string, blocks [][]string, maxBytes int) []partContent {
	if maxBytes < 1 {
		maxBytes = 1
	}
	var out []partContent
	current := partContent{header: header}
	currentBytes := 0
	flush := func() {
		if len(current.blocks) > 0 {
			out = append(out, current)
			current = partContent{header: header}
			currentBytes = 0
		}
	}
	blockBytes := func(block []string) int {
		n := 0
		for _, line := range block {
			n += len(line)
		}
		return n
	}
	for _, block := range blocks {
		if size := blockBytes(block); size > maxBytes {
			flush()
			for _, chunk := range chunkHunk(block, maxBytes) {
				out = append(out, partContent{header: header, blocks: [][]string{chunk}})
			}
			continue
		}
		if currentBytes > 0 && currentBytes+blockBytes(block) > maxBytes {
			flush()
		}
		current.blocks = append(current.blocks, block)
		currentBytes += blockBytes(block)
	}
	flush()
	return out
}

// unitBlocks answers the hunk blocks one unit splits on: its shaped diff
// where it has hunk structure, else synthesised hunks over its body. A
// unit with neither — binary or unreadable content — answers no blocks,
// and the caller carries it whole: such a unit never needs a split, and
// inventing content for it would review bytes the evidence never showed.
func unitBlocks(unit FileUnit) (header []string, blocks [][]string) {
	if len(unit.Diff) > 0 {
		if header, blocks := splitDiffContent(unit.Diff); len(blocks) > 0 {
			return header, blocks
		}
	}
	if len(unit.Body) > 0 {
		return synthBlocks(unit.Path, unit.Body)
	}
	return nil, nil
}
