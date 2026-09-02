package resolve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/carlosboeing/crossrev/internal/ui"

	"github.com/carlosboeing/crossrev/internal/config"
	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/cred"
	"github.com/carlosboeing/crossrev/internal/diff"
	"github.com/carlosboeing/crossrev/internal/exec"
	"github.com/carlosboeing/crossrev/internal/forge"
	"github.com/carlosboeing/crossrev/internal/harness"
	"github.com/carlosboeing/crossrev/internal/policy"
	"github.com/carlosboeing/crossrev/internal/prompt"
	"github.com/carlosboeing/crossrev/internal/prstate"
	"github.com/carlosboeing/crossrev/internal/sandbox"
	"github.com/carlosboeing/crossrev/internal/validate"
	"github.com/carlosboeing/crossrev/internal/vcs"
)

func (l *Leg) prepareWorktree(ctx context.Context, s *session) (string, error) {
	wt, err := vcs.WorktreeDir(s.repo, s.req.PR)
	if err != nil {
		return "", err
	}
	head := s.pr.HeadRefOid
	if ok, err := l.Git.HasCommit(ctx, head); err != nil {
		return "", err
	} else if !ok {
		remote, err := l.pushRemote(ctx, s.pr.HeadRefName)
		if err != nil {
			return "", err
		}
		_ = l.Git.Fetch(ctx, remote, head.SHA())
		if ok, _ := l.Git.HasCommit(ctx, head); !ok {
			_ = l.Git.Fetch(ctx, remote, fmt.Sprintf("refs/pull/%d/head", s.req.PR))
		}
		if ok, _ := l.Git.HasCommit(ctx, head); !ok {
			_ = l.Git.Fetch(ctx, remote, s.pr.HeadRefName)
		}
		if ok, _ := l.Git.HasCommit(ctx, head); !ok {
			_ = l.Git.Fetch(ctx, remote, "")
		}
		if ok, _ := l.Git.HasCommit(ctx, head); !ok {
			return "", &Refusal{
				Message: fmt.Sprintf("could not find revision '%s' for %s#%d", head.SHA(), s.repo, s.req.PR),
				Hint:    fmt.Sprintf("Fetching from remote '%s' did not reach the pull request's head revision.", remote),
			}
		}
	}
	reusable, err := l.Git.WorktreeReusable(ctx, wt, head)
	if err != nil {
		return "", err
	}
	if !reusable {
		_ = os.RemoveAll(wt)
		l.Git.PruneWorktrees(ctx)
		if err := l.Git.AddWorktree(ctx, wt, head); err != nil {
			return "", err
		}
		if l.Log != nil {
			l.Log.Event("worktree", "created "+wt)
		}
	}
	work := l.Git.WithDir(wt)
	current, err := work.Head(ctx)
	if err != nil {
		return "", err
	}
	remote, err := l.pushRemote(ctx, s.pr.HeadRefName)
	if err != nil {
		return "", err
	}
	target, err := l.Git.ResolvePushRepo(ctx, remote)
	if err != nil {
		return "", err
	}
	// lib/run.sh:285-291. Only an explicit `isCrossRepository: false` means
	// this repository's own branch and lets the head repository default to it.
	// Every other case — a fork, or provenance the payload did not carry —
	// reads the head repository out of the payload and leaves it EMPTY when it
	// is not there, which lib/legs.sh:468 refuses. Defaulting to the origin
	// repository here skipped that refusal and the maintainer-edit check with
	// it.
	var headRepo core.Slug
	cross := policy.FlagFalse
	maint := policy.FlagFalse
	if s.pr.IsCrossRepository {
		cross = policy.FlagTrue
		if s.pr.HeadRepositoryOwner != "" && s.pr.HeadRepository != "" {
			if slug, err := core.NewSlug(s.pr.HeadRepositoryOwner, s.pr.HeadRepository); err == nil {
				headRepo = slug
			}
		}
		if s.pr.MaintainerCanModify {
			maint = policy.FlagTrue
		} else {
			maint = policy.FlagFalse
		}
	} else {
		headRepo = s.repo
	}
	defaultBranch := l.Forge.DefaultBranch(ctx, s.repo)
	if defaultBranch == "" {
		defaultBranch = s.pr.BaseRefName
	}
	if err := policy.AssertPushTarget(policy.PushTarget{
		Current:             current,
		Head:                head,
		HeadBranch:          s.pr.HeadRefName,
		DefaultBranch:       defaultBranch,
		HeadRepo:            headRepo,
		OriginRepo:          target.Repo,
		MaintainerCanModify: maint,
		CrossRepo:           cross,
	}); err != nil {
		return "", err
	}
	return wt, nil
}

