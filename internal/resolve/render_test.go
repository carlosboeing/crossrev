package resolve

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/core"
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
			got := ReplyBody(vector.Disposition, vector.Tracked, vector.Pass, vector.Harness, vector.Model, 0)
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
		wantFootnote := "<sub>claude does not report which model answered, so the model above is the one CrossRev requested. </sub>\n\n"
		if !strings.Contains(got, wantFootnote) {
			t.Errorf("summary footnote missing trailing space:\n got: %q\nwant containing: %q", got, wantFootnote)
		}
	})
}

// A quota stop names its resume time and next command instead of saying
// "a human is needed".
func TestReviewSummaryBodyQuotaStopNamesResumeTime(t *testing.T) {
	marker := prstate.Marker{
		Verdict:       prstate.Some(string(core.VerdictBlocked)),
		BlockedReason: prstate.Some("the codex harness failed: 429 rate limit exceeded"),
		Harness:       prstate.Some("codex"),
	}
	findings := json.RawMessage(`[]`)
	repo := mustSlug(t)
	got := reviewSummaryBody(findings, marker, repo, 42, core.SeverityMedium, 3, commentCoverage{})
	wantAlert := "The loop halts here — resumes after 5h, then run `crossrev review --pr 42`. Nothing in this comment is a judgement about the code."
	if !strings.Contains(got, wantAlert) {
		t.Errorf("summary body missing quota resume line:\n got: %s\nwant containing: %s", got, wantAlert)
	}
	if strings.Contains(got, "a human is needed") {
		t.Errorf("a quota stop should not claim a human is needed:\n%s", got)
	}

	// Explicit reset in reason
	marker.BlockedReason = prstate.Some("the codex harness failed: 429 rate limit exceeded (resets 2h 15m)")
	gotExplicit := reviewSummaryBody(findings, marker, repo, 42, core.SeverityMedium, 3, commentCoverage{})
	wantExplicit := "The loop halts here — resumes after 2h 15m, then run `crossrev review --pr 42`. Nothing in this comment is a judgement about the code."
	if !strings.Contains(gotExplicit, wantExplicit) {
		t.Errorf("summary body missing explicit reset line:\n got: %s\nwant containing: %s", gotExplicit, wantExplicit)
	}

	// Non-quota failure still says a human is needed
	marker.BlockedReason = prstate.Some("the harness CLI is not installed")
	gotNonQuota := reviewSummaryBody(findings, marker, repo, 42, core.SeverityMedium, 3, commentCoverage{})
	if !strings.Contains(gotNonQuota, "The loop halts here and a human is needed.") {
		t.Errorf("non-quota stop missing human is needed alert:\n%s", gotNonQuota)
	}
}

// A mixed pass keeps its held count when the resolve leg rewrites the
// review summary: the findings table still lists every finding, and the
// count below names the held ones, matching the review leg's own summary.
func TestReviewSummaryRewriteKeepsHeldCount(t *testing.T) {
	findings := json.RawMessage(`[{` +
		`"id":"aaaaaaaaaaaaaaaa","path":"a.go","line":1,"severity":"low","category":"maintainability","pre_existing":false,"title":"held nit","posted":false},` +
		`{` +
		`"id":"bbbbbbbbbbbbbbbb","path":"a.go","line":2,"severity":"high","category":"correctness","pre_existing":false,"title":"real bug"}]`)
	marker := prstate.Marker{
		Pass:    2,
		HeadSHA: prstate.Some(testHeadSHA),
		Harness: prstate.Some("claude"),
		Model:   prstate.Some("claude-3-7-sonnet"),
		Blocked: prstate.Some(false),
	}
	got := reviewSummaryBody(findings, marker, mustSlug(t), 42, core.SeverityMedium, 3, commentCoverage{})
	if !strings.Contains(got, "1 finding below medium recorded and not posted.") {
		t.Errorf("rewrite lost the held count:\n%s", got)
	}
	if !strings.Contains(got, "held nit") {
		t.Errorf("rewrite lost the held finding's table row:\n%s", got)
	}
}

