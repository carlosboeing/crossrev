package intel_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/diff"
	"github.com/carlosboeing/crossrev/internal/intel"
)

// This file sweeps the split path's rendered line numbers: every gutter the
// reviewer reads — whole files, hunks, parts, chunks, the first and last
// chunk, a chunk opening at line 1, a file with no trailing newline — must
// match the head file exactly. The oracle in every case is `git show`, never
// the bytes the test handed in, so a numbering bug cannot hide behind a
// shared fixture.

// gitOut runs git in dir and answers its stdout. The histories below have
// to be the same on every machine, so the operator's own git config is
// taken out of the picture with t.Setenv rather than a per-command
// environment, because internal/archtest reserves os.Environ for
// internal/exec.
func gitOut(t *testing.T, dir string, args ...string) []byte {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not on PATH, so the split gutters cannot be held against `git show`")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			t.Fatalf("git %v: %v\n%s", args, err, ee.Stderr)
		}
		t.Fatalf("git %v: %v", args, err)
	}
	return out
}

// twoRevRepo commits base files, then head files, and answers the repo dir
// with both SHAs. Head files overwrite base files with the same path.
func twoRevRepo(t *testing.T, base, head map[string]string) (dir, baseSHA, headSHA string) {
	t.Helper()
	dir = t.TempDir()
	gitOut(t, dir, "init", "-q")
	gitOut(t, dir, "config", "user.email", "sweep@example.com")
	gitOut(t, dir, "config", "user.name", "sweep")
	write := func(files map[string]string) {
		for path, content := range files {
			full := filepath.Join(dir, path)
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		gitOut(t, dir, "add", "-A")
	}
	write(base)
	gitOut(t, dir, "commit", "-qm", "base", "--allow-empty")
	baseSHA = strings.TrimSpace(string(gitOut(t, dir, "rev-parse", "HEAD")))
	write(head)
	gitOut(t, dir, "commit", "-qm", "head")
	headSHA = strings.TrimSpace(string(gitOut(t, dir, "rev-parse", "HEAD")))
	return dir, baseSHA, headSHA
}

// gitShowLines answers the head file's lines from `git show`, the ground
// truth every rendered gutter is held against.
func gitShowLines(t *testing.T, dir, rev, path string) []string {
	t.Helper()
	out := gitOut(t, dir, "--no-pager", "show", rev+":"+path)
	lines := strings.Split(string(out), "\n")
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	return lines
}

// gitFileDiff answers `git diff` for one path between the two revisions,
// the same bytes production shapes and splits.
func gitFileDiff(t *testing.T, dir, base, head, path string) []byte {
	t.Helper()
	return gitOut(t, dir, "--no-pager", "diff", base, head, "--", path)
}

// assertNumberedMatchesHead holds every new-side gutter in numbered output
// against the `git show` lines: the number must name a head line, and the
// line's bytes after its diff prefix must be that head line exactly.
// Deletions carry no new side and are skipped; headers and the no-newline
// marker carry no gutter and are skipped.
func assertNumberedMatchesHead(t *testing.T, label string, numbered []byte, oracle []string) {
	t.Helper()
	checked := 0
	for _, line := range strings.Split(string(numbered), "\n") {
		// Every body line Numbered emits carries its gutter ahead of " |";
		// a line without one is a bare header (diff --git, index, ---,
		// +++, @@) or the no-newline marker, none of which takes a number.
		sep := strings.Index(line, " |")
		if sep < 0 {
			continue
		}
		fields := strings.Fields(line[:sep])
		if len(fields) != 2 {
			t.Fatalf("%s: gutter holds %q, want an old/new pair: %q", label, line[:sep], line)
		}
		if fields[1] == "-" {
			continue
		}
		n, err := strconv.Atoi(fields[1])
		if err != nil || n < 1 || n > len(oracle) {
			t.Errorf("%s: new gutter %q names no head line (the head file has %d): %q", label, fields[1], len(oracle), line)
			continue
		}
		raw := line[sep+2:]
		body := ""
		if raw != "" {
			body = raw[1:]
		}
		if body != oracle[n-1] {
			t.Errorf("%s: gutter %d shows %q, `git show` line %d is %q", label, n, body, n, oracle[n-1])
			continue
		}
		checked++
	}
	if checked == 0 {
		t.Fatalf("%s: no numbered content line was held against `git show`", label)
	}
}

// splitScope builds a one-file scope the planner splits, with the head body
// and the `git diff` bytes production would shape.
func splitScope(t *testing.T, path string, change core.ChangeKind, body, fileDiff []byte) intel.Scope {
	t.Helper()
	base, head := stubRevisions(t)
	return intel.Scope{
		Base: base, Head: head,
		Engine:   core.FileEngineVersion,
		EngineID: core.FileEngineID(),
		Required: []intel.FileUnit{{
			ID:              core.FileUnitID(path),
			Path:            path,
			Change:          change,
			ContentRevision: head,
			BodyDigest:      core.BodyDigestHex(body),
			Body:            body,
			Available:       true,
			Diff:            fileDiff,
		}},
	}
}

// diffMeasure is the tests' rendered-call measure: file and part diff
// bytes. Small budgets against it force the planner down the split path.
func diffMeasure(call intel.Call) int {
	total := 0
	for _, f := range call.Files {
		total += len(f.Body) + len(f.Diff)
	}
	if call.Part != nil {
		total += len(call.Part.Diff)
	}
	return total
}

// planParts splits one file under a small budget and answers the part
// diffs in order, failing when the file never splits.
func planParts(t *testing.T, scope intel.Scope, pack, hard int) [][]byte {
	t.Helper()
	plan := intel.PlanCalls(scope, nil, intel.PlanOptions{
		Limits: intel.Limits{WindowTokens: 200000, PackBytes: pack, HardBytes: hard},
	}, diffMeasure)
	if plan.HaltReason != "" {
		t.Fatalf("halt = %q, want the file to split", plan.HaltReason)
	}
	if len(plan.Calls) < 2 {
		t.Fatalf("calls = %d, want at least 2 parts under the %d-byte budget", len(plan.Calls), pack)
	}
	var out [][]byte
	for i, call := range plan.Calls {
		if call.Part == nil {
			t.Fatalf("call %d holds whole files, want a part", i)
		}
		out = append(out, call.Part.Diff)
	}
	return out
}

// chunkSpan parses one part diff's @@ header into its old- and new-side
// starts and lengths.
func chunkSpan(t *testing.T, part []byte) (oldStart, oldLen, newStart, newLen int) {
	t.Helper()
	for _, line := range strings.Split(string(part), "\n") {
		if !strings.HasPrefix(line, "@@") {
			continue
		}
		if _, err := fmt.Sscanf(line, "@@ -%d,%d +%d,%d @@", &oldStart, &oldLen, &newStart, &newLen); err != nil {
			t.Fatalf("part header does not parse: %q", line)
		}
		return oldStart, oldLen, newStart, newLen
	}
	t.Fatal("the part carries no @@ header")
	return 0, 0, 0, 0
}

// numberedOldLines answers the old-side (number, content) pairs of one
// numbered rendering in order, skipping additions, which carry none.
func numberedOldLines(t *testing.T, numbered []byte) [][2]any {
	t.Helper()
	return numberedSide(t, numbered, 0)
}

// numberedNewLines answers the new-side (number, content) pairs of one
// numbered rendering in order.
func numberedNewLines(t *testing.T, numbered []byte) [][2]any {
	t.Helper()
	return numberedSide(t, numbered, 1)
}

func numberedSide(t *testing.T, numbered []byte, side int) [][2]any {
	t.Helper()
	var out [][2]any
	for _, line := range strings.Split(string(numbered), "\n") {
		sep := strings.Index(line, " |")
		if sep < 0 {
			continue
		}
		fields := strings.Fields(line[:sep])
		if len(fields) != 2 || fields[side] == "-" {
			continue
		}
		n, err := strconv.Atoi(fields[side])
		if err != nil {
			t.Fatalf("gutter %q is no number: %q", fields[side], line)
		}
		raw := line[sep+2:]
		body := ""
		if raw != "" {
			body = raw[1:]
		}
		out = append(out, [2]any{n, body})
	}
	return out
}

func headLines(prefix string, n int) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "%s line %04d\n", prefix, i)
	}
	return b.String()
}

