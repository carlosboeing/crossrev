package review

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/harness"
	"github.com/carlosboeing/crossrev/internal/intel"
	"github.com/carlosboeing/crossrev/internal/policy"
	"github.com/carlosboeing/crossrev/internal/prstate"
	"github.com/carlosboeing/crossrev/internal/ui"
)

// Finding is one review finding as the comment and summary renderers read it.
type Finding struct {
	ID            string  `json:"id"`
	Path          string  `json:"path"`
	Line          int     `json:"line"`
	Side          string  `json:"side"`
	Severity      string  `json:"severity"`
	Category      string  `json:"category"`
	PreExisting   bool    `json:"pre_existing"`
	Title         string  `json:"title"`
	Why           string  `json:"why"`
	Fix           string  `json:"fix"`
	Anchor        string  `json:"anchor"`
	AnchorKind    string  `json:"anchor_kind"`
	AnchorReason  string  `json:"anchor_reason"`
	ThreadID      *string `json:"thread_id"`
	RootCommentID *int64  `json:"root_comment_id"`
}

// RenderContext is the repository-level values the summary comment reads
// (CTX_REPO, CTX_PR, CTX_MIN_FIX_SEVERITY, CTX_MAX_PASSES_PER_CYCLE).
type RenderContext struct {
	Repo    string
	PR      int
	MinFix  string
	MaxPass int
	// NoChanges renders the pull request whose head matches its base, which
	// git and GitHub agree changes nothing. It is a state of the pull
	// request rather than a verdict on its code, so it replaces the whole
	// body: the findings table, the empty-review sentence and the
	// run-details table all describe a review that did not happen.
	NoChanges bool
	// BaseRef and HeadRef name the two branches being compared, for the
	// NoChanges body only.
	BaseRef string
	HeadRef string
	// Coverage carries the pass's own convergence counts for the
	// coverage footnote. Nil means no convergence was computed — the
	// frozen path — so the summary carries no footnote.
	Coverage *CoverageCounts
	// Skipped holds each generated file packing skipped, ready to render.
	// A skip opens the summary as a warning above the verdict alert.
	Skipped []SkippedFile
	// Excluded names the paths repository policy or the backlog rule
	// removed: one line under the footnote, information rather than a
	// warning. Skipped paths are not listed here; the warning carries them.
	Excluded []string
}

// SkippedFile is one generated file the pass did not review, ready to
// render.
type SkippedFile struct {
	// Path is the file's current path.
	Path string
	// Signal is the built-in rule that matched: lockfile, bundle-name,
	// header or minified.
	Signal string
	// Bytes is the file's byte size.
	Bytes int
	// Excerpt quotes the matched marker line for a header signal: sanitized
	// and capped by intel.HeaderExcerpt. Empty for the other signals.
	Excerpt string
}

// CoverageCounts is how many of the required files the pass reviewed, as
// its own convergence counted them. The footnote reads the counts here
// rather than out of the marker, so the sentence is the same whichever
// store published the generation.
type CoverageCounts struct {
	Covered int
	// Required counts the files the pass had to review. Skipped files are
	// not among them: they moved to the exclusion record.
	Required int
	// Excluded counts every path removed from the required set — policy,
	// backlog and skips — so the denominator Required+Excluded is every
	// changed file.
	Excluded int
}

const emDash = "—"

// SeverityEmoji is run_severity_emoji (lib/run.sh:371-378).
func SeverityEmoji(severity string) string {
	switch severity {
	case "high":
		return "🔴"
	case "medium":
		return "🟠"
	case "low":
		return "🔵"
	default:
		return "⚪"
	}
}

// CategoryEmoji is run_category_emoji (lib/run.sh:383-393).
func CategoryEmoji(category string) string {
	switch category {
	case "correctness":
		return "🐛"
	case "security":
		return "🔒"
	case "performance":
		return "⚡"
	case "maintainability":
		return "🧹"
	case "testing":
		return "🧪"
	case "docs":
		return "📄"
	default:
		return "❓"
	}
}

// FindingLabel is run_finding_label (lib/run.sh:416-422).
func FindingLabel(f Finding) string {
	return fmt.Sprintf("%s [%s · %s]", SeverityEmoji(orQ(f.Severity)), ucFirst(orQ(f.Severity)), ucFirst(orQ(f.Category)))
}

