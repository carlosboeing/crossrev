package review

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/forge"
	"github.com/carlosboeing/crossrev/internal/policy"
	"github.com/carlosboeing/crossrev/internal/prstate"
	"github.com/carlosboeing/crossrev/internal/ui"
)

// PassLabel is _pass_label (lib/run.sh:592-598).
func PassLabel(pass, cap int) string {
	if pass > cap {
		return fmt.Sprintf("pass %d (past the cycle cap of %d)", pass, cap)
	}
	return fmt.Sprintf("pass %d", pass)
}

// ClaimBody is the started-claim comment (lib/run.sh:1104-1107).
func ClaimBody(pass, cap int, markerJSON string) string {
	encoded, err := prstate.EncodeMarker(json.RawMessage(markerJSON))
	if err != nil {
		return ""
	}
	return fmt.Sprintf("**crossrev — reviewing, %s**\n\nReading the diff and any earlier review threads. This comment becomes the pass summary when the review finishes.%s",
		PassLabel(pass, cap), encoded)
}

type admission struct {
	pass       int
	claim      prstate.Marker
	recovering bool
	redrive    bool
	stale      string
	decline    string
	already    bool
	maxPasses  int
	maxPRs     int
	maxFiles   int
	files      int
	otherToday int
	warning    string
}

func (l *Leg) admit(ctx context.Context, req Request, loaded Context) (admission, error) {
	var ad admission
	cfg := loaded.Config
	ad.maxPasses = atoi(cfg.Get(".policy.max_passes_per_cycle"))
	current := prstate.CurrentReviewPass(loaded.Markers)
	claim, open := prstate.OpenClaim(loaded.Markers, current, core.LegReview)
	head := loaded.PR.HeadRefOid.SHA()

	switch {
	case open:
		if reason, stale := prstate.ClaimIsStale(claim, head, l.now(), prstate.DefaultClaimWindow); stale {
			ad.stale = reason
			claim.TS = l.now().Unix()
			claim.HeadSHA = prstate.Some(head)
			claim.Findings = json.RawMessage("[]")
			claim.Verdict = prstate.Null[string]()
			ad.recovering = true
			ad.pass = current
			ad.claim = claim
		} else {
			ad.recovering = true
			ad.pass = current
			ad.claim = claim
		}
	case prstate.IsNewRevision(loaded.Markers, head):
		ad.pass = current + 1
	default:
		done, ok := prstate.MarkerFor(loaded.Markers, current, core.LegReview)
		var reviewMarker *policy.ReviewMarker
		if ok {
			reviewMarker = &policy.ReviewMarker{State: done.State, Verdict: core.Verdict(done.Verdict.Value())}
		}
		if current > 0 && policy.ReviewRedrivable(reviewMarker) {
			ad.recovering = true
			ad.redrive = true
			ad.pass = current
			ad.claim = redriveClaim(done, head, req.RunID, l.now().Unix())
		} else {
			ad.already = true
			ad.pass = current
			return ad, nil
		}
	}

	maxPasses := 0
	if req.Trigger == TriggerAutomatic {
		maxPasses = atoi(cfg.Get(".policy.max_passes_per_cycle"))
		ad.maxPRs = atoi(cfg.Get(".policy.max_prs_per_day"))
		ad.files = loaded.PR.ChangedFiles
		ad.maxFiles = atoi(cfg.Get(".policy.max_files_changed_per_pr"))
		if ad.maxPRs > 0 {
			count, err := forge.PRsReviewedToday(ctx, l.Forge, forge.DailyCount{
				Repo:           loaded.Repo,
				Author:         loaded.Author,
				Cutoff:         l.now().Add(-24 * time.Hour),
				Cap:            ad.maxPRs,
				CurrentPR:      req.PR,
				CurrentMarkers: loaded.Markers,
			})
			if err == nil {
				ad.otherToday = count
			} else {
				ad.warning = ui.Warning(
					"could not read repository comments while checking max_prs_per_day",
					"The backstop rounds down to zero rather than stopping a healthy automatic review early. Check GitHub availability and the token's issues read permission.",
				)
			}
		}
	} else if req.Continuation {
		maxPasses = atoi(cfg.Get(".policy.max_passes_per_cycle"))
	}
	ad.maxPasses = atoi(cfg.Get(".policy.max_passes_per_cycle"))
	decision := policy.ShouldContinue(policy.Termination{
		Verdict:              core.VerdictIssuesRemain,
		Pass:                 core.PassNumber(ad.pass - 1),
		MaxPassesPerCycle:    maxPasses,
		OtherPRsToday:        ad.otherToday,
		MaxPRsPerDay:         ad.maxPRs,
		FilesChanged:         ad.files,
		MaxFilesChangedPerPR: ad.maxFiles,
	})
	if !decision.Continues() {
		ad.decline = decision.Reason
	}
	return ad, nil
}