// A small added file renders whole: its single hunk's gutters match `git
// show` line for line.
func TestSplitGutterWholeFileMatchesHead(t *testing.T) {
	body := headLines("whole", 10)
	dir, base, head := twoRevRepo(t, nil, map[string]string{"hello.go": body})
	oracle := gitShowLines(t, dir, head, "hello.go")
	fileDiff := gitFileDiff(t, dir, base, head, "hello.go")
	assertNumberedMatchesHead(t, "whole file", diff.Parse(fileDiff, core.RevisionPair{}).Numbered(), oracle)
}

// A modified file renders its hunks: gutters across both hunks match `git
// show`, deletions aside.
func TestSplitGutterHunksMatchHead(t *testing.T) {
	baseBody := headLines("base", 60)
	headBody := baseBody
	headBody = strings.Replace(headBody, "base line 0010\nbase line 0011\n", "changed line ten\neleven now reads differently\n", 1)
	headBody = strings.Replace(headBody, "base line 0040\n", "changed line forty\n", 1)
	dir, base, head := twoRevRepo(t,
		map[string]string{"edit.go": baseBody},
		map[string]string{"edit.go": headBody})
	oracle := gitShowLines(t, dir, head, "edit.go")
	fileDiff := gitFileDiff(t, dir, base, head, "edit.go")
	var hunks int
	for _, line := range strings.Split(string(fileDiff), "\n") {
		if strings.HasPrefix(line, "@@") {
			hunks++
		}
	}
	if hunks < 2 {
		t.Fatalf("the fixture diff holds %d hunks, want at least 2 for the hunks case", hunks)
	}
	assertNumberedMatchesHead(t, "hunks", diff.Parse(fileDiff, core.RevisionPair{}).Numbered(), oracle)
}