func (l *Leg) pushRemote(ctx context.Context, branch string) (string, error) {
	for _, key := range []string{
		"branch." + branch + ".pushRemote",
		"branch." + branch + ".remote",
		"remote.pushDefault",
	} {
		val, err := l.Git.ConfigGet(ctx, key)
		if err != nil {
			return "", err
		}
		if val != "" {
			return val, nil
		}
	}
	return "origin", nil
}

func (l *Leg) invoke(ctx context.Context, s *session, marker prstate.Marker, workdir string) Result {
	if resolutionCount(marker) != 0 {
		return Result{
			Outcome:     OutcomeInvoked,
			Pass:        s.pass,
			Marker:      marker,
			Resolutions: marker.Resolutions,
			// ui_say (lib/run.sh:1991). Said before the commit and reply
			// steps, which is where the shell says it.
			Messages: []ui.Line{ui.Say("The previous attempt already recorded its resolutions, so the resolver is not run again.")},
		}
	}

	threads := l.Forge.ReviewThreads(ctx, s.repo, s.req.PR)
	s.findings = backfillRoots(s.findings, threads)
	s.findings = enrichFindings(s.findings, s.markers, s.minFix)

	candidates, err := l.candidates(ctx, s)
	if err != nil {
		return wrapErr(err)
	}
	promptBytes, err := l.renderPrompt(ctx, s, threads, candidates, workdir)
	if err != nil {
		return wrapErr(err)
	}

	tmp, err := os.MkdirTemp("", "crossrev-resolve-*")
	if err != nil {
		return wrapErr(err)
	}
	defer os.RemoveAll(tmp)
	promptPath := filepath.Join(tmp, "prompt")
	schemaPath := filepath.Join(tmp, "schema.json")
	if err := os.WriteFile(promptPath, promptBytes, 0o600); err != nil {
		return wrapErr(err)
	}
	schema := validate.ResolveSchema()
	if err := os.WriteFile(schemaPath, schema, 0o600); err != nil {
		return wrapErr(err)
	}

	doc, err := l.document()
	if err != nil {
		return wrapErr(err)
	}
	adapter, err := l.adapterFor(doc, s.settings.Harness)
	if err != nil {
		return wrapErr(err)
	}

	ep, err := l.endpoint(s)
	if err != nil {
		return wrapErr(err)
	}
	if err := harness.AssertEnvClean(l.Env); err != nil {
		return wrapErr(err)
	}
	staged, err := cred.Prepare(doc.Credentials().For(s.settings.Harness), s.settings.Endpoint, cred.Options{
		Now: func() time.Time { return l.now() },
	})
	if err != nil {
		return wrapErr(err)
	}
	defer func() { _ = cred.Discard(staged) }()

	inv := harness.Invocation{
		Prompt:   harness.File{Path: promptPath, Text: string(promptBytes)},
		Schema:   harness.File{Path: schemaPath, Text: string(schema)},
		Workdir:  workdir,
		Model:    s.settings.Model,
		Effort:   s.settings.Effort,
		Endpoint: ep,
		Write:    core.WriteCapabilityFor(core.RoleResolver) == core.WriteYes,
		Env:      l.Env,
		Scratch:  filepath.Join(tmp, "scratch"),
	}
	if err := os.MkdirAll(inv.Scratch, 0o700); err != nil {
		return wrapErr(err)
	}

	entry, _ := doc.For(s.settings.Harness)
	shapeBudget := 1
	if !entry.SchemaNative {
		shapeBudget = 2
	}
	semanticBudget := 1

	work := l.Git.WithDir(workdir)
	snapIndex := filepath.Join(tmp, "index")
	snapTree, _ := work.CaptureTree(ctx, snapIndex)

	sbx, err := sandbox.LoadDescriptor(harness.DescriptorJSON())
	if err != nil {
		return wrapErr(err)
	}
	paths := sbx.Paths()

	// What the retry loop said, carried out with whatever it answers. Bash
	// prints these as it goes; a leg here answers its lines (internal/ui).
	var msgs []ui.Line

	for attempt := 1; ; attempt++ {
		transcript := ""
		if l.Log != nil {
			// The payload path per attempt, as the review leg does. Bash writes
			// "$CROSSREV_TRANSCRIPT_BASE.payload" for both legs (lib/run.sh:819).
			// Without it the codex adapter refuses with ErrScratch, so a native
			// resolve leg could not run under codex at all, and every other
			// harness lost its per-attempt payload from the run log.
			if base, ok := l.Log.TranscriptBase(attempt); ok {
				transcript = base
				inv.PayloadPath = base + ".payload"
			}
			l.Log.Event("invoke", fmt.Sprintf("harness=%s attempt=%d start", s.settings.Harness, attempt))
		}
		if _, err := sandbox.Quarantine(workdir, paths); err != nil {
			return wrapErr(err)
		}
		spec, err := adapter.Spec(inv)
		if err != nil {
			if _, _, restoreErr := sandbox.Restore(workdir, paths); restoreErr != nil {
				return restoreFailure(s.settings.Harness, err.Error(), restoreErr.Error())
			}
			return wrapErr(err)
		}
		started := l.now()
		res := l.runner().Run(ctx, spec)
		if _, _, restoreErr := sandbox.Restore(workdir, paths); restoreErr != nil {
			// The run's own failure is the cause, not a placeholder. Bash puts
			// the reason the attempt is being abandoned in this slot
			// (lib/run.sh:684, called from :881 and :893), so a runner that
			// died must say so here rather than be replaced by the restore.
			//
			// The envelope is built here rather than before the restore, so the
			// order of the two on the success path stays the shell's: Bash
			// restores at lib/run.sh:834 and only then reads `.ok` at :840. It
			// is safe to build after a failed restore because no adapter reads a
			// quarantined path — the one that touches disk at all, codex, reads
			// inv.PayloadPath, which runlog puts in the run directory.
			return restoreFailure(s.settings.Harness, runFailureCause(adapter.Envelope(inv, res), res), restoreErr.Error())
		}
		if l.Log != nil {
			// duration in whole seconds, which is what `$SECONDS` counts
			// (lib/run.sh:825, :831).
			l.Log.Event("invoke", fmt.Sprintf("harness=%s attempt=%d exit=%d duration=%ds",
				s.settings.Harness, attempt, res.ExitCode, int(l.now().Sub(started).Seconds())))
		}
		if res.Err != nil && exec.IsNotFound(res.Err) {
			if r := adapter.NotInstalled(); r != nil {
				return refuse(r.Reason, r.Action)
			}
		}
		env := adapter.Envelope(inv, res)
		// Archived after the parse and filtered in place, the order
		// lib/adapters/claude.sh:126-130 and :148-154 keep.
		l.Log.WriteTranscript(transcript, res.Stdout, res.Stderr)
		if !env.OK {
			msg := "no error reported"
			if env.Error != nil && *env.Error != "" {
				msg = *env.Error
			}
			out := refuse(fmt.Sprintf("the %s harness failed: %s", s.settings.Harness, msg),
				"If the error above mentions authentication, a token or a 401, the harness is installed and cannot log in.")
			out.Messages = append(msgs, out.Messages...)
			return out
		}
		err = validate.Resolve(env.Payload, s.expect(candidates))
		if err == nil {
			mapped, mapErr := mapNumbers(env.Payload, s.findings)
			if mapErr != nil {
				return wrapErr(mapErr)
			}
			return Result{
				Outcome:     OutcomeInvoked,
				Pass:        s.pass,
				Marker:      marker,
				Resolutions: mapped,
				Envelope:    env,
				Prompt:      promptBytes,
				Invocation:  inv,
				Messages:    msgs,
			}
		}
		var sem *validate.SemanticError
		if errors.As(err, &sem) {
			if semanticBudget > 0 {
				semanticBudget--
				if reset := l.retryReset(ctx, work, snapIndex, snapTree, s.settings.Harness, sem.Problem); reset != nil {
					reset.Messages = append(msgs, reset.Messages...)
					return *reset
				}
				// ui_warn, the pair kept apart (lib/run.sh:882-883).
				msgs = append(msgs, ui.Warn(
					fmt.Sprintf("%s returned an answer that contradicts what it was given — %s", s.settings.Harness, sem.Problem),
					"The shape is right, so this is the model drifting rather than a bug in CrossRev or the harness. Anything it edited has been put back, and it is being asked once more; a second one is fatal."))
				continue
			}
			msgs = append(msgs, l.invokeAbort(ctx, work, snapIndex, snapTree)...)
			out := refuse(fmt.Sprintf("%s twice returned an answer that contradicts what it was given — %s", s.settings.Harness, sem.Problem),
				"The shape was right both times, so the schema cannot catch this and CrossRev will not guess which finding was meant. Nothing has been written to the pull request, and the edits both rejected attempts made have been put back. Re-run the leg, or try the other harness.")
			out.Messages = append(msgs, out.Messages...)
			return out
		}
		problem := err.Error()
		shapeBudget--
		if shapeBudget > 0 {
			if reset := l.retryReset(ctx, work, snapIndex, snapTree, s.settings.Harness, problem); reset != nil {
				reset.Messages = append(msgs, reset.Messages...)
				return *reset
			}
			// ui_warn (lib/run.sh:894-895). Only a harness that does not
			// constrain its own output reaches here: a native one starts with
			// a budget of 1.
			msgs = append(msgs, ui.Warn(
				fmt.Sprintf("%s returned an object that does not match the schema — %s", s.settings.Harness, problem),
				"That harness does not constrain its own output, so this is the expected failure rather than a bug. Anything it edited has been put back, and it is being retried once; a second mismatch is fatal."))
			continue
		}
		msgs = append(msgs, l.invokeAbort(ctx, work, snapIndex, snapTree)...)
		// Two endings, and the difference is whose bug it is
		// (lib/run.sh:899-905).
		out := refuse(fmt.Sprintf("%s returned an object that does not match the schema — %s", s.settings.Harness, problem),
			shapeExhaustedAction(entry.SchemaNative))
		out.Messages = append(msgs, out.Messages...)
		return out
	}
}

