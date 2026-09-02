package cycle

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/carlosboeing/crossrev/internal/config"
	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/forge"
	"github.com/carlosboeing/crossrev/internal/policy"
	"github.com/carlosboeing/crossrev/internal/prstate"
	"github.com/carlosboeing/crossrev/internal/ui"
)

// Life is what can be shown about the process behind an unfinished claim
// (lib/run.sh:3284-3294).
//
// The empty value is a real answer and the common one: an unreadable lock, a
// run in another checkout, a pull request watched from a machine that never
// held the lock. Anything that cannot be shown is not claimed.
type Life string

// The four answers, spelled as STATUS_LIVENESS holds them.
const (
	// LifeUnknown is no evidence either way.
	LifeUnknown Life = ""
	// LifeRunning is a process that answers, or a workflow run GitHub says
	// has not finished.
	LifeRunning Life = "running"
	// LifeGone is positive evidence the run is over while the marker never
	// completed.
	LifeGone Life = "gone"
	// LifeElsewhere is a lock readable here whose pid belongs to another
	// host.
	LifeElsewhere Life = "elsewhere"
)

// Liveness answers whether the run behind one open claim is still working.
//
// It is an interface because the two answers come from different places and
// neither belongs to the report: a local run is answered from the lock file in
// the git dir, an automated one from the Actions API (lib/run.sh:3295-3353).
// The detail string carries the reason a `gone` is known, or the host an
// `elsewhere` is on, and is empty for the other two.
//
// An implementation must memoise on the marker's `run_id`, which is what
// lib/run.sh:3288-3290 does with a global rather than stdout: one report asks
// about the same claim twice — once for its row and once for the NEXT line
// under it — and for an automated leg an unmemoised answer is a second API
// call per row.
type Liveness interface {
	Alive(ctx context.Context, claim prstate.Marker) (Life, string)
}

// LegRow is one leg line, decided rather than laid out.
//
// The glyph reflects the OUTCOME and not whether the leg ran, which is the
// distinction lib/run.sh:3195-3206 draws: green for a normal outcome, red for a
// bad one, a dim circle for a leg that never ran, and the half circle only
// where the process behind an open claim answers.
//
// The gutter and the nine-column leg label are layout and are not here. The
// left column is always `review` or `resolve`, never a pseudo-leg standing in
// for a reason; the reason is in Description.
type LegRow struct {
	Pass        int
	Leg         core.Leg
	Step        ui.Step
	Description string
}

// NextLine is one line of the NEXT section, and whether it is a command.
//
// The two are printed differently — `ui_cmd` colours a command and gives it no
// glyph (lib/ui.sh:72) — and the difference is the section's contract: NEXT
// always ends in something the reader can type.
type NextLine struct {
	Text    string
	Command bool
}

// Report is one reading of a pull request's loop state: the header, the pass
// numbers, every leg row and the NEXT decision.
//
// It carries the pull request's own facts as well, because `status` reads them
// out of the single `gh pr view --json` call it already made
// (lib/run.sh:3070-3084) and nothing should go back to the forge to render.
type Report struct {
	Repo core.Slug
	PR   int

	// State is the header word, and Colour is what lib/run.sh:3055-3060
	// colours it with. Note is the watchdog qualifier, or empty.
	State  core.LoopState
	Colour ui.State
	Note   string

	Title        string
	URL          string
	HeadSHA      string
	HeadBranch   string
	ChangedFiles int
	Labels       []string

	Mode              string
	Author            string
	Pass              int
	MaxPass           int
	MaxPassesPerCycle int
	MinFixSeverity    core.Severity
	Backlog           config.Backlog

	Rows []LegRow
	Next []NextLine
}

// Status reads a pull request and decides what `crossrev status` has to say
// about it (lib/run.sh:3034-3103).
//
// The state comes off the pull request, and only the liveness of an unfinished
// leg comes off the process. A leg the usage limit killed after forty seconds
// and a leg happily working for forty seconds leave identical state on the pull
// request, so an age-based "still running" would hand a dead loop a reassuring
// line; a pid that answers is a different kind of fact, and it is the only
// thing that puts "running now" on a row.
type Status struct {
	Forge    forge.Forge
	Liveness Liveness
	// Now is the clock the age of an open claim is measured against. The
	// shell reads `date +%s` at each row (lib/run.sh:3246).
	Now func() time.Time
	// Show reads the configuration from the pull request's base revision,
	// which is where policy is read from and never the head (ADR 0003).
	Show config.ShowFile
}