// ActionableCount is run_actionable (lib/run.sh:356-366).
func ActionableCount(findings []Finding, minFix string) int {
	bar, err := core.ParseSeverity(minFix)
	if err != nil {
		bar = ""
	}
	n := 0
	for _, f := range findings {
		sev, err := core.ParseSeverity(f.Severity)
		if err != nil {
			continue
		}
		if policy.ShouldFix(sev, bar, f.PreExisting) {
			n++
		}
	}
	return n
}

// SameModel is _same_model (lib/run.sh:1455-1461).
func SameModel(want, got string) bool {
	want = foldASCII(want)
	got = foldASCII(got)
	if want == "" || got == "" {
		return false
	}
	return strings.Contains(got, want) || strings.Contains(want, got)
}

// Elapsed is _elapsed (lib/run.sh:1414-1421).
func Elapsed(from, to string) string {
	if !digitsOnly(from) || !digitsOnly(to) {
		return emDash
	}
	a, _ := strconv.Atoi(from)
	b, _ := strconv.Atoi(to)
	secs := b - a
	if secs < 0 {
		return emDash
	}
	if secs < 60 {
		return fmt.Sprintf("%ds", secs)
	}
	return fmt.Sprintf("%dm %ds", secs/60, secs%60)
}

// Thousands is _thousands (lib/run.sh:1429-1436).
func Thousands(n string) string {
	if !digitsOnly(n) {
		return emDash
	}
	out := ""
	for len(n) > 3 {
		out = "," + n[len(n)-3:] + out
		n = n[:len(n)-3]
	}
	return n + out
}

// URLPath is _url_path (lib/run.sh:1609-1611).
func URLPath(path string) string {
	parts := strings.Split(path, "/")
	for i, p := range parts {
		parts[i] = encodeURI(p)
	}
	return strings.Join(parts, "/")
}

func encodeURI(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if isURIUnreserved(c) {
			b.WriteByte(c)
			continue
		}
		fmt.Fprintf(&b, "%%%02X", c)
	}
	return b.String()
}

func isURIUnreserved(c byte) bool {
	switch {
	case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		return true
	case c == '-' || c == '.' || c == '_' || c == '~':
		return true
	}
	return false
}

// CommentBody is _review_comment_body (lib/run.sh:1373-1390).
func CommentBody(f Finding, pass int, harn, model, minFix string) string {
	note := fmt.Sprintf("Below this repository's `min_fix_severity` (%s), so it is reported and left to a human.", minFix)
	if f.PreExisting {
		note = "A real bug, but this pull request did not introduce it, so it is reported here and never fixed here."
	} else if shouldFixFinding(f, minFix) {
		note = fmt.Sprintf("At or above this repository's `min_fix_severity` (%s), so the resolve leg may change code for it.", minFix)
	}
	modelBit := ""
	if model != "" {
		modelBit = " (" + model + ")"
	}
	marker := ""
	if id, err := prstate.ParseFindingID(f.ID); err == nil {
		marker = prstate.EncodeFindingMarker(id, pass, core.LegReview)
	}
	return fmt.Sprintf("#### %s %s\n\n%s\n\n**Fix:** %s\n\n<sub>%s · crossrev pass %s, reviewed by %s%s. A second agent now verifies this point and either fixes it, defers it, or explains why it is wrong.</sub>%s",
		FindingLabel(f), oneLine(f.Title),
		f.Why, f.Fix,
		note, strconv.Itoa(pass), harn, modelBit,
		marker)
}

