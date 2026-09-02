package review

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/carlosboeing/crossrev/internal/config"
	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/cred"
	"github.com/carlosboeing/crossrev/internal/diff"
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

func (l *Leg) settings(req Request, loaded Context) (legSettings, ui.Line, error) {
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
		return s, ui.Line{}, noAdapterRefusal(l.Harness, s.harness)
	}
	if !l.Harness.ServesLeg(s.harness, string(core.LegReview)) {
		return s, ui.Line{}, servesLegRefusal(l.Harness, s.harness)
	}

	asked := s.harness
	if l.binaryInstalled(asked) {
		return s, ui.Line{}, nil
	}
	for _, name := range l.Harness.NamesForLeg(string(core.LegReview)) {
		if l.binaryInstalled(name) {
			s.harness = name
			s.model = ""
			s.endpoint = ""
			// ui_warn, condition and consequence apart (lib/run.sh:542-543).
			warn := ui.Warn(
				fmt.Sprintf("'%s' is not installed, so the reviewer runs on '%s' instead", asked, name),
				fmt.Sprintf("Both legs now run on the same harness, so a bug it misses while reviewing it also misses while resolving. Install %s to get the second lineage back.", asked))
			return s, warn, nil
		}
	}
	return s, ui.Line{}, notInstalledRefusal(l.Harness, asked)
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
// and with codex, agy and grok rewritten to legs ["resolve"]:
//
//	Install one of claude and opencode. CrossRev needs at least one, and two different ones is what makes the cross-model check mean anything.
func notInstalledRefusal(doc harness.Document, asked string) *ui.FatalError {
	leg := string(core.LegReview)
	return &ui.FatalError{
		Reason: fmt.Sprintf("the reviewer is configured to use '%s', which is not installed, and no other harness that can serve the %s leg is either", asked, leg),
		Action: fmt.Sprintf("Install one of %s. CrossRev needs at least one, and two different ones is what makes the cross-model check mean anything.",
			harness.NamesHuman(doc.NamesForLeg(leg))),
	}
}

// noAdapterRefusal is the refusal run_leg_settings prints when no adapter
// function exists for the configured name (lib/run.sh:500-508).
//
// The hint names the harnesses CrossRev drives, read off the descriptor rather
// than written into the sentence. A name the descriptor lists under not_driven
// gets a second half carrying the reason it has no adapter and the key that
// would work instead — and the leg word there is the CONFIG key (reviewer),
// not the descriptor's review/resolve vocabulary. Measured:
//
//	reviewer kimi   -> CrossRev drives claude, codex, agy, grok and opencode directly. Kimi is reached through the claude adapter as a named endpoint, so there is no adapter_kimi behind the name: define it under endpoints: and set reviewer.endpoint, not reviewer.harness.
//	reviewer nosuch -> CrossRev drives claude, codex, agy, grok and opencode directly.
//
// The resolve leg builds the same two sentences from the same descriptor reads.
// Sharing one function would mean one tier-3 package importing another, which
// internal/archtest refuses, so each leg carries its own copy against this
// citation.
func noAdapterRefusal(doc harness.Document, name string) *ui.FatalError {
	action := fmt.Sprintf("CrossRev drives %s directly.", doc.NamesHuman())
	if reason, notDriven := doc.NotDrivenReason(name); notDriven {
		action += fmt.Sprintf(" %s is %s: define it under endpoints: and set reviewer.endpoint, not reviewer.harness.",
			capitaliseName(name), reason)
	}
	return &ui.FatalError{
		Reason: fmt.Sprintf("there is no adapter for the harness '%s'", name),
		Action: action,
	}
}

