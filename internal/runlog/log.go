package runlog

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// eventTimestamp is the format every event line opens with: UTC, to the second,
// spelled the way `date -u '+%Y-%m-%dT%H:%M:%SZ'` spells it (lib/log.sh:86).
const eventTimestamp = "2006-01-02T15:04:05Z"

// logFile is the one file every run writes, whether or not it keeps a
// transcript.
const logFile = "run.log"

// Options is everything a run log needs that it must not work out for itself.
//
// Dir and SweepBase are handed in rather than resolved here, so nothing that
// creates a directory reads HOME. RunDir and RunsBase resolve them for a real
// run; a test hands in a temporary directory and never touches the state
// directory the developer is using.
type Options struct {
	// Dir is the run directory, from RunDir. Required.
	Dir string

	// SweepBase is the directory the retention sweep walks, from RunsBase.
	// Empty skips the sweep, which is what a test wants when its Dir is not
	// underneath a runs base at all.
	SweepBase string

	// RetentionDays is how long a run directory survives. Zero means unset
	// and Open sweeps with DefaultRetentionDays; use the RetentionDays
	// function to turn a configured string into one, and KeepEverything to
	// sweep nothing at all.
	//
	// The field and the function share a name and not a zero value, which is
	// the misread to avoid: the function answers an empty string with
	// DefaultRetentionDays, while this is a plain int that means nothing
	// until Open reads it. A literal zero would be the most destructive
	// window there is — agedOut takes it as "delete anything older than
	// twenty-four hours" — and the shell cannot reach that state, because
	// log_sweep defaults the window to 14 before its guard runs
	// (lib/log.sh:194).
	RetentionDays int

	// Repo and PR name the run in its opening event.
	Repo string
	PR   string

	// Revision is the build revision the run started from, from
	// buildinfo.Info.Revision. Empty reads as a dash on the `run start`
	// line: a build with no VCS stamp (go run, go test, an exported
	// tarball) still writes the field, with the same missing-identity
	// mark the call lines use for a harness that names no model.
	Revision string

	// KeepTranscripts asks for the transcripts of a successful invocation to
	// survive. The flag and the config key both reach here through
	// KeepTranscripts, which is where the string comparison lives.
	KeepTranscripts bool

	// Leg is the leg the invocation belongs to, so the transcript files name
	// review or resolve rather than the harness that happened to serve them.
	// SetLeg changes it, because one invocation of `crossrev cycle` runs both.
	Leg string

	// Now is the clock the event timestamps and the sweep read. Nil means
	// time.Now.
	Now func() time.Time
}

// retentionWindow is the window Open sweeps with, with the unset field
// resolved. See the RetentionDays field for why zero cannot mean a day.
func (o Options) retentionWindow() int {
	if o.RetentionDays == 0 {
		return DefaultRetentionDays
	}
	return o.RetentionDays
}

// Log is the per-run record on disk.
//
// A nil *Log is the disabled state, and every method tolerates it. That is the
// port of the empty CROSSREV_RUN_DIR the Bash library carries until log_init
// runs and falls back to whenever a directory cannot be made (lib/log.sh:26-30):
// a library consumer that never asked for a run directory shares the code
// without scattering directories, and nothing in this package may fail its
// caller, because it is called from the paths whose own failure is what is
// being recorded.
type Log struct {
	dir             string
	keepTranscripts bool
	now             func() time.Time

	mu  sync.Mutex
	leg string
	// redact is the credential filter. It is a field so that a test can supply
	// one that fails and reach the fail-closed branches; production never
	// replaces it.
	redact filter
	// mktemp opens the temporary file RedactFile rewrites through. A field for
	// the same reason redact is one: every way that rewrite can fail has to
	// discard the original, and a branch nothing can reach is a branch nobody
	// has checked. Production never replaces it.
	mktemp mkTemp
}

