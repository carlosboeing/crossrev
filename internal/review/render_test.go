package review_test

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/prstate"
	"github.com/carlosboeing/crossrev/internal/prstate/storetest"
	"github.com/carlosboeing/crossrev/internal/review"
)

type presentationFixture struct {
	MinFixSeverity string `json:"min_fix_severity"`
	MaxPasses      int    `json:"max_passes_per_cycle"`
	Repo           string `json:"repo"`
	PR             int    `json:"pr"`
	Severity       []struct {
		Name  string `json:"name"`
		Value string `json:"value"`
		Out   string `json:"out"`
	} `json:"severity"`
	Category []struct {
		Name  string `json:"name"`
		Value string `json:"value"`
		Out   string `json:"out"`
	} `json:"category"`
	SameModel []struct {
		Name string `json:"name"`
		Want string `json:"want"`
		Got  string `json:"got"`
		RC   int    `json:"rc"`
	} `json:"same_model"`
	Elapsed []struct {
		Name string `json:"name"`
		From string `json:"from"`
		To   string `json:"to"`
		Out  string `json:"out"`
	} `json:"elapsed"`
	Thousands []struct {
		Name string `json:"name"`
		In   string `json:"in"`
		Out  string `json:"out"`
	} `json:"thousands"`
	FindingLabel []struct {
		Name    string          `json:"name"`
		Finding json.RawMessage `json:"finding"`
		Out     string          `json:"out"`
	} `json:"finding_label"`
	Actionable []struct {
		Name     string          `json:"name"`
		Findings json.RawMessage `json:"findings"`
		Out      string          `json:"out"`
	} `json:"actionable"`
	URLPath []struct {
		Name string `json:"name"`
		Path string `json:"path"`
		Out  string `json:"out"`
	} `json:"url_path"`
	Comments []struct {
		Name    string          `json:"name"`
		Finding json.RawMessage `json:"finding"`
		Pass    int             `json:"pass"`
		Harness string          `json:"harness"`
		Model   string          `json:"model"`
		BodyB64 string          `json:"body_b64"`
	} `json:"comments"`
	Summaries []struct {
		Name     string          `json:"name"`
		Findings json.RawMessage `json:"findings"`
		Marker   json.RawMessage `json:"marker"`
		BodyB64  string          `json:"body_b64"`
	} `json:"summaries"`
}

