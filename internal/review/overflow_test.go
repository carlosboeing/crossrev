package review_test

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"testing"

	"github.com/carlosboeing/crossrev/internal/exec"
	"github.com/carlosboeing/crossrev/internal/prstate"
	"github.com/carlosboeing/crossrev/internal/review"
)

// This repository's own mean tracked-path length, measured 2026-09-20 with
// `git ls-files | awk '{s+=length($0); n++} END {print s/n}'` (780 files,
// mean 32.5, median 30). The overflow fixtures below must sit near it: the
// path table is what fills the comment, so a fixture of short paths would
// pass while a real pull request halted.
const overflowRepoMeanPathLen = 32.5

// requiredFilesWithRealisticPaths writes n required head files whose paths
// average near this repository's own mean, and answers them in scope order.
// A fixture averaging far below the repo mean proves nothing about the
// comment cap, so the helper refuses to build one.
func requiredFilesWithRealisticPaths(t *testing.T, e *env, n int) []string {
	t.Helper()
	paths := make([]string, n)
	total := 0
	for i := range paths {
		paths[i] = fmt.Sprintf("api/billing/invoices/item_%03d.go", i)
		total += len(paths[i])
		writeRequiredHead(e, paths[i], "package invoices\n")
	}
	sort.Strings(paths)
	avg := float64(total) / float64(n)
	if avg < overflowRepoMeanPathLen*0.75 || avg > overflowRepoMeanPathLen*1.25 {
		t.Fatalf("fixture paths average %.1f characters, want within 25%% of this repository's own %.1f", avg, overflowRepoMeanPathLen)
	}
	return paths
}

// requiredFilesExceeding writes required head files whose paths alone sum
// past cap bytes, and answers them in scope order. The compact generation
// embeds every path, so it necessarily exceeds the cap too — the bound is
// bytes of rendered comment, and this fixture is built to exceed it rather
// than to a file count.
func requiredFilesExceeding(t *testing.T, e *env, cap int) []string {
	t.Helper()
	var paths []string
	total := 0
	for i := 0; total <= cap; i++ {
		path := fmt.Sprintf("api/billing/invoices/item_%04d.go", i)
		writeRequiredHead(e, path, "package invoices\n")
		paths = append(paths, path)
		total += len(path)
	}
	sort.Strings(paths)
	return paths
}

// scriptOverflowAnswers appends one no-issue answer per 40-file batch over
// the given paths in order. Batches pack in path order, so under correct
// carry-forward these answer exactly the admitted batches.
func scriptOverflowAnswers(t *testing.T, e *env, paths []string) {
	t.Helper()
	for i := 0; i < len(paths); i += 40 {
		end := i + 40
		if end > len(paths) {
			end = len(paths)
		}
		e.runner.script = append(e.runner.script, exec.Result{ExitCode: 0, Stdout: claudeStdout(batchAnswerFor(t, paths[i:end]))})
	}
}

// unitSection matches one numbered file section in a batch prompt
// (`### 1. `path` — ...`), the record of what the reviewer was given.
var unitSection = regexp.MustCompile("(?m)^### \\d+\\. `([^`]+)`")

// reviewedPaths reads every numbered file section out of the given prompts,
// in order. It reads the harness input, not the fixture, so it reports what
// was actually re-examined rather than what the scope claims.
func reviewedPaths(prompts []string) []string {
	var out []string
	for _, prompt := range prompts {
		for _, m := range unitSection.FindAllStringSubmatch(prompt, -1) {
			out = append(out, m[1])
		}
	}
	return out
}

// checkNoRereview fails the test when any path prompted in this pass was
// already reviewed in an earlier one. Repeated sections within this pass's
// own prompts (a retry re-sends its batch) collapse first, so only
// cross-pass re-review — the previous generation telling the pass nothing —
// fails.
func checkNoRereview(t *testing.T, pass int, seen map[string]bool, prompts []string) {
	t.Helper()
	fresh := map[string]bool{}
	for _, path := range reviewedPaths(prompts) {
		if fresh[path] {
			continue
		}
		fresh[path] = true
		if seen[path] {
			t.Fatalf("pass %d re-reviewed %s; the previous generation told it nothing", pass, path)
		}
	}
	for path := range fresh {
		seen[path] = true
	}
}

