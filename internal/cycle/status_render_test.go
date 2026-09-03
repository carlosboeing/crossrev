package cycle_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/carlosboeing/crossrev/internal/config"
	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/cycle"
	"github.com/carlosboeing/crossrev/internal/forge"
	"github.com/carlosboeing/crossrev/internal/prstate"
	"github.com/carlosboeing/crossrev/internal/ui"
)

// statusPage is one whole `crossrev status` page, measured from the shell.
//
// Every field was taken off a fixture checkout the way tests/test-status.sh
// builds one: the same routes, the same marker bodies, the same committed
// configuration. `page` is the bytes `crossrev status --pr 42` wrote with
// stdout on a pipe, and `page_colour` the bytes it wrote with stdout on a pty,
// which is the only switch lib/ui.sh:19 has for colour.
type statusPage struct {
	Name         string            `json:"name"`
	Shell        string            `json:"shell"`
	Now          int64             `json:"now"`
	ConfigYAML   string            `json:"config_yaml"`
	Author       string            `json:"author"`
	HeadSHA      string            `json:"head_sha"`
	BaseSHA      string            `json:"base_sha"`
	URL          string            `json:"url"`
	Title        string            `json:"title"`
	HeadBranch   string            `json:"head_branch"`
	ChangedFiles int               `json:"changed_files"`
	Labels       []string          `json:"labels"`
	Markers      []json.RawMessage `json:"markers"`
	Liveness     statusCaseLife    `json:"liveness"`
	Page         string            `json:"page"`
	PageColour   string            `json:"page_colour"`
}

func statusPages(t *testing.T) []statusPage {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join("testdata", "status", "pages", "*.json"))
	if err != nil {
		t.Fatalf("globbing the page fixtures: %v", err)
	}
	if len(paths) == 0 {
		t.Fatal("no page fixtures found")
	}
	pages := make([]statusPage, 0, len(paths))
	for _, path := range paths {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		var p statusPage
		if err := json.Unmarshal(body, &p); err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		pages = append(pages, p)
	}
	return pages
}

// statusPageReport drives the real Load for one page fixture.
//
// Nothing here substitutes a value the fixture did not record: the title, the
// url, the branch, the file count, the author and the whole configuration file
// come off the fixture, so a page measured with an empty url loads with an
// empty url rather than with a default that would hide the omitted line.
func statusPageReport(t *testing.T, p statusPage) cycle.Report {
	t.Helper()
	head, err := core.NewRevision(p.HeadSHA)
	if err != nil {
		t.Fatalf("%s: head revision: %v", p.Name, err)
	}
	base, err := core.NewRevision(p.BaseSHA)
	if err != nil {
		t.Fatalf("%s: base revision: %v", p.Name, err)
	}
	slug, err := core.ParseSlug(statusRepo)
	if err != nil {
		t.Fatalf("%s: slug: %v", p.Name, err)
	}

	labels := make([]forge.Label, 0, len(p.Labels))
	for _, name := range p.Labels {
		labels = append(labels, forge.Label{Name: name})
	}
	comments := make([]forge.IssueComment, 0, len(p.Markers))
	for i, raw := range p.Markers {
		body, err := prstate.EncodeMarker(raw)
		if err != nil {
			t.Fatalf("%s: encoding marker %d: %v", p.Name, i, err)
		}
		comments = append(comments, forge.IssueComment{
			ID:          int64(9001 + i),
			AuthorLogin: p.Author,
			Body:        "Summary." + body,
		})
	}

	f := &statusPageForge{
		login: p.Author,
		pr: forge.PullRequest{
			Number:       statusPR,
			Title:        p.Title,
			URL:          p.URL,
			HeadRefName:  p.HeadBranch,
			HeadRefOid:   head,
			BaseRefName:  "main",
			BaseRefOid:   base,
			ChangedFiles: p.ChangedFiles,
			Labels:       labels,
			State:        "OPEN",
		},
		comments: comments,
	}
	s := &cycle.Status{
		Forge:    f,
		Liveness: statusLife{life: cycle.Life(p.Liveness.Life), detail: p.Liveness.Detail},
		Now:      func() time.Time { return time.Unix(p.Now, 0) },
		Show: func(_ context.Context, rev core.Revision, path string) ([]byte, config.FileStatus, error) {
			if rev.SHA() == p.BaseSHA && path == ".github/crossrev.yml" {
				return []byte(p.ConfigYAML), config.IsFile, nil
			}
			return nil, config.NotFound, nil
		},
	}
	report, err := s.Load(context.Background(), slug, statusPR)
	if err != nil {
		t.Fatalf("%s: Load: %v", p.Name, err)
	}
	return report
}