// A file past the budget splits at hunk boundaries: every part repeats the
// header with its hunk subset, and every part's gutters match `git show`.
// Part headers stay byte-identical to the original hunks — nothing chunks.
func TestSplitGutterPartsMatchHead(t *testing.T) {
	var base, head strings.Builder
	for i := 1; i <= 300; i++ {
		fmt.Fprintf(&base, "base line %04d\n", i)
		if i%15 == 0 {
			fmt.Fprintf(&head, "edited line %04d\n", i)
		} else {
			fmt.Fprintf(&head, "base line %04d\n", i)
		}
	}
	dir, baseRev, headRev := twoRevRepo(t,
		map[string]string{"parts.go": base.String()},
		map[string]string{"parts.go": head.String()})
	oracle := gitShowLines(t, dir, headRev, "parts.go")
	fileDiff := gitFileDiff(t, dir, baseRev, headRev, "parts.go")
	headBytes := []byte(head.String())
	parts := planParts(t, splitScope(t, "parts.go", core.ChangeModified, headBytes, fileDiff), 3000, 6000)
	var original []string
	for _, line := range strings.Split(string(fileDiff), "\n") {
		if strings.HasPrefix(line, "@@") {
			original = append(original, line)
		}
	}
	var carried []string
	for i, part := range parts {
		assertNumberedMatchesHead(t, fmt.Sprintf("part %d", i), diff.Parse(part, core.RevisionPair{}).Numbered(), oracle)
		for _, line := range strings.Split(string(part), "\n") {
			if strings.HasPrefix(line, "@@") {
				carried = append(carried, line)
			}
		}
	}
	if len(carried) != len(original) {
		t.Fatalf("parts carry %d hunk headers, want the original %d uncut", len(carried), len(original))
	}
	for i := range original {
		if carried[i] != original[i] {
			t.Fatalf("part hunk %d = %q, want original %q", i, carried[i], original[i])
		}
	}
}