// A degraded pass keeps its reads reason when the resolve leg rewrites the
// review summary: the rewrite re-renders from the marker, so the posted
// degradation line must survive it the way the redrive notice and the skip
// warning do.
func TestReviewSummaryRewriteKeepsReadsDegradation(t *testing.T) {
	readsMarker := func(reason string, calls, reads, refused int) prstate.Marker {
		marker := prstate.Marker{
			Pass:    2,
			Harness: prstate.Some("claude"),
			Model:   prstate.Some("claude-3-7-sonnet"),
			Blocked: prstate.Some(false),
		}
		raw, err := json.Marshal(prstate.NewReadsEnvelope("served", "supplied", reason, calls, reads, 0, refused, false))
		if err != nil {
			t.Fatal(err)
		}
		marker.Reads = raw
		return marker
	}

	got := reviewSummaryBody(json.RawMessage(`[]`), readsMarker("self_test_failed", 0, 0, 0), mustSlug(t), 42, core.SeverityMedium, 3, commentCoverage{})
	if !strings.Contains(got, "Served reads degraded (self_test_failed): this review judged the supplied content alone.") {
		t.Errorf("rewrite lost the degrade line:\n%.1200s", got)
	}

	got = reviewSummaryBody(json.RawMessage(`[]`), readsMarker("calls_refused", 2, 3, 1), mustSlug(t), 42, core.SeverityMedium, 3, commentCoverage{})
	if !strings.Contains(got, "Served reads degraded (calls_refused: 1 refused, 3 served)") {
		t.Errorf("rewrite lost the refused and served counts:\n%.1200s", got)
	}

	// The blocked and tripwire gates travel with the line: a halted pass
	// reports its halt, and a command event is not a degradation.
	halted := readsMarker("missing_handshake", 0, 0, 0)
	halted.Verdict = prstate.Some("blocked")
	halted.BlockedReason = prstate.Some("served reads are unavailable: missing_handshake (reads_unavailable)")
	if got := reviewSummaryBody(json.RawMessage(`[]`), halted, mustSlug(t), 42, core.SeverityMedium, 3, commentCoverage{}); strings.Contains(got, "Served reads degraded") {
		t.Errorf("a halted pass prints the degrade line beside its halt:\n%.1200s", got)
	}
	tripped := readsMarker("review_leg_ran_command", 0, 0, 0)
	if got := reviewSummaryBody(json.RawMessage(`[]`), tripped, mustSlug(t), 42, core.SeverityMedium, 3, commentCoverage{}); strings.Contains(got, "Served reads degraded") {
		t.Errorf("a tripwire halt prints as a degradation:\n%.1200s", got)
	}
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

// A mixed pass renders honest counts after the rewrite too: the review
// summary the resolve leg rewrites scopes its resolving claims to posted
// findings, and the resolve summary says the posted findings were
// verified and keeps the held count — matching the review leg's summary
// from before the rewrite.
func TestMixedPassRendersHonestCountsAfterRewrite(t *testing.T) {
	findings := json.RawMessage(`[{` +
		`"id":"aaaaaaaaaaaaaaaa","path":"a.go","line":1,"severity":"low","category":"maintainability","pre_existing":false,"title":"held nit","posted":false},` +
		`{` +
		`"id":"bbbbbbbbbbbbbbbb","path":"a.go","line":2,"severity":"high","category":"correctness","pre_existing":false,"title":"real bug"}]`)
	marker := prstate.Marker{
		Pass:    2,
		HeadSHA: prstate.Some(testHeadSHA),
		Harness: prstate.Some("claude"),
		Model:   prstate.Some("claude-3-7-sonnet"),
		Blocked: prstate.Some(false),
	}
	rewrite := reviewSummaryBody(findings, marker, mustSlug(t), 42, core.SeverityMedium, 3, commentCoverage{})
	if !strings.Contains(rewrite, "1 posted finding needs resolving.") {
		t.Errorf("rewrite alert does not scope to posted findings:\n%.800s", rewrite)
	}
	if strings.Contains(rewrite, "verifies every finding below") {
		t.Errorf("rewrite alert still claims every finding is verified:\n%.800s", rewrite)
	}
	if !strings.Contains(rewrite, "1 finding below medium recorded and not posted.") {
		t.Errorf("rewrite lost the held count:\n%.800s", rewrite)
	}

	resolutions := json.RawMessage(`[{"finding_id":"bbbbbbbbbbbbbbbb","resolution":"fixed"}]`)
	resolveMarker := prstate.Marker{
		Pass:    2,
		Summary: prstate.Some("Fixed it."),
		HeadSHA: prstate.Some(testHeadSHA),
		Harness: prstate.Some("claude"),
		Blocked: prstate.Some(false),
	}
	got := ResolveSummaryBody(resolutions, findings, "", resolveMarker, "acme/widget", 42, 3)
	if !strings.Contains(got, "Every posted finding was verified") {
		t.Errorf("resolve summary does not scope verification to posted findings:\n%.800s", got)
	}
	if strings.Contains(got, "Every finding was verified") {
		t.Errorf("resolve summary still claims every finding was verified:\n%.800s", got)
	}
	if !strings.Contains(got, "1 held finding recorded and not posted") {
		t.Errorf("resolve summary lost the held count:\n%.800s", got)
	}

	reply := ReplyBody(json.RawMessage(`{"finding_id":"bbbbbbbbbbbbbbbb","resolution":"fixed","reply":"done"}`), "", 2, "claude", "", 1)
	if !strings.Contains(reply, "Every posted finding is verified") {
		t.Errorf("reply footer does not scope verification to posted findings:\n%.800s", reply)
	}
	if strings.Contains(reply, "Every finding is verified") {
		t.Errorf("reply footer still claims every finding is verified:\n%.800s", reply)
	}
	pure := ReplyBody(json.RawMessage(`{"finding_id":"bbbbbbbbbbbbbbbb","resolution":"fixed","reply":"done"}`), "", 2, "claude", "", 0)
	if !strings.Contains(pure, "Every finding is verified") {
		t.Errorf("pure-pass footer changed wording:\n%.800s", pure)
	}
}

// The pass-3 end of a posted-held-upgraded sequence renders the posted
// occurrence: under a duplicate id the resolutions table and the commit
// body read the medium upgrade on its current thread, not the held low
// entry beside it, which carries neither severity nor thread.
func TestResolveTablesPreferPostedOccurrence(t *testing.T) {
	const fid = "2222222222222222"
	findings := json.RawMessage(`[
{"id":"` + fid + `","path":"a.go","line":1,"severity":"low","category":"correctness","title":"same nit","posted":false,"resolution":null},
{"id":"` + fid + `","path":"a.go","line":1,"severity":"medium","category":"correctness","title":"same nit","root_comment_id":77,"resolution":null}
]`)
	resolutions := json.RawMessage(`[{"finding_id":"` + fid + `","resolution":"fixed"}]`)
	marker := prstate.Marker{
		Pass:    3,
		Summary: prstate.Some("Fixed it."),
		HeadSHA: prstate.Some(testHeadSHA),
		Harness: prstate.Some("claude"),
		Blocked: prstate.Some(false),
	}
	got := ResolveSummaryBody(resolutions, findings, "", marker, "acme/widget", 42, 3)
	if !strings.Contains(got, "Medium") {
		t.Errorf("resolutions table does not show the posted upgrade's severity:\n%.800s", got)
	}
	if strings.Contains(got, "Low") {
		t.Errorf("resolutions table shows the held entry's severity:\n%.800s", got)
	}
	if !strings.Contains(got, "https://github.com/acme/widget/pull/42/files#r77") {
		t.Errorf("resolutions table does not link the current thread:\n%.800s", got)
	}
	body := CommitBody(resolutions, findings, "fixed", testHeadSHA, 3, "acme/widget", 42)
	if !strings.Contains(body, "https://github.com/acme/widget/pull/42/files#r77") {
		t.Errorf("commit body does not link the current thread:\n%s", body)
	}
}