// shapeExhaustedAction is lib/run.sh:901 and :904.
func shapeExhaustedAction(schemaNative bool) string {
	if schemaNative {
		return "This harness validates output against the schema natively, so a mismatch is an adapter or harness bug rather than model drift. Nothing has been written to the pull request, and the rejected attempt's edits have been put back."
	}
	return "That harness does not constrain its own output, so two mismatches in a row is the model failing the JSON instruction rather than an adapter bug. Name a model that follows a JSON instruction. Nothing has been written to the pull request, and the rejected attempt's edits have been put back."
}

// retryReset is _run_retry_reset (lib/run.sh:680-686): put the tree back, or
// refuse to ask again at all.
//
// Asking again on top of a discarded attempt's edits is worse than losing the
// pass — the accepted answer is then recorded against changes it never made,
// and the commit carries both. A nil answer means the reset held and the retry
// may run.
func (l *Leg) retryReset(ctx context.Context, work Git, index, tree, harnessName, problem string) *Result {
	if work.RestoreTree(ctx, index, tree) == nil {
		return nil
	}
	out := refuse(
		fmt.Sprintf("%s needs asking again, and the working tree it already edited could not be put back — %s", harnessName, problem),
		"Retrying on top of a discarded attempt's edits would commit changes no accepted answer describes. Nothing has been written to the pull request; check `git status` in the checkout and re-run the leg.")
	return &out
}

