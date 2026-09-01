package review_test

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/forge"
	"github.com/carlosboeing/crossrev/internal/policy"
	"github.com/carlosboeing/crossrev/internal/review"
)

func TestAdmission(t *testing.T) {
	threeDoneAtOldHead := func(t *testing.T) []forge.IssueComment {
		t.Helper()
		var comments []forge.IssueComment
		for pass := 1; pass <= 3; pass++ {
			raw := fmt.Sprintf(`{"v":1,"leg":"review","pass":%d,"state":"complete","ts":1699950000,"run_id":"x","head_sha":%q,"verdict":"issues-remain","findings":[]}`, pass, oldSHA)
			comments = append(comments, commentWithMarker(t, int64(8000+pass), parseMarker(t, raw)))
		}
		return comments
	}

	tests := []struct {
		name         string
		setup        func(*testing.T, *env, *review.Request)
		want         review.Outcome
		wantReason   string
		wantPass     int
		wantHarness  bool
		wantClaim    bool
		wantDeclined bool
		wantLabel    string
	}{
		{
			name: "human ignores the file cap",
			setup: func(_ *testing.T, e *env, req *review.Request) {
				req.Trigger = review.TriggerHuman
				e.forge.pr.ChangedFiles = 500
			},
			want:        review.OutcomeInvoked,
			wantPass:    1,
			wantHarness: true,
			wantClaim:   true,
		},
		{
			name: "automatic file cap declines",
			setup: func(_ *testing.T, e *env, req *review.Request) {
				req.Trigger = review.TriggerAutomatic
				e.forge.pr.ChangedFiles = 500
			},
			want:         review.OutcomeDeclined,
			wantReason:   "500 files changed, above max_files_changed_per_pr (200)",
			wantPass:     1,
			wantDeclined: true,
			wantLabel:    policy.LabelHalted,
		},
		{
			name: "automatic daily cap declines",
			setup: func(t *testing.T, e *env, req *review.Request) {
				req.Trigger = review.TriggerAutomatic
				e.cfg = mustConfig(t, "version: 1\npolicy:\n  max_prs_per_day: 1\n")
				other := parseMarker(t, fmt.Sprintf(`{"v":1,"leg":"review","pass":1,"state":"complete","ts":1699950000,"run_id":"x","head_sha":%q,"verdict":"converged","findings":[]}`, headSHA))
				encoded, err := other.Encode()
				if err != nil {
					t.Fatal(err)
				}
				e.forge.repoComments = []forge.IssueComment{{
					ID:          1,
					AuthorLogin: author,
					Body:        "other" + encoded,
					CreatedAt:   "2023-11-14T22:13:20Z",
					IssueURL:    "https://api.github.com/repos/acme/widget/issues/99",
				}}
			},
			want:         review.OutcomeDeclined,
			wantReason:   "reached max_prs_per_day (1) — 1 other pull requests were already reviewed in the last 24 hours",
			wantPass:     1,
			wantDeclined: true,
			wantLabel:    policy.LabelHalted,
		},
		{
			name: "automatic pass cap declines a fourth pass",
			setup: func(t *testing.T, e *env, req *review.Request) {
				req.Trigger = review.TriggerAutomatic
				e.forge.comments = threeDoneAtOldHead(t)
			},
			want:         review.OutcomeDeclined,
			wantReason:   "reached max_passes_per_cycle (3)",
			wantPass:     4,
			wantDeclined: true,
			wantLabel:    policy.LabelHalted,
		},
		{
			name: "human without continuation runs past the pass cap",
			setup: func(t *testing.T, e *env, req *review.Request) {
				req.Trigger = review.TriggerHuman
				e.forge.comments = threeDoneAtOldHead(t)
			},
			want:        review.OutcomeInvoked,
			wantPass:    4,
			wantHarness: true,
			wantClaim:   true,
		},
		{
			name: "continuation honours the pass cap",
			setup: func(t *testing.T, e *env, req *review.Request) {
				req.Trigger = review.TriggerHuman
				req.Continuation = true
				e.forge.comments = threeDoneAtOldHead(t)
			},
			want:         review.OutcomeDeclined,
			wantReason:   "reached max_passes_per_cycle (3)",
			wantPass:     4,
			wantDeclined: true,
			wantLabel:    policy.LabelHalted,
		},
		{
			name: "automatic draft skips without writing",
			setup: func(_ *testing.T, e *env, req *review.Request) {
				req.Trigger = review.TriggerAutomatic
				e.forge.pr.IsDraft = true
			},
			want:       review.OutcomeSkipped,
			wantReason: "draft pull request",
		},
		{
			name: "human reviews a draft",
			setup: func(_ *testing.T, e *env, req *review.Request) {
				req.Trigger = review.TriggerHuman
				e.forge.pr.IsDraft = true
			},
			want:        review.OutcomeInvoked,
			wantPass:    1,
			wantHarness: true,
			wantClaim:   true,
		},
		{
			name: "stop label skips without writing",
			setup: func(_ *testing.T, e *env, req *review.Request) {
				e.forge.pr.Labels = []forge.Label{{Name: policy.LabelStop}}
			},
			want:       review.OutcomeSkipped,
			wantReason: "crossrev/stop",
		},
		{
			name: "already reviewed at this head skips",
			setup: func(t *testing.T, e *env, _ *review.Request) {
				raw := fmt.Sprintf(`{"v":1,"leg":"review","pass":1,"state":"complete","ts":1699950000,"run_id":"x","head_sha":%q,"verdict":"issues-remain","findings":[{"severity":"high"}]}`, headSHA)
				e.forge.comments = []forge.IssueComment{commentWithMarker(t, 9001, parseMarker(t, raw))}
			},
			want:       review.OutcomeSkipped,
			wantReason: "already reviewed",
			wantPass:   1,
		},
		{
			name: "stale claim by age is abandoned then invoked",
			setup: func(t *testing.T, e *env, _ *review.Request) {
				raw := fmt.Sprintf(`{"v":1,"leg":"review","pass":1,"state":"started","ts":%d,"comment_id":9001,"run_id":"x","head_sha":%q,"findings":[],"verdict":null}`, frozenNow.Unix()-7200, headSHA)
				e.forge.comments = []forge.IssueComment{commentWithMarker(t, 9001, parseMarker(t, raw))}
			},
			want:        review.OutcomeInvoked,
			wantReason:  "abandoning the unfinished pass-1 review",
			wantPass:    1,
			wantHarness: true,
			wantClaim:   false,
		},
		{
			name: "stale claim against a moved head is abandoned then invoked",
			setup: func(t *testing.T, e *env, _ *review.Request) {
				raw := fmt.Sprintf(`{"v":1,"leg":"review","pass":1,"state":"started","ts":%d,"comment_id":9001,"run_id":"x","head_sha":%q,"findings":[],"verdict":null}`, frozenNow.Unix(), oldSHA)
				e.forge.comments = []forge.IssueComment{commentWithMarker(t, 9001, parseMarker(t, raw))}
			},
			want:        review.OutcomeInvoked,
			wantReason:  "and the pull request is now at",
			wantPass:    1,
			wantHarness: true,
		},
		{
			name: "open claim recovers and invokes",
			setup: func(t *testing.T, e *env, _ *review.Request) {
				raw := fmt.Sprintf(`{"v":1,"leg":"review","pass":1,"state":"started","ts":%d,"comment_id":9001,"run_id":"x","head_sha":%q,"findings":[],"verdict":null}`, frozenNow.Unix(), headSHA)
				e.forge.comments = []forge.IssueComment{commentWithMarker(t, 9001, parseMarker(t, raw))}
			},
			want:        review.OutcomeInvoked,
			wantPass:    1,
			wantHarness: true,
		},
		{
			name: "blocked complete review redrives",
			setup: func(t *testing.T, e *env, _ *review.Request) {
				raw := fmt.Sprintf(`{"v":1,"leg":"review","pass":1,"state":"complete","ts":1699950000,"comment_id":9001,"run_id":"x","head_sha":%q,"verdict":"blocked","findings":[]}`, headSHA)
				e.forge.comments = []forge.IssueComment{commentWithMarker(t, 9001, parseMarker(t, raw))}
			},
			want:        review.OutcomeInvoked,
			wantReason:  "ended blocked",
			wantPass:    1,
			wantHarness: true,
		},
		{
			name: "automatic fork is refused",
			setup: func(_ *testing.T, e *env, req *review.Request) {
				req.Trigger = review.TriggerAutomatic
				e.forge.pr.IsCrossRepository = true
			},
			want:       review.OutcomeError,
			wantReason: "comes from a fork",
		},
		{
			name: "a pull request that is not open is refused",
			setup: func(_ *testing.T, e *env, _ *review.Request) {
				e.forge.pr.State = "CLOSED"
			},
			want:       review.OutcomeError,
			wantReason: "is not open",
		},
		{
			name: "PR 0 is refused",
			setup: func(_ *testing.T, _ *env, req *review.Request) {
				req.PR = 0
			},
			want:       review.OutcomeError,
			wantReason: "needs a pull request number",
		},
		{
			name: "unknown trigger is refused",
			setup: func(_ *testing.T, _ *env, req *review.Request) {
				req.Trigger = "cron"
			},
			want:       review.OutcomeError,
			wantReason: "unknown review trigger",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			req := e.request(t)
			tt.setup(t, e, &req)
			got := runLeg(t, e, req)
			if tt.want == review.OutcomeError {
				if got.Err == nil {
					t.Fatal("wanted an error")
				}
				if tt.wantReason != "" && !strings.Contains(got.Err.Error(), tt.wantReason) && !strings.Contains(got.Reason, tt.wantReason) {
					t.Errorf("err = %q reason = %q, want %q", got.Err, got.Reason, tt.wantReason)
				}
			} else if got.Err != nil {
				t.Fatalf("Run err = %v", got.Err)
			}
			if got.Outcome != tt.want {
				t.Errorf("Outcome = %q, want %q (reason %q)", got.Outcome, tt.want, got.Reason)
			}
			if tt.want != review.OutcomeError && tt.wantReason != "" && !strings.Contains(got.Reason, tt.wantReason) {
				t.Errorf("Reason = %q, want it to contain %q", got.Reason, tt.wantReason)
			}
			if tt.wantPass != 0 && got.Pass != tt.wantPass {
				t.Errorf("Pass = %d, want %d", got.Pass, tt.wantPass)
			}
			if gotHarness := len(e.runner.Specs()) > 0; gotHarness != tt.wantHarness {
				t.Errorf("harness invoked = %v, want %v", gotHarness, tt.wantHarness)
			}
			if tt.wantClaim && len(e.forge.created) == 0 {
				t.Error("wanted a claim comment, none was posted")
			}
			if !tt.wantClaim && !tt.wantDeclined && len(e.forge.created) != 0 {
				t.Errorf("posted %d comments, want none", len(e.forge.created))
			}
			if tt.wantDeclined {
				if len(e.forge.created) == 0 {
					t.Fatal("wanted a declined marker comment")
				}
				body := e.forge.created[0]
				if !strings.Contains(body, `"state":"declined"`) {
					t.Errorf("declined body missing state declined: %s", body)
				}
				if !strings.Contains(body, tt.wantReason) && tt.wantReason != "" {
					t.Errorf("declined body missing reason %q: %s", tt.wantReason, body)
				}
			}
			if tt.wantLabel != "" {
				found := false
				for _, label := range e.forge.labelsAdded {
					if label == tt.wantLabel {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("labels added = %v, want %q", e.forge.labelsAdded, tt.wantLabel)
				}
			}
			if req.PR != 0 && (req.Trigger == "" || req.Trigger == review.TriggerHuman || req.Trigger == review.TriggerAutomatic) {
				if e.forge.prCalls != 1 {
					t.Errorf("PullRequest called %d times, want 1", e.forge.prCalls)
				}
			} else if e.forge.prCalls != 0 {
				t.Errorf("PullRequest called %d times, want 0 before context load", e.forge.prCalls)
			}
		})
	}
}