// statusPageForge is statusForge with the viewer's login taken from the
// fixture, because the LOOP section prints it.
type statusPageForge struct {
	statusForge
	login string
}

func (f *statusPageForge) ViewerLogin(context.Context) (string, error) { return f.login, nil }
func (f *statusPageForge) AwaitingPullRequests(context.Context, core.Slug) []forge.AwaitingPullRequest {
	return nil
}

// TestStatusRendersThePageTheShellPrints asserts the whole page byte for byte
// against what `crossrev status` wrote (lib/run.sh:3055-3103): the five
// sections, the PULL REQUEST and LOOP wording, the three-way passes line, the
// backlog line, the gutter, the nine-column leg label and the outcome glyphs.
func TestStatusRendersThePageTheShellPrints(t *testing.T) {
	for _, p := range statusPages(t) {
		t.Run(p.Name, func(t *testing.T) {
			report := statusPageReport(t, p)
			var buf bytes.Buffer
			cycle.Render(&ui.IO{Out: &buf}, report)
			if buf.String() != p.Page {
				t.Errorf("page (%s):\n--- got ---\n%s\n--- want ---\n%s\n%s",
					p.Shell, buf.String(), p.Page, statusFirstDifference(buf.String(), p.Page))
			}
		})
	}
}

// TestStatusRendersTheColouredPage is the same assertion with the palette a
// terminal gets, so the escape codes are pinned where the shell puts them
// rather than only the words between them.
func TestStatusRendersTheColouredPage(t *testing.T) {
	for _, p := range statusPages(t) {
		t.Run(p.Name, func(t *testing.T) {
			if p.PageColour == "" {
				t.Fatalf("%s records no coloured page", p.Name)
			}
			report := statusPageReport(t, p)
			var buf bytes.Buffer
			cycle.Render(&ui.IO{Out: &buf, Palette: ui.Colour()}, report)
			if buf.String() != p.PageColour {
				t.Errorf("coloured page (%s):\n--- got ---\n%q\n--- want ---\n%q\n%s",
					p.Shell, buf.String(), p.PageColour, statusFirstDifference(buf.String(), p.PageColour))
			}
		})
	}
}

// statusFirstDifference names the byte the two pages part at, because a diff of
// two forty-line pages is unreadable without one.
func statusFirstDifference(got, want string) string {
	limit := len(got)
	if len(want) < limit {
		limit = len(want)
	}
	for i := 0; i < limit; i++ {
		if got[i] != want[i] {
			from := i - 40
			if from < 0 {
				from = 0
			}
			return "first difference at byte " + strconv.Itoa(i) +
				"\n  got  ..." + strconv.Quote(got[from:min(len(got), i+40)]) +
				"\n  want ..." + strconv.Quote(want[from:min(len(want), i+40)])
		}
	}
	if len(got) != len(want) {
		return "the shorter page is a prefix of the longer one: got " +
			strconv.Itoa(len(got)) + " bytes, want " + strconv.Itoa(len(want))
	}
	return ""
}

// TestStatusOmitsThePassesSectionWhenNothingRan pins the one section that is
// dropped rather than printed empty (lib/run.sh:3087-3097). A heading with an
// empty body reads as a bug, and the passes line above already says none yet.
func TestStatusOmitsThePassesSectionWhenNothingRan(t *testing.T) {
	for _, p := range statusPages(t) {
		t.Run(p.Name, func(t *testing.T) {
			report := statusPageReport(t, p)
			var buf bytes.Buffer
			cycle.Render(&ui.IO{Out: &buf}, report)
			printed := strings.Contains(buf.String(), "PASSES")
			if printed != (report.MaxPass > 0) {
				t.Errorf("PASSES printed=%v with max_pass=%d (%s)",
					printed, report.MaxPass, p.Shell)
			}
		})
	}
}