// invokeAbort is _run_invoke_abort (lib/run.sh:698-704): the way out when an
// answer is rejected and there is no attempt left to make.
//
// The exhausted path restores too. Without it the last rejected attempt's edits
// sit in the checkout with nothing on the pull request to say so, and the next
// run captures them as its own baseline. A restore that will not apply is
// warned about rather than hidden: the leg is dying anyway, and the operator is
// the one who has to deal with what is left.
func (l *Leg) invokeAbort(ctx context.Context, work Git, index, tree string) []ui.Line {
	if tree == "" || work.RestoreTree(ctx, index, tree) == nil {
		return nil
	}
	return []ui.Line{ui.Warn(
		"the rejected attempt's edits could not be put back",
		"They are still in the checkout, and a later run would capture them as its own baseline. Check `git status` before re-running the leg.")}
}

func (l *Leg) renderPrompt(ctx context.Context, s *session, threads []forge.ReviewThread, candidates prompt.Candidates, workdir string) ([]byte, error) {
	findings, err := toPromptFindings(s.findings)
	if err != nil {
		return nil, err
	}
	sbx, err := sandbox.LoadDescriptor(harness.DescriptorJSON())
	if err != nil {
		return nil, err
	}
	rawDiff, err := l.Forge.PullRequestDiff(ctx, s.repo, s.pr.BaseRefOid, s.pr.HeadRefOid)
	if err != nil {
		return nil, err
	}
	exclude := []string{}
	if s.backlog.Destination == config.DestinationRepository {
		exclude = []string{s.backlog.Path, ".crossrev"}
	}
	diffBytes := diff.Parse(rawDiff, core.RevisionPair{}).Excluded(exclude)

	logBytes, _ := l.Git.LogSubjects(ctx, s.pr.BaseRefOid)
	template, _, _ := l.Git.Show(ctx, s.pr.BaseRefOid, ".gitmessage")
	email := os.Getenv("CROSSREV_GIT_EMAIL")
	if email == "" {
		email = "crossrev@users.noreply.github.com"
	}

	r := prompt.Resolve{
		Skill:            prompt.ResolveSkill(),
		Diff:             diffBytes,
		Meta:             promptMeta(s),
		Findings:         findings,
		Threads:          toPromptThreads(threads),
		Candidates:       candidates,
		QuarantinedPaths: sbx.Paths(),
		Convention: prompt.CommitConvention{
			Base:         s.pr.BaseRefOid.SHA(),
			Log:          logBytes,
			ExcludeEmail: email,
			Template:     template,
		},
	}
	_ = workdir
	return r.Render(), nil
}