// 900 files, degrading across successive runs, converging rather than
// reviewing the same first 400 forever. Paths are realistic lengths: a
// fixture of short paths would pass while a real pull request halted, because
// the path table is what fills the comment.
func TestOverflowConvergesAcrossPassesRatherThanLooping(t *testing.T) {
	const files = 900
	e := newEnv(t)
	e.cfg = mustConfig(t, "coverage:\n  store: marker\n  on_overflow: degrade\n")
	paths := requiredFilesWithRealisticPaths(t, e, files)
	prompts := capturePrompt(e)

	seen := map[string]bool{}
	degraded := false
	readFrom := 0
	for pass := 1; pass <= 4; pass++ {
		var remaining []string
		for _, path := range paths {
			if !seen[path] {
				remaining = append(remaining, path)
			}
		}
		admit := remaining
		if len(admit) > 400 {
			admit = admit[:400]
		}
		scriptOverflowAnswers(t, e, admit)
		got := runLeg(t, e, e.request(t))
		checkNoRereview(t, pass, seen, (*prompts)[readFrom:])
		readFrom = len(*prompts)
		if got.Err != nil {
			t.Fatalf("pass %d: Run: %v", pass, got.Err)
		}
		if d, ok := got.Marker.CoverageDegraded.Get(); ok && d {
			degraded = true
		}
		if len(seen) == files {
			if !degraded {
				t.Fatal("the fixture never provoked degradation, so it proves nothing")
			}
			return
		}
	}
	t.Fatalf("900 files did not converge in four passes; %d accounted for", len(seen))
}

// The other side of the bound. Degradation is bounded, not infinitely elastic,
// and a pass that cannot represent its own coverage must say so.
//
// The fixture is built to exceed CommentCap rather than to a file count: the
// bound is bytes of rendered comment, and how many paths reach it depends on
// their length, on the findings payload and on the prose beside them.
func TestARenderedCommentTooSmallForOneCompactGenerationExhausts(t *testing.T) {
	e := newEnv(t)
	e.cfg = mustConfig(t, "coverage:\n  store: marker\n  on_overflow: degrade\n")
	requiredFilesExceeding(t, e, prstate.CommentCap)

	got := runLeg(t, e, e.request(t))
	if got.Err != nil {
		t.Fatalf("a pull request that cannot fit one compact generation errored instead of halting: %v", got.Err)
	}
	if got.Outcome != review.OutcomeHalted {
		t.Fatalf("Outcome = %q, want halted (a pull request that cannot fit one compact generation must halt, not loop or converge)", got.Outcome)
	}
	if got.Reason != prstate.CoverageStopLimit {
		t.Fatalf("Reason = %q, want %q", got.Reason, prstate.CoverageStopLimit)
	}
	stop, ok := got.Marker.CoverageStop.Get()
	if !ok {
		t.Fatal("halted marker carries no coverage_stop")
	}
	if stop.Limit != prstate.CoverageStopLimit {
		t.Fatalf("stop limit %q", stop.Limit)
	}
	if stop.MeasuredBytes <= prstate.CommentCap {
		t.Fatalf("the halt reports %d measured bytes against a %d cap; the operator cannot see why it stopped",
			stop.MeasuredBytes, prstate.CommentCap)
	}
}

