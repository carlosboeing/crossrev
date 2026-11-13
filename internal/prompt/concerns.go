package prompt

import "strings"

// ConcernBlock renders a concern's checklist from the embedded review skill.
// An empty concern keeps callers without a focus byte-identical.
func ConcernBlock(concern string) string {
	if concern != "correctness" && concern != "consistency" {
		return ""
	}
	skill := string(ReviewSkill())
	heading := "### Concern: " + concern + "\n"
	_, body, found := strings.Cut(skill, heading)
	if !found {
		return ""
	}
	end := len(body)
	for _, next := range []string{"\n### ", "\n## "} {
		if i := strings.Index(body, next); i >= 0 && i < end {
			end = i
		}
	}
	return "## Review focus: " + concern + "\n\n" + strings.TrimSpace(body[:end]) + "\n\n"
}