// One oversized hunk chunks into lines: every chunk's gutters match `git
// show` across the whole file.
func TestSplitGutterChunksMatchHead(t *testing.T) {
	var head strings.Builder
	for i := 1; i <= 400; i++ {
		fmt.Fprintf(&head, "var x%04d = %d\n", i, i)
	}
	dir, base, headRev := twoRevRepo(t, nil, map[string]string{"big.go": head.String()})
	oracle := gitShowLines(t, dir, headRev, "big.go")
	fileDiff := gitFileDiff(t, dir, base, headRev, "big.go")
	headBytes := []byte(head.String())
	parts := planParts(t, splitScope(t, "big.go", core.ChangeAdded, headBytes, fileDiff), 3000, 6000)
	for i, part := range parts {
		assertNumberedMatchesHead(t, fmt.Sprintf("chunk %d", i), diff.Parse(part, core.RevisionPair{}).Numbered(), oracle)
	}
}

// The first chunk opens where its header declares: its first numbered line
// carries the header's start, and the next chunk continues the run.
func TestSplitGutterFirstChunkMatchesHead(t *testing.T) {
	var head strings.Builder
	for i := 1; i <= 400; i++ {
		fmt.Fprintf(&head, "var x%04d = %d\n", i, i)
	}
	dir, base, headRev := twoRevRepo(t, nil, map[string]string{"first.go": head.String()})
	oracle := gitShowLines(t, dir, headRev, "first.go")
	fileDiff := gitFileDiff(t, dir, base, headRev, "first.go")
	headBytes := []byte(head.String())
	parts := planParts(t, splitScope(t, "first.go", core.ChangeAdded, headBytes, fileDiff), 3000, 6000)
	_, _, start, length := chunkSpan(t, parts[0])
	lines := numberedNewLines(t, diff.Parse(parts[0], core.RevisionPair{}).Numbered())
	if len(lines) == 0 {
		t.Fatal("the first chunk numbers no new-side line")
	}
	if lines[0][0].(int) != start {
		t.Errorf("the first chunk's first gutter is %d, want the header's start %d", lines[0][0].(int), start)
	}
	if lines[0][1].(string) != oracle[start-1] {
		t.Errorf("the first chunk opens with %q, `git show` line %d is %q", lines[0][1].(string), start, oracle[start-1])
	}
	_, _, next, _ := chunkSpan(t, parts[1])
	if next != start+length {
		t.Errorf("the second chunk starts at %d, want %d where the first chunk's run ends", next, start+length)
	}
}

// The last chunk closes the file: its final numbered line is the head
// file's final line, and its header span ends there.
func TestSplitGutterLastChunkMatchesHead(t *testing.T) {
	var head strings.Builder
	for i := 1; i <= 400; i++ {
		fmt.Fprintf(&head, "var x%04d = %d\n", i, i)
	}
	dir, base, headRev := twoRevRepo(t, nil, map[string]string{"last.go": head.String()})
	oracle := gitShowLines(t, dir, headRev, "last.go")
	fileDiff := gitFileDiff(t, dir, base, headRev, "last.go")
	headBytes := []byte(head.String())
	parts := planParts(t, splitScope(t, "last.go", core.ChangeAdded, headBytes, fileDiff), 3000, 6000)
	last := parts[len(parts)-1]
	_, _, start, length := chunkSpan(t, last)
	if start+length != len(oracle)+1 {
		t.Errorf("the last chunk spans to new line %d, want past the head file's %d lines", start+length, len(oracle))
	}
	lines := numberedNewLines(t, diff.Parse(last, core.RevisionPair{}).Numbered())
	if len(lines) == 0 {
		t.Fatal("the last chunk numbers no new-side line")
	}
	end := lines[len(lines)-1]
	if end[0].(int) != len(oracle) {
		t.Errorf("the last chunk's final gutter is %d, want the head file's last line %d", end[0].(int), len(oracle))
	}
	if end[1].(string) != oracle[len(oracle)-1] {
		t.Errorf("the last chunk ends with %q, `git show` ends with %q", end[1].(string), oracle[len(oracle)-1])
	}
}

