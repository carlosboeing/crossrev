package prompt_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/prompt"
)

// coverage_test.go — Task A3's named acceptance tests: the reviewer coverage
// contract in the canonical findings schema and the numbered batch input in
// the review prompt.
//
// The frozen parity fixture under tests/fixtures/parity/ stays read-only:
// these tests read prompt_review.json and never write it. File coverage only,
// verification deferred — no verification, intent-capsule, parser or
// structural-analysis duties enter here.
//
// TestReviewSchemaRequiresCoverageAndScopeReport requires the canonical
// findings schema to carry the reviewer coverage contract: coverage[],
// examined_scope and known_limits as required top-level fields, with the
// unit_number/disposition/finding_numbers/evidence/reason member shape and
// the git|search|convention|reviewer evidence source set.
//
// It fails before the change because a schema-valid answer can omit coverage
// entirely: the required list names only the parity-era four.
func TestReviewSchemaRequiresCoverageAndScopeReport(t *testing.T) {
	raw := readFile(t, "../../schemas/findings.schema.json")
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		t.Fatalf("the canonical findings schema is not JSON: %v", err)
	}
	var required []string
	if err := json.Unmarshal(top["required"], &required); err != nil {
		t.Fatalf("the schema has no required list: %v", err)
	}
	for _, want := range []string{"coverage", "examined_scope", "known_limits"} {
		if !containsString(required, want) {
			t.Errorf("required = %q, want it to contain %q", required, want)
		}
	}
	var props map[string]json.RawMessage
	if err := json.Unmarshal(top["properties"], &props); err != nil {
		t.Fatalf("the schema has no properties: %v", err)
	}

	coverageRaw, ok := props["coverage"]
	if !ok {
		t.Fatalf("the schema has no coverage property")
	}
	var coverage struct {
		Type  string `json:"type"`
		Items struct {
			Required   []string                     `json:"required"`
			Properties map[string]json.RawMessage `json:"properties"`
		} `json:"items"`
	}
	// coverage is null only with verdict blocked, when no batch could be
	// examined — so the type arrives as a set. Decode either spelling.
	if err := json.Unmarshal(coverageRaw, &coverage); err != nil {
		var nullable struct {
			Type  []string `json:"type"`
			Items struct {
				Required   []string                     `json:"required"`
				Properties map[string]json.RawMessage `json:"properties"`
			} `json:"items"`
		}
		if err := json.Unmarshal(coverageRaw, &nullable); err != nil {
			t.Fatalf("coverage is not an array schema: %v", err)
		}
		if !containsString(nullable.Type, "array") {
			t.Errorf("coverage type = %q, want it to hold array", nullable.Type)
		}
		coverage.Items = nullable.Items
	} else if coverage.Type != "array" {
		t.Errorf("coverage type = %q, want array", coverage.Type)
	}
	for _, want := range []string{"unit_number", "disposition", "finding_numbers", "evidence", "reason"} {
		if !containsString(coverage.Items.Required, want) {
			t.Errorf("coverage items require %q, want it listed", want)
		}
	}
	dispositionRaw, ok := coverage.Items.Properties["disposition"]
	if !ok {
		t.Fatalf("coverage items have no disposition")
	}
	var disposition struct {
		Enum []string `json:"enum"`
	}
	if err := json.Unmarshal(dispositionRaw, &disposition); err != nil {
		t.Fatalf("disposition is not an enum schema: %v", err)
	}
	for _, want := range []string{"no_issue", "finding", "not_affected", "could_not_review"} {
		if !containsString(disposition.Enum, want) {
			t.Errorf("disposition enum = %q, want it to contain %q", disposition.Enum, want)
		}
	}
	evidenceRaw, ok := coverage.Items.Properties["evidence"]
	if !ok {
		t.Fatalf("coverage items have no evidence")
	}
	var evidence struct {
		Type  string `json:"type"`
		Items struct {
			Required   []string                     `json:"required"`
			Properties map[string]json.RawMessage `json:"properties"`
		} `json:"items"`
	}
	if err := json.Unmarshal(evidenceRaw, &evidence); err != nil {
		t.Fatalf("evidence is not an array schema: %v", err)
	}
	if evidence.Type != "array" {
		t.Errorf("evidence type = %q, want array", evidence.Type)
	}
	for _, want := range []string{"path", "revision", "start_line", "end_line", "source", "note"} {
		if !containsString(evidence.Items.Required, want) {
			t.Errorf("evidence items require %q, want it listed", want)
		}
	}
	sourceRaw, ok := evidence.Items.Properties["source"]
	if !ok {
		t.Fatalf("evidence items have no source")
	}
	var source struct {
		Enum []string `json:"enum"`
	}
	if err := json.Unmarshal(sourceRaw, &source); err != nil {
		t.Fatalf("source is not an enum schema: %v", err)
	}
	for _, want := range []string{"git", "search", "convention", "reviewer"} {
		if !containsString(source.Enum, want) {
			t.Errorf("evidence source enum = %q, want it to contain %q", source.Enum, want)
		}
	}

	examinedRaw, ok := props["examined_scope"]
	if !ok {
		t.Fatalf("the schema has no examined_scope property")
	}
	var examined struct {
		Type      string `json:"type"`
		MinLength int    `json:"minLength"`
	}
	if err := json.Unmarshal(examinedRaw, &examined); err != nil {
		t.Fatalf("examined_scope is not a string schema: %v", err)
	}
	if examined.Type != "string" || examined.MinLength < 1 {
		t.Errorf("examined_scope = %+v, want a string with minLength >= 1", examined)
	}
	limitsRaw, ok := props["known_limits"]
	if !ok {
		t.Fatalf("the schema has no known_limits property")
	}
	var limits struct {
		Type  string `json:"type"`
		Items struct {
			Type string `json:"type"`
		} `json:"items"`
	}
	if err := json.Unmarshal(limitsRaw, &limits); err != nil {
		t.Fatalf("known_limits is not an array schema: %v", err)
	}
	if limits.Type != "array" || limits.Items.Type != "string" {
		t.Errorf("known_limits = %+v, want an array of strings", limits)
	}

	// Both harnesses constrain output to this schema natively, so the
	// harness-forced shapes stay: no meta-schema key, and every property
	// listed in required.
	if _, present := top["$schema"]; present {
		t.Error("the schema carries $schema, which Claude Code cannot resolve")
	}
	if _, present := top["$id"]; present {
		t.Error("the schema carries $id, which Claude Code cannot resolve")
	}
	var names []string
	for name := range props {
		names = append(names, name)
	}
	for _, name := range names {
		if !containsString(required, name) {
			t.Errorf("property %q is not listed in required, which strict mode demands", name)
		}
	}
}

