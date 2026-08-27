package config

import "github.com/carlosboeing/crossrev/internal/core"

// Defaults is the configuration with no config file anywhere.
//
// It reproduces _cfg_defaults at lib/config.sh:94-130, in the order that jq
// literal writes its keys, because `crossrev config show` prints the merge.
//
// Deliberately not what `init` writes (lib/config.sh:88-93). The CI starter
// names a specific pairing; a local user who has never heard of it would
// otherwise be told to set an API key before their first review. So: no
// endpoints, two different local harnesses, local mode, and nothing persisted
// anywhere uninvited.
func Defaults() *Object {
	policy := NewObject()
	policy.Set("min_fix_severity", string(core.SeverityMedium))
	policy.Set("max_passes_per_cycle", Number("3"))
	policy.Set("max_files_changed_per_pr", Number("200"))
	policy.Set("max_prs_per_day", Number("25"))

	git := NewObject()
	git.Set("hooks", "skip")

	logs := NewObject()
	logs.Set("retention_days", Number("14"))
	logs.Set("keep_transcripts", false)

	githubIssues := NewObject()
	githubIssues.Set("labels", []any{})
	githubIssues.Set("tracking_label", "crossrev-review")
	githubIssues.Set("create_missing_labels", true)
	githubIssues.Set("comment_on_existing_issue", false)

	repository := NewObject()
	repository.Set("layout", string(LayoutFolder))
	repository.Set("path", nil)

	backlog := NewObject()
	backlog.Set("destination", string(DestinationAuto))
	backlog.Set("github_issues", githubIssues)
	backlog.Set("repository", repository)

	out := NewObject()
	out.Set("version", Number(Version))
	out.Set("mode", "local")
	out.Set("runner", "github-hosted")
	out.Set("policy", policy)
	out.Set("git", git)
	out.Set("logs", logs)
	out.Set("endpoints", NewObject())
	out.Set("backlog", backlog)
	out.Set("reviewer", defaultLeg(defaultReviewer))
	out.Set("resolver", defaultLeg(defaultResolver))
	out.Set("enable_automation_hint", true)
	return out
}

// The default pairing, which _cfg_defaults reads out of
// lib/harnesses.json's `.default_pairing` at lib/config.sh:101-102. Two
// different local harnesses, because a pairing that reviews and resolves with
// one model is not a cross-model review.
const (
	defaultReviewer = core.HarnessCodex
	defaultResolver = core.HarnessClaude
)

func defaultLeg(harness core.HarnessName) *Object {
	leg := NewObject()
	leg.Set("harness", string(harness))
	leg.Set("model", nil)
	leg.Set("effort", nil)
	leg.Set("endpoint", nil)
	return leg
}
