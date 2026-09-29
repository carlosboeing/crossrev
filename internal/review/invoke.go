package review

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
	"github.com/carlosboeing/crossrev/internal/runlog"
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
	// The canonical reviewer, never the singular key: reading that key
	// directly would accept a plural configuration and then run the singular
	// default sitting underneath it. The resolver also carries the codex
	// default an empty harness used to fall back to here, so a resolved slot
	// always names a harness.
	reviewer := loaded.Config.Reviewers()[0]
	s := legSettings{
		harness:  reviewer.Harness,
		model:    reviewer.Model,
		effort:   reviewer.Effort,
		endpoint: reviewer.Endpoint,
	}
	if req.HarnessOverride != "" {
		s.harness = req.HarnessOverride
		s.model = ""
		s.endpoint = ""
	}
	if req.ModelOverride != "" {
		s.model = req.ModelOverride
	}
	if req.EffortOverride != "" {
		s.effort = req.EffortOverride
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
			// ui_warn, condition and consequence apart (lib/run.sh:548-549).
			warn := ui.Warn(
				fmt.Sprintf("'%s' is not installed, so the reviewer runs on '%s' instead", asked, name),
				fmt.Sprintf("Both legs now run on the same harness, so a bug it misses while reviewing it also misses while resolving. Install %s to get the second lineage back.", asked))
			return s, warn, nil
		}
	}
	return s, ui.Line{}, notInstalledRefusal(l.Harness, asked)
}

// notInstalledRefusal is the last refusal in run_leg_settings
// (lib/run.sh:544-546), reached once the configured harness has no binary and
// the substitution loop at lib/run.sh:537-543 finds no other harness that
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
// function exists for the configured name (lib/run.sh:506-514).
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
// (lib/run.sh:559-564), reached from run_leg_settings at lib/run.sh:526.
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
// at lib/run.sh:509: the first character upper-cased, the rest untouched.
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

func (l *Leg) invoke(ctx context.Context, req Request, loaded Context, settings legSettings, pass int) (envelope harness.Envelope, payload json.RawMessage, msgs []ui.Line, retErr error) {
	if err := harness.AssertEnvClean(l.Env); err != nil {
		return harness.Envelope{}, nil, msgs, err
	}

	adapter, known := harness.For(l.Harness, settings.harness)
	if !known {
		return harness.Envelope{}, nil, msgs, noAdapterRefusal(l.Harness, settings.harness)
	}

	entry, _ := l.Harness.For(settings.harness)
	staged, err := cred.Prepare(l.Harness.Credentials().For(settings.harness), settings.endpoint, cred.Options{Now: l.Now})
	if err != nil {
		return harness.Envelope{}, nil, msgs, err
	}
	defer func() { _ = cred.Discard(staged) }()

	tmp, err := os.MkdirTemp("", "crossrev-review-")
	if err != nil {
		return harness.Envelope{}, nil, msgs, err
	}
	defer os.RemoveAll(tmp)

	diffBytes, err := l.reviewDiff(ctx, loaded)
	if err != nil {
		return harness.Envelope{}, nil, msgs, err
	}

	promptBytes := prompt.Review{
		Skill:    prompt.ReviewSkill(),
		Diff:     diffBytes,
		Meta:     reviewMeta(loaded, req, pass),
		Prior:    priorFindings(loaded),
		Threads:  promptThreads(l.Forge.ReviewThreads(ctx, loaded.Repo, req.PR)),
		ReviewMD: loaded.ReviewMD,
	}.Render()

	start := l.now()
	envelope, payload, outMsgs, err := l.runPrompt(ctx, req, loaded, settings, adapter, entry, staged, tmp, promptBytes, msgs, 1)
	if err == nil {
		l.logAcceptedCall(1, promptBytes, len(diffBytes), envelope, l.now().Sub(start).Milliseconds())
	}
	return envelope, payload, outMsgs, err
}

