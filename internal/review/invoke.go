package review

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/cred"
	"github.com/carlosboeing/crossrev/internal/exec"
	"github.com/carlosboeing/crossrev/internal/forge"
	"github.com/carlosboeing/crossrev/internal/harness"
	"github.com/carlosboeing/crossrev/internal/prompt"
	"github.com/carlosboeing/crossrev/internal/sandbox"
	"github.com/carlosboeing/crossrev/internal/ui"
	"github.com/carlosboeing/crossrev/internal/validate"
)

type legSettings struct {
	harness  string
	model    string
	effort   string
	endpoint string
}

func (l *Leg) settings(req Request, loaded Context) (legSettings, error) {
	cfg := loaded.Config
	s := legSettings{
		harness:  cfg.Get(".reviewer.harness"),
		model:    cfg.Get(".reviewer.model"),
		effort:   cfg.Get(".reviewer.effort"),
		endpoint: cfg.Get(".reviewer.endpoint"),
	}
	if req.HarnessOverride != "" {
		s.harness = req.HarnessOverride
		s.model = ""
		s.endpoint = ""
	}
	if s.harness == "" {
		s.harness = string(core.HarnessCodex)
	}
	if !l.Harness.Known(s.harness) {
		return s, &ui.FatalError{
			Reason: fmt.Sprintf("there is no adapter for the harness '%s'", s.harness),
			Action: "CrossRev drives the named harnesses directly.",
		}
	}
	if !l.Harness.ServesLeg(s.harness, string(core.LegReview)) {
		return s, &ui.FatalError{
			Reason: fmt.Sprintf("the harness '%s' cannot serve the review leg", s.harness),
			Action: "Point reviewer.harness at a harness that serves review.",
		}
	}
	return s, nil
}