func promptMeta(s *session) prompt.Meta {
	return prompt.Meta{
		Repo:           prompt.Str(s.repo.String()),
		PR:             prompt.Num(s.req.PR),
		Pass:           prompt.Num(s.pass),
		HeadSHA:        prompt.Str(s.pr.HeadRefOid.SHA()),
		Title:          prompt.Str(s.pr.Title),
		Body:           prompt.Str(s.pr.Body),
		MinFixSeverity: prompt.Str(string(s.minFix)),
		Backlog:        prompt.Str(s.backlog.String()),
	}
}

func (l *Leg) candidates(ctx context.Context, s *session) (prompt.Candidates, error) {
	if s.backlog.Destination != config.DestinationGitHubIssues {
		return nil, nil
	}
	var out prompt.Candidates
	limit := 10
	searched := 0
	for _, f := range s.findings {
		if searched >= limit {
			break
		}
		searched++
		path := f.Member("path").StringVal()
		issues := l.Forge.IssueCandidates(ctx, s.repo, path, "")
		if len(issues) == 0 {
			continue
		}
		set := prompt.CandidateSet{FindingID: f.Member("id").StringVal()}
		for _, iss := range issues {
			set.Issues = append(set.Issues, prompt.Issue{
				Number: prompt.Num(iss.Number),
				State:  prompt.Str(iss.State),
				Title:  prompt.Str(iss.Title),
			})
		}
		out = append(out, set)
	}
	return out, nil
}

