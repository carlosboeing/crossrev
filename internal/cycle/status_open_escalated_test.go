package cycle_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/carlosboeing/crossrev/internal/config"
	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/cycle"
	"github.com/carlosboeing/crossrev/internal/forge"
	"github.com/carlosboeing/crossrev/internal/prstate"
)

// TestStatusNextRecountsOpenHumanDecisionsFromThreadState pins the C5 item 6
// fix: the NEXT escalation count is read off thread state rather than off the
// markers alone. A human who settles an escalated thread by hand leaves the
// marker saying `escalated`, so counting markers reports a decision that is
// no longer open. A thread the human resolved is settled; one still open, or
// with no thread state to consult, still needs a human decision.
func TestStatusNextRecountsOpenHumanDecisionsFromThreadState(t *testing.T) {
	const first = "1f3b64041e298591"
	const second = "aaaaaaaaaaaaaaaa"

	resolutions, err := json.Marshal([]map[string]string{
		{"finding_id": first, "resolution": "escalated", "reply": "needs a human"},
		{"finding_id": second, "resolution": "escalated", "reply": "needs a human"},
	})
	if err != nil {
		t.Fatalf("resolutions: %v", err)
	}

	thread := func(id string, resolved bool) forge.ReviewThread {
		parsed, err := prstate.ParseFindingID(id)
		if err != nil {
			t.Fatalf("finding id: %v", err)
		}
		return forge.ReviewThread{
			ID:         "thread-for-" + id,
			IsResolved: resolved,
			FindingIDs: []core.FindingID{parsed},
		}
	}

	cases := []struct {
		name    string
		threads []forge.ReviewThread
		// wantOpen is the human-decision count NEXT should report. Zero
		// means NEXT must not name a human decision at all.
		wantOpen int
	}{
		{"both threads settled by hand", []forge.ReviewThread{thread(first, true), thread(second, true)}, 0},
		{"one thread still open", []forge.ReviewThread{thread(first, true), thread(second, false)}, 1},
		{"both threads still open", []forge.ReviewThread{thread(first, false), thread(second, false)}, 2},
		{"no thread state to consult", nil, 2},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := statusNextForThreads(t, resolutions, tc.threads)
			var decision string
			for _, line := range got {
				if strings.Contains(line.Text, "need a human decision") {
					decision = line.Text
				}
			}
			if tc.wantOpen == 0 {
				if decision != "" {
					t.Errorf("NEXT reports %q, want no human-decision line: %v", decision, got)
				}
				return
			}
			want := "need a human decision"
			if decision == "" {
				t.Fatalf("NEXT names no human decision, want %d open: %v", tc.wantOpen, got)
			}
			if !strings.Contains(decision, want) {
				t.Errorf("NEXT line = %q, want it to contain %q", decision, want)
			}
			prefix := string(rune('0'+tc.wantOpen)) + " finding"
			if !strings.HasPrefix(decision, prefix) {
				t.Errorf("NEXT line = %q, want it to start with %q", decision, prefix)
			}
		})
	}
}

// statusNextForThreads loads a halted pass-1 report whose resolve marker
// carries the given resolutions, with the given review threads behind it,
// and answers its NEXT section.
func statusNextForThreads(t *testing.T, resolutions json.RawMessage, threads []forge.ReviewThread) []cycle.NextLine {
	t.Helper()
	const head = "2c4a46cb321db01826d116b5ef2add6b0284d68c"
	headRev, err := core.NewRevision(head)
	if err != nil {
		t.Fatalf("head revision: %v", err)
	}
	baseRev, err := core.NewRevision(statusBase)
	if err != nil {
		t.Fatalf("base revision: %v", err)
	}
	slug, err := core.ParseSlug(statusRepo)
	if err != nil {
		t.Fatalf("slug: %v", err)
	}
	review := json.RawMessage(`{"v":1,"leg":"review","pass":1,"state":"complete",` +
		`"ts":1786999940,"done_ts":1786999970,"run_id":"x","head_sha":"` + head + `",` +
		`"harness":"claude","model":"m","effort":"high","model_reported":"m",` +
		`"tokens":1000,"verdict":"issues-remain","findings":[{"severity":"high"}]}`)
	resolve := json.RawMessage(`{"v":1,"leg":"resolve","pass":1,"state":"complete",` +
		`"ts":1786999940,"done_ts":1786999970,"run_id":"x","head_sha":"` + head + `",` +
		`"harness":"claude","model":"m","effort":"high","model_reported":"m",` +
		`"tokens":2000,"blocked":false,"summary":"s","resolutions":` + string(resolutions) + `}`)
	comments := make([]forge.IssueComment, 0, 2)
	for i, raw := range []json.RawMessage{review, resolve} {
		body, err := prstate.EncodeMarker(raw)
		if err != nil {
			t.Fatalf("encoding marker %d: %v", i, err)
		}
		comments = append(comments, forge.IssueComment{
			ID:          int64(9001 + i),
			AuthorLogin: statusAuthor,
			Body:        "Summary." + body,
		})
	}
	s := &cycle.Status{
		Forge: &statusForge{
			pr: forge.PullRequest{
				Number:       statusPR,
				Title:        "Add refresh",
				URL:          "https://github.com/x",
				HeadRefName:  "feature",
				HeadRefOid:   headRev,
				BaseRefName:  "main",
				BaseRefOid:   baseRev,
				ChangedFiles: 1,
				Labels:       []forge.Label{{Name: "crossrev/halted"}},
				State:        "OPEN",
			},
			comments: comments,
			threads:  threads,
		},
		Liveness: statusLife{},
		Now:      func() time.Time { return time.Unix(1787000000, 0) },
		Show: func(_ context.Context, _ core.Revision, path string) ([]byte, config.FileStatus, error) {
			if path != ".github/crossrev.yml" {
				return nil, config.NotFound, nil
			}
			return []byte("version: 2\nmode: local\npolicy:\n  min_fix_severity: medium\n" +
				"  max_passes_per_cycle: 3\n  max_files_changed_per_pr: 200\n  max_prs_per_day: 25\n" +
				"reviewer:\n  harness: claude\n  model: reviewer-model\nresolver:\n  harness: claude\n" +
				"  model: resolver-model\nbacklog:\n  destination: none\n"), config.IsFile, nil
		},
	}
	report, err := s.Load(context.Background(), slug, statusPR)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if string(report.State) != "halted" {
		t.Fatalf("state = %q, want halted", report.State)
	}
	return report.Next
}