func loadPresentation(t *testing.T) presentationFixture {
	t.Helper()
	raw, err := os.ReadFile("../../tests/fixtures/parity/presentation.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture presentationFixture
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func TestRenderSeverityEmoji(t *testing.T) {
	for _, c := range loadPresentation(t).Severity {
		if got := review.SeverityEmoji(c.Value); got != c.Out {
			t.Errorf("%s: SeverityEmoji(%q) = %q, want %q", c.Name, c.Value, got, c.Out)
		}
	}
}

func TestRenderCategoryEmoji(t *testing.T) {
	for _, c := range loadPresentation(t).Category {
		if got := review.CategoryEmoji(c.Value); got != c.Out {
			t.Errorf("%s: CategoryEmoji(%q) = %q, want %q", c.Name, c.Value, got, c.Out)
		}
	}
}

func TestRenderSameModel(t *testing.T) {
	for _, c := range loadPresentation(t).SameModel {
		got := review.SameModel(c.Want, c.Got)
		want := c.RC == 0
		if got != want {
			t.Errorf("%s: SameModel(%q, %q) = %v, want %v", c.Name, c.Want, c.Got, got, want)
		}
	}
}

func TestRenderElapsed(t *testing.T) {
	for _, c := range loadPresentation(t).Elapsed {
		if got := review.Elapsed(c.From, c.To); got != c.Out {
			t.Errorf("%s: Elapsed(%q, %q) = %q, want %q", c.Name, c.From, c.To, got, c.Out)
		}
	}
}

func TestRenderThousands(t *testing.T) {
	for _, c := range loadPresentation(t).Thousands {
		if got := review.Thousands(c.In); got != c.Out {
			t.Errorf("%s: Thousands(%q) = %q, want %q", c.Name, c.In, got, c.Out)
		}
	}
}

func TestRenderURLPath(t *testing.T) {
	for _, c := range loadPresentation(t).URLPath {
		got := review.URLPath(c.Path) + "\n"
		if got != c.Out {
			t.Errorf("%s: URLPath(%q) = %q, want %q", c.Name, c.Path, got, c.Out)
		}
	}
}

func TestRenderURLPathDivergencesAndUnreserved(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"a+b", "a%2Bb"},
		{"a@b", "a%40b"},
		{"a:b", "a%3Ab"},
		{"a&b", "a%26b"},
		{"a=b", "a%3Db"},
		{"a$b", "a%24b"},
		{"a+b/a@b/a:b/a&b/a=b/a$b", "a%2Bb/a%40b/a%3Ab/a%26b/a%3Db/a%24b"},
		{
			"ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-._~",
			"ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-._~",
		},
		{
			"dir/ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-._~/file.txt",
			"dir/ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-._~/file.txt",
		},
	}
	for _, tc := range cases {
		if got := review.URLPath(tc.in); got != tc.want {
			t.Errorf("URLPath(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestRenderFindingLabel(t *testing.T) {
	for _, c := range loadPresentation(t).FindingLabel {
		var f review.Finding
		if err := json.Unmarshal(c.Finding, &f); err != nil {
			t.Fatalf("%s: %v", c.Name, err)
		}
		if got := review.FindingLabel(f); got != c.Out {
			t.Errorf("%s: FindingLabel = %q, want %q", c.Name, got, c.Out)
		}
	}
}

func TestRenderActionable(t *testing.T) {
	fx := loadPresentation(t)
	for _, c := range fx.Actionable {
		var findings []review.Finding
		if err := json.Unmarshal(c.Findings, &findings); err != nil {
			t.Fatalf("%s: %v", c.Name, err)
		}
		got := review.ActionableCount(findings, fx.MinFixSeverity)
		want := strings.TrimSuffix(c.Out, "\n")
		if fmt.Sprint(got) != want {
			t.Errorf("%s: ActionableCount = %d, want %s", c.Name, got, want)
		}
	}
}

func TestRenderCommentBody(t *testing.T) {
	fx := loadPresentation(t)
	for _, c := range fx.Comments {
		var f review.Finding
		if err := json.Unmarshal(c.Finding, &f); err != nil {
			t.Fatalf("%s: %v", c.Name, err)
		}
		got := review.CommentBody(f, c.Pass, c.Harness, c.Model, fx.MinFixSeverity)
		want := decodeB64(t, c.BodyB64)
		if got != want {
			t.Errorf("%s: comment body mismatch\n got: %q\nwant: %q", c.Name, got, want)
		}
	}
}

func TestRenderSummaryBody(t *testing.T) {
	fx := loadPresentation(t)
	ctx := review.RenderContext{Repo: fx.Repo, PR: fx.PR, MinFix: fx.MinFixSeverity, MaxPass: fx.MaxPasses}
	for _, c := range fx.Summaries {
		var findings []review.Finding
		if err := json.Unmarshal(c.Findings, &findings); err != nil {
			t.Fatalf("%s findings: %v", c.Name, err)
		}
		marker, err := prstate.ParseMarker(c.Marker)
		if err != nil {
			t.Fatalf("%s marker: %v", c.Name, err)
		}
		got := review.SummaryBody(findings, marker, ctx)
		want := decodeB64(t, c.BodyB64)
		if got != want {
			t.Errorf("%s: summary body mismatch\n got: %q\nwant: %q", c.Name, got, want)
		}
	}
}

func decodeB64(t *testing.T, s string) string {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// TestTheCoverageFootnoteReadsAsASentence pins the footnote's register: a
// human sentence with counts and the short head SHA, not a machine-facing
// generation line. This file is external so the footnote is asserted
// through SummaryBody; render.go carries no other coverage wording, so the
// leak list still pins the footnote and nothing else. The counts arrive
// with the render context, the way publish supplies the pass's own
// convergence; the inline payload below only keeps the marker-store claim
// well-formed.
func TestTheCoverageFootnoteReadsAsASentence(t *testing.T) {
	const head = "c2c67070123456789abcdef0123456789abcdef0"
	gen := storetest.FixtureGeneration(t, prstate.GenerationFull)
	gen.Gen = 3
	gen.Paths = make([]string, 12)
	gen.Records = make([]prstate.Record, 12)
	for i := range gen.Records {
		gen.Paths[i] = fmt.Sprintf("file%d.go", i+1)
		gen.Records[i] = prstate.Record{
			Type:       prstate.CoverageRecordUnit,
			UnitID:     fmt.Sprintf("%016x", i),
			PathIndex:  i,
			Kind:       prstate.CoverageGranularityFile,
			Change:     "modified",
			BodyDigest: fmt.Sprintf("%064x", i),
			Verdict:    prstate.Some("no_issue"),
			FindingIDs: []string{},
			Evidence:   []prstate.Evidence{},
			Reason:     prstate.Null[string](),
			Supplied: prstate.Some(prstate.SuppliedInput{
				Digest:    fmt.Sprintf("%064x", i+100),
				Form:      "full_text",
				Truncated: false,
			}),
			Reaction: prstate.UnimplementedReaction(),
		}
	}
	manifest, records, err := prstate.EncodeGenerationV2(gen)
	if err != nil {
		t.Fatalf("EncodeGenerationV2: %v", err)
	}
	payload, err := json.Marshal(map[string]json.RawMessage{"manifest": manifest, "records": records})
	if err != nil {
		t.Fatalf("payload envelope: %v", err)
	}
	marker := prstate.Marker{
		HeadSHA:         prstate.Some(head),
		Harness:         prstate.Some("claude"),
		CoverageGen:     prstate.Some(3),
		CoverageRef:     prstate.Some(prstate.HandleMarker),
		CoveragePayload: payload,
	}
	got := review.SummaryBody(nil, marker, review.RenderContext{
		Repo: "acme/widget", PR: 42, MinFix: "medium", MaxPass: 3,
		Coverage: &review.CoverageCounts{Covered: 12, Required: 12, Excluded: 2},
	})
	if want := "Reviewed 12 of 14 changed files at `c2c6707`."; !strings.Contains(got, want) {
		t.Fatalf("footnote:\n got: %q\nwant it to contain: %q", got, want)
	}
	for _, leak := range []string{"generation", "Generation", "shard", "refs/crossrev"} {
		if strings.Contains(got, leak) {
			t.Fatalf("the footnote leaks %q into a summary a reviewer reads", leak)
		}
	}
}

// TestTheCoverageFootnoteRendersOnARefStoreClaim pins the footnote's
// store-agnosticism: a ref-store claim carries a generation and a commit
// but no inline payload, and the sentence still renders from the counts
// the caller counted.
func TestTheCoverageFootnoteRendersOnARefStoreClaim(t *testing.T) {
	const head = "c2c67070123456789abcdef0123456789abcdef0"
	const commit = "9f3c1abdeadbeef9f3c1abdeadbeef9f3c1abd"
	marker := prstate.Marker{
		HeadSHA:        prstate.Some(head),
		Harness:        prstate.Some("claude"),
		CoverageGen:    prstate.Some(3),
		CoverageRef:    prstate.Some("refs/crossrev/pr/42/reviewer1/coverage"),
		CoverageCommit: prstate.Some(commit),
	}
	got := review.SummaryBody(nil, marker, review.RenderContext{
		Repo: "acme/widget", PR: 42, MinFix: "medium", MaxPass: 3,
		Coverage: &review.CoverageCounts{Covered: 12, Required: 12, Excluded: 2},
	})
	if want := "Reviewed 12 of 14 changed files at `c2c6707`."; !strings.Contains(got, want) {
		t.Fatalf("footnote:\n got: %q\nwant it to contain: %q", got, want)
	}
	for _, leak := range []string{"generation", "Generation", "shard", "refs/crossrev"} {
		if strings.Contains(got, leak) {
			t.Fatalf("the footnote leaks %q into a summary a reviewer reads", leak)
		}
	}
}

// skipMarker builds a marker with a well-formed coverage claim, so the
// footnote renders. The ref-store shape needs no inline payload.
func skipMarker(verdict string) prstate.Marker {
	return prstate.Marker{
		Verdict:        prstate.Some(verdict),
		HeadSHA:        prstate.Some("2c4a46cb321db01826d116b5ef2add6b0284d68c"),
		Harness:        prstate.Some("claude"),
		CoverageGen:    prstate.Some(3),
		CoverageRef:    prstate.Some("refs/crossrev/pr/42/reviewer1/coverage"),
		CoverageCommit: prstate.Some("9f3c1abdeadbeef9f3c1abdeadbeef9f3c1abd"),
	}
}

// A skip opens the summary as a warning above the verdict alert, on a
// converged pass as much as any other: that alert is the first sentence a
// person reads, and a converged pass with a skip still says so at the top.
func TestTheSkipWarningOpensAConvergedSummary(t *testing.T) {
	got := review.SummaryBody(nil, skipMarker("converged"), review.RenderContext{
		Repo: "acme/widget", PR: 42, MinFix: "medium", MaxPass: 3,
		Coverage: &review.CoverageCounts{Covered: 6, Required: 6, Excluded: 1},
		Skipped: []review.SkippedFile{{
			Path:    "src/webAssets.ts",
			Signal:  "header",
			Bytes:   350797,
			Excerpt: "// generated by scripts/build-embed.mjs — do not edit",
		}},
	})

	warning := strings.Index(got, "**Warning: 1 changed file was not reviewed.**")
	if warning < 0 {
		t.Fatalf("no warning block:\n%s", got)
	}
	if alertAt := strings.Index(got, "[!TIP]"); alertAt < 0 || warning > alertAt {
		t.Errorf("the warning does not sit above the converged alert:\n%s", got)
	}
	for _, want := range []string{
		"- `src/webAssets.ts` — generated (header: `// generated by scripts/build-embed.mjs — do not edit`), 350,797 bytes, over the 184,320-byte budget. Check it yourself, or review the file that generates it.",
		"To have CrossRev review a file like this, mark it `-linguist-generated` in `.gitattributes`.",
		"To exclude it without this warning, mark it `linguist-generated`.",
		"Reviewed 6 of 7 changed files at `2c4a46c`. 1 was not reviewed; see the warning above.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("summary is missing %q\n--- summary ---\n%s", want, got)
		}
	}
}

// The same block opens a findings pass, above the findings alert.
func TestTheSkipWarningOpensAFindingsSummary(t *testing.T) {
	findings := []review.Finding{{
		ID: "f1", Path: "a.go", Line: 2, Side: "RIGHT", Severity: "high",
		Category: "correctness", Title: "Unchecked fetch", Why: "w", Fix: "f",
		Anchor: "line", AnchorKind: "line",
	}}
	got := review.SummaryBody(findings, skipMarker("issues-remain"), review.RenderContext{
		Repo: "acme/widget", PR: 42, MinFix: "medium", MaxPass: 3,
		Skipped: []review.SkippedFile{{Path: "dist/bundle.js", Signal: "bundle-name", Bytes: 900000}},
	})
	warning := strings.Index(got, "**Warning: 1 changed file was not reviewed.**")
	if warning < 0 {
		t.Fatalf("no warning block:\n%s", got)
	}
	if alertAt := strings.Index(got, "[!CAUTION]"); alertAt < 0 || warning > alertAt {
		t.Errorf("the warning does not sit above the findings alert:\n%s", got)
	}
	if !strings.Contains(got, "- `dist/bundle.js` — generated (bundle-name), 900,000 bytes, over the 184,320-byte budget.") {
		t.Errorf("the bundle skip line is missing:\n%s", got)
	}
}

// A blocked generated-only pass carries the warning block above the blocked
// alert, and the exclusion line renders without a coverage footnote.
func TestTheSkipWarningOpensABlockedSummary(t *testing.T) {
	marker := skipMarker("blocked")
	marker.BlockedReason = prstate.Some("Every changed file was recognised as generated and is too large for one review prompt, so nothing was reviewed.")
	marker.CoverageGen = prstate.Null[int]()
	marker.CoverageRef = prstate.Null[string]()
	marker.CoverageCommit = prstate.Null[string]()
	got := review.SummaryBody(nil, marker, review.RenderContext{
		Repo: "acme/widget", PR: 42, MinFix: "medium", MaxPass: 3,
		Skipped:  []review.SkippedFile{{Path: "gen/big.ts", Signal: "minified", Bytes: 400000}},
		Excluded: []string{"dist/bundle.js"},
	})
	warning := strings.Index(got, "**Warning: 1 changed file was not reviewed.**")
	if warning < 0 {
		t.Fatalf("no warning block:\n%s", got)
	}
	if alertAt := strings.Index(got, "[!WARNING]"); alertAt < 0 || warning > alertAt {
		t.Errorf("the warning does not sit above the blocked alert:\n%s", got)
	}
	if !strings.Contains(got, "Excluded by repository policy: `dist/bundle.js`") {
		t.Errorf("the exclusion line is missing:\n%s", got)
	}
	if strings.Contains(got, "Reviewed") {
		t.Errorf("a pass with no coverage claim printed a footnote:\n%s", got)
	}
}

// A lockfile skip says its resolved URLs and hashes were not read; the
// bundle guidance about a generating file would be wrong for one.
func TestTheLockfileSkipCarriesLockfileGuidance(t *testing.T) {
	got := review.SummaryBody(nil, skipMarker("converged"), review.RenderContext{
		Repo: "acme/widget", PR: 42, MinFix: "medium", MaxPass: 3,
		Skipped: []review.SkippedFile{{Path: "web/package-lock.json", Signal: "lockfile", Bytes: 700000}},
	})
	if !strings.Contains(got, "generated (lockfile), 700,000 bytes") {
		t.Errorf("the lockfile skip line is missing:\n%s", got)
	}
	if !strings.Contains(got, "check its resolved URLs and hashes by hand or with a lockfile linter") {
		t.Errorf("the lockfile guidance is missing:\n%s", got)
	}
	if strings.Contains(got, "review the file that generates it") {
		t.Errorf("a lockfile skip got the bundle guidance:\n%s", got)
	}
}

// The skip list is bounded like the halt report: past 8 KiB it ends with a
// count rather than filling the comment.
func TestTheSkipWarningBoundsItsList(t *testing.T) {
	var skipped []review.SkippedFile
	for i := 0; i < 90; i++ {
		skipped = append(skipped, review.SkippedFile{
			Path:   fmt.Sprintf("gen/some/rather/long/path/number-%03d/bundle.js", i),
			Signal: "bundle-name",
			Bytes:  300000,
		})
	}
	got := review.SummaryBody(nil, skipMarker("converged"), review.RenderContext{
		Repo: "acme/widget", PR: 42, MinFix: "medium", MaxPass: 3,
		Skipped: skipped,
	})
	if !strings.Contains(got, "**Warning: 90 changed files were not reviewed.**") {
		t.Errorf("the plural opening is missing:\n%s", got)
	}
	if !strings.Contains(got, "more skipped files") {
		t.Errorf("no remaining-count line past the bound:\n%s", got)
	}
	if strings.Contains(got, "number-089") {
		t.Errorf("the list ran past its bound to the last path:\n%s", got)
	}
}

// A policy exclusion is footnote information, not a warning: the denominator
// counts it, and the line below the footnote names it.
func TestPolicyExclusionsAreFootnoteInformation(t *testing.T) {
	got := review.SummaryBody(nil, skipMarker("converged"), review.RenderContext{
		Repo: "acme/widget", PR: 42, MinFix: "medium", MaxPass: 3,
		Coverage: &review.CoverageCounts{Covered: 5, Required: 5, Excluded: 1},
		Excluded: []string{"docs/backlog/item.md"},
	})
	if !strings.Contains(got, "Reviewed 5 of 6 changed files at `2c4a46c`.") {
		t.Errorf("the footnote does not count the exclusion:\n%s", got)
	}
	if !strings.Contains(got, "Excluded by repository policy: `docs/backlog/item.md`") {
		t.Errorf("the exclusion line is missing:\n%s", got)
	}
	if strings.Contains(got, "Warning:") {
		t.Errorf("a policy exclusion printed a warning:\n%s", got)
	}
}

// A pass with nothing excluded and nothing skipped keeps the footnote it has
// always had.
func TestTheFootnoteIsUnchangedWithoutExclusions(t *testing.T) {
	got := review.SummaryBody(nil, skipMarker("converged"), review.RenderContext{
		Repo: "acme/widget", PR: 42, MinFix: "medium", MaxPass: 3,
		Coverage: &review.CoverageCounts{Covered: 12, Required: 12},
	})
	if want := "Reviewed 12 of 12 changed files at `2c4a46c`."; !strings.Contains(got, want) {
		t.Errorf("footnote changed without exclusions:\n%s", got)
	}
	if strings.Contains(got, "Excluded by repository policy") {
		t.Errorf("an exclusion line printed with no exclusions:\n%s", got)
	}
}
