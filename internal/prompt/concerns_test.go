package prompt_test

import (
	"github.com/carlosboeing/crossrev/internal/prompt"
	"strings"
	"testing"
)

func TestConcernBlockBeforeOutput(t *testing.T) {
	for _, concern := range []string{"correctness", "consistency"} {
		r := prompt.Review{Skill: prompt.ReviewSkill(), Concern: concern, Reads: prompt.ReadsBlock("served")}
		got := string(r.Render())
		focus := strings.Index(got, "## Review focus: "+concern)
		if focus < strings.Index(got, r.Reads) || focus >= strings.LastIndex(got, "## Output") {
			t.Fatal("concern block not between shared context and output")
		}
		if block := prompt.ConcernBlock(concern); !strings.Contains(block, "other concern") || !strings.Contains(got, block) {
			t.Fatalf("missing concern checklist: %s", block)
		}
	}
}