// SummaryBody is _review_summary_body (lib/run.sh:1675-1730).
func SummaryBody(findings []Finding, marker prstate.Marker, ctx RenderContext) string {
	n := len(findings)
	actionable := ActionableCount(findings, ctx.MinFix)
	verdict := "issues-remain"
	if v, ok := marker.Verdict.Get(); ok && v != "" {
		verdict = v
	}
	blocked, _ := marker.BlockedReason.Get()
	pass := marker.Pass
	if pass == 0 {
		pass = 1
	}

	var b strings.Builder
	fmt.Fprintf(&b, "## crossrev review — %s\n\n", PassLabel(pass, ctx.MaxPass))

	if ctx.NoChanges {
		b.WriteString(noChangesBody(ctx))
		return b.String()
	}

	// A skip is the first block of the summary, above the verdict alert,
	// because that alert is the first sentence a person reads — and a
	// converged pass with a skip still says so at the top.
	if len(ctx.Skipped) > 0 {
		b.WriteString(skipWarning(ctx.Skipped))
	}

	noun := "findings"
	if n == 1 {
		noun = "finding"
	}
	switch verdict {
	case "converged":
		b.WriteString(alert("TIP", fmt.Sprintf("**Converged.** Nothing at or above `min_fix_severity` (%s) remains, so the loop stops here. Findings below the threshold, and pre-existing ones, are reported but cannot keep the loop alive — a loop that cannot converge because of a naming quibble is one nobody leaves switched on.", ctx.MinFix)))
	case "blocked":
		reason := blocked
		if reason == "" {
			reason = "No reason was given."
		}
		b.WriteString(alert("WARNING", fmt.Sprintf("**The review could not be completed:** %s The loop halts here and a human is needed. Nothing in this comment is a judgement about the code.", reason)))
	default:
		b.WriteString(alert("CAUTION", fmt.Sprintf("**%d %s need resolving.** A second agent now verifies every finding below against the codebase and either fixes it, skips it, defers it, or explains why it is wrong. It may change code for the %d at or above `min_fix_severity` (%s); the rest are verified and reported, never silently dropped.", n, noun, actionable, ctx.MinFix)))
	}

	fmt.Fprintf(&b, "Verdict: **%s**.\n\n", verdict)

	if n == 0 {
		b.WriteString("No findings. Low-severity and pre-existing issues would be listed here too, so this is an empty review rather than a filtered one.\n\n")
	} else {
		sha, _ := marker.HeadSHA.Get()
		b.WriteString(findingsTable(findings, ctx.Repo, sha))
	}
	b.WriteString(coverageFootnote(marker, ctx))
	b.WriteString(exclusionLine(ctx.Excluded))

	unanchored := 0
	if u, ok := marker.Unanchored.Get(); ok {
		unanchored = u
	}
	switch {
	case unanchored == 1:
		b.WriteString("One finding could not be anchored to a line of the diff, so it is a top-level comment on this pull request naming its location instead. Its reply will land there too, because there is no review thread to put one in.\n\n")
	case unanchored > 1:
		fmt.Fprintf(&b, "%d findings could not be anchored to a line of the diff, so they are top-level comments on this pull request naming their locations instead. Their replies will land there too, because there are no review threads to put them in.\n\n", unanchored)
	}

	b.WriteString(runDetails(marker, "review"))
	return b.String()
}

// coverageFootnote renders one human sentence for the coverage the pass
// published: how many of the changed files it reviewed, and at which head.
// The counts come from the caller — the pass's own convergence — so the
// sentence reads the same on either store: a ref-store claim names a
// generation render cannot count without a store client, but the caller
// already counted it. The marker still gates the sentence: only a claimed,
// well-formed handle gets one. Anything without something to count — no
// convergence, no claim, a corrupt claim, no head — carries no footnote,
// so the frozen summary bytes stay exactly as they were and a summary a
// reviewer reads never carries a machine-facing generation line.
//
// The denominator is every changed file, required plus excluded, so the
// sentence never reads "6 of 6" beside a warning about an unread file. A
// skip adds its own pointer to the warning above.
func coverageFootnote(marker prstate.Marker, ctx RenderContext) string {
	cov := ctx.Coverage
	if cov == nil {
		return ""
	}
	if _, claimed, err := marker.CoverageHandle(); err != nil || !claimed {
		return ""
	}
	sha, _ := marker.HeadSHA.Get()
	if sha == "" {
		return ""
	}
	total := cov.Required + cov.Excluded
	out := fmt.Sprintf("Reviewed %d of %d changed files at `%s`.", cov.Covered, total, shortSHA(sha))
	switch skipped := len(ctx.Skipped); {
	case skipped == 1:
		out += " 1 was not reviewed; see the warning above."
	case skipped > 1:
		out += fmt.Sprintf(" %d were not reviewed; see the warning above.", skipped)
	}
	return out + "\n\n"
}