// Load reads the pull request and derives the whole report.
//
// Argument handling belongs to the CLI: this takes a repository and a number
// the way ctx_load does (lib/run.sh:3051). The trigger is human, so the fork
// and draft refusals ctx_load applies to an automatic invocation are not
// reachable here; the closed-pull-request refusal is.
func (s *Status) Load(ctx context.Context, repo core.Slug, pr int) (Report, error) {
	var report Report
	if repo.Incomplete() {
		slug, err := s.Forge.RepoSlug(ctx)
		if err != nil {
			return report, &ui.FatalError{
				Reason: "could not work out which repository this is",
				Action: "Run crossrev from a checkout with a GitHub remote, or pass --repo owner/name.",
			}
		}
		repo = slug
	}
	pull, err := s.Forge.PullRequest(ctx, repo, pr)
	if err != nil {
		return report, &ui.FatalError{
			Reason: fmt.Sprintf("could not read %s#%d", repo, pr),
			Action: "Check the number, and that `gh auth status` passes for that repository.",
		}
	}
	if !strings.EqualFold(pull.State, "OPEN") {
		return report, &ui.FatalError{
			Reason: fmt.Sprintf("%s#%d is not open", repo, pr),
			Action: "crossrev only runs on open pull requests. Reopen it, or pick another number.",
		}
	}

	cfg, err := config.Load(ctx, pull.BaseRefOid, s.Show)
	if err != nil {
		return report, err
	}
	backlog, err := cfg.ResolveBacklog(ctx, pull.BaseRefOid, cfg.Get(".backlog.destination"))
	if err != nil {
		return report, err
	}
	author, err := trustedAuthor(ctx, s.Forge, cfg.Get(".mode"), repo, pr)
	if err != nil {
		return report, err
	}

	maxPasses, _ := strconv.Atoi(cfg.Get(".policy.max_passes_per_cycle"))
	in := statusInput{
		repo:      repo,
		pr:        pr,
		labels:    statusLabelNames(pull.Labels),
		markers:   statusMarkers(s.Forge.IssueComments(ctx, repo, pr), author),
		headSHA:   pull.HeadRefOid.SHA(),
		minFix:    core.Severity(cfg.Get(".policy.min_fix_severity")),
		maxPasses: maxPasses,
		now:       s.Now(),
		liveness:  s.Liveness,
	}

	report = Report{
		Repo:              repo,
		PR:                pr,
		State:             statusState(in),
		Title:             pull.Title,
		URL:               pull.URL,
		HeadSHA:           in.headSHA,
		HeadBranch:        pull.HeadRefName,
		ChangedFiles:      pull.ChangedFiles,
		Labels:            in.labels,
		Mode:              cfg.Get(".mode"),
		Author:            author,
		Pass:              prstate.CurrentReviewPass(in.markers),
		MaxPass:           prstate.MaxPass(in.markers),
		MaxPassesPerCycle: maxPasses,
		MinFixSeverity:    in.minFix,
		Backlog:           backlog,
	}
	report.Colour = statusColour(report.State)
	if statusHasLabel(in.labels, policy.LabelWatchdogRetried) {
		report.Note = "(retried once)"
	}

	// Every pass, refused ones included, which is what MaxPass counts
	// (lib/run.sh:3088-3097).
	for pass := 1; pass <= report.MaxPass; pass++ {
		report.Rows = append(report.Rows,
			statusLegRow(ctx, in, pass, core.LegReview),
			statusLegRow(ctx, in, pass, core.LegResolve))
	}
	report.Next = statusNext(ctx, in, report.State, report.Pass)
	return report, nil
}

// statusInput is everything the decisions below read, gathered once.
type statusInput struct {
	repo      core.Slug
	pr        int
	labels    []string
	markers   []prstate.Marker
	headSHA   string
	minFix    core.Severity
	maxPasses int
	now       time.Time
	liveness  Liveness
}

// statusColour is the header colour for a state word (lib/run.sh:3055-3060).
func statusColour(state core.LoopState) ui.State {
	switch state {
	case core.LoopConverged:
		return ui.StateOK
	case core.LoopHalted:
		return ui.StateWarn
	case core.LoopStopped:
		return ui.StateBad
	default:
		return ui.StateNeutral
	}
}

