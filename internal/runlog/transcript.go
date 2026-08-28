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
// The three files are pre-created so that the adapters' redirects inherit 0600
// rather than the process umask.
func (l *Log) TranscriptBase(attempt int) (string, bool) {
	if l == nil || l.dir == "" {
		return "", false
	}
	leg := l.Leg()
	if leg == "" {
		return "", false
	}
	base := filepath.Join(l.dir, leg+".attempt-"+strconv.Itoa(attempt))
	for _, stream := range transcriptStreams {
		_ = CreatePrivate(base + stream)
	}
	return base, true
}

// ClearTranscripts deletes one attempt's transcripts, or every attempt's for
// the current leg when base is empty (log_transcripts_clear, lib/log.sh:232).
//
// Success-path hygiene only: the failure path is the reason the files exist,
// and nothing on it calls this.
func (l *Log) ClearTranscripts(base string) {
	if l == nil || l.dir == "" || l.TranscriptsKept() {
		return
	}
	pattern := base + ".*"
	if base == "" {
		pattern = filepath.Join(l.dir, l.Leg()+".attempt-*.*")
	}
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return
	}
	for _, match := range matches {
		_ = os.Remove(match)
	}
}