// exclusionLine lists the paths repository policy or the backlog rule
// removed, in one line under the footnote. The repository chose those
// exclusions, so they are information, not a warning. The list has the halt
// report's byte bound, ending with a count past it, because a repository can
// mark thousands of changed paths generated.
func exclusionLine(excluded []string) string {
	if len(excluded) == 0 {
		return ""
	}
	budget := haltPathListBudget
	quoted := make([]string, 0, len(excluded))
	for _, path := range excluded {
		item := "`" + path + "`"
		if len(item)+len(", ") > budget {
			break
		}
		quoted = append(quoted, item)
		budget -= len(item) + len(", ")
	}
	if rest := len(excluded) - len(quoted); rest > 0 {
		quoted = append(quoted, fmt.Sprintf("…and %d more", rest))
	}
	return fmt.Sprintf("Excluded by repository policy: %s — not reviewed.\n\n", strings.Join(quoted, ", "))
}

// skipWarning renders the block that opens a summary whose pass skipped
// generated files. Each line names the path, the rule that matched, the size
// and the budget, and ends with the guidance for its rule. The list has the
// halt report's byte bound, ending with a count past it.
func skipWarning(skipped []SkippedFile) string {
	var b strings.Builder
	if len(skipped) == 1 {
		b.WriteString("> **Warning: 1 changed file was not reviewed.** CrossRev recognised it as generated and it is too large for one review prompt.\n>\n")
	} else {
		fmt.Fprintf(&b, "> **Warning: %d changed files were not reviewed.** CrossRev recognised them as generated and they are too large for one review prompt.\n>\n", len(skipped))
	}
	budget := haltPathListBudget
	listed := 0
	for _, s := range skipped {
		line := "> - " + skipLine(s) + "\n"
		if len(line) > budget {
			break
		}
		b.WriteString(line)
		budget -= len(line)
		listed++
	}
	if rest := len(skipped) - listed; rest > 0 {
		fmt.Fprintf(&b, "> - …and %d more skipped files\n", rest)
	}
	b.WriteString(">\n")
	b.WriteString("> To have CrossRev review a file like this, mark it `-linguist-generated` in `.gitattributes`. It is then reviewed if it fits, or the pass halts. To exclude it without this warning, mark it `linguist-generated`.\n\n")
	return b.String()
}

// skipLine renders one skipped file for the warning block: path, rule, size,
// budget, and the guidance for its rule.
func skipLine(s SkippedFile) string {
	rule := s.Signal
	if s.Signal == "header" && s.Excerpt != "" {
		rule = "header: `" + s.Excerpt + "`"
	}
	return fmt.Sprintf("`%s` — generated (%s), %s bytes, over the %s-byte budget. %s",
		s.Path, rule, Thousands(strconv.Itoa(s.Bytes)), Thousands(strconv.Itoa(intel.MaxPromptBytes)), skipGuidance(s.Signal))
}

// skipRenderDetails turns packing's skipped units into the render shape. The
// header excerpt derives from the same bounded window the detector read; it
// is computed here and never stored in the ledger.
func skipRenderDetails(skipped []intel.FileUnit) []SkippedFile {
	if len(skipped) == 0 {
		return nil
	}
	out := make([]SkippedFile, 0, len(skipped))
	for _, unit := range skipped {
		detail := SkippedFile{Path: unit.Path, Signal: unit.Generated, Bytes: len(unit.Body)}
		if unit.Generated == intel.SignalHeader {
			detail.Excerpt = intel.HeaderExcerpt(unit.Body)
		}
		out = append(out, detail)
	}
	return out
}

// policyExclusionPaths lists the exclusions that are repository policy — the
// base tree's .gitattributes and the backlog rule — not packing's skips,
// which the warning block carries.
func policyExclusionPaths(scope intel.Scope) []string {
	skipped := make(map[string]bool, len(scope.Skipped))
	for _, unit := range scope.Skipped {
		skipped[unit.Path] = true
	}
	var out []string
	for _, e := range scope.Excluded {
		if !skipped[e.Path] {
			out = append(out, e.Path)
		}
	}
	return out
}

