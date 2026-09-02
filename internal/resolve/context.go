package resolve

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/carlosboeing/crossrev/internal/config"
	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/cred"
	"github.com/carlosboeing/crossrev/internal/forge"
	"github.com/carlosboeing/crossrev/internal/harness"
	"github.com/carlosboeing/crossrev/internal/policy"
	"github.com/carlosboeing/crossrev/internal/prompt"
	"github.com/carlosboeing/crossrev/internal/prstate"
	"github.com/carlosboeing/crossrev/internal/validate"
	"github.com/carlosboeing/crossrev/internal/vcs"
)

type session struct {
	req       Request
	repo      core.Slug
	pr        forge.PullRequest
	cfg       *config.Config
	markers   []prstate.Marker
	author    string
	pass      int
	review    prstate.Marker
	findings  []harness.Node
	backlog   config.Backlog
	minFix    core.Severity
	maxPasses int
	mode      string
	redrive   prstate.Marker
	redriving bool
	settings  legSettings
}

type legSettings struct {
	Harness  string
	Model    string
	Effort   string
	Endpoint string
}

func (l *Leg) load(ctx context.Context, req Request) (*session, Result) {
	if req.Trigger == "" {
		req.Trigger = "human"
	}
	s := &session{req: req}

	repo := req.Repo
	if repo.Incomplete() {
		var err error
		repo, err = l.Forge.RepoSlug(ctx)
		if err != nil {
			return nil, refuse("could not work out which repository this is",
				"Run crossrev from a checkout with a GitHub remote, or pass --repo owner/name.")
		}
	}
	s.repo = repo

	pr, err := l.Forge.PullRequest(ctx, repo, req.PR)
	if err != nil {
		return nil, refuse(fmt.Sprintf("could not read %s#%d", repo, req.PR),
			"Check the number, and that `gh auth status` passes for that repository.")
	}
	s.pr = pr

	if req.Trigger == TriggerAutomatic && pr.IsCrossRepository {
		return nil, refuse(fmt.Sprintf("%s#%d comes from a fork", repo, req.PR),
			"crossrev does not run on fork pull requests: GitHub withholds secrets from them. Review it locally or by hand.")
	}
	if !strings.EqualFold(pr.State, "OPEN") {
		return nil, refuse(fmt.Sprintf("%s#%d is not open", repo, req.PR),
			"crossrev only runs on open pull requests. Reopen it, or pick another number.")
	}
	if req.Trigger == TriggerAutomatic && pr.IsDraft {
		return s, Result{
			Outcome: OutcomeSkipped,
			Message: fmt.Sprintf("%s#%d is a draft pull request, so an automatic invocation does not review it.", repo, req.PR),
		}
	}

	cfg, err := config.Load(ctx, pr.BaseRefOid, l.showFile)
	if err != nil {
		return nil, wrapErr(err)
	}
	s.cfg = cfg
	s.mode = cfg.Get(".mode")
	s.minFix = core.Severity(cfg.Get(".policy.min_fix_severity"))
	s.maxPasses, _ = strconv.Atoi(cfg.Get(".policy.max_passes_per_cycle"))
	s.backlog, err = cfg.ResolveBacklog(ctx, pr.BaseRefOid, cfg.Get(".backlog.destination"))
	if err != nil {
		return nil, wrapErr(err)
	}

	author, err := l.trustedAuthor(ctx, req)
	if err != nil {
		return nil, wrapErr(err)
	}
	s.author = author
	s.markers = markersFromComments(l.Forge.IssueComments(ctx, repo, req.PR), author)

	for _, lab := range pr.Labels {
		if lab.Name == "crossrev/stop" {
			return s, Result{
				Outcome: OutcomeStopped,
				Pass:    prstate.CurrentReviewPass(s.markers),
				Message: fmt.Sprintf("crossrev/stop is on %s#%d, so nothing is resolved.", repo, req.PR),
			}
		}
	}

	pass := prstate.CurrentReviewPass(s.markers)
	s.pass = pass
	if pass == 0 {
		return nil, refuse(fmt.Sprintf("%s#%d has no review to resolve", repo, req.PR),
			fmt.Sprintf("The resolve leg acts on a review leg's findings. Run: crossrev review --pr %d", req.PR))
	}
	review, ok := prstate.MarkerFor(s.markers, pass, core.LegReview)
	if !ok {
		return nil, refuse(fmt.Sprintf("%s#%d has no review to resolve", repo, req.PR),
			fmt.Sprintf("The resolve leg acts on a review leg's findings. Run: crossrev review --pr %d", req.PR))
	}
	s.review = review
	if review.State != core.PassComplete {
		return nil, refuse(fmt.Sprintf("the pass-%d review on %s#%d did not finish", pass, repo, req.PR),
			fmt.Sprintf("Resolving a half-posted review would reply to findings the reviewer may not have finished recording. Re-run: crossrev review --pr %d", req.PR))
	}
	if v, _ := review.Verdict.Get(); v == string(core.VerdictBlocked) {
		return nil, refuse(fmt.Sprintf("the pass-%d review on %s#%d was blocked", pass, repo, req.PR),
			fmt.Sprintf("A blocked review is not a set of findings to resolve. Once whatever stopped it is fixed, re-run: crossrev review --pr %d", req.PR))
	}

	if prstate.CurrentPassComplete(s.markers, pass, core.LegResolve) {
		done, _ := prstate.MarkerFor(s.markers, pass, core.LegResolve)
		if policy.ResolveRedrivable(asPolicyResolve(done)) {
			s.redrive = done
			s.redriving = true
		} else {
			return s, Result{
				Outcome: OutcomeAlreadyResolved,
				Pass:    pass,
				Marker:  done,
				Message: fmt.Sprintf("pass %d of %s#%d is already resolved.", pass, repo, req.PR),
			}
		}
	}

	s.findings = unmarshalFindings(review.Findings)
	if len(s.findings) == 0 {
		verdict, _ := review.Verdict.Get()
		if verdict != string(core.VerdictConverged) && escalatedCount(s.markers) > 0 {
			return s, Result{
				Outcome: OutcomeHalted,
				Pass:    pass,
				Message: fmt.Sprintf("pass %d raised nothing new on %s#%d, and the escalated findings still need a human decision.", pass, repo, req.PR),
			}
		}
		return s, Result{
			Outcome: OutcomeNoFindings,
			Pass:    pass,
			Message: fmt.Sprintf("pass %d found nothing to resolve on %s#%d.", pass, repo, req.PR),
		}
	}
	return s, Result{}
}

