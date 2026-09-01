package resolve

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/harness"
	"github.com/carlosboeing/crossrev/internal/prstate"
	"github.com/carlosboeing/crossrev/internal/sandbox"
	"github.com/carlosboeing/crossrev/internal/vcs"
)

func (l *Leg) commitAndPush(ctx context.Context, s *session, workdir string, recs, findings []harness.Node, marker prstate.Marker, wrote bool, remote string) (sha string, messages []string, emptyRemote bool, err error) {
	existing, _ := marker.CommitSHA.Get()
	if existing != "" && existing != "null" {
		return existing, nil, false, nil
	}

	fixed := 0
	for _, d := range recs {
		if d.Member("resolution").StringVal() == "fixed" {
			fixed++
		}
	}
	if fixed == 0 && !wrote {
		return "", nil, false, nil
	}

	if sbx, loadErr := sandbox.LoadDescriptor(harness.DescriptorJSON()); loadErr == nil {
		if _, warn, restoreErr := sandbox.Restore(workdir, sbx.Paths()); restoreErr != nil {
			return "", nil, false, restoreErr
		} else if warn != nil {
			messages = append(messages, warn.Message)
		}
	}

	work := l.Git.WithDir(workdir)
	work.StageAll(ctx)
	staged, stageErr := work.HasStagedChanges(ctx)
	if stageErr != nil {
		return "", messages, false, stageErr
	}
	if !staged {
		if fixed > 0 {
			messages = append(messages, fmt.Sprintf("the resolver reported %d fix(es) but changed no files\n   The replies below will claim a fix that is not in the diff, so their threads stay open and the pass halts for a person. Treat those resolutions as unverified and read the thread before merging.", fixed))
		}
		return "", messages, false, nil
	}

	target, targetErr := l.Git.ResolvePushRepo(ctx, remote)
	if targetErr != nil {
		return "", messages, false, targetErr
	}
	for _, w := range target.Warnings {
		messages = append(messages, w.Message)
	}
	headRepo := s.repo
	if s.pr.IsCrossRepository && s.pr.HeadRepositoryOwner != "" && s.pr.HeadRepository != "" {
		if slug, slugErr := core.NewSlug(s.pr.HeadRepositoryOwner, s.pr.HeadRepository); slugErr == nil {
			headRepo = slug
		}
	}
	if target.Repo != headRepo {
		return "", messages, false, &Refusal{
			Message: fmt.Sprintf("remote '%s' pushes to '%s', but the head repository of this pull request is '%s'",
				remote, target.Slug(), headRepo),
			Hint: fmt.Sprintf("CrossRev pushes only to the head repository of the pull request under review. The resolver's changes are still in the working tree and nothing was pushed. Check `git config --get-all remote.%s.pushurl`.", remote),
		}
	}

	msg, commitWarning := l.commitMessage(s, recs, findings, marker, fixed)
	if commitWarning != "" {
		messages = append(messages, commitWarning)
	}
	runHooks := s.gitHooksRun()
	if l.Log != nil {
		l.Log.Event("commit", "start branch="+s.pr.HeadRefName)
	}
	opts := vcs.CommitOptions{
		Message:  msg,
		Name:     firstNonEmpty(lookupEnv(l.Env, "CROSSREV_GIT_NAME"), os.Getenv("CROSSREV_GIT_NAME")),
		Email:    firstNonEmpty(lookupEnv(l.Env, "CROSSREV_GIT_EMAIL"), os.Getenv("CROSSREV_GIT_EMAIL")),
		RunHooks: runHooks,
	}
	if err := work.Commit(ctx, opts); err != nil {
		if l.Log != nil {
			l.Log.Event("commit", "failed: "+err.Error())
		}
		return "", messages, false, err
	}
	if l.Log != nil {
		l.Log.Event("commit", "exit=0")
	}

	sha, emptyRemote, err = l.pushHead(ctx, work, s, remote, runHooks)
	return sha, messages, emptyRemote, err
}

func (l *Leg) commitMessage(s *session, recs, findings []harness.Node, marker prstate.Marker, fixed int) (string, string) {
	resolutions := marshalResolutions(recs)
	findingsRaw, _ := json.Marshal(findings)
	sha, _ := marker.HeadSHA.Get()
	if fixed > 0 {
		subject, _ := marker.CommitSubject.Get()
		raw, _ := marker.MarshalJSON()
		if !CommitSubjectOK(subject, string(raw)) {
			commitWarning := ""
			if subject != "" && subject != "null" {
				commitWarning = warning(
					"the resolver's commit subject was rejected, so the commit carries a generic one",
					"A subject must be one line of at most 100 characters, with no control characters. The fix itself is unaffected.",
				)
			}
			subject = fmt.Sprintf("fix: resolve crossrev review findings (pass %d)", s.pass)
			return subject + "\n\n" + CommitBody(resolutions, findingsRaw, "fixed", sha, s.pass, s.repo.String(), s.req.PR), commitWarning
		}
		return subject + "\n\n" + CommitBody(resolutions, findingsRaw, "fixed", sha, s.pass, s.repo.String(), s.req.PR), ""
	}
	return fmt.Sprintf("chore: record deferred crossrev findings (pass %d)\n\n%s",
		s.pass, CommitBody(resolutions, findingsRaw, "deferred", sha, s.pass, s.repo.String(), s.req.PR)), ""
}

func (l *Leg) pushHead(ctx context.Context, work Git, s *session, remote string, runHooks bool) (string, bool, error) {
	url, err := work.PushURL(ctx, remote)
	if err != nil {
		return "", false, err
	}
	remoteHead, err := work.RemoteHead(ctx, url, s.pr.HeadRefName)
	if err != nil {
		remoteHead = ""
	}
	empty := remoteHead == ""
	if !empty {
		want := s.pr.HeadRefOid.SHA()
		if remoteHead != want {
			shortGot, shortWant := remoteHead, want
			if len(shortGot) > 7 {
				shortGot = shortGot[:7]
			}
			if len(shortWant) > 7 {
				shortWant = shortWant[:7]
			}
			return "", empty, &Refusal{
				Message: fmt.Sprintf("%s moved while this leg was running — it is now at %s, not %s",
					s.pr.HeadRefName, shortGot, shortWant),
				Hint: "Someone else pushed. The fix is committed locally and not pushed; rebase onto the new head and re-run: crossrev resolve --pr <n>",
			}
		}
	}
	if l.Log != nil {
		l.Log.Event("push", "start branch="+s.pr.HeadRefName+" remote="+remote)
	}
	if err := work.Push(ctx, remote, s.pr.HeadRefName, runHooks); err != nil {
		if l.Log != nil {
			l.Log.Event("push", "failed: "+err.Error())
		}
		return "", empty, err
	}
	if l.Log != nil {
		l.Log.Event("push", "exit=0")
	}
	head, err := work.Head(ctx)
	if err != nil {
		return "", empty, err
	}
	return head.SHA(), empty, nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
