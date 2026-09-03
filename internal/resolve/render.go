package resolve

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/harness"
	"github.com/carlosboeing/crossrev/internal/policy"
	"github.com/carlosboeing/crossrev/internal/prstate"
)

// ReplyBody is _resolve_reply_body at lib/run.sh:2570-2585.
func ReplyBody(disposition json.RawMessage, tracked string, pass int, harnessName, model string) string {
	var d struct {
		FindingID  string `json:"finding_id"`
		Resolution string `json:"resolution"`
		Reply      string `json:"reply"`
	}
	_ = json.Unmarshal(disposition, &d)
	lead := resolutionLead(d.Resolution)
	var b strings.Builder
	b.WriteString(lead)
	b.WriteByte(' ')
	b.WriteString(stripResolutionLead(d.Reply))
	b.WriteByte('\n')
	if tracked != "" {
		b.WriteString("\nTracked outside this pull request as ")
		b.WriteString(tracked)
		b.WriteString(", so it survives the merge.\n")
	}
	b.WriteString("\n<sub>crossrev pass ")
	b.WriteString(strconv.Itoa(pass))
	b.WriteString(", verified by ")
	b.WriteString(harnessName)
	if model != "" {
		b.WriteString(" (")
		b.WriteString(model)
		b.WriteByte(')')
	}
	b.WriteString(". Every finding is verified whatever its severity — severity governs what happens afterwards, not whether the check happens.</sub>")
	id, err := prstate.ParseFindingID(d.FindingID)
	if err == nil {
		b.WriteString(prstate.EncodeFindingMarker(id, pass, core.LegResolve))
	}
	return b.String()
}

func resolutionLead(disp string) string {
	switch disp {
	case "fixed":
		return "**Fixed.**"
	case "skipped":
		return "**Skipped.**"
	case "deferred":
		return "**Deferred.**"
	case "disputed":
		return "**Not changing this.**"
	case "escalated":
		return "**This needs a human decision.**"
	default:
		return "**" + disp + ".**"
	}
}

var resolutionLeadRe = regexp.MustCompile(`(?s)^[ \t]*(\*\*)?(Fixed|Skipped|Deferred|Not changing this|This needs a human decision)\.(\*\*)?[ \t]*`)

func stripResolutionLead(text string) string {
	head, rest, found := strings.Cut(text, "\n")
	prev := ""
	for head != prev {
		prev = head
		head = resolutionLeadRe.ReplaceAllString(head, "")
	}
	if found {
		return head + "\n" + rest
	}
	return head
}

// CommitSubjectOK is _commit_subject_ok at lib/run.sh:2641-2663.
func CommitSubjectOK(s, rawJSON string) bool {
	if s == "" || s == "null" {
		return false
	}
	if rawJSON != "" {
		var doc map[string]json.RawMessage
		if json.Unmarshal([]byte(rawJSON), &doc) == nil {
			if sub, ok := doc["commit_subject"]; ok {
				if containsJSONControl(sub) {
					return false
				}
			}
		}
	}
	if strings.Contains(s, "\n") {
		return false
	}
	if hasControl(s) {
		return false
	}
	if utf8.RuneCountInString(s) > 100 {
		return false
	}
	if strings.Contains(s, prstate.MarkerPrefix) {
		return false
	}
	return true
}

func containsJSONControl(raw json.RawMessage) bool {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return bytesHaveControl(raw)
	}
	return hasControl(s)
}

func hasControl(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c <= 0x1f || c == 0x7f {
			return true
		}
	}
	return false
}

func bytesHaveControl(b []byte) bool {
	for _, c := range b {
		if c <= 0x1f || c == 0x7f {
			return true
		}
	}
	return false
}

func commitLine(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c <= 0x1f || c == 0x7f {
			b.WriteByte(' ')
		} else {
			b.WriteByte(c)
		}
	}
	return b.String()
}

func oneLine(s string) string { return commitLine(s) }

func mdCell(s string) string {
	return strings.ReplaceAll(oneLine(s), "|", `\|`)
}