func (l *Leg) trustedAuthor(ctx context.Context, req Request) (string, error) {
	if req.Author != "" {
		return req.Author, nil
	}
	if req.Trigger == TriggerAutomatic {
		// lib/state.sh:35-40. Measured: CROSSREV_APP_SLUG=crossrev → crossrev[bot].
		slug := os.Getenv("CROSSREV_APP_SLUG")
		if slug == "" {
			return "", &Refusal{
				Message: "cannot determine which App's markers to trust",
				Hint:    "Automated mode reads markers only from the App that writes them. In a workflow, set CROSSREV_APP_SLUG from the token step's app-slug output. Locally, run: crossrev auth status",
			}
		}
		return slug + "[bot]", nil
	}
	author, err := l.Forge.ViewerLogin(ctx)
	if err != nil || author == "" {
		return "", &Refusal{
			Message: fmt.Sprintf("could not resolve whose markers to trust on %s#%d", req.Repo, req.PR),
			Hint:    "Pass numbering, revision detection and the daily cap all read from the trusted author. Run: gh auth login",
		}
	}
	return author, nil
}

func (l *Leg) showFile(ctx context.Context, revision core.Revision, path string) ([]byte, config.FileStatus, error) {
	b, st, err := l.Git.Show(ctx, revision, path)
	return b, config.FileStatus(st), err
}

func markersFromComments(comments []forge.IssueComment, author string) []prstate.Marker {
	var lines []string
	for _, c := range comments {
		if c.AuthorLogin != author {
			continue
		}
		raw, err := json.Marshal(prstate.Comment{ID: c.ID, Body: c.Body, CreatedAt: c.CreatedAt})
		if err != nil {
			continue
		}
		lines = append(lines, string(raw))
	}
	return prstate.Markers([]byte(strings.Join(lines, "\n")))
}

func asPolicyResolve(m prstate.Marker) policy.ResolveMarker {
	out := policy.ResolveMarker{CommitSHA: m.CommitSHA.Value()}
	if b, ok := m.Blocked.Get(); ok {
		out.Blocked = b
	}
	var recs []struct {
		Resolution string  `json:"resolution"`
		Tracked    *string `json:"crossrev_tracked"`
	}
	_ = m.DecodeResolutions(&recs)
	for _, r := range recs {
		rec := policy.ResolutionRecord{Resolution: core.Resolution(r.Resolution)}
		if r.Tracked != nil {
			rec.Tracked = core.NewTracked(*r.Tracked)
		}
		out.Resolutions = append(out.Resolutions, rec)
	}
	return out
}

