// reads_leg.go — the review leg's served-reads health, tripwire and ledger.
//
// A review leg declares the descriptor's read mode and runs the effective
// one: file_tool resolves to supplied (unwired until slice 9), served runs
// the served tool, supplied runs on the prompt alone. Served calls start
// with a self-test before any harness child — initialize, tools/list, one
// read byte-checked against bytes the leg already holds — and end with the
// post-call checks: the handshake (initialize plus tools_list in the server
// log), refusal matching, and the review tripwire over the harness's own
// event stream.
//
// A failed self-test, a missing handshake or refused calls degrade visibly
// with the reason in the pass comment, the ledger and the run log, or halt
// the leg where `.policy.on_reads_unavailable` says halt. Any command event
// is not a degradation: it halts with review_leg_ran_command, discards the
// call unpublished, and redacts the command into the run log only.

package review

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/harness"
	"github.com/carlosboeing/crossrev/internal/prompt"
	"github.com/carlosboeing/crossrev/internal/prstate"
	"github.com/carlosboeing/crossrev/internal/readserve"
	"github.com/carlosboeing/crossrev/internal/ui"
)

// errNoProbeFile is a probe selection that found no small readable file:
// an empty required set with an empty file listing. The self-test is
// skipped rather than failed — there is nothing to byte-check against —
// and the post-call handshake and refusal checks still guard the call.
var errNoProbeFile = errors.New("no small readable file to byte-check")

// readsNote is one call's reads health, recorded for the marker and
// generation envelopes finalized later in the pass. Calls run sequentially,
// so the slice needs no mutex.
type readsNote struct {
	declared  harness.ReadMode
	effective harness.ReadMode
	reason    string
	stats     prstate.ReadsStats
	calls     []prstate.ReadsCall
}

// maxProbeBytes caps the self-test probe: the check is line-by-line
// containment, so a whole large file buys nothing over its head.
const maxProbeBytes = 4096

// probeLineCount counts the lines the served read would render: the server
// cuts a result at DefaultMaxResultLines, so a candidate past that cap
// comes back with a cut marker instead of its last lines and the byte-check
// would fail on a healthy tool. Selection skips such a candidate for the
// next one rather than failing the self-test on what the path is.
func probeLineCount(body []byte) int {
	if len(body) == 0 {
		return 0
	}
	lines := bytes.Count(body, []byte{'\n'})
	if !bytes.HasSuffix(body, []byte{'\n'}) {
		lines++
	}
	return lines
}

// readsPolicy answers the degradation policy for this pass, defaulting to
// degrade. A nil config — fixtures, and any caller that never loaded one —
// degrades rather than refusing to run.
func readsPolicy(loaded Context) string {
	if loaded.Config != nil {
		return loaded.Config.OnReadsUnavailable()
	}
	return "degrade"
}

// serveSession is the read-server invocation for one call: the crossrev
// binary re-executed over stdio, reading between the pass's own revisions
// and appending this call's log beside the prompt and schema files.
func serveSession(workdir, tmp string, base, head core.Revision, call int) (command string, session readserve.Session, err error) {
	exe, err := os.Executable()
	if err != nil {
		return "", readserve.Session{}, fmt.Errorf("reading the crossrev binary path: %w", err)
	}
	return exe, readserve.Session{
		Repo:    workdir,
		Base:    base.SHA(),
		Head:    head.SHA(),
		LogPath: filepath.Join(tmp, "reads.jsonl"),
		Call:    strconv.Itoa(call),
	}, nil
}