// statusState is the header word, read off the loop labels rather than computed
// beside them (lib/run.sh:3112-3120).
//
// One to one with the fixed labels, no exceptions, in the order a human would
// read them: a stop request outranks everything, then a halt, then convergence,
// then whichever leg is owed. Someone who learns the labels on GitHub already
// knows the terminal's words, and the header stops being computed independently
// of the label it duplicates.
func statusState(in statusInput) core.LoopState {
	switch {
	case statusHasLabel(in.labels, policy.LabelStop):
		return core.LoopStopped
	case statusHasLabel(in.labels, policy.LabelHalted):
		return core.LoopHalted
	case statusHasLabel(in.labels, policy.LabelConverged):
		return core.LoopConverged
	case statusHasLabel(in.labels, policy.LabelAwaitingResolution):
		return core.LoopAwaitingResolution
	case statusHasLabel(in.labels, policy.LabelAwaitingReview):
		return core.LoopAwaitingReview
	}
	return statusStateFromMarkers(in)
}

// statusStateFromMarkers answers the header word for a pull request carrying no
// state label at all, which is not the same question as which one
// (lib/run.sh:3131-3178).
//
// Locally a label that will not apply is a warning rather than a fatal — one
// process drives both legs, so the chain does not depend on it — which means a
// repository that never ran `crossrev init` runs the loop perfectly well with
// no labels on it. Answering "awaiting review" there whenever a resolve leg is
// plainly owed would send the reader to the wrong command, so the markers
// answer instead. They say the same thing the labels would have; they are just
// the copy that is always written.
func statusStateFromMarkers(in statusInput) core.LoopState {
	pass := prstate.CurrentReviewPass(in.markers)
	if pass == 0 {
		return core.LoopAwaitingReview
	}

	// A pass a cap refused to start is a halt, and it is recorded one pass
	// ahead of the last one that ran.
	if next, ok := prstate.MarkerFor(in.markers, pass+1, core.LegReview); ok && next.State.Declined() {
		return core.LoopHalted
	}

	review, ok := prstate.MarkerFor(in.markers, pass, core.LegReview)
	if !ok || review.State != core.PassComplete {
		return core.LoopAwaitingReview
	}
	switch core.Verdict(review.Verdict.Value()) {
	case core.VerdictConverged:
		return core.LoopConverged
	case core.VerdictBlocked:
		return core.LoopHalted
	}

	resolve, hasResolve := prstate.MarkerFor(in.markers, pass, core.LegResolve)
	if !hasResolve || resolve.State != core.PassComplete {
		// The marker copy of the label the review leg wrote (PassLabel), so
		// the two cannot disagree on a pull request with no labels to read:
		// an empty pass while an escalation stands is halted, not a resolve
		// leg owed — there is nothing for the leg to do.
		if statusActionable(review.Findings, in.minFix) == 0 && statusMarkersEscalated(in.markers) > 0 {
			return core.LoopHalted
		}
		return core.LoopAwaitingResolution
	}

	// The marker copy of the label the resolve leg wrote (ResolvePassLabel),
	// read off the same marker by the same rule. A pass that escalated is one
	// of the halts inside it: reading only `blocked` here would answer
	// "awaiting review" on the path where the loop is waiting on a person,
	// and send the reader to start a pass that settles nothing.
	switch policy.ResolvePassLabel(statusResolveMarker(resolve), statusMarkersEscalated(in.markers)) {
	case policy.PassHalted:
		return core.LoopHalted
	case policy.PassConverged:
		return core.LoopConverged
	default:
		return core.LoopAwaitingReview
	}
}

// statusEscalated is how many findings one resolve leg handed to a human
// (lib/run.sh:3182-3185). An absent marker counts as none, so callers can ask
// before they know one exists.
func statusEscalated(m prstate.Marker) int {
	n := 0
	for _, r := range statusResolutions(m) {
		if r.Resolution == string(core.ResolutionEscalated) {
			n++
		}
	}
	return n
}

// statusMarkersEscalated counts escalated findings across every resolve marker
// on the pull request (lib/run.sh:3191-3193).
//
// Which pass handed one to a human stops mattering when a newer pass runs: the
// halt it caused is still standing, and only re-driving that pass — which
// rewrites its marker — or settling the thread by hand clears it.
func statusMarkersEscalated(markers []prstate.Marker) int {
	n := 0
	for _, m := range markers {
		if m.Leg != core.LegResolve {
			continue
		}
		n += statusEscalated(m)
	}
	return n
}