func (l *Leg) endpoint(s *session) (harness.Endpoint, error) {
	name := s.settings.Endpoint
	if name == "" || name == "null" {
		return harness.Endpoint{}, nil
	}
	resolved, err := s.cfg.Endpoint(name)
	if err != nil {
		return harness.Endpoint{}, err
	}
	token := lookupEnv(l.Env, resolved.TokenEnv)
	return harness.Endpoint{
		Name:     resolved.Name,
		URL:      resolved.BaseURL,
		TokenVar: resolved.TokenEnv,
		Token:    token,
	}, nil
}

func lookupEnv(env []string, name string) string {
	prefix := name + "="
	for _, e := range env {
		if len(e) >= len(prefix) && e[:len(prefix)] == prefix {
			return e[len(prefix):]
		}
	}
	return ""
}

func backfillRoots(findings []harness.Node, threads []forge.ReviewThread) []harness.Node {
	for i := range findings {
		if root := findings[i].Member("root_comment_id"); root.Present() && !root.IsNull() {
			continue
		}
		id := findings[i].Member("id").StringVal()
		for _, th := range threads {
			for _, fid := range th.FindingIDs {
				if string(fid) == id && th.RootCommentID != 0 {
					findings[i].Set("root_comment_id", harness.FromInt(th.RootCommentID))
				}
			}
		}
	}
	return findings
}

func enrichFindings(findings []harness.Node, markers []prstate.Marker, minFix core.Severity) []harness.Node {
	priors := priorResolutions(markers)
	for i := range findings {
		n := i + 1
		findings[i].Set("number", harness.FromInt(int64(n)))
		sev := core.Severity(findings[i].Member("severity").StringVal())
		pre := findings[i].Member("pre_existing").StringVal() == "true"
		may := policy.ShouldFix(sev, minFix, pre)
		findings[i].Set("may_fix", harness.FromBool(may))
		if prior, ok := priors[findings[i].Member("id").StringVal()]; ok {
			findings[i].Set("prior_resolution", harness.FromString(prior))
		} else {
			findings[i].Set("prior_resolution", harness.FromNull())
		}
	}
	return findings
}

func priorResolutions(markers []prstate.Marker) map[string]string {
	out := map[string]string{}
	for _, m := range markers {
		if m.Leg != core.LegResolve {
			continue
		}
		var recs []struct {
			FindingID  string `json:"finding_id"`
			Resolution string `json:"resolution"`
		}
		_ = m.DecodeResolutions(&recs)
		for _, r := range recs {
			if r.FindingID != "" && r.Resolution != "" {
				out[r.FindingID] = r.Resolution
			}
		}
	}
	return out
}

func toPromptFindings(in []harness.Node) ([]prompt.Finding, error) {
	raw, err := json.Marshal(in)
	if err != nil {
		return nil, err
	}
	var out []prompt.Finding
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func toPromptThreads(threads []forge.ReviewThread) []prompt.Thread {
	out := make([]prompt.Thread, 0, len(threads))
	for _, th := range threads {
		comments := make([]prompt.Comment, len(th.Comments))
		for i, c := range th.Comments {
			comments[i] = prompt.Comment{Author: prompt.Login(c.Author), Body: prompt.Str(c.Body)}
		}
		out = append(out, prompt.Thread{
			Path:       prompt.Str(th.Path),
			Line:       prompt.Num(th.Line),
			IsResolved: prompt.Bool(th.IsResolved),
			Comments:   comments,
		})
	}
	return out
}

func jsonString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	return string(raw)
}