func escalatedCount(markers []prstate.Marker) int {
	n := 0
	for _, m := range markers {
		if m.Leg != core.LegResolve {
			continue
		}
		var recs []struct {
			Resolution string `json:"resolution"`
		}
		_ = m.DecodeResolutions(&recs)
		for _, r := range recs {
			if r.Resolution == string(core.ResolutionEscalated) {
				n++
			}
		}
	}
	return n
}

func (l *Leg) settings(s *session) (*Refusal, string, error) {
	// Derived from the leg, not configured. lib/run.sh:488-489:
	// LEG_WRITE=no
	// [[ "$leg" == "resolver" ]] && LEG_WRITE=yes
	name := s.cfg.Get(".resolver.harness")
	model := s.cfg.Get(".resolver.model")
	effort := s.cfg.Get(".resolver.effort")
	endpoint := s.cfg.Get(".resolver.endpoint")
	if s.req.Harness != "" {
		// Bash clears the model and the endpoint and keeps the effort
		// (lib/run.sh:495): a model id for the harness that was asked for is
		// wrong for a different one, but an effort level is not tied to a
		// harness. Clearing effort here wrote "effort":null into the marker
		// where Bash writes the configured value.
		name = s.req.Harness
		model, endpoint = "", ""
	}
	doc, err := l.document()
	if err != nil {
		return nil, "", err
	}
	if _, ok := harnessFor(doc, name); !ok {
		return noAdapterRefusal(doc, name), "", nil
	}
	if !doc.ServesLeg(name, "resolve") {
		return servesLegRefusal(doc, name), "", nil
	}

	asked := name
	if l.binaryInstalled(asked) {
		s.settings = legSettings{Harness: name, Model: model, Effort: effort, Endpoint: endpoint}
		return nil, "", nil
	}
	for _, alt := range doc.NamesForLeg("resolve") {
		if l.binaryInstalled(alt) {
			s.settings = legSettings{Harness: alt, Model: "", Effort: effort, Endpoint: ""}
			warn := fmt.Sprintf("'%s' is not installed, so the resolver runs on '%s' instead"+"\n   "+
				"Both legs now run on the same harness, so a bug it misses while reviewing it also misses while resolving. Install %s to get the second lineage back.", asked, alt, asked)
			return nil, warn, nil
		}
	}
	return notInstalledRefusal(doc, asked), "", nil
}

// notInstalledRefusal is the last refusal in run_leg_settings
// (lib/run.sh:538-540), reached once the configured harness has no binary and
// the substitution loop at lib/run.sh:531-537 finds no other harness that
// serves the leg.
//
// The hint names every harness that CAN take the leg, read off the descriptor
// with harness_names_for_leg — which is why the refused harness appears in the
// list it is told to install from. Measured on the shipped descriptor with a
// PATH carrying jq and yq but no harness binary:
//
//	Install one of claude, codex, agy, grok and opencode. CrossRev needs at least one, and two different ones is what makes the cross-model check mean anything.
//
// and with codex, agy and grok rewritten to legs ["review"]:
//
//	Install one of claude and opencode. CrossRev needs at least one, and two different ones is what makes the cross-model check mean anything.
func notInstalledRefusal(doc harness.Document, asked string) *Refusal {
	const leg = "resolve"
	return &Refusal{
		Message: fmt.Sprintf("the resolver is configured to use '%s', which is not installed, and no other harness that can serve the %s leg is either", asked, leg),
		Hint: fmt.Sprintf("Install one of %s. CrossRev needs at least one, and two different ones is what makes the cross-model check mean anything.",
			harness.NamesHuman(doc.NamesForLeg(leg))),
	}
}