// statusActionable counts the findings the resolve leg may change code for
// (lib/run.sh:350-360), which is policy.ShouldFix applied one finding at a
// time.
func statusActionable(findings json.RawMessage, minFix core.Severity) int {
	n := 0
	for _, f := range statusFindings(findings) {
		if policy.ShouldFix(core.Severity(f.Severity), minFix, f.PreExisting) {
			n++
		}
	}
	return n
}

// statusLegRow decides one leg line: which glyph, and which words
// (lib/run.sh:3207-3267).
func statusLegRow(ctx context.Context, in statusInput, pass int, leg core.Leg) LegRow {
	row := LegRow{Pass: pass, Leg: leg}
	m, ok := prstate.MarkerFor(in.markers, pass, leg)
	if !ok {
		row.Step = ui.StepIdle
		row.Description = statusLegAbsent(in, pass, leg)
		return row
	}

	switch m.State {
	case core.PassDeclined:
		reason, hasReason := m.Reason.Get()
		if !hasReason {
			reason = "a cap stopped it"
		}
		row.Step = ui.StepNo
		row.Description = "never started — " + reason
		return row
	case core.PassComplete:
		return statusLegComplete(row, m)
	}

	// An open claim is not an outcome, so the row says whether the process
	// behind it is still working — and says it only where the evidence is
	// nameable. Positive evidence outranks the age, in both directions: a leg
	// the API says is `completed` is abandoned two minutes in, and a leg whose
	// pid answers is running at ninety minutes. The window only decides the
	// case where there is no evidence either way, and there the row states the
	// absence rather than inventing a verdict for it.
	age := in.now.Unix() - m.TS
	started := fmt.Sprintf("started %d minute(s) ago", age/60)
	life, detail := in.liveness.Alive(ctx, m)
	switch life {
	case LifeRunning:
		row.Step = ui.StepRun
		row.Description = "running now — " + started
	case LifeElsewhere:
		row.Step = ui.StepIdle
		row.Description = started + " on " + detail
	case LifeGone:
		row.Step = ui.StepNo
		row.Description = started + ", abandoned — " + detail
	default:
		// A stale claim carries its own reason, and both reasons already
		// say either how old it is or which revision it was made against,
		// so the age prefix would be printing the same fact twice.
		if stale, isStale := prstate.ClaimIsStale(m, in.headSHA, in.now, prstate.DefaultClaimWindow); isStale {
			row.Step = ui.StepNo
			row.Description = "abandoned — " + stale
		} else {
			row.Step = ui.StepIdle
			row.Description = started + ", no result yet"
		}
	}
	return row
}

// statusLegAbsent is what a leg with no marker at all should say
// (lib/run.sh:3354-3373). "Has not run" reads as a step still outstanding,
// which is wrong for every case here but the first.
func statusLegAbsent(in statusInput, pass int, leg core.Leg) string {
	if leg != core.LegResolve {
		return "not run yet"
	}
	if statusHasLabel(in.labels, policy.LabelStop) {
		return "not run — crossrev/stop is applied"
	}

	review, ok := prstate.MarkerFor(in.markers, pass, core.LegReview)
	if !ok {
		return "not run yet"
	}
	// A pass a cap refused to start never reached the resolve leg and never
	// will, so "not run yet" would promise something that is not coming.
	if review.State == core.PassDeclined {
		return "not run"
	}
	if review.State != core.PassComplete {
		return "not run yet"
	}

	// A converged review is the reason the loop stopped and a blocked one
	// hands over to a human, so in neither case was a resolve leg ever owed.
	verdict := core.Verdict(review.Verdict.Value())
	switch verdict {
	case core.VerdictConverged, core.VerdictBlocked:
		return "not needed, the review " + verdict.String()
	}
	return "not run yet"
}

// statusLegComplete describes a finished leg by what it found or did
// (lib/run.sh:3376-3415).
func statusLegComplete(row LegRow, m prstate.Marker) LegRow {
	if row.Leg == core.LegReview {
		verdict := m.Verdict.Value()
		if verdict == string(core.VerdictBlocked) {
			reason, ok := m.BlockedReason.Get()
			if !ok {
				reason = "the reviewer could not complete"
			}
			row.Step = ui.StepNo
			row.Description = "blocked — " + reason
			return row
		}
		row.Step = ui.StepOK
		if parts := statusSeverityCounts(m.Findings); parts != "" {
			row.Description = parts
		} else {
			row.Description = "no findings — " + verdict
		}
		return row
	}

	if blocked, ok := m.Blocked.Get(); ok && blocked {
		reason, hasReason := m.BlockedReason.Get()
		if !hasReason {
			reason = "the resolve leg could not complete"
		}
		row.Step = ui.StepNo
		row.Description = "blocked — " + reason
		return row
	}

	parts := strings.TrimSuffix(statusResolutionCounts(m.Resolutions), ".")
	if commit := m.CommitSHA.Value(); commit != "" && commit != "null" {
		parts += ", pushed " + statusAbbreviate(commit)
	}
	// An escalated resolution halted the loop for a human, so the row cannot
	// carry the tick a settled pass gets. The header above already says
	// halted, and a green leg line underneath it contradicts the section it
	// sits in.
	if statusEscalated(m) > 0 {
		row.Step = ui.StepNo
	} else {
		row.Step = ui.StepOK
	}
	row.Description = parts
	return row
}