func ucfirst(s string) string {
	if s == "" {
		return s
	}
	r, size := utf8.DecodeRuneInString(s)
	return string(unicode.ToUpper(r)) + s[size:]
}

// URLPath is _url_path at lib/run.sh:1609-1611: percent-encode each segment
// the way jq `@uri` does.
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

func blobURL(repo core.Slug, sha, path, line string) string {
	return "https://github.com/" + repo.String() + "/blob/" + sha + "/" + URLPath(path) + "#L" + line
}

func threadURL(repo core.Slug, pr int, root string) string {
	return fmt.Sprintf("https://github.com/%s/pull/%d/files#r%s", repo, pr, root)
}

func locationLink(path, line, url string) string {
	if path == "" || path == "null" {
		return "—"
	}
	return "[`" + mdCell(path) + ":" + mdCell(line) + "`](" + url + ")"
}

func findingLocation(f harness.Node, sha string, repo core.Slug, pr int) string {
	path := f.Member("path").StringVal()
	line := f.Member("line").StringVal()
	root := f.Member("root_comment_id").StringVal()
	if root != "" && root != "null" {
		return locationLink(path, line, threadURL(repo, pr, root))
	}
	return locationLink(path, line, blobURL(repo, sha, path, line))
}

func findingByID(findings []harness.Node, id string) harness.Node {
	for _, f := range findings {
		if f.Member("id").StringVal() == id {
			return f
		}
	}
	return harness.Node{}
}

// CommitBody is _commit_body at lib/run.sh:2689-2724.
func CommitBody(resolutions, findings json.RawMessage, want, sha string, pass int, repo string, pr int) string {
	var recs []harness.Node
	_ = json.Unmarshal(resolutions, &recs)
	var fs []harness.Node
	_ = json.Unmarshal(findings, &fs)
	slug, _ := core.ParseSlug(repo)
	var b strings.Builder
	for _, r := range recs {
		if r.Member("resolution").StringVal() != want {
			continue
		}
		id := r.Member("finding_id").StringVal()
		f := findingByID(fs, id)
		title := f.Member("title").StringVal()
		path := f.Member("path").StringVal()
		line := f.Member("line").StringVal()
		root := f.Member("root_comment_id").StringVal()
		bullet := commitLine(title)
		if bullet == "" {
			bullet = commitLine(id)
		}
		if bullet != "" && !strings.ContainsAny(bullet[len(bullet)-1:], ".!?") {
			bullet += "."
		}
		b.WriteString("- ")
		b.WriteString(bullet)
		b.WriteByte('\n')
		if path == "" || path == "null" {
			continue
		}
		path, line = commitLine(path), commitLine(line)
		url := blobURL(slug, sha, path, line)
		if root != "" && root != "null" {
			url = threadURL(slug, pr, root)
		}
		fmt.Fprintf(&b, "  %s:%s - %s\n", path, line, url)
	}
	fmt.Fprintf(&b, "\nCrossrev-pr: %s#%d\nCrossrev-pass: %d\n", repo, pr, pass)
	return b.String()
}