// skipGuidance is the per-rule closing advice of a skip line: a lockfile
// alone holds the resolved URLs and integrity hashes, so its guidance names
// them rather than a generating file.
func skipGuidance(signal string) string {
	if signal == "lockfile" {
		return "It was not read, and its manifest is not a substitute: check its resolved URLs and hashes by hand or with a lockfile linter."
	}
	return "Check it yourself, or review the file that generates it."
}

// skipWarnLine is the terminal warning for one skipped file, carrying the
// same path, rule and size as the comment's warning block.
func skipWarnLine(unit intel.FileUnit) ui.Line {
	return ui.Warn(
		fmt.Sprintf("%s was recognised as generated (%s), %s bytes, and fits no review prompt — it was not reviewed", unit.Path, unit.Generated, Thousands(strconv.Itoa(len(unit.Body)))),
		skipGuidance(unit.Generated))
}

// shortSHA abbreviates a commit SHA the way printed messages do: the first
// seven characters, or the whole string when it is shorter.
func shortSHA(sha string) string {
	if len(sha) <= 7 {
		return sha
	}
	return sha[:7]
}

// noChangesBody renders the pull request whose head matches its base.
//
// It names no commit. A branch reaches this state by a revert, by a merge or
// rebase that brought the same work in from the base, or by a cherry-pick
// upstream, and telling those apart needs history analysis whose answer
// would still be a guess. The comparison is a fact CrossRev reads directly
// and holds however the branch got here.
//
// It does not tell the reader a push restarts the loop. The generated review
// workflow subscribes to opened, ready_for_review and labeled, plus issue
// comments, and never to synchronize (templates/crossrev-review.yml:7-11),
// so a new commit alone fires nothing. This path also clears the awaiting
// labels, which is what the watchdog looks for.
func noChangesBody(ctx RenderContext) string {
	base, head := ctx.BaseRef, ctx.HeadRef
	if base == "" {
		base = "the base branch"
	}
	if head == "" {
		head = "this branch"
	}

	var b strings.Builder
	b.WriteString(alert("NOTE", "**Nothing to review.** This pull request's head is identical to its base, so it changes no files. CrossRev stops rather than calling a reviewer with nothing to read."))
	fmt.Fprintf(&b, "| | |\n|---|---|\n| Comparing | `%s` → `%s` |\n| Files changed | **0** |\n\n", base, head)
	fmt.Fprintf(&b, "**Why this happens.** Either the branch's changes were undone, or the same work already reached `%s` another way — a merge, a rebase, or a cherry-pick that brought it in. Either way there is no difference left to review.\n\n", base)
	b.WriteString("**What to do**\n\n")
	b.WriteString("- **Close this pull request** if the change is no longer wanted.\n")
	fmt.Fprintf(&b, "- **Add a commit** if it isn't finished, then comment `/crossrev review` to start the next pass. A push on its own does not restart the loop — the review workflow listens for that comment and for the `crossrev/awaiting-review` label, not for new commits. Locally, run `crossrev review --pr %d`.\n\n", ctx.PR)
	b.WriteString("No reviewer was called, so this pass cost nothing.\n\n")
	return b.String()
}

func alert(kind, body string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "> [!%s]\n", kind)
	for _, line := range strings.Split(body, "\n") {
		b.WriteString("> ")
		b.WriteString(line)
		b.WriteByte('\n')
	}
	b.WriteByte('\n')
	return b.String()
}

func findingsTable(findings []Finding, repo, sha string) string {
	var b strings.Builder
	b.WriteString("| Severity | Category | Finding | Location |\n|---|---|---|---|\n")
	for _, f := range findings {
		pre := ""
		if f.PreExisting {
			pre = " <sub>· pre-existing</sub>"
		}
		fmt.Fprintf(&b, "| %s&nbsp;%s | %s&nbsp;%s | %s%s | %s |\n",
			SeverityEmoji(orQ(f.Severity)), ucFirst(orQ(f.Severity)),
			CategoryEmoji(orQ(f.Category)), ucFirst(orQ(f.Category)),
			mdCell(f.Title), pre,
			locationLink(f.Path, strconv.Itoa(f.Line), blobURL(repo, sha, f.Path, f.Line)))
	}
	b.WriteByte('\n')
	return b.String()
}

func blobURL(repo, sha, path string, line int) string {
	return fmt.Sprintf("https://github.com/%s/blob/%s/%s#L%d", repo, sha, URLPath(path), line)
}