// statusNext is the NEXT section (lib/run.sh:3421-3535).
//
// It always ends in something the reader can type: a command, or the condition
// that has to change first and the command that follows it. Never an empty
// section, never a bare dash, and never "nothing automatic" as the last word —
// a tool whose job is telling you what to do next should not end on a dead end.
func statusNext(ctx context.Context, in statusInput, state core.LoopState, pass int) []NextLine {
	var next statusLines
	review := "crossrev review --pr " + strconv.Itoa(in.pr)
	resolve := "crossrev resolve --pr " + strconv.Itoa(in.pr)

	switch state {
	case core.LoopStopped:
		// The second command is the leg that was owed when the brake went
		// on, which is what "continue" means here. Read from the labels
		// beside the stop, or from the markers when none is there.
		resume := review
		if statusHasLabel(in.labels, policy.LabelAwaitingResolution) ||
			statusStateFromMarkers(in) == core.LoopAwaitingResolution {
			resume = resolve
		}
		// Same rule as the cap halt below: with no pass behind it there is
		// nothing to continue from, and "pass 0" names one that never existed.
		if pass > 0 {
			next.line("someone applied crossrev/stop. To continue from pass %d:", pass)
		} else {
			next.line("someone applied crossrev/stop. To start the loop:")
		}
		next.cmd("gh pr edit %d --remove-label crossrev/stop", in.pr)
		next.cmd("%s", resume)
		return next

	case core.LoopConverged:
		// A revision pushed after the loop converged is unreviewed, and
		// nothing starts a pass for it by itself: the generated review
		// workflow listens for labels and comments rather than
		// `synchronize`, and the converged label stays where the loop left
		// it, so no label change fires anything.
		if pass > 0 && prstate.IsNewRevision(in.markers, in.headSHA) {
			next.line("the loop converged on pass %d, and the branch has moved since.", pass)
			next.line("Nothing reviews a new revision on its own, so pass %d is owed:", pass+1)
			next.cmd("%s", review)
			return next
		}
		// Deliberately the same vocabulary as the converged summary comment,
		// so a reader moving between the terminal and GitHub is not
		// translating between two descriptions of one state.
		next.line("nothing to run — the loop converged on pass %d: nothing at or", pass)
		next.line("above min_fix_severity (%s) remains.", in.minFix)
		return next

	case core.LoopHalted:
		return statusNextHalted(in, pass)

	case core.LoopAwaitingResolution:
		next.cmd("%s", resolve)
		if m, ok := prstate.MarkerFor(in.markers, pass, core.LegResolve); ok && m.State == core.PassStarted {
			if life, _ := in.liveness.Alive(ctx, m); life == LifeRunning {
				next.running(pass)
			}
		}
		return next
	}

	// awaiting review, and why one is owed.
	//
	// A resolve pass that settled every finding without pushing is done: the
	// reviewer declines an unchanged head, so the review command below is one
	// that refuses. Passes labelled after the fact carry converged and never
	// reach here; this arm is the same decision (ResolvePassLabel) read off
	// the marker, for the pull requests whose awaiting-review label predates
	// it. A revision pushed after the settle is genuinely unreviewed, so the
	// head has to not have moved for the pass to be over.
	if m, ok := prstate.MarkerFor(in.markers, pass, core.LegResolve); pass > 0 && ok &&
		m.State == core.PassComplete &&
		policy.ResolvePassLabel(statusResolveMarker(m), statusMarkersEscalated(in.markers)) == policy.PassConverged &&
		!prstate.IsNewRevision(in.markers, in.headSHA) {
		next.line("nothing to run — pass %d settled every finding without a code", pass)
		next.line("change, so a re-review would decline: there is nothing new to see.")
		return next
	}

	// The cap comes before the plain invitation, because a pass at
	// max_passes_per_cycle is owed a review the loop will not start by itself.
	// The bound is a bound on the loop continuing, not on a person: an
	// automatic trigger and a cycle's own later passes meet it, while
	// `crossrev review --pr N` typed by hand runs one attended pass
	// regardless. So the state is described rather than the command withheld,
	// and the condition that changes the *automatic* behaviour goes above the
	// command — the shape the halted and stopped sections already use.
	m, hasReview := prstate.MarkerFor(in.markers, pass, core.LegReview)
	if pass >= in.maxPasses && hasReview && m.State == core.PassComplete &&
		prstate.CurrentPassComplete(in.markers, pass, core.LegResolve) {
		next.line("pass %d reached max_passes_per_cycle (%d), so the loop will", pass, in.maxPasses)
		next.line("not start another pass on its own. Raise policy.max_passes_per_cycle in")
		next.line(".github/crossrev.yml to let it continue by itself. Asking for one pass")
		next.line("by hand runs it either way:")
		next.cmd("%s", review)
		return next
	}

	next.cmd("%s", review)
	switch {
	case hasReview && m.State == core.PassStarted:
		life, _ := in.liveness.Alive(ctx, m)
		stale, isStale := prstate.ClaimIsStale(m, in.headSHA, in.now, prstate.DefaultClaimWindow)
		switch {
		case life == LifeRunning:
			next.running(pass)
		case isStale:
			next.line("The unfinished pass is stale — %s — so a re-run abandons it", stale)
			next.line("and starts pass %d again.", pass)
		default:
			next.line("The head has not moved, so a re-run resumes pass %d and posts", pass)
			next.line("only what is missing.")
		}
	case pass > 0 && prstate.IsNewRevision(in.markers, in.headSHA):
		next.line("Pass %d is closed and the branch moved, so pass %d reviews", pass, pass+1)
		next.line("the new revision.")
	}
	if statusHasLabel(in.labels, policy.LabelWatchdogRetried) {
		next.line("The watchdog has already retried this leg once — a second failure")
		next.line("halts the loop rather than retrying again.")
	}
	return next
}

