package resolve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

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
	headRepo := s.repo
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

	for attempt := 1; ; attempt++ {
		if l.Log != nil {
			l.Log.Event("invoke", fmt.Sprintf("harness=%s attempt=%d start", s.settings.Harness, attempt))
		}
		if _, err := sandbox.Quarantine(workdir, paths); err != nil {
			return wrapErr(err)
		}
		spec, err := adapter.Spec(inv)
		if err != nil {
			_, _, _ = sandbox.Restore(workdir, paths)
			return wrapErr(err)
		}
		res := l.runner().Run(ctx, spec)
		_, _, _ = sandbox.Restore(workdir, paths)
		if l.Log != nil {
			l.Log.Event("invoke", fmt.Sprintf("harness=%s attempt=%d exit=%d", s.settings.Harness, attempt, res.ExitCode))
		}
		if res.Err != nil && exec.IsNotFound(res.Err) {
			if r := adapter.NotInstalled(); r != nil {
				return refuse(r.Reason, r.Action)
			}
		}
		env := adapter.Envelope(inv, res)
		if !env.OK {
			msg := "no error reported"
			if env.Error != nil && *env.Error != "" {
				msg = *env.Error
			}
			return refuse(fmt.Sprintf("the %s harness failed: %s", s.settings.Harness, msg),
				"If the error above mentions authentication, a token or a 401, the harness is installed and cannot log in.")
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
			}
		}
		var sem *validate.SemanticError
		if errors.As(err, &sem) {
			if semanticBudget > 0 {
				semanticBudget--
				_ = work.RestoreTree(ctx, snapIndex, snapTree)
				continue
			}
			return refuse(fmt.Sprintf("%s twice returned an answer that contradicts what it was given — %s", s.settings.Harness, sem.Problem),
				"The shape was right both times, so the schema cannot catch this and crossrev will not guess which finding was meant. Nothing has been written to the pull request, and the edits both rejected attempts made have been put back. Re-run the leg, or try the other harness.")
		}
		shapeBudget--
		if shapeBudget > 0 {
			_ = work.RestoreTree(ctx, snapIndex, snapTree)
			continue
		}
		problem := err.Error()
		return refuse(fmt.Sprintf("%s returned an object that does not match the schema — %s", s.settings.Harness, problem),
			"This harness validates output against the schema natively, so a mismatch is an adapter or harness bug rather than model drift. Nothing has been written to the pull request, and the rejected attempt's edits have been put back.")
	}
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
		path := jsonString(f["path"])
		issues := l.Forge.IssueCandidates(ctx, s.repo, path, "")
		if len(issues) == 0 {
			continue
		}
		set := prompt.CandidateSet{FindingID: jsonString(f["id"])}
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

func backfillRoots(findings []map[string]json.RawMessage, threads []forge.ReviewThread) []map[string]json.RawMessage {
	for _, f := range findings {
		if _, ok := f["root_comment_id"]; ok && string(f["root_comment_id"]) != "null" {
			continue
		}
		id := jsonString(f["id"])
		for _, th := range threads {
			for _, fid := range th.FindingIDs {
				if string(fid) == id && th.RootCommentID != 0 {
					f["root_comment_id"] = json.RawMessage(strconv.FormatInt(th.RootCommentID, 10))
				}
			}
		}
	}
	return findings
}

func enrichFindings(findings []map[string]json.RawMessage, markers []prstate.Marker, minFix core.Severity) []map[string]json.RawMessage {
	priors := priorResolutions(markers)
	for i, f := range findings {
		n := i + 1
		f["number"] = json.RawMessage(strconv.Itoa(n))
		sev := core.Severity(jsonString(f["severity"]))
		pre := jsonString(f["pre_existing"]) == "true"
		may := policy.ShouldFix(sev, minFix, pre)
		if may {
			f["may_fix"] = json.RawMessage("true")
		} else {
			f["may_fix"] = json.RawMessage("false")
		}
		if prior, ok := priors[jsonString(f["id"])]; ok {
			b, _ := json.Marshal(prior)
			f["prior_resolution"] = b
		} else {
			f["prior_resolution"] = json.RawMessage("null")
		}
		findings[i] = f
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

func toPromptFindings(in []map[string]json.RawMessage) ([]prompt.Finding, error) {
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