func TestAdmissionPassLabelMatchesPresentationAndShell(t *testing.T) {
	raw, err := os.ReadFile("../../tests/fixtures/parity/presentation.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		PassLabel []struct {
			Name string `json:"name"`
			Pass int    `json:"pass"`
			Cap  int    `json:"cap"`
			Out  string `json:"out"`
		} `json:"pass_label"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	if len(fixture.PassLabel) == 0 {
		t.Fatal("presentation.json records no pass_label vectors")
	}
	root := repoRootPath(t)
	for _, vector := range fixture.PassLabel {
		got := review.PassLabel(vector.Pass, vector.Cap)
		if got != vector.Out {
			t.Errorf("%s: Go = %q, fixture = %q", vector.Name, got, vector.Out)
		}
		shell := shellPassLabel(t, root, vector.Pass, vector.Cap)
		if got != shell {
			t.Errorf("%s: Go = %q, Bash = %q", vector.Name, got, shell)
		}
	}
}

func shellPassLabel(t *testing.T, root string, pass, cap int) string {
	t.Helper()
	const script = `
set -uo pipefail
ROOT="$1"
export ROOT
# shellcheck source=/dev/null
source "$ROOT/lib/ui.sh"
# shellcheck source=/dev/null
source "$ROOT/lib/run.sh"
_pass_label "$2" "$3"
`
	out := bashOutput(t, script, root, strconv.Itoa(pass), strconv.Itoa(cap))
	return strings.TrimRight(out, "\n")
}

func TestAdmissionStaleReviewWarningContainsFullText(t *testing.T) {
	e := newEnv(t)
	raw := fmt.Sprintf(`{"v":1,"leg":"review","pass":1,"state":"started","ts":%d,"comment_id":9001,"run_id":"x","head_sha":%q,"findings":[],"verdict":null}`, frozenNow.Unix(), oldSHA)
	e.forge.comments = []forge.IssueComment{commentWithMarker(t, 9001, parseMarker(t, raw))}
	req := e.request(t)
	got := runLeg(t, e, req)
	if got.Err != nil {
		t.Fatalf("Run: %v", got.Err)
	}
	wantWarning := fmt.Sprintf("abandoning the unfinished pass-1 review — it started against %s and the pull request is now at %s\n   Resuming it would reconcile against findings that no longer describe this code. Starting the pass again instead.", oldSHA[:7], headSHA[:7])
	found := false
	for _, msg := range got.Messages {
		if msg == wantWarning {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("messages = %q, want warning %q", got.Messages, wantWarning)
	}
}