func redriveClaim(done prstate.Marker, head, runID string, ts int64) prstate.Marker {
	done.State = core.PassStarted
	done.TS = ts
	done.DoneTS = prstate.Null[int64]()
	done.HeadSHA = prstate.Some(head)
	if runID != "" {
		done.RunID = prstate.Some(runID)
	}
	done.Findings = json.RawMessage("[]")
	done.Verdict = prstate.Null[string]()
	done.BlockedReason = prstate.Null[string]()
	done.ModelReported = prstate.Null[string]()
	done.Tokens = json.RawMessage("null")
	done.Usage = json.RawMessage("null")
	done.Billing = prstate.Null[string]()
	return done
}

func (l *Leg) postClaim(ctx context.Context, req Request, loaded Context, ad admission, settings legSettings) (prstate.Marker, int64, error) {
	cap := atoi(loaded.Config.Get(".policy.max_passes_per_cycle"))
	if ad.recovering {
		id := ad.claim.CommentID()
		marker := ad.claim
		if ad.redrive {
			marker.Harness = prstate.Some(settings.harness)
			marker.Model = nullOrSome(settings.model)
			marker.Effort = nullOrSome(settings.effort)
			marker.Endpoint = nullOrSome(settings.endpoint)
			raw, err := marker.MarshalJSON()
			if err != nil {
				return prstate.Marker{}, 0, err
			}
			body := fmt.Sprintf("**crossrev — reviewing, %s**\n\nDriving the pass again: the previous attempt was blocked. Reading the diff and any earlier review threads. This comment becomes the pass summary when the review finishes.%s",
				PassLabel(ad.pass, cap), mustEncode(raw))
			if err := l.Forge.CommentEdit(ctx, loaded.Repo, id, body); err != nil {
				return prstate.Marker{}, 0, err
			}
		}
		return marker, id, nil
	}

	marker := startedMarker(ad.pass, loaded.PR.HeadRefOid.SHA(), req.RunID, l.now().Unix(), settings)
	raw, err := marker.MarshalJSON()
	if err != nil {
		return prstate.Marker{}, 0, err
	}
	body := ClaimBody(ad.pass, cap, string(raw))
	id, err := l.Forge.CommentCreate(ctx, loaded.Repo, req.PR, body)
	if err != nil || id == 0 {
		return prstate.Marker{}, 0, &ui.FatalError{
			Reason: fmt.Sprintf("the claim comment did not post on %s#%d", loaded.Repo, req.PR),
			Action: "The marker is what makes a retry safe, so crossrev stops rather than reviewing without one.",
		}
	}
	return marker, id, nil
}