func severityEmoji(sev string) string {
	switch sev {
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

func categoryEmoji(kind string) string {
	switch kind {
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

func alert(kind, body string) string {
	var b strings.Builder
	b.WriteString("> [!")
	b.WriteString(kind)
	b.WriteString("]\n")
	for _, line := range strings.Split(body, "\n") {
		b.WriteString("> ")
		b.WriteString(line)
		b.WriteByte('\n')
	}
	b.WriteByte('\n')
	return b.String()
}

func resolutionCounts(resolutions json.RawMessage) string {
	var recs []struct {
		Resolution string `json:"resolution"`
	}
	_ = json.Unmarshal(resolutions, &recs)
	by := map[string]int{}
	for _, r := range recs {
		by[r.Resolution]++
	}
	order := []string{"fixed", "skipped", "deferred", "disputed", "escalated"}
	var parts []string
	for _, k := range order {
		if n, ok := by[k]; ok {
			parts = append(parts, fmt.Sprintf("%d %s", n, k))
		}
	}
	if len(parts) == 0 {
		return "Nothing to resolution."
	}
	return strings.Join(parts, ", ") + "."
}

func resolutionsTable(resolutions, findings json.RawMessage, sha string, repo core.Slug, pr int) string {
	var recs []harness.Node
	_ = json.Unmarshal(resolutions, &recs)
	var fs []harness.Node
	_ = json.Unmarshal(findings, &fs)
	var b strings.Builder
	b.WriteString("| Severity | Finding | Location | Resolution |\n|---|---|---|---|\n")
	for _, d := range recs {
		id := d.Member("finding_id").StringVal()
		f := findingByID(fs, id)
		sev := f.Member("severity").StringVal()
		if sev == "" {
			sev = "?"
		}
		title := f.Member("title").StringVal()
		if title == "" {
			title = "finding `" + id + "` is not in the review record"
		}
		fmt.Fprintf(&b, "| %s&nbsp;%s | %s | %s | %s |\n",
			severityEmoji(sev), ucfirst(sev),
			mdCell(title),
			findingLocation(f, sha, repo, pr),
			d.Member("resolution").StringVal())
	}
	b.WriteByte('\n')
	return b.String()
}

func thousands(s string) string {
	if s == "" {
		return "—"
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return "—"
		}
	}
	n := s
	out := ""
	for len(n) > 3 {
		out = "," + n[len(n)-3:] + out
		n = n[:len(n)-3]
	}
	return n + out
}

func elapsed(from, to int64) string {
	if from == 0 || to == 0 {
		return "—"
	}
	secs := to - from
	if secs < 0 {
		return "—"
	}
	if secs < 60 {
		return strconv.FormatInt(secs, 10) + "s"
	}
	return fmt.Sprintf("%dm %ds", secs/60, secs%60)
}

func runDetails(m prstate.Marker, leg string) string {
	harnessName, _ := m.Harness.Get()
	if harnessName == "" {
		harnessName = "?"
	}
	model, _ := m.Model.Get()
	reported, _ := m.ModelReported.Get()
	effort, _ := m.Effort.Get()
	effortReported, _ := m.EffortReported.Get()
	endpoint, _ := m.Endpoint.Get()
	billing, _ := m.Billing.Get()

	agent := "`" + harnessName + "`"
	gaps := ""
	if reported != "" && reported != "null" {
		agent += " · `" + reported + "`"
		if model != "" && model != "null" && !harness.SameModel(model, reported) {
			agent += " — **requested `" + model + "`, a different model answered**"
		}
	} else if model != "" && model != "null" {
		agent += " · `" + model + "`"
		gaps = harnessName + " does not report which model answered, so the model above is the one CrossRev requested."
	}
	var usage *harness.Usage
	if len(m.Usage) > 0 && string(m.Usage) != "null" {
		var u harness.Usage
		if json.Unmarshal(m.Usage, &u) == nil {
			usage = &u
			if n := len(u.Models); n > 1 {
				agent += fmt.Sprintf(" +%d more", n-1)
			}
		}
	}
	if effortReported != "" && effortReported != "null" {
		effort = effortReported
	}
	if effort != "" && effort != "null" {
		agent += " · " + effort + " effort"
	}
	if billing != "" && billing != "null" {
		agent += " · " + billing
	}
	if endpoint != "" && endpoint != "null" && endpoint != "vendor" {
		agent += " · via `" + endpoint + "`"
	}

	cached, cost := "—", "—"
	costSource := ""
	if usage != nil {
		cached = thousands(strconv.FormatInt(usage.Cached(), 10))
		if usage.CostUSD != nil {
			cost = harness.FormatCost(strconv.FormatFloat(*usage.CostUSD, 'f', -1, 64))
		} else {
			cost = harness.FormatCost("")
		}
		if usage.CostSource != nil {
			costSource = *usage.CostSource
		}
	}

	tokens := "—"
	if len(m.Tokens) > 0 && string(m.Tokens) != "null" {
		var n json.Number
		if json.Unmarshal(m.Tokens, &n) == nil {
			tokens = thousands(n.String())
		} else {
			tokens = thousands(strings.Trim(string(m.Tokens), `"`))
		}
	}

	var b strings.Builder
	b.WriteString("**Run details**\n\n")
	b.WriteString("| Leg | Agent | Duration | Tokens | Cached | Est. cost |\n|---|---|---|---|---|---|\n")
	fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %s |\n\n",
		leg, agent, elapsed(m.TS, m.DoneTS.Value()), tokens, cached, cost)
	var foot string
	if gaps != "" {
		foot = gaps + " " + harness.Footnote(costSource, billing)
	} else {
		foot = harness.Footnote(costSource, billing)
	}
	if foot != "" {
		b.WriteString("<sub>")
		b.WriteString(foot)
		b.WriteString("</sub>\n\n")
	}
	return b.String()
}

// ResolveSummaryBody is _resolve_summary_body at lib/run.sh:2729-2781.
func ResolveSummaryBody(resolutions, findings json.RawMessage, deferredLines string, marker prstate.Marker, repo string, pr, maxPasses int) string {
	summary, _ := marker.Summary.Get()
	pass := marker.Pass
	if pass == 0 {
		pass = 1
	}
	commit, _ := marker.CommitSHA.Get()
	blocked, _ := marker.Blocked.Get()
	blockedReason, _ := marker.BlockedReason.Get()
	slug, _ := core.ParseSlug(repo)

	var b strings.Builder
	fmt.Fprintf(&b, "## crossrev resolved %s\n\n", passLabel(pass, maxPasses))

	counts := resolutionCounts(resolutions)
	var recs []struct {
		Resolution string `json:"resolution"`
	}
	_ = json.Unmarshal(resolutions, &recs)
	escalated := 0
	for _, r := range recs {
		if r.Resolution == "escalated" {
			escalated++
		}
	}
	noun := "findings"
	if escalated == 1 {
		noun = "finding"
	}
	if blocked {
		b.WriteString(alert("WARNING", fmt.Sprintf("**Blocked:** %s The loop halts here and needs a human. %s", blockedReason, counts)))
	} else if escalated > 0 {
		b.WriteString(alert("WARNING", fmt.Sprintf("**%d %s need a human decision.** `crossrev/stop` is applied, so the loop halts until somebody removes it. %s", escalated, noun, counts)))
	} else {
		b.WriteString(alert("NOTE", fmt.Sprintf("**%s** Every finding was verified whatever its severity — severity governs what happens afterwards, not whether the check happens.", counts)))
	}

	if commit != "" && commit != "null" {
		short := commit
		if len(short) > 7 {
			short = short[:7]
		}
		fmt.Fprintf(&b, "Fixes pushed as `%s`.\n\n", short)
	} else {
		b.WriteString("No code changed this pass.\n\n")
	}

	unthreaded := marker.Unthreaded.Value()
	if unthreaded == 1 {
		b.WriteString("One reply could not be posted in the review thread it answers, so it is a top-level comment on this pull request naming its finding instead.\n\n")
	} else if unthreaded > 1 {
		fmt.Fprintf(&b, "%d replies could not be posted in the review threads they answer, so they are top-level comments on this pull request naming their findings instead.\n\n", unthreaded)
	}

	fmt.Fprintf(&b, "%s\n\n", summary)
	sha, _ := marker.HeadSHA.Get()
	b.WriteString(resolutionsTable(resolutions, findings, sha, slug, pr))

	if deferredLines != "" {
		b.WriteString("## Deferred work filed\n")
		fmt.Fprintf(&b, "%s\n\n", deferredLines)
		b.WriteString("An unresolved thread on a merged pull request is visible in no GitHub view, which is why a deferred finding goes somewhere durable before its thread is resolved.\n\n")
	}

	b.WriteString(runDetails(marker, "resolve"))
	return b.String()
}

func findingsTable(findings json.RawMessage, sha string, repo core.Slug) string {
	var fs []harness.Node
	_ = json.Unmarshal(findings, &fs)
	var b strings.Builder
	b.WriteString("| Severity | Category | Finding | Location |\n|---|---|---|---|\n")
	for _, f := range fs {
		sev := f.Member("severity").StringVal()
		if sev == "" {
			sev = "?"
		}
		kind := f.Member("category").StringVal()
		if kind == "" {
			kind = "?"
		}
		path := f.Member("path").StringVal()
		line := f.Member("line").StringVal()
		pre := ""
		if f.Member("pre_existing").StringVal() == "true" {
			pre = " <sub>· pre-existing</sub>"
		}
		fmt.Fprintf(&b, "| %s&nbsp;%s | %s&nbsp;%s | %s%s | %s |\n",
			severityEmoji(sev), ucfirst(sev),
			categoryEmoji(kind), ucfirst(kind),
			mdCell(f.Member("title").StringVal()), pre,
			locationLink(path, line, blobURL(repo, sha, path, line)))
	}
	b.WriteByte('\n')
	return b.String()
}

func actionableCount(findings []harness.Node, minFix core.Severity) int {
	n := 0
	for _, f := range findings {
		sev := core.Severity(f.Member("severity").StringVal())
		pre := f.Member("pre_existing").StringVal() == "true"
		if policy.ShouldFix(sev, minFix, pre) {
			n++
		}
	}
	return n
}

func reviewSummaryBody(findings json.RawMessage, marker prstate.Marker, repo core.Slug, minFix core.Severity, maxPasses int) string {
	var fs []harness.Node
	_ = json.Unmarshal(findings, &fs)
	n := len(fs)
	actionable := actionableCount(fs, minFix)
	verdict, ok := marker.Verdict.Get()
	if !ok || verdict == "" {
		verdict = string(core.VerdictIssuesRemain)
	}
	blockedReason, _ := marker.BlockedReason.Get()
	pass := marker.Pass
	if pass == 0 {
		pass = 1
	}

	var b strings.Builder
	fmt.Fprintf(&b, "## crossrev review — %s\n\n", passLabel(pass, maxPasses))
	noun := "findings"
	if n == 1 {
		noun = "finding"
	}
	switch verdict {
	case string(core.VerdictConverged):
		b.WriteString(alert("TIP", fmt.Sprintf("**Converged.** Nothing at or above `min_fix_severity` (%s) remains, so the loop stops here. Findings below the threshold, and pre-existing ones, are reported but cannot keep the loop alive — a loop that cannot converge because of a naming quibble is one nobody leaves switched on.", minFix)))
	case string(core.VerdictBlocked):
		if blockedReason == "" {
			blockedReason = "No reason was given."
		}
		b.WriteString(alert("WARNING", fmt.Sprintf("**The review could not be completed:** %s The loop halts here and a human is needed. Nothing in this comment is a judgement about the code.", blockedReason)))
	default:
		b.WriteString(alert("CAUTION", fmt.Sprintf("**%d %s need resolving.** A second agent now verifies every finding below against the codebase and either fixes it, skips it, defers it, or explains why it is wrong. It may change code for the %d at or above `min_fix_severity` (%s); the rest are verified and reported, never silently dropped.", n, noun, actionable, minFix)))
	}
	fmt.Fprintf(&b, "Verdict: **%s**.\n\n", verdict)
	if n == 0 {
		b.WriteString("No findings. Low-severity and pre-existing issues would be listed here too, so this is an empty review rather than a filtered one.\n\n")
	} else {
		sha, _ := marker.HeadSHA.Get()
		b.WriteString(findingsTable(findings, sha, repo))
	}
	unanchored := marker.Unanchored.Value()
	if unanchored == 1 {
		b.WriteString("One finding could not be anchored to a line of the diff, so it is a top-level comment on this pull request naming its location instead. Its reply will land there too, because there is no review thread to put one in.\n\n")
	} else if unanchored > 1 {
		fmt.Fprintf(&b, "%d findings could not be anchored to a line of the diff, so they are top-level comments on this pull request naming their locations instead. Their replies will land there too, because there are no review threads to put them in.\n\n", unanchored)
	}
	b.WriteString(runDetails(marker, "review"))
	return b.String()
}