// copyReadLog archives one call's read-server log into the run directory,
// beside the transcripts WriteTranscript archives just above. The scratch
// copy is removed only after the run-directory write succeeds: a failed
// copy keeps the evidence where the read server left it and records the
// loss in the run log, in the same words WriteTranscript uses for a
// transcript it could not write. A missing or empty source means no read
// server ran for the call, so there is nothing to archive.
func copyReadLog(l *runlog.Log, tmp string, call int) {
	readLog := filepath.Join(tmp, "reads.jsonl")
	b, err := os.ReadFile(readLog)
	if err != nil || len(b) == 0 {
		return
	}
	target := filepath.Join(l.Dir(), fmt.Sprintf("reads.call-%d.jsonl", call))
	f, err := os.OpenFile(target, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		l.Event("transcript", "could not write "+target)
		return
	}
	if _, err := f.Write(b); err != nil {
		_ = f.Close()
		l.Event("transcript", "could not write "+target)
		return
	}
	if err := f.Close(); err != nil {
		l.Event("transcript", "could not write "+target)
		return
	}
	_ = os.Remove(readLog)
}

// runPrompt runs one rendered prompt through the harness child with the
// leg's validation seam: one semantic retry naming the rejected numbers,
// then a fatal refusal that publishes nothing. The deferred sandbox restore
// assigns through the named retErr return, so a restore failure after a
// successful answer still fails the leg the way the frozen path does. call
// is the call's number in the pass, naming its transcripts.
func (l *Leg) runPrompt(ctx context.Context, req Request, loaded Context, settings legSettings, adapter harness.Adapter, entry harness.Descriptor, staged *cred.Staged, tmp string, promptBytes []byte, msgs []ui.Line, call int) (envelope harness.Envelope, payload json.RawMessage, outMsgs []ui.Line, retErr error) {
	outMsgs = msgs

	promptPath := filepath.Join(tmp, "prompt")
	schemaPath := filepath.Join(tmp, "schema.json")
	if err := os.WriteFile(promptPath, promptBytes, 0o600); err != nil {
		return harness.Envelope{}, nil, outMsgs, err
	}
	schemaBytes := validate.FindingsSchema()
	if err := os.WriteFile(schemaPath, schemaBytes, 0o600); err != nil {
		return harness.Envelope{}, nil, outMsgs, err
	}

	// The harness runs in the pinned worktree Run prepared, which an
	// explicit req.Workdir overrides. Only a leg without a git reader
	// reaches the fallback: its frozen path has no worktree to pin, so the
	// harness runs where the operator stands. Quarantine below moves files
	// in this directory, never in the operator checkout.
	workdir := req.Workdir
	if workdir == "" {
		workdir, _ = os.Getwd()
	}
	desc, err := sandbox.LoadDescriptor(l.Harness.Raw())
	if err != nil {
		return harness.Envelope{}, nil, outMsgs, err
	}
	paths := desc.Paths()
	moved, err := sandbox.Quarantine(workdir, paths)
	if err != nil {
		return harness.Envelope{}, nil, outMsgs, err
	}
	defer func() {
		// The causal error stays in the message. Overwriting retErr outright
		// lost why the harness never answered, which is the half a reader acts
		// on; Bash keeps both (lib/run.sh:690, called from :881 and :893).
		if _, warn, err := sandbox.Restore(workdir, moved); err != nil {
			cause := "the attempt finished and its answer was not read"
			if retErr != nil {
				cause = retErr.Error()
			}
			retErr = newSandboxRestoreFailure(settings.harness, cause, err.Error())
		} else if warn != nil {
			// ui_warn: sandbox.Restore answers both halves (lib/sandbox.sh).
			outMsgs = append(outMsgs, ui.Warn(warn.Message, warn.Hint))
		}
	}()

	// The endpoint is resolved here rather than in the adapter, because an
	// adapter reads no configuration. The Bash adapter calls cfg_endpoint
	// itself (lib/adapters/claude.sh:78-92), and an unresolved name stops the
	// leg: falling back to the vendor would run Claude while the config says
	// Ollama, which is the silent substitution the divergence guard exists to
	// catch arriving through a different door (lib/config.sh:394-399).
	endpoint, err := l.endpoint(loaded, settings)
	if err != nil {
		return harness.Envelope{}, nil, outMsgs, err
	}

	inv := harness.Invocation{
		Prompt:   harness.File{Path: promptPath, Text: string(promptBytes)},
		Schema:   harness.File{Path: schemaPath, Text: string(schemaBytes)},
		Workdir:  workdir,
		Model:    settings.model,
		Effort:   settings.effort,
		Endpoint: endpoint,
		Write:    false,
		// staged, not l.Env: the allowlist was read before Prepare staged
		// anything, so the staging variable reaches the child only from here.
		Env:     staged.Apply(l.Env),
		Scratch: tmp,
	}

	// The version gate, before anything starts: an adapter that pins its CLI
	// version refuses an install it does not drive rather than run a leg on it.
	if refusal := harness.CheckVersion(ctx, l.runner(), adapter, inv); refusal != nil {
		return harness.Envelope{}, nil, outMsgs, refusal
	}

	shapeBudget := 1
	if !entry.SchemaNative {
		shapeBudget = 2
	}
	semanticBudget := 1
	// transientBudget is the one more attempt a server-side or transport
	// failure earns: the harness failed before answering rather than
	// answering badly. Authentication, quota and refusal errors never draw
	// from it.
	transientBudget := 1
	// refused sums the usage buckets of the attempts this prompt turned
	// away. A refused answer judged nothing, but its call was spent, and
	// the marker reports what the pass spent.
	var refused *harness.Usage

	for attempt := 1; ; attempt++ {
		transcript := ""
		if l.Log != nil {
			if base, ok := l.Log.TranscriptBaseForCall(call, attempt); ok {
				transcript = base
				inv.PayloadPath = base + ".payload"
			}
			l.Log.Event("invoke", fmt.Sprintf("harness=%s attempt=%d start", settings.harness, attempt))
		}
		spec, err := adapter.Spec(inv)
		if err != nil {
			return harness.Envelope{}, nil, outMsgs, err
		}
		started := l.now()
		res := l.runner().Run(ctx, spec)
		if l.Log != nil {
			// duration in whole seconds, which is what `$SECONDS` counts
			// (lib/run.sh:831, :831).
			l.Log.Event("invoke", fmt.Sprintf("harness=%s attempt=%d exit=%d duration=%ds",
				settings.harness, attempt, res.ExitCode, int(l.now().Sub(started).Seconds())))
		}
		if res.Err != nil && exec.IsNotFound(res.Err) {
			return harness.Envelope{}, nil, outMsgs, adapter.NotInstalled()
		}
		envelope := adapter.Envelope(inv, res)
		// The two streams are archived AFTER the envelope has been parsed out
		// of them, then filtered in place. Filtering first would rewrite the
		// model's own answer, so identical harness output would produce
		// different findings depending on whether a run directory exists
		// (lib/adapters/claude.sh:126-130, :148-154).
		l.Log.WriteTranscript(transcript, res.Stdout, res.Stderr)
		if l.Log != nil && l.Log.Dir() != "" {
			copyReadLog(l.Log, tmp, call)
		}
		if res.Interrupted() {
			// A signal death is an interrupt, not a harness failure: the
			// child was killed rather than answering badly. The refusal
			// carries the interrupt the terminal prints, joined with
			// context.Canceled — the identity the exit mapping and the
			// fatal-report skip already read — so the leg exits 130 and
			// leaves the claim resumable instead of printing the
			// harness-failure message. Bare context.Canceled would reach
			// the terminal as a plain error with the doctor hint.
			return envelope, nil, outMsgs, errors.Join(&ui.FatalError{
				Reason: fmt.Sprintf("the %s harness was interrupted", settings.harness),
				Action: "The harness did not answer. Re-run the leg.",
			}, context.Canceled)
		}
		if !envelope.OK {
			msg := "no error reported"
			if envelope.Error != nil && *envelope.Error != "" {
				msg = *envelope.Error
			}
			if transientBudget > 0 && harness.IsTransientHarnessError(msg) {
				transientBudget--
				refused = foldAttempt(refused, envelope.Usage)
				// ui_warn, the pair kept apart. A failure before any answer
				// is a server-side or transport failure worth asking once
				// more about, never model drift.
				outMsgs = append(outMsgs, ui.Warn(
					fmt.Sprintf("%s hit a transient harness failure — %s", settings.harness, msg),
					"The harness failed before answering rather than answering badly, so this looks like a server-side or transport failure. It is being asked once more; a second failure is fatal."))
				continue
			}
			return envelope, nil, outMsgs, &ui.FatalError{
				Reason: fmt.Sprintf("the %s harness failed: %s", settings.harness, msg),
				Action: "If the error above mentions authentication, a token or a 401, the harness is installed and cannot log in.",
			}
		}
		// The second child, for the one adapter whose telemetry is not in its
		// own output (lib/adapters/opencode.sh:261-273). Telemetry, not the
		// answer: an export that will not build or will not run leaves the
		// fields unset and the leg stands.
		l.mergeExport(ctx, adapter, inv, res, &envelope)

		// A SUCCESS that answered nothing is a harness failure, not clean
		// coverage: empty output never means the code was examined. A
		// harness that constrains its own output failing to produce any is
		// worth asking once more about before the shape check below refuses
		// it, which is where the second empty answer still lands — the
		// refusal keeps its existing words. A harness without a native
		// schema already retries a shape miss below, so spending the
		// transient budget there too would ask a third time.
		if len(bytes.TrimSpace(envelope.Payload)) == 0 && entry.SchemaNative && transientBudget > 0 {
			transientBudget--
			refused = foldAttempt(refused, envelope.Usage)
			outMsgs = append(outMsgs, ui.Warn(
				fmt.Sprintf("%s answered successfully with an empty payload — empty output is never clean coverage", settings.harness),
				"The harness constrains its own output and still answered nothing, so this looks like a harness failure rather than model drift. It is being asked once more; a second empty answer is fatal."))
			continue
		}

		problem := l.checkPayload(envelope.Payload)
		if problem == nil {
			foldRefusedAttempts(&envelope, refused)
			return envelope, envelope.Payload, outMsgs, nil
		}
		code := validateCode(problem)
		if code == 2 {
			if semanticBudget > 0 {
				semanticBudget--
				refused = foldAttempt(refused, envelope.Usage)
				// ui_warn, the pair kept apart (lib/run.sh:888-889).
				outMsgs = append(outMsgs, ui.Warn(
					fmt.Sprintf("%s returned an answer that contradicts what it was given — %s", settings.harness, problem),
					"The shape is right, so this is the model drifting rather than a bug in CrossRev or the harness. Anything it edited has been put back, and it is being asked once more; a second one is fatal."))
				continue
			}
			return envelope, nil, outMsgs, &ui.FatalError{
				Reason: fmt.Sprintf("%s twice returned an answer that contradicts what it was given — %s", settings.harness, problem),
				Action: "The shape was right both times, so the schema cannot catch this and CrossRev will not guess which finding was meant. Nothing has been written to the pull request, and the edits both rejected attempts made have been put back. Re-run the leg, or try the other harness.",
			}
		}
		shapeBudget--
		if shapeBudget > 0 {
			refused = foldAttempt(refused, envelope.Usage)
			// ui_warn (lib/run.sh:900-901). Only a harness that does not
			// constrain its own output ever reaches here, because a native one
			// starts with a budget of 1.
			outMsgs = append(outMsgs, ui.Warn(
				fmt.Sprintf("%s returned an object that does not match the schema — %s", settings.harness, problem),
				"That harness does not constrain its own output, so this is the expected failure rather than a bug. Anything it edited has been put back, and it is being retried once; a second mismatch is fatal."))
			continue
		}
		// Two endings, and the difference is whose bug it is
		// (lib/run.sh:905-911). Printing the native-schema one for a harness
		// that has no native schema sends the reader to the adapter over a
		// model that simply did not follow the instruction.
		return envelope, nil, outMsgs, &ui.FatalError{
			Reason: fmt.Sprintf("%s returned an object that does not match the schema — %s", settings.harness, problem),
			Action: shapeExhaustedAction(entry.SchemaNative),
		}
	}
}