func containsString(set []string, want string) bool {
	for _, got := range set {
		if got == want {
			return true
		}
	}
	return false
}

// batchReview builds a review prompt carrying the named batch units, advisory
// refs and exclusions over the frozen oracle's skill, diff, meta, prior and
// threads — the same inputs TestReviewMatchesTheFrozenPrompt pins, plus the
// one intentional addition.
func batchReview(o reviewOracle, units []prompt.BatchUnit, advisory []prompt.AdvisoryRef, excluded []prompt.ExclusionRef) prompt.Review {
	return prompt.Review{
		Skill:    []byte(o.Inputs.Skill),
		Diff:     []byte(o.Inputs.Diff),
		Meta:     o.Inputs.Meta,
		Prior:    o.Inputs.Prior,
		Threads:  o.Inputs.Threads,
		Batch:    units,
		Advisory: advisory,
		Excluded: excluded,
	}
}

// revisionOf answers a validated revision for batch fixtures. The value is
// illustrative — batch tests assert that the prompt prints the unit's own
// revision, not that any particular SHA is right.
func revisionOf(t *testing.T, sha string) core.Revision {
	t.Helper()
	rev, err := core.NewRevision(sha)
	if err != nil {
		t.Fatalf("revision %q: %v", sha, err)
	}
	return rev
}