func startedMarker(pass int, head, runID string, ts int64, settings legSettings) prstate.Marker {
	return prstate.Marker{
		Version:       core.MarkerVersion,
		Leg:           core.LegReview,
		Pass:          pass,
		State:         core.PassStarted,
		TS:            ts,
		DoneTS:        prstate.Null[int64](),
		RunID:         prstate.Some(runID),
		HeadSHA:       prstate.Some(head),
		Harness:       prstate.Some(settings.harness),
		Model:         nullOrSome(settings.model),
		Effort:        nullOrSome(settings.effort),
		Endpoint:      nullOrSome(settings.endpoint),
		ModelReported: prstate.Null[string](),
		Tokens:        json.RawMessage("null"),
		Usage:         json.RawMessage("null"),
		Billing:       prstate.Null[string](),
		Verdict:       prstate.Null[string](),
		BlockedReason: prstate.Null[string](),
		Findings:      json.RawMessage("[]"),
	}
}

func (l *Leg) postDeclined(ctx context.Context, req Request, loaded Context, ad admission) error {
	reason := ad.decline
	haltBody := "No review ran, so nothing here is a judgement about the code. Raising the cap in `.github/crossrev.yml` and pushing a revision would start it again."
	if strings.Contains(reason, "max_passes_per_cycle") {
		haltBody = fmt.Sprintf("CrossRev did not review this revision. It runs a maximum of %s passes automatically. To run another pass, comment `/crossrev review`, or run `crossrev review --pr %d` locally. To change the limit, set [`policy.max_passes_per_cycle`](https://github.com/carlosboeing/crossrev/blob/main/docs/configuration.md#policy) in `.github/crossrev.yml`.",
			loaded.Config.Get(".policy.max_passes_per_cycle"), req.PR)
	}
	marker := prstate.Marker{
		Version:       core.MarkerVersion,
		Leg:           core.LegReview,
		Pass:          ad.pass,
		State:         core.PassDeclined,
		TS:            l.now().Unix(),
		DoneTS:        prstate.Some(l.now().Unix()),
		RunID:         prstate.Some(req.RunID),
		HeadSHA:       prstate.Some(loaded.PR.HeadRefOid.SHA()),
		Harness:       prstate.Null[string](),
		Model:         prstate.Null[string](),
		Effort:        prstate.Null[string](),
		Endpoint:      prstate.Null[string](),
		ModelReported: prstate.Null[string](),
		Tokens:        json.RawMessage("null"),
		Usage:         json.RawMessage("null"),
		Billing:       prstate.Null[string](),
		Verdict:       prstate.Some(string(core.VerdictDeclined)),
		Reason:        prstate.Some(reason),
		Findings:      json.RawMessage("[]"),
	}
	raw, err := marker.MarshalJSON()
	if err != nil {
		return err
	}
	encoded, err := prstate.EncodeMarker(raw)
	if err != nil {
		return err
	}
	body := fmt.Sprintf("**crossrev stopped before pass %d** — %s.\n\n%s%s", ad.pass, reason, haltBody, encoded)
	if _, err := l.Forge.CommentCreate(ctx, loaded.Repo, req.PR, body); err != nil {
		return err
	}
	labelPass := ad.pass
	if labelPass > 1 {
		labelPass--
	} else {
		labelPass = 1
	}
	// Bash calls run_pass_labels ... halted here (lib/run.sh:1056), which adds
	// the pass label and the halted label AND removes the three outcome labels
	// that are mutually exclusive with it. Adding two labels without removing
	// the others leaves awaiting-review or converged standing beside halted on
	// the same pull request, and the loop is label-driven.
	_, _ = l.applyPassLabels(ctx, req, loaded, labelPass, policy.PassHalted)
	return nil
}

func nullOrSome(v string) prstate.Opt[string] {
	if v == "" || v == "null" {
		return prstate.Null[string]()
	}
	return prstate.Some(v)
}

func mustEncode(raw json.RawMessage) string {
	encoded, err := prstate.EncodeMarker(raw)
	if err != nil {
		return ""
	}
	return encoded
}

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

func mustPass(n int) core.PassNumber {
	p, err := core.NewPassNumber(n)
	if err != nil {
		return 1
	}
	return p
}
