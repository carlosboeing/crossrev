package review_test

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/prstate"
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
