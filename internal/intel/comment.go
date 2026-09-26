package intel

import (
	"fmt"
	"strconv"
	"strings"
)

// commentListBudget is the byte bound the halt report uses for a path list.
// A skip warning and an exclusion line share it, so a repository that marks
// thousands of changed paths generated still fits in one comment.
const commentListBudget = 8 * 1024

// SkipNotice is one skipped file as the warning renders it. Reason is the
// text SkipReason writes. Excerpt is the header marker quote, empty for
// every other signal and when the head blob has no marker line.
type SkipNotice struct {
	Path    string
	Reason  string
	Excerpt string
}

// CoverageCounts is the footnote's arithmetic. Skipped is how many of
// Excluded the warning names; Excluded counts skips and policy together,
// so the denominator Required+Excluded is every changed file.
type CoverageCounts struct {
	Covered  int
	Required int
	Excluded int
	Skipped  int
}

// SkipGuidance is the per-rule closing advice of a skip line. A lockfile
// alone holds the resolved URLs and integrity hashes, so its guidance names
// them rather than a generating file.
func SkipGuidance(signal string) string {
	if signal == SignalLockfile {
		return "It was not read, and its manifest is not a substitute: check its resolved URLs and hashes by hand or with a lockfile linter."
	}
	return "Check it yourself, or review the file that generates it."
}

// SkipLine renders one skipped file: path, rule, size, budget, and the
// guidance for its rule. A header skip quotes Excerpt inside the rule when
// one was read. A reason ParseSkipReason refuses still names the path.
func SkipLine(path, reason, excerpt string) string {
	signal, size, budget, ok := ParseSkipReason(reason)
	if !ok {
		return fmt.Sprintf("`%s` — %s", path, reason)
	}
	rule := signal
	if signal == SignalHeader && excerpt != "" {
		rule = "header: `" + excerpt + "`"
	}
	return fmt.Sprintf("`%s` — generated (%s), %s bytes, over the %s-byte budget. %s",
		path, rule, thousands(strconv.Itoa(size)), thousands(strconv.Itoa(budget)), SkipGuidance(signal))
}

// SkipWarning renders the block that opens a summary whose pass skipped
// generated files. The list has commentListBudget, ending with a count past it.
func SkipWarning(skipped []SkipNotice) string {
	if len(skipped) == 0 {
		return ""
	}
	var b strings.Builder
	if len(skipped) == 1 {
		b.WriteString("> **Warning: 1 changed file was not reviewed.** CrossRev recognised it as generated and it is too large for one review prompt.\n>\n")
	} else {
		fmt.Fprintf(&b, "> **Warning: %d changed files were not reviewed.** CrossRev recognised them as generated and they are too large for one review prompt.\n>\n", len(skipped))
	}
	budget := commentListBudget
	listed := 0
	for _, s := range skipped {
		line := "> - " + SkipLine(s.Path, s.Reason, s.Excerpt) + "\n"
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

// ExclusionLine lists the paths repository policy or the backlog rule
// removed, in one line under the footnote. The repository chose those
// exclusions, so they are information, not a warning. The list has
// commentListBudget, ending with a count past it.
func ExclusionLine(excluded []string) string {
	if len(excluded) == 0 {
		return ""
	}
	budget := commentListBudget
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

// CoverageFootnote renders how many of the changed files the pass reviewed,
// and at which head. sha empty renders nothing. A skip adds a pointer to
// the warning above. The caller decides whether a pass has a footnote at all.
func CoverageFootnote(counts CoverageCounts, sha string) string {
	if sha == "" {
		return ""
	}
	total := counts.Required + counts.Excluded
	out := fmt.Sprintf("Reviewed %d of %d changed files at `%s`.", counts.Covered, total, shortSHA(sha))
	switch counts.Skipped {
	case 1:
		out += " 1 was not reviewed; see the warning above."
	default:
		if counts.Skipped > 1 {
			out += fmt.Sprintf(" %d were not reviewed; see the warning above.", counts.Skipped)
		}
	}
	return out + "\n\n"
}

func shortSHA(sha string) string {
	if len(sha) <= 7 {
		return sha
	}
	return sha[:7]
}

func thousands(n string) string {
	if !digitsOnly(n) {
		return "—"
	}
	out := ""
	for len(n) > 3 {
		out = "," + n[len(n)-3:] + out
		n = n[:len(n)-3]
	}
	return n + out
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