// foldAttempt joins one refused attempt's usage buckets into the running
// sum, answering the sum to keep. A nil record contributes nothing.
func foldAttempt(sum, attempt *harness.Usage) *harness.Usage {
	if attempt == nil {
		return sum
	}
	if sum == nil {
		fresh := *attempt
		return &fresh
	}
	addUsageBuckets(sum, attempt)
	return sum
}

// foldRefusedAttempts carries the refused attempts' buckets into the
// accepted envelope, so one prompt's envelope reports every call it
// cost. Identity stays on the accepted attempt: model, effort and the
// non-bucket usage fields are untouched.
func foldRefusedAttempts(envelope *harness.Envelope, refused *harness.Usage) {
	if refused == nil {
		return
	}
	if envelope.Usage == nil {
		total := refused.WithTotal()
		envelope.Usage = &total
		envelope.Tokens = total.Total
		return
	}
	sum := *envelope.Usage
	addUsageBuckets(&sum, refused)
	total := sum.WithTotal()
	envelope.Usage = &total
	envelope.Tokens = total.Total
}

// shapeExhaustedAction is lib/run.sh:907 and :904.
func shapeExhaustedAction(schemaNative bool) string {
	if schemaNative {
		return "This harness validates output against the schema natively, so a mismatch is an adapter or harness bug rather than model drift. Nothing has been written to the pull request, and the rejected attempt's edits have been put back."
	}
	return "That harness does not constrain its own output, so two mismatches in a row is the model failing the JSON instruction rather than an adapter bug. Name a model that follows a JSON instruction. Nothing has been written to the pull request, and the rejected attempt's edits have been put back."
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
		// A finding the pass recorded without posting never reached the
		// pull request, so the resolve leg never saw it and no resolution
		// exists for it. It still reaches the next review, marked
		// not_posted, so the reviewer can tell a held finding from a
		// settled one — and re-raise it if the code now warrants more.
		for i := range findings {
			if findings[i].Posted.IsFalse() {
				findings[i].Resolution = prompt.Str("not_posted")
			}
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
// (lib/run.sh:1073). `${model:+, $model}` and `${effort:+, $effort effort}`
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

// mergeExport runs an adapter's export child and folds its answer in, for an
// adapter that has one (lib/adapters/opencode.sh:261-273).
func (l *Leg) mergeExport(ctx context.Context, adapter harness.Adapter, inv harness.Invocation, res exec.Result, envelope *harness.Envelope) {
	exporter, ok := adapter.(harness.Exporter)
	if !ok || !envelope.OK {
		return
	}
	session := exporter.SessionID(res)
	if session == "" {
		return
	}
	spec, err := exporter.ExportSpec(inv, session)
	if err != nil {
		return
	}
	exported := l.runner().Run(ctx, spec)
	if exported.Err != nil || exported.ExitCode != 0 {
		return
	}
	exporter.MergeExport(envelope, exported.Stdout)
}

// endpoint resolves the configured endpoint name against the config, the way
// cfg_endpoint does for the Bash adapter (lib/config.sh:400-412).
//
// An unset name is not an endpoint at all: cfg_endpoint returns 1 without a
// message for it (lib/config.sh:402), which is the vendor's own API.
func (l *Leg) endpoint(loaded Context, settings legSettings) (harness.Endpoint, error) {
	name := settings.endpoint
	if name == "" || name == "null" || loaded.Config == nil {
		return harness.Endpoint{}, nil
	}
	resolved, err := loaded.Config.Endpoint(name)
	if err != nil {
		return harness.Endpoint{}, err
	}
	return harness.Endpoint{
		Name:     resolved.Name,
		URL:      resolved.BaseURL,
		TokenVar: resolved.TokenEnv,
		Token:    envValue(l.Env, resolved.TokenEnv),
	}, nil
}

// envValue reads one name out of the allowlist the leg hands a child.
func envValue(env []string, name string) string {
	prefix := name + "="
	for _, e := range env {
		if strings.HasPrefix(e, prefix) {
			return e[len(prefix):]
		}
	}
	return ""
}