// A degraded generation still carries its verdicts, so the next pass resumes
// them instead of re-judging: coverage completeness holds and carry-forward
// works. Verdicts are persisted without the evidence behind them — what is
// lost is detail.
func TestADegradedGenerationStillConvergesAndStillResumes(t *testing.T) {
	e := newEnv(t)
	e.cfg = mustConfig(t, "coverage:\n  store: marker\n  on_overflow: degrade\n")
	paths := requiredFilesWithRealisticPaths(t, e, 410)
	prompts := capturePrompt(e)

	scriptOverflowAnswers(t, e, paths[:400])
	first := runLeg(t, e, e.request(t))
	seen := map[string]bool{}
	checkNoRereview(t, 1, seen, *prompts)
	readFrom := len(*prompts)
	if first.Err != nil {
		t.Fatalf("first Run: %v", first.Err)
	}
	if first.Outcome != review.OutcomeHalted {
		t.Fatalf("first Outcome = %q, want halted (410 files carry 10 past the 400-file pass budget)", first.Outcome)
	}
	if d, ok := first.Marker.CoverageDegraded.Get(); !ok || !d {
		t.Fatal("the fixture never provoked degradation, so it proves nothing")
	}

	published := capturePublished(t)
	scriptOverflowAnswers(t, e, paths[400:])
	second := runLeg(t, e, e.request(t))
	checkNoRereview(t, 2, seen, (*prompts)[readFrom:])
	if second.Err != nil {
		t.Fatalf("re-drive Run: %v", second.Err)
	}
	if second.Outcome != review.OutcomeInvoked {
		t.Fatalf("re-drive Outcome = %q, want invoked", second.Outcome)
	}
	if !prstate.MarkerConverges(second.Marker) {
		t.Error("the settled marker does not converge after the re-drive covers the remainder")
	}
	var converged bool
	for _, label := range e.forge.labelsAdded {
		if label == "crossrev/converged" {
			converged = true
		}
	}
	if !converged {
		t.Errorf("labels added = %v, want crossrev/converged after the re-drive covers the remainder", e.forge.labelsAdded)
	}

	// The carried verdicts resume without their evidence: completeness
	// holds, detail does not.
	if len(*published) == 0 {
		t.Fatal("no complete generation published on the re-drive")
	}
	last := (*published)[len(*published)-1]
	carried := suppliedRecordFor(t, last, paths[0])
	if _, ok := carried.Verdict.Get(); !ok {
		t.Fatalf("the carried record for %s lost its verdict; resumption carried nothing", paths[0])
	}
	if len(carried.Evidence) != 0 || len(carried.FindingIDs) != 0 || carried.Reason.Present() {
		t.Fatalf("the carried record for %s kept its detail: %+v", paths[0], carried)
	}
}

// The ref store has no comment cap, so the overflow path is the marker
// store's alone. The same 900-file fixture converges there with no
// generation ever degraded — a test that passed for both would be measuring
// nothing.
func TestTheSameFixtureUnderTheRefStoreNeverDegrades(t *testing.T) {
	const files = 900
	e := newEnv(t)
	e.cfg = mustConfig(t, "coverage:\n  store: refs\n  on_overflow: degrade\n")
	paths := requiredFilesWithRealisticPaths(t, e, files)
	prompts := capturePrompt(e)

	seen := map[string]bool{}
	readFrom := 0
	for pass := 1; pass <= 4; pass++ {
		var remaining []string
		for _, path := range paths {
			if !seen[path] {
				remaining = append(remaining, path)
			}
		}
		admit := remaining
		if len(admit) > 400 {
			admit = admit[:400]
		}
		scriptOverflowAnswers(t, e, admit)
		got := runLeg(t, e, e.request(t))
		checkNoRereview(t, pass, seen, (*prompts)[readFrom:])
		readFrom = len(*prompts)
		if got.Err != nil {
			t.Fatalf("pass %d: Run: %v", pass, got.Err)
		}
		if d, ok := got.Marker.CoverageDegraded.Get(); ok && d {
			t.Fatalf("pass %d degraded under the ref store, which has no comment cap", pass)
		}
		if len(seen) == files {
			return
		}
	}
	t.Fatalf("900 files did not converge in four passes; %d accounted for", len(seen))
}