// statusNextHalted is what NEXT says under each of the halt's shapes
// (lib/run.sh:3554-3655).
//
// A halt has three shapes and they need different levers: a cap wants raising,
// a blocked leg wants the underlying decision made, and an escalated finding
// wants the disagreement settled. All three are read off the marker that
// recorded the halt rather than off the label, which says only that one
// happened.
func statusNextHalted(in statusInput, pass int) []NextLine {
	var next statusLines
	review := "crossrev review --pr " + strconv.Itoa(in.pr)
	resolve := "crossrev resolve --pr " + strconv.Itoa(in.pr)

	if declined, ok := prstate.MarkerFor(in.markers, pass+1, core.LegReview); ok && declined.State.Declined() {
		reason, hasReason := declined.Reason.Get()
		if !hasReason {
			reason = "a cap stopped it"
		}
		next.line("pass %d never began — %s.", pass+1, reason)
		if pass == 0 {
			// A cap that refuses the FIRST pass leaves nothing behind it.
			// There is no pass 0, and warning that its changes are
			// unverified describes a review that never ran over code
			// nobody looked at.
			next.line("No review has run on this pull request at all. Raise the cap in")
		} else {
			next.line("So anything pass %d changed is unverified. Raise the cap in", pass)
		}
		next.line(".github/crossrev.yml, then:")
		next.cmd("%s", review)
		return next
	}

	// Escalation is tested before blocked, and counted across every resolve
	// marker rather than only this pass's. A blocked pass is always a
	// completed pass, so a marker carrying both flags sent the reader to the
	// one command that declines; and a later pass that adds nothing leaves the
	// halt standing while the finding that caused it moves a pass back.
	//
	// An escalated finding is the one halt nobody can automate past: two
	// agents disagreed twice, or the point needs a judgement that is not
	// theirs. The thread is left open on purpose, so the lever is reading it,
	// not re-running the leg that already declined to decide.
	if escalated := statusMarkersEscalated(in.markers); escalated > 0 {
		noun := "findings"
		if escalated == 1 {
			noun = "finding"
		}
		next.line("%d %s need a human decision. The resolve leg left the", escalated, noun)
		next.line("thread open and said why in it. Once you have settled it:")
		if statusHasLabel(in.labels, policy.LabelStop) {
			next.cmd("gh pr edit %d --remove-label crossrev/stop", in.pr)
		}
		next.cmd("%s", review)
		return next
	}

	// Reachable because the resolve leg re-drives a blocked pass: what stopped
	// it was the environment rather than a disagreement, so once a human has
	// fixed that, running the leg again is exactly the remedy.
	m, hasResolve := prstate.MarkerFor(in.markers, pass, core.LegResolve)
	blocked, hasBlocked := m.Blocked.Get()
	settledPass := hasResolve && m.State == core.PassComplete && !(hasBlocked && blocked)
	record := statusResolveMarker(m)
	switch {
	// A deferral whose record never landed is not settled: the thread stayed
	// open on purpose, and the remedy is filing the work and driving the pass
	// again.
	case settledPass && policy.UnfiledDeferrals(record) > 0:
		next.line("a deferred finding was never filed anywhere durable, so its thread")
		next.line("stays open. Put the work somewhere tracked, then drive the pass again:")
		next.cmd("%s", resolve)
		return next

	// A fix the resolver claimed and never committed. The finding is real by
	// the resolver's own answer and the code is unchanged, so the thread
	// stayed open and the pass halted rather than converging over it.
	case settledPass && policy.ResolveUnpushedFix(record):
		next.line("the resolver claimed a fix and pushed no commit, so its thread stays")
		next.line("open. Read the reply, then either make the change yourself or drive")
		next.line("the pass again:")
		next.cmd("%s", resolve)
		return next

	// A pass whose resolutions were never recorded, written by a crossrev old
	// enough not to carry them. Nothing on it can be shown to have settled.
	case settledPass && policy.ResolveUnrecorded(record):
		next.line("the pass recorded no resolutions, so what it settled cannot be read")
		next.line("back. Drive it again to answer the findings on the record:")
		next.cmd("%s", resolve)
		return next

	case hasResolve && hasBlocked && blocked:
		next.line("the resolve leg reported blocked and left its reasoning in the thread")
		next.line("it belongs to. Once that is settled:")
		next.cmd("%s", resolve)
		return next
	}

	if r, ok := prstate.MarkerFor(in.markers, pass, core.LegReview); ok &&
		r.Verdict.Value() == string(core.VerdictBlocked) {
		next.line("the review leg reported blocked, so what happens next is a human's")
		next.line("call. Once you have looked:")
		next.cmd("%s", review)
		return next
	}

	next.line("the loop stopped short and needs a human. Remove crossrev/halted once")
	next.line("you have looked, then:")
	next.cmd("%s", review)
	return next
}

