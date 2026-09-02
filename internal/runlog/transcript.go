package runlog

import (
	"os"
	"path/filepath"
	"strconv"
)

// transcriptStreams are the files an attempt's stem is pre-created for. Codex
// also writes a .payload; the empty file is harmless for the others and is
// swept with the rest of the stem (lib/log.sh:223-225).
var transcriptStreams = []string{".stdout", ".stderr", ".payload"}

// KeepTranscripts reads the raw flag or config value that asks for the
// transcripts of a successful invocation to survive. Only "1" does: empty and
// "0" both mean the default, which is that a transcript survives a failed leg
// only (log_transcripts_kept, lib/log.sh:210).
func KeepTranscripts(value string) bool { return value == "1" }

// TranscriptsKept reports whether this run keeps the transcripts of a
// successful invocation.
func (l *Log) TranscriptsKept() bool {
	return l != nil && l.keepTranscripts
}

// TranscriptBase is the stem an attempt's transcripts are written under, and
// whether there is one at all (log_transcript_base, lib/log.sh:216).
//
// There is none when the run has no directory or no leg, and the adapters fall
// back to anonymous temporary files then — the behaviour every caller of an
// adapter outside a run already has.
//
// The stem is concatenated rather than joined, for the reason RunDir is: the
// Bash function builds it with printf, so a run directory carrying a doubled
// separator keeps it here too. Cleaning the stem would hand an adapter a
// different string from the one the shell hands it, and the stem is a string a
// subprocess is given rather than only a path this package opens.
//
// The three files are pre-created so that the adapters' redirects inherit 0600
// rather than the process umask. That is best-effort on both sides — the Bash
// helper swallows its own failure — so a pre-create that fails still yields a
// stem, and the run log says which file the guarantee did not reach.
func (l *Log) TranscriptBase(attempt int) (string, bool) {
	if l == nil || l.dir == "" {
		return "", false
	}
	leg := l.Leg()
	if leg == "" {
		return "", false
	}
	base := l.dir + "/" + leg + ".attempt-" + strconv.Itoa(attempt)
	for _, stream := range transcriptStreams {
		if err := CreatePrivate(base + stream); err != nil {
			l.Event("transcript", "could not pre-create "+base+stream+"; the adapter's redirect will use the process umask")
		}
	}
	return base, true
}

// ClearTranscripts deletes one attempt's transcripts, or every attempt's for
// the current leg when base is empty (log_transcripts_clear, lib/log.sh:232).
//
// Success-path hygiene only: the failure path is the reason the files exist,
// and nothing on it calls this.
//
// The dot before the star is load-bearing in both patterns, and it is what
// `rm -f "$base".*` has. Without it, clearing attempt 1 also matches
// attempt-10, attempt-11 and every later attempt's transcripts, which are the
// files another attempt is still writing into.
func (l *Log) ClearTranscripts(base string) {
	if l == nil || l.dir == "" || l.TranscriptsKept() {
		return
	}
	pattern := base + ".*"
	if base == "" {
		pattern = l.dir + "/" + l.Leg() + ".attempt-*.*"
	}
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return
	}
	for _, match := range matches {
		_ = os.Remove(match)
	}
}

// WriteTranscript archives one attempt's two streams under its stem and masks
// every credential shape in them.
//
// The Bash adapters redirect the harness's stdout and stderr straight into
// these files and parse the answer back out of them, then call log_redact_file
// on each (lib/adapters/claude.sh:95-106, :148-154). A Go adapter parses the
// bytes it already holds, so the write happens here instead — after the parse,
// which is the order that matters: filtering first would rewrite the model's
// own answer, and identical harness output would then produce different
// findings depending on whether a run directory exists.
//
// An empty base means the run has no directory, which is the anonymous
// temporary file the Bash adapters fall back to and then delete. Nothing is
// written in that case.
func (l *Log) WriteTranscript(base string, stdout, stderr []byte) {
	if l == nil || base == "" {
		return
	}
	for _, stream := range []struct {
		suffix string
		body   []byte
	}{{".stdout", stdout}, {".stderr", stderr}} {
		path := base + stream.suffix
		if err := os.WriteFile(path, stream.body, 0o600); err != nil {
			l.Event("transcript", "could not write "+path)
			continue
		}
		l.RedactFile(path)
	}
}