// noAdapterRefusal is the refusal run_leg_settings prints when no adapter
// function exists for the configured name (lib/run.sh:500-508).
//
// The hint names the harnesses CrossRev drives, read off the descriptor rather
// than written into the sentence. A name the descriptor lists under not_driven
// gets a second half carrying the reason it has no adapter and the key that
// would work instead — and the leg word there is the CONFIG key (resolver),
// not the descriptor's review/resolve vocabulary. Measured:
//
//	resolver kimi   -> CrossRev drives claude, codex, agy, grok and opencode directly. Kimi is reached through the claude adapter as a named endpoint, so there is no adapter_kimi behind the name: define it under endpoints: and set resolver.endpoint, not resolver.harness.
//	resolver nosuch -> CrossRev drives claude, codex, agy, grok and opencode directly.
//
// The review leg builds the same two sentences from the same descriptor reads.
// Sharing one function would mean one tier-3 package importing another, which
// internal/archtest refuses, so each leg carries its own copy against this
// citation.
func noAdapterRefusal(doc harness.Document, name string) *Refusal {
	hint := fmt.Sprintf("CrossRev drives %s directly.", doc.NamesHuman())
	if reason, notDriven := doc.NotDrivenReason(name); notDriven {
		hint += fmt.Sprintf(" %s is %s: define it under endpoints: and set resolver.endpoint, not resolver.harness.",
			capitaliseName(name), reason)
	}
	return &Refusal{
		Message: fmt.Sprintf("there is no adapter for the harness '%s'", name),
		Hint:    hint,
	}
}

// servesLegRefusal is _run_assert_harness_serves_leg for the resolve leg
// (lib/run.sh:553-558), reached from run_leg_settings at lib/run.sh:520.
//
// The message is the product: it names the harness, the leg, the harnesses that
// can take the leg, and the legs the refused harness actually serves. Measured
// with grok rewritten to legs ["review"]:
//
//	the harness 'grok' cannot serve the resolve leg
//	CrossRev runs the resolve leg on claude, codex, agy and opencode. Grok is limited to the review leg.
//
// The leg list is `harness_get "$harness" '.legs // [] | join(", ")'`, whose
// default is the EMPTY array rather than the review-resolve pair
// harness_serves_leg defaults to. The difference cannot show: an entry that
// declares no legs serves both and never reaches this line, and the validator
// refuses a legs field that is not a non-empty array drawn from review and
// resolve (lib/harnesses.sh:66-70). So a refused entry has declared exactly one
// leg, and Descriptor.Legs is its declared list.
func servesLegRefusal(doc harness.Document, name string) *Refusal {
	const leg = "resolve"
	entry, _ := doc.For(name)
	return &Refusal{
		Message: fmt.Sprintf("the harness '%s' cannot serve the %s leg", name, leg),
		Hint: fmt.Sprintf("CrossRev runs the %s leg on %s. %s is limited to the %s leg.",
			leg,
			harness.NamesHuman(doc.NamesForLeg(leg)),
			entry.ProductName,
			strings.Join(entry.Legs(), ", ")),
	}
}

// capitaliseName is the Bash
// `$(printf '%s' "${h:0:1}" | tr '[:lower:]' '[:upper:]')${h:1}`
// at lib/run.sh:503: the first character upper-cased, the rest untouched.
func capitaliseName(name string) string {
	runes := []rune(name)
	if len(runes) == 0 {
		return ""
	}
	return strings.ToUpper(string(runes[0])) + string(runes[1:])
}

func wrapErr(err error) Result {
	if err == nil {
		return Result{}
	}
	if r, ok := err.(*Refusal); ok {
		return Result{Outcome: OutcomeRefused, Err: r}
	}
	if r, ok := err.(*config.Refusal); ok {
		return refuse(r.Message, r.Hint)
	}
	if r, ok := err.(*vcs.Refusal); ok {
		return refuse(r.Message, r.Hint)
	}
	if r, ok := err.(*policy.Refusal); ok {
		return refuse(r.Message, r.Hint)
	}
	if r, ok := err.(*harness.Refusal); ok {
		return refuse(r.Reason, r.Action)
	}
	if r, ok := err.(*cred.Refusal); ok {
		return refuse(r.Reason, r.Action)
	}
	return refuse(err.Error(), "See the message above.")
}

func (s *session) expect(candidates prompt.Candidates) *validate.Expectations {
	var nums []int
	seen := map[int]bool{}
	for _, set := range candidates {
		for _, issue := range set.Issues {
			n, _ := strconv.Atoi(issue.Number.String())
			if n == 0 {
				continue
			}
			if seen[n] {
				continue
			}
			seen[n] = true
			nums = append(nums, n)
		}
	}
	return &validate.Expectations{Findings: len(s.findings), Candidates: nums}
}