// TestReviewPromptNumbersEveryBatchUnitWithReadableEvidence requires the
// rendered batch block to number every unit exactly once, with readable
// content or an explicit access limit, plus visible advisory and exclusion
// blocks.
//
// It fails before the change because the prompt has no numbered unit
// contract: Render carries no batch input at all.
func TestReviewPromptNumbersEveryBatchUnitWithReadableEvidence(t *testing.T) {
	o := loadReviewOracle(t)
	base := "1111111111111111111111111111111111111111"
	head := "2222222222222222222222222222222222222222"
	units := []prompt.BatchUnit{
		{Path: "src/added.go", Change: core.ChangeAdded, ContentRevision: revisionOf(t, head),
			Body: []byte("package added\n"), Available: true,
			NumberedDiff: []byte("diff --git a/src/added.go b/src/added.go\n")},
		{Path: "src/deleted.go", OldPath: "src/old-deleted.go", Change: core.ChangeDeleted,
			ContentRevision: revisionOf(t, base), Body: []byte("package deleted\n"), Available: true},
		{Path: "assets/logo.bin", Change: core.ChangeAdded, ContentRevision: revisionOf(t, head),
			Available: true, Binary: true},
		{Path: "vendor/secret.bin", Change: core.ChangeModified, ContentRevision: revisionOf(t, head),
			Reason: "permission denied"},
	}
	advisory := []prompt.AdvisoryRef{
		{Path: "src/helper.go", Rule: "search", Term: "SaveOrder"},
		{Path: "src/app_test.go", Rule: "convention"},
	}
	excluded := []prompt.ExclusionRef{{Path: "docs/backlog/item.md", Reason: "backlog destination"}}

	got := string(batchReview(o, units, advisory, excluded).Render())

	// Every unit number appears exactly once, as its own heading.
	for n := 1; n <= len(units); n++ {
		heading := "### " + itoa(n) + ". `"
		if got := countHeading(got, heading); got != 1 {
			t.Errorf("heading %q appears %d times, want exactly once", heading, got)
		}
	}
	// Every unit shows its change kind and its evidence revision.
	for _, want := range []string{
		"src/added.go` — added at `", "src/deleted.go` — deleted at `",
		"assets/logo.bin` — added at `", "vendor/secret.bin` — modified at `",
		head, base,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the batch block does not show %q", want)
		}
	}
	// Readable units show their content; the rename names its old path.
	for _, want := range []string{"package added\n", "package deleted\n", "Previously `src/old-deleted.go`."} {
		if !strings.Contains(got, want) {
			t.Errorf("readable evidence %q is missing", want)
		}
	}
	// The numbered diff travels with its unit.
	if !strings.Contains(got, "Its numbered diff:") || !strings.Contains(got, "diff --git a/src/added.go b/src/added.go") {
		t.Error("the unit's numbered diff is missing")
	}
	// Binary and unreadable units name their access limit and stay required.
	for _, want := range []string{"Binary content is not shown", "No readable content: permission denied"} {
		if !strings.Contains(got, want) {
			t.Errorf("access limit %q is missing", want)
		}
	}
	if strings.Contains(got, "could_not_review` with the failed fallbacks") == false {
		t.Error("the unreadable unit does not say what disposition it needs")
	}
	// Advisory and exclusion blocks are visible with their rules and reasons.
	for _, want := range []string{
		"### Advisory context", "`src/helper.go` (search `SaveOrder`)",
		"`src/app_test.go` (convention)", "### Excluded paths",
		"`docs/backlog/item.md` — backlog destination",
		"They take no disposition and are not omitted in silence.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("advisory/exclusion block does not show %q", want)
		}
	}
	// The coverage contract travels with the batch: every number exactly once,
	// and the scope report even when nothing was found.
	for _, want := range []string{
		"Name every numbered file above in `coverage`, one entry per number",
		"`examined_scope` and `known_limits`",
		"`not_affected` does not exempt a changed file",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the coverage contract does not state %q", want)
		}
	}

	// A batch-free prompt renders no batch block at all, so the frozen prompt
	// stays byte-identical: asserted by TestReviewMatchesTheFrozenPrompt, and
	// the absence of every batch heading here is its readable half.
	plain := string(batchReview(o, nil, nil, nil).Render())
	for _, heading := range []string{"## The files under review", "### 1. `", "### Advisory context", "### Excluded paths"} {
		if strings.Contains(plain, heading) {
			t.Errorf("a batch-free prompt renders %q", heading)
		}
	}
}