// A chunked hunk opening at line 1 numbers from 1 on both sides: the
// leading deletion-only chunks open at old line 1 against the base, and
// the first added line opens at new line 1 against the head. The file top
// is no off-by-one behind a header the splitter rewrote.
func TestSplitGutterChunkAtLineOneMatchesHead(t *testing.T) {
	var base, head strings.Builder
	for i := 1; i <= 300; i++ {
		fmt.Fprintf(&base, "base line %04d\n", i)
	}
	for i := 1; i <= 150; i++ {
		fmt.Fprintf(&head, "rewritten line %04d\n", i)
	}
	for i := 151; i <= 300; i++ {
		fmt.Fprintf(&head, "base line %04d\n", i)
	}
	dir, baseRev, headRev := twoRevRepo(t,
		map[string]string{"top.go": base.String()},
		map[string]string{"top.go": head.String()})
	baseOracle := gitShowLines(t, dir, baseRev, "top.go")
	oracle := gitShowLines(t, dir, headRev, "top.go")
	fileDiff := gitFileDiff(t, dir, baseRev, headRev, "top.go")
	headBytes := []byte(head.String())
	parts := planParts(t, splitScope(t, "top.go", core.ChangeModified, headBytes, fileDiff), 3000, 6000)
	oldStart, _, newStart, _ := chunkSpan(t, parts[0])
	if oldStart != 1 || newStart != 1 {
		t.Fatalf("the first chunk spans -%d +%d, want both sides opening at 1", oldStart, newStart)
	}
	oldLines := numberedOldLines(t, diff.Parse(parts[0], core.RevisionPair{}).Numbered())
	if len(oldLines) == 0 {
		t.Fatal("the first chunk numbers no old-side line")
	}
	if oldLines[0][0].(int) != 1 || oldLines[0][1].(string) != baseOracle[0] {
		t.Errorf("the first chunk's old side opens with gutter %d showing %q, want 1 showing %q",
			oldLines[0][0].(int), oldLines[0][1].(string), baseOracle[0])
	}
	var firstNew [2]any
	var found bool
outer:
	for _, part := range parts {
		for _, pair := range numberedNewLines(t, diff.Parse(part, core.RevisionPair{}).Numbered()) {
			firstNew, found = pair, true
			break outer
		}
	}
	if !found {
		t.Fatal("no chunk numbers a new-side line")
	}
	if firstNew[0].(int) != 1 || firstNew[1].(string) != oracle[0] {
		t.Errorf("the first new-side gutter is %d showing %q, want 1 showing %q",
			firstNew[0].(int), firstNew[1].(string), oracle[0])
	}
}

// A file with no trailing newline chunks with its marker glued to its
// line: every gutter still matches `git show`, and no chunk boundary
// strands the marker from the line it annotates.
func TestSplitGutterNoTrailingNewlineMatchesHead(t *testing.T) {
	var head strings.Builder
	for i := 1; i <= 300; i++ {
		fmt.Fprintf(&head, "var y%04d = %d\n", i, i)
	}
	head.WriteString("final line without a newline")
	dir, base, headRev := twoRevRepo(t, nil, map[string]string{"noeol.go": head.String()})
	oracle := gitShowLines(t, dir, headRev, "noeol.go")
	if oracle[len(oracle)-1] != "final line without a newline" {
		t.Fatalf("`git show` ends with %q, want the unterminated line", oracle[len(oracle)-1])
	}
	fileDiff := gitFileDiff(t, dir, base, headRev, "noeol.go")
	if !strings.Contains(string(fileDiff), "\\ No newline") {
		t.Fatalf("the fixture diff carries no no-newline marker:\n%s", fileDiff)
	}
	headBytes := []byte(head.String())
	parts := planParts(t, splitScope(t, "noeol.go", core.ChangeAdded, headBytes, fileDiff), 3000, 6000)
	var markerChunks int
	for i, part := range parts {
		assertNumberedMatchesHead(t, fmt.Sprintf("chunk %d", i), diff.Parse(part, core.RevisionPair{}).Numbered(), oracle)
		lines := strings.Split(string(part), "\n")
		for j, line := range lines {
			if !strings.HasPrefix(line, "\\") {
				continue
			}
			markerChunks++
			if j == 0 || strings.HasPrefix(lines[j-1], "@@") || strings.HasPrefix(lines[j-1], "diff --git") ||
				strings.HasPrefix(lines[j-1], "--- ") || strings.HasPrefix(lines[j-1], "+++ ") {
				t.Errorf("chunk %d strands the no-newline marker from its line", i)
			}
		}
	}
	if markerChunks != 1 {
		t.Errorf("the marker renders in %d chunks, want 1 (glued to its line)", markerChunks)
	}
}