// servesLegRefusal is _run_assert_harness_serves_leg for the review leg
// (lib/run.sh:553-558), reached from run_leg_settings at lib/run.sh:520.
//
// The message is the product: it names the harness, the leg, the harnesses that
// can take the leg, and the legs the refused harness actually serves. Measured
// with grok rewritten to legs ["resolve"]:
//
//	the harness 'grok' cannot serve the review leg
//	CrossRev runs the review leg on claude, codex, agy and opencode. Grok is limited to the resolve leg.
//
// The leg list is `harness_get "$harness" '.legs // [] | join(", ")'`, whose
// default is the EMPTY array rather than the review-resolve pair
// harness_serves_leg defaults to. The difference cannot show: an entry that
// declares no legs serves both and never reaches this line, and the validator
// refuses a legs field that is not a non-empty array drawn from review and
// resolve (lib/harnesses.sh:66-70). So a refused entry has declared exactly one
// leg, and Descriptor.Legs is its declared list.
func servesLegRefusal(doc harness.Document, name string) *ui.FatalError {
	leg := string(core.LegReview)
	entry, _ := doc.For(name)
	return &ui.FatalError{
		Reason: fmt.Sprintf("the harness '%s' cannot serve the %s leg", name, leg),
		Action: fmt.Sprintf("CrossRev runs the %s leg on %s. %s is limited to the %s leg.",
			leg,
			harness.NamesHuman(doc.NamesForLeg(leg)),
			entry.ProductName,
			strings.Join(entry.Legs(), ", ")),
	}
}

// capitaliseName is the Bash
// `$(printf '%s' "${LEG_HARNESS:0:1}" | tr '[:lower:]' '[:upper:]')${LEG_HARNESS:1}`
// at lib/run.sh:503: the first character upper-cased, the rest untouched.
func capitaliseName(name string) string {
	runes := []rune(name)
	if len(runes) == 0 {
		return ""
	}
	return strings.ToUpper(string(runes[0])) + string(runes[1:])
}

func (l *Leg) binaryInstalled(name string) bool {
	entry, ok := l.Harness.For(name)
	binary := name
	if ok && entry.Binary != "" {
		binary = entry.Binary
	}
	look := l.LookPath
	if look == nil {
		look = exec.LookPath
	}
	_, err := look(binary)
	return err == nil
}

func (l *Leg) invoke(ctx context.Context, req Request, loaded Context, settings legSettings, pass int) (envelope harness.Envelope, payload json.RawMessage, retErr error) {
	if err := harness.AssertEnvClean(l.Env); err != nil {
		return harness.Envelope{}, nil, err
	}

	adapter, known := harness.For(l.Harness, settings.harness)
	if !known {
		return harness.Envelope{}, nil, noAdapterRefusal(l.Harness, settings.harness)
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

	diffBytes, err := l.reviewDiff(ctx, loaded)
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
	defer func() {
		// The causal error stays in the message. Overwriting retErr outright
		// lost why the harness never answered, which is the half a reader acts
		// on; Bash keeps both (lib/run.sh:684, called from :881 and :893).
		if _, _, err := sandbox.Restore(workdir, moved); err != nil {
			cause := "the attempt finished and its answer was not read"
			if retErr != nil {
				cause = retErr.Error()
			}
			retErr = newSandboxRestoreFailure(settings.harness, cause, err.Error())
		}
	}()

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
				Action: "The shape was right both times, so the schema cannot catch this and CrossRev will not guess which finding was meant.",
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

func (l *Leg) reviewDiff(ctx context.Context, loaded Context) ([]byte, error) {
	raw, err := l.Forge.PullRequestDiff(ctx, loaded.Repo, loaded.PR.BaseRefOid, loaded.PR.HeadRefOid)
	if err != nil {
		return nil, err
	}
	if loaded.Backlog.Destination != config.DestinationRepository {
		return raw, nil
	}
	return diff.Parse(raw, core.RevisionPair{}).Excluded([]string{loaded.Backlog.Path, ".crossrev"}), nil
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

// describe is the harness half of the run header's Reviewer line
// (lib/run.sh:1067). `${model:+, $model}` and `${effort:+, $effort effort}`
// expand to nothing when unset, so an empty half is omitted rather than
// printed as a trailing comma.
func (s legSettings) describe() string {
	out := s.harness
	if s.model != "" {
		out += ", " + s.model
	}
	if s.effort != "" {
		out += ", " + s.effort + " effort"
	}
	return out
}