// selectProbeCandidates lists the self-test reads in try order: a small
// available unit the leg already holds evidence bytes for, at the revision
// it read them from. A frozen pass with no scope falls back to the changed
// paths, shown through the VCS layer. An error fails the self-test rather
// than running served reads no byte-check vouches for.
func (l *Leg) selectProbeCandidates(ctx context.Context, loaded Context) ([]readserve.Probe, error) {
	// The required units first: they are the files this pass judges, with
	// the evidence bytes already in hand. An empty required set falls
	// through to the file listing, so a pass the enumeration left empty
	// still byte-checks when git names files.
	if loaded.Scope != nil {
		var out []readserve.Probe
		for _, unit := range loaded.Scope.Required {
			if !unit.Available || unit.Binary || len(unit.Body) == 0 || len(unit.Body) > maxProbeBytes ||
				probeLineCount(unit.Body) > readserve.DefaultMaxResultLines {
				continue
			}
			revision := "head"
			if unit.ContentRevision.SHA() == loaded.Scope.Base.SHA() {
				revision = "base"
			}
			out = append(out, readserve.Probe{Path: unit.Path, Revision: revision, Want: unit.Body})
		}
		if len(out) > 0 {
			return out, nil
		}
	}
	changed, err := l.VCS.ChangedFiles(ctx, loaded.PR.BaseRefOid, loaded.PR.HeadRefOid)
	if err != nil {
		return nil, fmt.Errorf("listing changed files: %w", err)
	}
	var out []readserve.Probe
	tried := 0
	for _, file := range changed {
		if tried >= 5 {
			break
		}
		for _, revision := range []struct {
			name string
			rev  core.Revision
		}{{"head", loaded.PR.HeadRefOid}, {"base", loaded.PR.BaseRefOid}} {
			body, _, err := l.VCS.Show(ctx, revision.rev, file.Path)
			if err != nil || len(body) == 0 || len(body) > maxProbeBytes ||
				probeLineCount(body) > readserve.DefaultMaxResultLines {
				continue
			}
			out = append(out, readserve.Probe{Path: file.Path, Revision: revision.name, Want: body})
		}
		tried++
	}
	if len(out) == 0 {
		return nil, errNoProbeFile
	}
	return out, nil
}

// runReadsSelfTest speaks the handshake to a fresh server before any
// harness child starts. It answers the error the assessment degrades or
// halts on, or nil when the tool serves. The probe session logs beside the
// call log, never inside it: self-test traffic is the leg checking the
// tool, not the harness reading through it, and the post-call check must
// not count it.
func (l *Leg) runReadsSelfTest(ctx context.Context, command string, session readserve.Session, loaded Context) error {
	probes, err := l.selectProbeCandidates(ctx, loaded)
	if err != nil {
		if errors.Is(err, errNoProbeFile) {
			return nil
		}
		return err
	}
	session.LogPath = filepath.Join(filepath.Dir(session.LogPath), "reads.selftest.jsonl")
	session.Call = "selftest"
	// The server runs on the leg's own environment, never on a freshly
	// read one: l.Env is the allowlisted orchestrator environment without
	// any staged harness credential, which the read server neither needs
	// nor receives.
	for _, probe := range probes {
		err := readserve.SelfTest(ctx, l.runner(), command, session.Args(), l.Env, probe)
		if err == nil {
			return nil
		}
		// A symlink or submodule candidate is unservable for what it is,
		// not for a broken tool: try the next candidate rather than
		// failing the self-test on a healthy server.
		if errors.Is(err, readserve.ErrProbeUnservable) {
			continue
		}
		return err
	}
	// Every candidate unservable: there is nothing to byte-check against,
	// the same skip an empty file listing earns.
	return nil
}

// noteReads records one call's reads health for the envelopes.
func (l *Leg) noteReads(note readsNote) {
	l.readsNotes = append(l.readsNotes, note)
}

// rewriteReadsBlock swaps the served reads block for the supplied one in
// an already-rendered prompt. Prompts render before the leg-start
// self-test runs, so a call that falls back to supplied rewrites what the
// reviewer is told rather than naming a tool the child was not granted.
func rewriteReadsBlock(promptBytes []byte) []byte {
	return bytes.Replace(promptBytes,
		[]byte(prompt.ReadsBlock(string(harness.ReadModeServed))),
		[]byte(prompt.ReadsBlock(string(harness.ReadModeSupplied))), 1)
}