func (l *Leg) invoke(ctx context.Context, req Request, loaded Context, settings legSettings, pass int) (harness.Envelope, json.RawMessage, error) {
	if err := harness.AssertEnvClean(l.Env); err != nil {
		return harness.Envelope{}, nil, err
	}

	adapter, known := harness.For(l.Harness, settings.harness)
	if !known {
		return harness.Envelope{}, nil, &ui.FatalError{
			Reason: fmt.Sprintf("there is no adapter for the harness '%s'", settings.harness),
			Action: "CrossRev drives the named harnesses directly.",
		}
	}

	entry, _ := l.Harness.For(settings.harness)
	staged, err := cred.Prepare(l.Harness.Credentials().For(settings.harness), settings.endpoint, cred.Options{Now: l.Now})
	if err != nil {
		return harness.Envelope{}, nil, err
	}
	defer func() { _ = cred.Discard(staged) }()

	tmp, err := os.MkdirTemp("", "crossrev-review-")
	if err != nil {
		return harness.Envelope{}, nil, err
	}
	defer os.RemoveAll(tmp)

	diffBytes, err := l.Forge.PullRequestDiff(ctx, loaded.Repo, loaded.PR.BaseRefOid, loaded.PR.HeadRefOid)
	if err != nil {
		return harness.Envelope{}, nil, err
	}

	promptBytes := prompt.Review{
		Skill:    prompt.ReviewSkill(),
		Diff:     diffBytes,
		Meta:     reviewMeta(loaded, req, pass),
		Prior:    priorFindings(loaded),
		Threads:  promptThreads(l.Forge.ReviewThreads(ctx, loaded.Repo, req.PR)),
		ReviewMD: loaded.ReviewMD,
	}.Render()

	promptPath := filepath.Join(tmp, "prompt")
	schemaPath := filepath.Join(tmp, "schema.json")
	if err := os.WriteFile(promptPath, promptBytes, 0o600); err != nil {
		return harness.Envelope{}, nil, err
	}
	schemaBytes := validate.FindingsSchema()
	if err := os.WriteFile(schemaPath, schemaBytes, 0o600); err != nil {
		return harness.Envelope{}, nil, err
	}

	workdir := req.Workdir
	if workdir == "" {
		workdir, _ = os.Getwd()
	}
	desc, err := sandbox.LoadDescriptor(l.Harness.Raw())
	if err != nil {
		return harness.Envelope{}, nil, err
	}
	paths := desc.Paths()
	moved, err := sandbox.Quarantine(workdir, paths)
	if err != nil {
		return harness.Envelope{}, nil, err
	}
	defer func() { _, _, _ = sandbox.Restore(workdir, moved) }()

	inv := harness.Invocation{
		Prompt:  harness.File{Path: promptPath, Text: string(promptBytes)},
		Schema:  harness.File{Path: schemaPath, Text: string(schemaBytes)},
		Workdir: workdir,
		Model:   settings.model,
		Effort:  settings.effort,
		Write:   false,
		Env:     l.Env,
		Scratch: tmp,
	}

	shapeBudget := 1
	if !entry.SchemaNative {
		shapeBudget = 2
	}
	semanticBudget := 1

	var envelope harness.Envelope
	for attempt := 1; ; attempt++ {
		if l.Log != nil {
			if base, ok := l.Log.TranscriptBase(attempt); ok {
				inv.PayloadPath = base + ".payload"
			}
			l.Log.Event("invoke", fmt.Sprintf("harness=%s attempt=%d start", settings.harness, attempt))
		}
		spec, err := adapter.Spec(inv)
		if err != nil {
			return harness.Envelope{}, nil, err
		}
		res := l.runner().Run(ctx, spec)
		if l.Log != nil {
			l.Log.Event("invoke", fmt.Sprintf("harness=%s attempt=%d exit=%d", settings.harness, attempt, res.ExitCode))
		}
		if res.Err != nil && exec.IsNotFound(res.Err) {
			return harness.Envelope{}, nil, adapter.NotInstalled()
		}
		envelope = adapter.Envelope(inv, res)
		if !envelope.OK {
			msg := "no error reported"
			if envelope.Error != nil && *envelope.Error != "" {
				msg = *envelope.Error
			}
			return envelope, nil, &ui.FatalError{
				Reason: fmt.Sprintf("the %s harness failed: %s", settings.harness, msg),
				Action: "If the error above mentions authentication, a token or a 401, the harness is installed and cannot log in.",
			}
		}
		problem := l.checkPayload(envelope.Payload)
		if problem == nil {
			return envelope, envelope.Payload, nil
		}
		code := validateCode(problem)
		if code == 2 {
			if semanticBudget > 0 {
				semanticBudget--
				continue
			}
			return envelope, nil, &ui.FatalError{
				Reason: fmt.Sprintf("%s twice returned an answer that contradicts what it was given — %s", settings.harness, problem),
				Action: "The shape was right both times, so the schema cannot catch this and crossrev will not guess which finding was meant.",
			}
		}
		shapeBudget--
		if shapeBudget > 0 {
			continue
		}
		return envelope, nil, &ui.FatalError{
			Reason: fmt.Sprintf("%s returned an object that does not match the schema — %s", settings.harness, problem),
			Action: "This harness validates output against the schema natively, so a mismatch is an adapter or harness bug rather than model drift.",
		}
	}
}

func reviewMeta(loaded Context, req Request, pass int) prompt.Meta {
	minFix := "medium"
	if loaded.Config != nil {
		if v := loaded.Config.Get(".policy.min_fix_severity"); v != "" {
			minFix = v
		}
	}
	return prompt.Meta{
		Repo:           prompt.Str(loaded.Repo.String()),
		PR:             prompt.Num(req.PR),
		Pass:           prompt.Num(pass),
		HeadSHA:        prompt.Str(loaded.PR.HeadRefOid.SHA()),
		Title:          prompt.Str(loaded.PR.Title),
		Body:           prompt.Str(loaded.PR.Body),
		MinFixSeverity: prompt.Str(minFix),
	}
}

func priorFindings(loaded Context) []prompt.Prior {
	var priors []prompt.Prior
	for _, m := range loaded.Markers {
		if m.Leg != core.LegReview {
			continue
		}
		var findings []prompt.Prior
		if err := m.DecodeFindings(&findings); err != nil {
			continue
		}
		priors = append(priors, findings...)
	}
	return priors
}

func promptThreads(threads []forge.ReviewThread) []prompt.Thread {
	out := make([]prompt.Thread, 0, len(threads))
	for _, t := range threads {
		comments := make([]prompt.Comment, 0, len(t.Comments))
		for _, c := range t.Comments {
			comments = append(comments, prompt.Comment{
				Author: prompt.Login(c.Author),
				Body:   prompt.Str(c.Body),
			})
		}
		out = append(out, prompt.Thread{
			Path:       prompt.Str(t.Path),
			Line:       prompt.Num(t.Line),
			IsResolved: prompt.Bool(t.IsResolved),
			Comments:   comments,
		})
	}
	return out
}