// statusLines collects the NEXT section as it is decided.
type statusLines []NextLine

func (l *statusLines) line(format string, args ...any) {
	*l = append(*l, NextLine{Text: fmt.Sprintf(format, args...)})
}

func (l *statusLines) cmd(format string, args ...any) {
	*l = append(*l, NextLine{Text: fmt.Sprintf(format, args...), Command: true})
}

// running is what NEXT says under a command whose leg is already running
// (lib/run.sh:3544-3548).
//
// The row above says the leg is alive, so a section that reads as an invitation
// to start it again would contradict the one thing this display exists to get
// right. The command keeps its place — it is what resumes the pass if that run
// dies, and NEXT always ends in something typable — but it stops being the
// thing to do now.
func (l *statusLines) running(pass int) {
	l.line("Pass %d is running now, so wait for it rather than starting a", pass)
	l.line("second run over the same pull request. The command above resumes")
	l.line("the pass if that one dies.")
}

// statusResolutionCounts is the one-line summary of a resolve marker's
// resolutions (lib/run.sh:2585-2592), in the order the schema lists them.
func statusResolutionCounts(resolutions json.RawMessage) string {
	by := map[string]int{}
	for _, r := range statusResolutionsOf(resolutions) {
		by[r.Resolution]++
	}
	var parts []string
	for _, word := range core.Resolutions() {
		if n, ok := by[string(word)]; ok {
			parts = append(parts, strconv.Itoa(n)+" "+string(word))
		}
	}
	if len(parts) == 0 {
		return "Nothing to resolution."
	}
	return strings.Join(parts, ", ") + "."
}

// statusSeverityCounts is the review row's finding summary: the severities that
// appear, highest first, with the count of each (lib/run.sh:3384-3389).
func statusSeverityCounts(findings json.RawMessage) string {
	by := map[string]int{}
	for _, f := range statusFindings(findings) {
		by[f.Severity]++
	}
	var parts []string
	for _, severity := range []core.Severity{core.SeverityHigh, core.SeverityMedium, core.SeverityLow} {
		if n, ok := by[string(severity)]; ok && n > 0 {
			parts = append(parts, strconv.Itoa(n)+" "+string(severity))
		}
	}
	return strings.Join(parts, ", ")
}