// Open creates the run directory, sweeps the expired ones and writes the
// opening event (log_init, lib/log.sh:64).
//
// A directory that cannot be created returns a nil *Log and an error. Both
// halves matter: the error is there for a caller that wants to say so, and the
// nil Log is usable as-is, so a caller that follows the Bash library and
// degrades to a no-op can ignore the error and keep going.
//
// log_init's idempotency guard has no equivalent here, and this is where it
// went. The Bash function returns early when CROSSREV_RUN_DIR is already set,
// so the second leg of `crossrev cycle` does not restart the record the first
// began (lib/log.sh:66). The guard is there because the run directory is a
// global any leg can re-derive; a *Log is a value one caller opens and hands to
// both legs, and SetLeg is what moves it between them. Open is therefore called
// once per invocation, not once per leg — calling it twice on one directory
// appends a second `run start` event rather than being refused.
func Open(opts Options) (*Log, error) {
	if opts.Dir == "" {
		return nil, os.ErrInvalid
	}
	if err := os.MkdirAll(opts.Dir, dirMode); err != nil {
		return nil, err
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	l := &Log{
		dir:             opts.Dir,
		keepTranscripts: opts.KeepTranscripts,
		now:             now,
		leg:             opts.Leg,
	}
	if opts.SweepBase != "" {
		Sweep(opts.SweepBase, opts.retentionWindow(), now())
	}
	revision := opts.Revision
	if revision == "" {
		revision = "-"
	}
	l.Event("run", "start repo="+opts.Repo+" pr="+opts.PR+" revision="+revision)
	return l, nil
}

// Dir is the run directory, or the empty string when there is none.
func (l *Log) Dir() string {
	if l == nil {
		return ""
	}
	return l.dir
}

// SetLeg names the leg the following invocations belong to.
func (l *Log) SetLeg(leg string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.leg = leg
}

// Leg is the leg the current invocation belongs to.
func (l *Log) Leg() string {
	if l == nil {
		return ""
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.leg
}

// filter is the credential filter this log writes through, defaulting to the
// package one.
func (l *Log) filter() filter {
	if l == nil || l.redact == nil {
		return filterBytes
	}
	return l.redact
}

// createTemp is the temporary-file opener RedactFile rewrites through,
// defaulting to the package one.
func (l *Log) createTemp() mkTemp {
	if l == nil || l.mktemp == nil {
		return createTempFile
	}
	return l.mktemp
}

// Event appends one line to the run log: timestamp, phase, detail
// (log_event, lib/log.sh:78).
//
// Every line goes through the credential filter even though the callers build
// these strings from names and exit codes, so the rule "nothing reaches the
// file unfiltered" has no exceptions to remember. Callers pass git tails and
// die reasons that already contain newlines, so those are collapsed and the
// one-line-per-event invariant holds regardless.
//
// The filter it writes through is the package one, Redact, and not l.filter().
// That is the single deliberate difference from Publish and RedactFile, and it
// is not an escape from the rule above: both filters are the same patterns, and
// production never replaces either. The field exists so a test can supply a
// filter that fails and reach the two fail-closed branches — and the events
// those branches write are the record of the failure, so routing this through
// the same failing filter would withhold the only account of what went wrong.
//
// The timestamp is converted to UTC rather than formatted in whatever zone the
// clock is in, because eventTimestamp ends in a literal Z. A local clock
// formatted directly would read hours off and still claim to be UTC.
func (l *Log) Event(phase, detail string) {
	if l == nil || l.dir == "" {
		return
	}
	detail = eventDetail.Replace(detail)
	path := filepath.Join(l.dir, logFile)

	l.mu.Lock()
	defer l.mu.Unlock()
	if info, err := os.Stat(path); err != nil || !info.Mode().IsRegular() {
		_ = CreatePrivate(path)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, fileMode)
	if err != nil {
		return
	}
	defer f.Close()
	line := l.now().UTC().Format(eventTimestamp) + " " + phase + " " + detail + "\n"
	_, _ = f.WriteString(Redact(line))
}

var eventDetail = strings.NewReplacer("\n", " ", "\r", " ")

// Phase records one review-preparation step's wall time: enumerate, reads,
// diff, search or pack. A nil Log writes nothing, like every other method.
func (l *Log) Phase(name string, ms int64) {
	if l == nil {
		return
	}
	l.Event("phase", name+" ms="+strconv.FormatInt(ms, 10))
}

// PhaseTerms records the advisory term walk: the whole-pass search set's
// size alongside its wall time.
func (l *Log) PhaseTerms(terms int, ms int64) {
	if l == nil {
		return
	}
	l.Event("phase", "terms terms="+strconv.Itoa(terms)+" ms="+strconv.FormatInt(ms, 10))
}

// Call records one accepted model call: the rendered prompt's byte length,
// the evidence bytes handed over with it, the usage buckets the accepted
// envelope folded in, the call's served reads from the reads ledger, the
// answering model (or a dash where the harness names none), and the call's
// wall time including its refused attempts.
//
// reads counts what the call served through the served read tool, summed
// across its attempts the way the usage buckets fold in refused attempts;
// a call with no served reader — supplied mode — reads zero. commands
// reads zero because an accepted call never ran one: any command event
// halts the leg with review_leg_ran_command before a call line is written.
func (l *Log) Call(call, promptBytes, suppliedBytes, reads int, fresh, cached, output int64, model string, ms int64) {
	l.callKind("", call, promptBytes, suppliedBytes, reads, fresh, cached, output, model, ms)
}

// CheckCall records one accepted cross-model check call: the same line a
// review call writes, with kind=check beside the call number so the
// checker's usage reads back separately from the reviewer's.
func (l *Log) CheckCall(call, promptBytes, suppliedBytes, reads int, fresh, cached, output int64, model string, ms int64) {
	l.callKind("check", call, promptBytes, suppliedBytes, reads, fresh, cached, output, model, ms)
}

// callKind writes one accepted call's line, naming the kind only when the
// caller names one. An empty kind keeps the review line's bytes exactly.
func (l *Log) callKind(kind string, call, promptBytes, suppliedBytes, reads int, fresh, cached, output int64, model string, ms int64) {
	if l == nil {
		return
	}
	if model == "" {
		model = "-"
	}
	line := strconv.Itoa(call)
	if kind != "" {
		line += " kind=" + kind
	}
	l.Event("call", line+
		" prompt_bytes="+strconv.Itoa(promptBytes)+
		" supplied_bytes="+strconv.Itoa(suppliedBytes)+
		" fresh="+strconv.FormatInt(fresh, 10)+
		" cached="+strconv.FormatInt(cached, 10)+
		" output="+strconv.FormatInt(output, 10)+
		" reads="+strconv.Itoa(reads)+" commands=0"+
		" model="+model+
		" ms="+strconv.FormatInt(ms, 10))
}

// Settings records one leg's resolved harness, model and effort: what the
// config and the command-line overrides settled on before the claim. The
// call lines name only the answering model, so without this a run's
// requested model and effort cannot be read back from the run log. Unset
// halves read as dashes, the way Call renders an unnamed model.
func (l *Log) Settings(harness, model, effort string) {
	l.ReviewSettings(harness, model, effort, ReviewDetail{})
}

// EffectiveSetting is one resolved setting: the value the leg runs with
// and where it came from — flag, config or default. An empty source
// leaves the setting off the line, which is how the resolve leg omits the
// review input policy. An empty value with a source set records honestly:
// no required check reads as an empty list from the default.
type EffectiveSetting struct {
	Value  string
	Source string
}

// ReviewDetail carries the review-contract settings beside harness, model
// and effort: the effective input policy, concerns, check mode, required
// checks and wait, each with its source.
type ReviewDetail struct {
	InputPolicy    EffectiveSetting
	Concerns       EffectiveSetting
	Check          EffectiveSetting
	RequiredChecks EffectiveSetting
	CheckWait      EffectiveSetting
}

// ReviewSettings also records the effective review settings and their
// sources. A setting with no source stays off the line.
func (l *Log) ReviewSettings(harness, model, effort string, detail ReviewDetail) {
	if l == nil {
		return
	}
	if model == "" {
		model = "-"
	}
	if effort == "" {
		effort = "-"
	}
	line := "harness=" + harness + " model=" + model + " effort=" + effort
	for _, field := range []struct {
		name    string
		setting EffectiveSetting
	}{
		{"input_policy", detail.InputPolicy},
		{"concerns", detail.Concerns},
		{"check", detail.Check},
		{"required_checks", detail.RequiredChecks},
		{"check_wait", detail.CheckWait},
	} {
		if field.setting.Source == "" {
			continue
		}
		line += " " + field.name + "=" + field.setting.Value + " " + field.name + "_source=" + field.setting.Source
	}
	l.Event("settings", line)
}