// assessCallReads is the post-call reads check: the handshake in this
// call's server log, and refused calls matched against it. A prior reason
// (an unwired file_tool mode, a fallen-back self-test) is recorded, not a
// failure. A fresh failure degrades visibly where the policy says degrade
// — the terminal warning, the summary line the pass comment carries, and
// the run-log event beside them — and halts the leg where it says halt,
// publishing nothing.
func (l *Leg) assessCallReads(loaded Context, tmp string, declared, effective harness.ReadMode, priorReason string) (readsNote, []ui.Line, error) {
	raw, _ := os.ReadFile(filepath.Join(tmp, "reads.jsonl")) //nolint:gosec // the leg's own scratch file
	calls, stats := prstate.ParseReadLog(raw)
	note := readsNote{declared: declared, effective: effective, reason: priorReason, stats: stats, calls: calls}
	if priorReason != "" {
		return note, nil, nil
	}
	reason, halt := AssessReads(readsPolicy(loaded), effective, stats)
	if reason == "" {
		return note, nil, nil
	}
	note.reason = reason
	if halt {
		return note, nil, readsUnavailableFatal(reason)
	}
	if l.Log != nil {
		l.Log.Event("reads", fmt.Sprintf("degraded (%s): calls=%d reads=%d refused=%d", reason, stats.Calls, stats.Reads, stats.Refused))
	}
	return note, []ui.Line{readsDegradedWarning(reason, stats)}, nil
}

// readsUnavailableFatal halts the leg where the policy says halt: served
// reads are unavailable and the call publishes nothing.
func readsUnavailableFatal(detail string) *ui.FatalError {
	return &ui.FatalError{
		Reason: fmt.Sprintf("served reads are unavailable: %s (reads_unavailable)", detail),
		Action: "The review leg reads only through the served tool, and the tool is not serving. " +
			"Re-run the leg; a repeated failure degrades visibly under `.policy.on_reads_unavailable: degrade`, the default, " +
			"or stops the leg under `halt`. The run log carries the detail.",
	}
}

// summarizeReads folds every recorded call into one envelope and the
// served calls behind reads.json. The reason is the first non-empty one:
// a pass degrades once, on whatever failed first.
func (l *Leg) summarizeReads() (envelope prstate.ReadsEnvelope, calls []prstate.ReadsCall, ok bool) {
	if len(l.readsNotes) == 0 {
		return prstate.ReadsEnvelope{}, nil, false
	}
	first := l.readsNotes[0]
	effective := first.effective
	var reason string
	var stats prstate.ReadsStats
	for _, note := range l.readsNotes {
		// A pass runs one mode until a failed self-test falls it back:
		// the envelope reports the degraded mode when any call ran it.
		if note.effective != first.effective {
			effective = harness.ReadModeSupplied
		}
		stats.Calls += note.stats.Calls
		stats.Reads += note.stats.Reads
		stats.Bytes += note.stats.Bytes
		stats.Refused += note.stats.Refused
		stats.BudgetExhausted = stats.BudgetExhausted || note.stats.BudgetExhausted
		calls = append(calls, note.calls...)
		if note.reason != "" && reason == "" {
			reason = note.reason
		}
	}
	envelope = ReadsEnvelope(first.declared, effective, reason, stats)
	return envelope, calls, true
}

// attachReads records the reads envelope on the marker: envelope-only,
// never the per-call detail. A pass that degraded carries its reason; a
// pass whose calls read through the tool carries its counts. A pass with
// no reason and no served reads records nothing, so healthy markers keep
// their bytes exactly.
func (l *Leg) attachReads(marker *prstate.Marker) {
	envelope, _, ok := l.summarizeReads()
	if !ok {
		return
	}
	if envelope.Reason == "" && envelope.Calls == 0 {
		return
	}
	raw, err := json.Marshal(envelope)
	if err != nil {
		return
	}
	marker.Reads = raw
}

// readsDegradedWarning is the terminal half of a degradation: the reason,
// in the open, where the pass comment carries it. A refused call beside
// served reads is worded for what happened: the reviewer read the rest
// through the tool, so the warning names the refused and served counts
// instead of claiming the review judged the supplied content alone —
// which holds only when nothing was served.
func readsDegradedWarning(reason string, stats prstate.ReadsStats) ui.Line {
	if reason == ReadsReasonCallsRefused && stats.Reads > 0 {
		return ui.Warn(
			prstate.ReadsDegradedSentence(reason, stats.Refused, stats.Reads),
			"The run log carries the refused calls and the ledger carries the envelope.")
	}
	return ui.Warn(
		prstate.ReadsDegradedSentence(reason, stats.Refused, stats.Reads),
		"The served read path is not serving, so the reviewer saw only the prompt. The run log carries the detail; the ledger carries the envelope.")
}
