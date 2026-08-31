package review

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/harness"
	"github.com/carlosboeing/crossrev/internal/policy"
	"github.com/carlosboeing/crossrev/internal/prstate"
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
}

const emDash = "—"

// SeverityEmoji is run_severity_emoji (lib/run.sh:365-372).
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

// CategoryEmoji is run_category_emoji (lib/run.sh:377-387).
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

// FindingLabel is run_finding_label (lib/run.sh:410-416).
func FindingLabel(f Finding) string {
	return fmt.Sprintf("%s [%s · %s]", SeverityEmoji(orQ(f.Severity)), ucFirst(orQ(f.Severity)), ucFirst(orQ(f.Category)))
}

// ActionableCount is run_actionable (lib/run.sh:350-360).
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

// SameModel is _same_model (lib/run.sh:1449-1455).
func SameModel(want, got string) bool {
	want = foldASCII(want)
	got = foldASCII(got)
	if want == "" || got == "" {
		return false
	}
	return strings.Contains(got, want) || strings.Contains(want, got)
}

// Elapsed is _elapsed (lib/run.sh:1408-1415).
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

// Thousands is _thousands (lib/run.sh:1423-1430).
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

// URLPath is _url_path (lib/run.sh:1603-1605).
func URLPath(path string) string {
	parts := strings.Split(path, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, "/")
}

// CommentBody is _review_comment_body (lib/run.sh:1367-1384).
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

// SummaryBody is _review_summary_body (lib/run.sh:1669-1724).
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
		gaps = harn + " does not report which model answered, so the model above is the one crossrev requested."
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