func locationLink(path, line, dest string) string {
	if path == "" || path == "null" {
		return emDash
	}
	return fmt.Sprintf("[`%s:%s`](%s)", mdCell(path), mdCell(line), dest)
}

func runDetails(marker prstate.Marker, leg string) string {
	harn, _ := marker.Harness.Get()
	if harn == "" {
		harn = "?"
	}
	model, _ := marker.Model.Get()
	reported, _ := marker.ModelReported.Get()
	effort, _ := marker.Effort.Get()
	if er, ok := marker.EffortReported.Get(); ok && er != "" {
		effort = er
	}
	endpoint, _ := marker.Endpoint.Get()
	billing, _ := marker.Billing.Get()

	agent := "`" + harn + "`"
	var gaps string
	if reported != "" {
		agent += " · `" + reported + "`"
		if model != "" && !SameModel(model, reported) {
			agent += " — **requested `" + model + "`, a different model answered**"
		}
	} else if model != "" {
		agent += " · `" + model + "`"
		gaps = harn + " does not report which model answered, so the model above is the one CrossRev requested."
	}

	var usage *harness.Usage
	if len(marker.Usage) > 0 && string(marker.Usage) != "null" {
		var u harness.Usage
		if json.Unmarshal(marker.Usage, &u) == nil {
			usage = &u
			if n := len(u.Models); n > 1 {
				agent += fmt.Sprintf(" +%d more", n-1)
			}
		}
	}
	if effort != "" {
		agent += " · " + effort + " effort"
	}
	if billing != "" {
		agent += " · " + billing
	}
	if endpoint != "" && endpoint != "vendor" {
		agent += " · via `" + endpoint + "`"
	}

	cached := Thousands("")
	cost := harness.FormatCost("")
	costSource := ""
	if usage != nil {
		cached = Thousands(strconv.FormatInt(usage.Cached(), 10))
		if usage.CostUSD != nil {
			cost = harness.FormatCost(strconv.FormatFloat(*usage.CostUSD, 'f', -1, 64))
		}
		if usage.CostSource != nil {
			costSource = *usage.CostSource
		}
	}

	var b strings.Builder
	b.WriteString("**Run details**\n\n")
	b.WriteString("| Leg | Agent | Duration | Tokens | Cached | Est. cost |\n|---|---|---|---|---|---|\n")
	fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %s |\n\n",
		leg, agent,
		Elapsed(intString(marker.TS), optIntString(marker.DoneTS)),
		Thousands(tokenString(marker.Tokens)), cached, cost)

	foot := ""
	if gaps != "" {
		foot = gaps + " "
	}
	foot += harness.Footnote(costSource, billing)
	if foot != "" {
		fmt.Fprintf(&b, "<sub>%s</sub>\n\n", foot)
	}
	return b.String()
}

func shouldFixFinding(f Finding, minFix string) bool {
	sev, err := core.ParseSeverity(f.Severity)
	if err != nil {
		return false
	}
	bar, err := core.ParseSeverity(minFix)
	if err != nil {
		return false
	}
	return policy.ShouldFix(sev, bar, f.PreExisting)
}

func orQ(s string) string {
	if s == "" {
		return "?"
	}
	return s
}

func oneLine(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c < 0x20 || c == 0x7f {
			b[i] = ' '
		}
	}
	return string(b)
}

func mdCell(s string) string {
	return strings.ReplaceAll(oneLine(s), "|", `\|`)
}

func ucFirst(s string) string {
	if s == "" {
		return s
	}
	c := s[0]
	if c >= 'a' && c <= 'z' {
		return string(c-32) + s[1:]
	}
	return s
}

func foldASCII(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + ('a' - 'A')
		}
	}
	return string(b)
}

func digitsOnly(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func intString(n int64) string {
	if n == 0 {
		return ""
	}
	return strconv.FormatInt(n, 10)
}

func optIntString(o prstate.Opt[int64]) string {
	v, ok := o.Get()
	if !ok {
		return ""
	}
	return strconv.FormatInt(v, 10)
}

func tokenString(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	return strings.Trim(string(raw), `"`)
}

func parseFindings(raw json.RawMessage) []Finding {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var out []Finding
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	return out
}
