package resolve

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/prstate"
)

func TestRender(t *testing.T) {
	t.Run("replies match presentation.json", func(t *testing.T) {
		raw, err := os.ReadFile(filepath.Join(repoRoot(t), "tests/fixtures/parity/presentation.json"))
		if err != nil {
			t.Fatal(err)
		}
		var fixture struct {
			Replies []struct {
				Name        string          `json:"name"`
				Disposition json.RawMessage `json:"disposition"`
				Tracked     string          `json:"tracked"`
				Pass        int             `json:"pass"`
				Harness     string          `json:"harness"`
				Model       string          `json:"model"`
				BodyB64     string          `json:"body_b64"`
			} `json:"replies"`
			CommitSubject []struct {
				Name    string `json:"name"`
				Subject string `json:"subject"`
				RC      int    `json:"rc"`
			} `json:"commit_subject"`
			URLPath []struct {
				Name string `json:"name"`
				Path string `json:"path"`
				Out  string `json:"out"`
			} `json:"url_path"`
		}
		if err := json.Unmarshal(raw, &fixture); err != nil {
			t.Fatal(err)
		}
		if len(fixture.Replies) == 0 {
			t.Fatal("presentation.json records no reply vectors")
		}
		for _, vector := range fixture.Replies {
			got := ReplyBody(vector.Disposition, vector.Tracked, vector.Pass, vector.Harness, vector.Model)
			want, err := base64.StdEncoding.DecodeString(vector.BodyB64)
			if err != nil {
				t.Fatalf("%s: decode body: %v", vector.Name, err)
			}
			if got != string(want) {
				t.Errorf("%s:\n got %q\nwant %q", vector.Name, got, want)
			}
		}
		if len(fixture.CommitSubject) == 0 {
			t.Fatal("presentation.json records no commit_subject vectors")
		}
		for _, vector := range fixture.CommitSubject {
			ok := CommitSubjectOK(vector.Subject, "")
			want := vector.RC == 0
			if ok != want {
				t.Errorf("%s: CommitSubjectOK(%q) = %v, want %v (rc %d)",
					vector.Name, vector.Subject, ok, want, vector.RC)
			}
		}
		if len(fixture.URLPath) == 0 {
			t.Fatal("presentation.json records no url_path vectors")
		}
		for _, vector := range fixture.URLPath {
			got := URLPath(vector.Path)
			want := strings.TrimRight(vector.Out, "\n")
			if got != want {
				t.Errorf("%s: URLPath(%q) = %q, want %q", vector.Name, vector.Path, got, want)
			}
		}
	})

	t.Run("a commit body names the title, location and trailers", func(t *testing.T) {
		resolutions := json.RawMessage(`[{"finding_id":"` + testFinding + `","resolution":"fixed"}]`)
		findings := defaultFindings()
		head := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		got := CommitBody(resolutions, findings, "fixed", head, 1, "acme/widget", 42)
		if !strings.Contains(got, "- nil deref.") {
			t.Errorf("body missing titled bullet: %s", got)
		}
		if !strings.Contains(got, "app.ts:2 - https://github.com/acme/widget/pull/42/files#r") &&
			!strings.Contains(got, "app.ts:2 - https://github.com/acme/widget/blob/") {
			t.Errorf("body missing location: %s", got)
		}
		if !strings.Contains(got, "Crossrev-pr: acme/widget#42") {
			t.Errorf("body missing pr trailer: %s", got)
		}
		if !strings.Contains(got, "Crossrev-pass: 1") {
			t.Errorf("body missing pass trailer: %s", got)
		}
		if strings.Contains(got, testFinding) {
			t.Errorf("finding id leaked into the body: %s", got)
		}
	})

	t.Run("a summary records unthreaded replies and a short commit", func(t *testing.T) {
		resolutions := json.RawMessage(`[{"finding_id":"` + testFinding + `","resolution":"fixed"}]`)
		findings := defaultFindings()
		marker := prstate.Marker{
			Pass:       1,
			Summary:    prstate.Some("Fixed it."),
			CommitSHA:  prstate.Some("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"),
			Unthreaded: prstate.Some(1),
			HeadSHA:    prstate.Some(testHeadSHA),
			Harness:    prstate.Some("claude"),
			Blocked:    prstate.Some(false),
		}
		got := ResolveSummaryBody(resolutions, findings, "", marker, "acme/widget", 42, 3)
		if !strings.Contains(got, "## crossrev resolved pass 1") {
			t.Errorf("heading missing: %s", got)
		}
		if !strings.Contains(got, "Fixes pushed as `aaaaaaa`.") {
			t.Errorf("short commit missing: %s", got)
		}
		if !strings.Contains(got, "One reply could not be posted in the review thread") {
			t.Errorf("unthreaded note missing: %s", got)
		}
		if !strings.Contains(got, "Fixed it.") {
			t.Errorf("summary missing: %s", got)
		}
	})

	t.Run("empty footnote with non-empty gaps keeps trailing space", func(t *testing.T) {
		resolutions := json.RawMessage(`[]`)
		findings := json.RawMessage(`[]`)
		marker := prstate.Marker{
			Pass:    1,
			HeadSHA: prstate.Some(testHeadSHA),
			Harness: prstate.Some("claude"),
			Model:   prstate.Some("claude-3-5-sonnet"),
			Blocked: prstate.Some(false),
		}
		got := ResolveSummaryBody(resolutions, findings, "", marker, "acme/widget", 42, 3)
		wantFootnote := "<sub>claude does not report which model answered, so the model above is the one crossrev requested. </sub>\n\n"
		if !strings.Contains(got, wantFootnote) {
			t.Errorf("summary footnote missing trailing space:\n got: %q\nwant containing: %q", got, wantFootnote)
		}
	})
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("no go.mod above %s", dir)
		}
		dir = parent
	}
}