// An initial-exhaustion halt keeps the prior coverage claim: the halted
// marker still names the last complete generation, so the halt's own "the
// last complete generation stands" is true and a re-drive resumes from it
// instead of failing closed on a corrupt claim. The seed is a pass that
// halted at the 400-file budget with a complete generation on its marker;
// the scope then grows past what one compact generation can fit, so the
// re-drive's initial publication exhausts before any batch runs.
func TestInitialExhaustionKeepsThePriorCoverageClaim(t *testing.T) {
	e := newEnv(t)
	e.cfg = mustConfig(t, "coverage:\n  store: marker\n  on_overflow: degrade\n")
	paths := requiredFilesWithRealisticPaths(t, e, 410)

	scriptOverflowAnswers(t, e, paths[:400])
	first := runLeg(t, e, e.request(t))
	if first.Err != nil {
		t.Fatalf("seed Run: %v", first.Err)
	}
	if first.Outcome != review.OutcomeHalted {
		t.Fatalf("seed Outcome = %q, want halted (410 files carry 10 past the 400-file pass budget)", first.Outcome)
	}
	prior, claimed, err := first.Marker.CoverageHandle()
	if err != nil {
		t.Fatalf("seed marker's coverage claim does not validate: %v", err)
	}
	if !claimed {
		t.Fatal("seed marker carries no coverage claim, so the halt below proves nothing")
	}
	if prior.Gen <= 0 {
		t.Fatalf("seed marker names gen %d, want a published generation", prior.Gen)
	}

	// Grow the scope past what one compact generation can fit. The new
	// paths are distinct from the seed's, and the head does not move, so
	// the next run re-drives the halted pass rather than starting a new one.
	requiredFilesExceeding(t, e, prstate.CommentCap)

	halted := runLeg(t, e, e.request(t))
	if halted.Err != nil {
		t.Fatalf("a re-drive past one compact generation errored instead of halting: %v", halted.Err)
	}
	if halted.Outcome != review.OutcomeHalted {
		t.Fatalf("Outcome = %q, want halted", halted.Outcome)
	}
	if halted.Reason != prstate.CoverageStopLimit {
		t.Fatalf("Reason = %q, want %q", halted.Reason, prstate.CoverageStopLimit)
	}
	stop, ok := halted.Marker.CoverageStop.Get()
	if !ok {
		t.Fatal("halted marker carries no coverage_stop")
	}
	if stop.Limit != prstate.CoverageStopLimit {
		t.Fatalf("stop limit %q", stop.Limit)
	}
	if stop.MeasuredBytes <= prstate.CommentCap {
		t.Fatalf("the halt reports %d measured bytes against a %d cap; the operator cannot see why it stopped",
			stop.MeasuredBytes, prstate.CommentCap)
	}

	kept, claimed, err := halted.Marker.CoverageHandle()
	if err != nil {
		t.Fatalf("the halted marker's coverage claim is corrupt, so the last complete generation does not stand: %v", err)
	}
	if !claimed {
		t.Fatal("the halted marker lost its coverage claim; the last complete generation does not stand")
	}
	if kept.Gen != prior.Gen {
		t.Fatalf("the halted marker names gen %d, want the prior gen %d", kept.Gen, prior.Gen)
	}
	if kept.Location != prstate.HandleMarker {
		t.Fatalf("the halted marker names location %q, want %q", kept.Location, prstate.HandleMarker)
	}
	if halted.Marker.CoverageCommit.Present() {
		t.Fatal("the halted marker gained a commit SHA the marker store never writes")
	}
	behind, err := prstate.NewMarkerStore(nil, prstate.OverflowDegrade, nil).ReadGeneration(context.Background(), prstate.SlotRef{}, kept)
	if err != nil {
		t.Fatalf("the generation the halted marker names does not read back: %v", err)
	}
	if behind.Gen != prior.Gen {
		t.Fatalf("the generation behind the halted marker is gen %d, want the prior gen %d", behind.Gen, prior.Gen)
	}

	again := runLeg(t, e, e.request(t))
	if again.Err != nil {
		t.Fatalf("the re-drive after the halt failed closed instead of resuming: %v", again.Err)
	}
	if again.Outcome != review.OutcomeHalted {
		t.Fatalf("re-drive Outcome = %q, want halted", again.Outcome)
	}
}