// countHeading counts lines starting with the heading text, so "### 1. `"
// does not match "### 12. `".
func countHeading(prompt, heading string) int {
	n := 0
	for _, line := range strings.Split(prompt, "\n") {
		if strings.HasPrefix(line, heading) {
			n++
		}
	}
	return n
}

// TestReviewIntelligenceSupersedesOnlyTheFrozenPromptSections pins the A3
// contract against the frozen parity fixture: a batch-free prompt stays
// byte-identical (TestReviewMatchesTheFrozenPrompt), and a prompt carrying the
// coverage batch differs from the frozen bytes only in the coverage
// instructions and input blocks.
//
// The frozen fixture stays read-only: this test reads prompt_review.json and
// never writes it. The named differences are the "## The files under review"
// block with its numbered units, advisory and exclusion sections, and the one
// coverage-contract sentence in "## Output". Anything else differing is
// unrelated prompt drift and fails this test.
func TestReviewIntelligenceSupersedesOnlyTheFrozenPromptSections(t *testing.T) {
	o := loadReviewOracle(t)
	head := "2222222222222222222222222222222222222222"
	units := []prompt.BatchUnit{
		{Path: "src/added.go", Change: core.ChangeAdded, ContentRevision: revisionOf(t, head),
			Body: []byte("package added\n"), Available: true},
	}
	advisory := []prompt.AdvisoryRef{{Path: "src/helper.go", Rule: "search", Term: "SaveOrder"}}
	excluded := []prompt.ExclusionRef{{Path: "docs/backlog/item.md", Reason: "backlog destination"}}

	withBatch := string(batchReview(o, units, advisory, excluded).Render())
	frozen := o.Prompt

	// Strip the two intentional additions, and what remains must equal the
	// frozen bytes: the skill, REVIEW.md handling, untrusted notice,
	// pull-request block, prior table, open threads, full diff and output
	// instruction are untouched.
	// The oracle's own skill input is the frozen skill text; Render embeds
	// whatever skill it is given, so the skill section is identical on both
	// sides by construction. The ## Output PROMPT section (not the skill's
	// own ## Output) is where the intentional coverage sentence lands.
	stripped := withBatch
	filesAt := strings.Index(stripped, "## The files under review\n\n")
	// The skill carries its own "## Output" section, so the prompt's own
	// output instruction is the LAST occurrence; the batch block sits
	// between the full diff and that instruction.
	outputAt := strings.LastIndex(stripped, "## Output\n\n")
	if filesAt < 0 || outputAt < 0 || !(filesAt < outputAt) {
		t.Fatalf("the batch block or the output heading is missing")
	}
	stripped = stripped[:filesAt] + stripped[outputAt:]
	contract := "Name every numbered file above in `coverage`, one entry per number — " +
		"no more, no fewer, no duplicates — and state `examined_scope` and `known_limits` " +
		"even when the review found nothing.\n"
	if !strings.Contains(stripped, contract) {
		t.Fatalf("the coverage-contract sentence is missing from ## Output")
	}
	stripped = strings.Replace(stripped, contract, "", 1)
	if stripped != frozen {
		t.Fatalf("after removing the batch block and the coverage sentence, the prompt differs from the frozen fixture:\n%s",
			firstDifference([]byte(stripped), []byte(frozen)))
	}

	// The intentional additions are exactly the coverage instructions and
	// input blocks: numbered units, readable evidence, advisory, exclusions.
	for _, want := range []string{
		"## The files under review", "### 1. `src/added.go`", "package added",
		"### Advisory context", "### Excluded paths", contract,
	} {
		if !strings.Contains(withBatch, want) {
			t.Errorf("the intentional addition %q is missing", want)
		}
	}
}
