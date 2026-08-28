package runlog

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// RedactNotice is the line appended to a published body the filter changed. A
// masked string on its own inside a review comment reads as a CrossRev defect
// to whoever finds it on the pull request, so the body says what happened to it
// (LOG_REDACT_NOTICE, lib/log.sh:124).
const RedactNotice = "_CrossRev masked a string in this comment that matched a credential pattern._"

// WithheldText is published in place of a body the filter could not read. A
// body that could not be filtered is exactly the body that might carry the
// credential (lib/log.sh:155).
const WithheldText = "CrossRev could not filter this text for credential shapes, so it withheld it rather than publishing it."

// mask is what replaces the body of a credential. The prefix survives so a
// redacted line still names the kind of token it held.
const mask = "…[redacted]"

// credentialPatterns are the credential shapes CrossRev handles, masked
// wherever they appear (log_redact, lib/log.sh:96-107). Kept deliberately
// broader than the tokens a run is expected to hold: a harness echoing its
// environment on a failure path is the case this exists for.
//
// Order is load-bearing and matches the order of the -e flags. The generic
// sk- rule runs last, over text the first rule has already masked, and a masked
// string no longer matches it — which is where the idempotence comes from.
//
// Byte-oriented, like the LC_ALL=C the Bash filter pins. Every class here is
// ASCII, and Go's regexp decodes a byte that is not valid UTF-8 as one
// unmatchable rune rather than refusing the input, so binary noise from a
// failing harness passes through unchanged instead of aborting the filter the
// way a UTF-8 sed does.
var credentialPatterns = []struct {
	re   *regexp.Regexp
	with string
}{
	{regexp.MustCompile(`(sk-ant-[A-Za-z0-9_-]{6})[A-Za-z0-9_-]+`), "${1}" + mask},
	{regexp.MustCompile(`(github_pat_[A-Za-z0-9_]{6})[A-Za-z0-9_]+`), "${1}" + mask},
	{regexp.MustCompile(`(gh[pousr]_[A-Za-z0-9]{6})[A-Za-z0-9]+`), "${1}" + mask},
	{regexp.MustCompile(`(xai-[A-Za-z0-9_-]{6})[A-Za-z0-9_-]+`), "${1}" + mask},
	{regexp.MustCompile(`(sk-[A-Za-z0-9_-]{6})[A-Za-z0-9_-]{12,}`), "${1}" + mask},
}

// filter is the signature of the credential filter, and it reports an error
// because the thing being ported can fail: the Bash filter is an external sed,
// and every caller of it is written around the possibility that it dies. Go's
// replacement cannot fail, so filterBytes never returns an error — but the
// callers keep the branch that withholds the text, and the tests keep a filter
// that fails so those branches are exercised rather than assumed.
type filter func([]byte) ([]byte, error)

func filterBytes(in []byte) ([]byte, error) {
	out := in
	for _, pattern := range credentialPatterns {
		out = pattern.re.ReplaceAll(out, []byte(pattern.with))
	}
	return out, nil
}

// Redact masks every credential shape in a string.
//
// The harness error message is extracted from the raw capture and then reaches
// a pull request comment, so it is filtered whether or not a transcript is kept
// (log_redact_str, lib/log.sh:116).
func Redact(s string) string {
	out, err := filterBytes([]byte(s))
	if err != nil {
		return WithheldText
	}
	return string(out)
}

// Publish masks a body on its way to GitHub, and says whether it could
// (log_redact_publish, lib/log.sh:148).
//
// The three routes out of a run are not the same one. RedactFile covers the
// transcript kept on disk, Redact covers the harness error message, and this
// covers the third: on a successful leg the findings text is parsed from the
// raw capture and published verbatim into inline review comments, replies,
// filed issues and the pass marker. The filter runs at the publish boundary
// rather than at the parse, so a leg's payload does not depend on whether a run
// directory exists.
//
// Idempotent. A masked string no longer matches any of the patterns, so a body
// that passes through twice is masked once and noted once.
//
// Fails closed, and says so to its caller: an error returns WithheldText rather
// than the body, so a caller that cannot afford to lose what it was given can
// refuse instead of publishing the notice as the body.
//
// The receiver may be nil. A run with no directory still publishes, and the one
// event this writes is the only reason it is a method at all.
func (l *Log) Publish(body string) (string, error) {
	out, err := l.filter()(([]byte)(body))
	if err != nil {
		l.Event("redact", "publish filter failed; body withheld")
		return WithheldText, err
	}
	filtered := string(out)
	// One trailing newline is ignored on both sides. BSD and GNU sed disagree
	// about a final line that carries no newline, and the notice must mean a
	// mask happened rather than that a platform added a byte.
	if strings.TrimSuffix(filtered, "\n") != strings.TrimSuffix(body, "\n") {
		filtered += "\n\n" + RedactNotice
	}
	return filtered, nil
}

// RedactFile masks every credential shape in a file, in place.
//
// It reports nothing, because it runs from the paths whose own failure is the
// thing being recorded: a filter error here must not fail an exiting run
// (lib/log.sh:174).
//
// Fails closed: a filter error must not leave the original on disk, so the
// unredacted copy is replaced with a notice. The rewrite goes through a
// neighbouring temporary file rather than an in-place edit, which is also what
// leaves the file 0600 whatever it was before.
func (l *Log) RedactFile(path string) {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return
	}
	out, filterErr := l.filter()(raw)
	if filterErr != nil {
		_ = CreatePrivate(path)
		_ = os.WriteFile(path, []byte("redaction failed; original discarded\n"), fileMode)
		l.Event("redact", "failed "+path)
		return
	}
	// The temporary file sits beside the target so the rename cannot cross a
	// filesystem, and os.CreateTemp opens it 0600, so the renamed result is
	// 0600 whatever the file's mode was before. No chmod runs anywhere in this
	// package, which is what makes "never briefly wider" a property of the code
	// rather than of the order two calls happen to be written in.
	tmp, err := os.CreateTemp(filepath.Dir(path), ".crossrev-redact-*")
	if err != nil {
		return
	}
	name := tmp.Name()
	if _, err := tmp.Write(out); err != nil {
		tmp.Close()
		os.Remove(name)
		return
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return
	}
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
	}
}