// statusFinding is the part of a finding the row and the actionable count read.
type statusFinding struct {
	Severity    string `json:"severity"`
	PreExisting bool   `json:"pre_existing"`
}

func statusFindings(findings json.RawMessage) []statusFinding {
	var out []statusFinding
	if len(findings) == 0 {
		return nil
	}
	_ = json.Unmarshal(findings, &out)
	return out
}

// statusResolution is the part of a resolution entry the counts read.
type statusResolution struct {
	Resolution string  `json:"resolution"`
	Tracked    *string `json:"crossrev_tracked"`
}

func statusResolutions(m prstate.Marker) []statusResolution {
	var out []statusResolution
	_ = m.DecodeResolutions(&out)
	return out
}

func statusResolutionsOf(resolutions json.RawMessage) []statusResolution {
	var out []statusResolution
	if len(resolutions) == 0 {
		return nil
	}
	_ = json.Unmarshal(resolutions, &out)
	return out
}

// statusResolveMarker is the completed resolve marker in the shape the redrive
// and pass-label decisions read (lib/legs.sh:225-248).
func statusResolveMarker(m prstate.Marker) policy.ResolveMarker {
	out := policy.ResolveMarker{CommitSHA: m.CommitSHA.Value()}
	if blocked, ok := m.Blocked.Get(); ok {
		out.Blocked = blocked
	}
	for _, r := range statusResolutions(m) {
		record := policy.ResolutionRecord{Resolution: core.Resolution(r.Resolution)}
		if r.Tracked != nil {
			record.Tracked = core.NewTracked(*r.Tracked)
		}
		out.Resolutions = append(out.Resolutions, record)
	}
	return out
}

// statusMarkers reads the trusted author's markers off the conversation
// comments (lib/state.sh:56-63).
func statusMarkers(comments []forge.IssueComment, author string) []prstate.Marker {
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

func statusLabelNames(labels []forge.Label) []string {
	names := make([]string, 0, len(labels))
	for _, label := range labels {
		names = append(names, label.Name)
	}
	return names
}

// statusHasLabel is the label test. Bash asks `grep -qw` of the space-joined
// label names, which also matches a name ending in the one asked for; equality
// is what every other reader in this tree uses, and no repository mints a label
// the two would disagree on.
func statusHasLabel(labels []string, want string) bool {
	for _, label := range labels {
		if label == want {
			return true
		}
	}
	return false
}

// statusAbbreviate takes the first seven bytes of a revision, and a shorter one
// whole. Bash's `${commit:0:7}` does exactly that rather than refusing.
func statusAbbreviate(sha string) string {
	if len(sha) <= 7 {
		return sha
	}
	return sha[:7]
}

// trustedAuthor is state_trusted_author (lib/state.sh:24-47), which cmd_status
// reaches through ctx_load (lib/run.sh:3051 then lib/run.sh:309).
//
// It is keyed on the MODE the configuration names, not on who is at the
// keyboard: `automated` is the App's <slug>[bot] and nothing else, because a
// forged marker there makes an agent push a commit or believe a leg finished.
// Every other mode is the invoking user, whose worst case is being misled
// about work they asked for.
func trustedAuthor(ctx context.Context, client forge.Forge, mode string, repo core.Slug, pr int) (string, error) {
	if mode == "automated" {
		// lib/state.sh:35-38. The metadata-file fallback the shell reads for an
		// automated run started from a machine lives in internal/app, which is
		// a tier-3 peer this package may not import.
		slug := os.Getenv("CROSSREV_APP_SLUG")
		if slug == "" {
			return "", &ui.FatalError{
				Reason: "cannot determine which App's markers to trust",
				Action: "Automated mode reads markers only from the App that writes them. In a workflow, set CROSSREV_APP_SLUG from the token step's app-slug output. Locally, run: crossrev auth status",
			}
		}
		return slug + "[bot]", nil
	}
	author, err := client.ViewerLogin(ctx)
	if err != nil || author == "" {
		return "", &ui.FatalError{
			Reason: fmt.Sprintf("could not resolve whose markers to trust on %s#%d", repo, pr),
			Action: "Pass numbering, revision detection and the daily cap all read from the trusted author. Run: gh auth login",
		}
	}
	return author, nil
}
