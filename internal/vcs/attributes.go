package vcs

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/carlosboeing/crossrev/internal/core"
)

// AttributeDecision is the typed answer for one path's linguist-generated
// attribute, read from the base tree in one check-attr call.
type AttributeDecision int

const (
	// AttributeUnspecified means .gitattributes says nothing about the path,
	// so the built-in generated-file rules decide.
	AttributeUnspecified AttributeDecision = iota
	// AttributeSet means the base tree marks the path: `linguist-generated`,
	// `=true`, or any other value but false, which is how GitHub reads it.
	AttributeSet
	// AttributeNegated means the base tree explicitly unmarks the path:
	// `-linguist-generated` or `=false`. Every built-in rule is suppressed.
	AttributeNegated
)

// checkAttrSourceFloor is the git release that learned check-attr --source:
// 2.40 ("'git check-attr' learned to take an optional tree-ish to read the
// .gitattributes file from", the 2.40.0 release notes).
const (
	checkAttrSourceMinMajor = 2
	checkAttrSourceMinMinor = 40
)

// gitVersionShape is the first dotted version token of `git version` output:
// "git version 2.50.1", "git version 2.39.3 (Apple Git-154)",
// "git version 1.8.3.1" or "git version 2.40.0.windows.1" all carry one.
var gitVersionShape = regexp.MustCompile(`(\d+)\.(\d+)(\.\d+)*`)

// GitVersion is a parsed `git version` answer.
type GitVersion struct {
	// Token is the matched version text, e.g. "2.50.1".
	Token string
	Major int
	Minor int
}

// ParseGitVersion reads the first version-shaped token of `git version`
// output. Output with no such token is malformed and reports not-ok rather
// than a zero version, which would read as "too old" and warn wrongly.
func ParseGitVersion(text string) (GitVersion, bool) {
	match := gitVersionShape.FindStringSubmatch(text)
	if match == nil {
		return GitVersion{}, false
	}
	major, err := strconv.Atoi(match[1])
	if err != nil {
		return GitVersion{}, false
	}
	minor, err := strconv.Atoi(match[2])
	if err != nil {
		return GitVersion{}, false
	}
	return GitVersion{Token: match[0], Major: major, Minor: minor}, true
}

// AtLeast compares parsed numbers, never string order: "2.9" sorts after
// "2.40" as text and is older than it as a version.
func (v GitVersion) AtLeast(major, minor int) bool {
	return v.Major > major || v.Major == major && v.Minor >= minor
}

// GeneratedAttributes answers linguist-generated for each current path from
// the base tree, in one NUL-delimited
// `git check-attr --source=<base> -z --stdin linguist-generated` call. The
// base tree answers, never the working copy, so a pull request cannot change
// the policy it is reviewed under (ADR 0003). A deleted path is answered at
// its old name; a renamed path at its new one, because the caller passes the
// current paths.
//
// A git older than 2.40 has no --source: the answer is unspecified for every
// path, plus one warning naming the version, and the built-in rules decide.
// Every other failure — a malformed version, a check-attr that exits badly,
// a base object that cannot be read, a response that drops a path — is an
// error, because reading it as unspecified would review paths the repository
// marked linguist-generated.
func (r *Repository) GeneratedAttributes(ctx context.Context, base core.Revision, paths []string) (map[string]AttributeDecision, *Warning, error) {
	answers := make(map[string]AttributeDecision, len(paths))
	for _, path := range paths {
		answers[path] = AttributeUnspecified
	}
	if len(paths) == 0 {
		return answers, nil, nil
	}

	versionOut, err := r.Run(ctx, "version")
	if err != nil {
		return nil, nil, err
	}
	if !versionOut.OK() {
		return nil, nil, fmt.Errorf("git version exited %d: %s", versionOut.ExitCode, versionOut.Stderr)
	}
	version, ok := ParseGitVersion(versionOut.Text())
	if !ok {
		return nil, nil, fmt.Errorf("could not parse a git version from %q", versionOut.Text())
	}
	if !version.AtLeast(checkAttrSourceMinMajor, checkAttrSourceMinMinor) {
		return answers, &Warning{
			Message: fmt.Sprintf("git %s is older than 2.40, so .gitattributes linguist-generated is not read at the base", version.Token),
			Hint:    "The built-in generated-file rules still apply. Upgrade git to 2.40 or newer to read repository policy.",
		}, nil
	}

	// check-attr answers "unspecified" with exit 0 when the source object is
	// missing — a shallow clone that never fetched the base reads as "no
	// policy", which would review paths the repository excluded. Probe the
	// object first.
	probe, err := r.Run(ctx, "cat-file", "-e", base.SHA())
	if err != nil {
		return nil, nil, err
	}
	if !probe.OK() {
		return nil, nil, fmt.Errorf("git cat-file -e %s exited %d: %s", base.Short(), probe.ExitCode, probe.Stderr)
	}

	var stdin strings.Builder
	for _, path := range paths {
		stdin.WriteString(path)
		stdin.WriteByte(0)
	}
	output, err := r.git.Run(ctx, Call{
		Dir:   r.dir,
		Args:  []string{"check-attr", "--source=" + base.SHA(), "-z", "--stdin", "linguist-generated"},
		Stdin: []byte(stdin.String()),
	})
	if err != nil {
		return nil, nil, err
	}
	if !output.OK() {
		return nil, nil, fmt.Errorf("git check-attr --source=%s exited %d: %s", base.Short(), output.ExitCode, output.Stderr)
	}
	return parseCheckAttrAnswers(output.Stdout, answers)
}

// parseCheckAttrAnswers reads the NUL-delimited triples
// `<path>\0linguist-generated\0<value>\0` into answers, which holds every
// queried path. A record for a path nobody asked about, a dropped path, or a
// record that does not parse is an error: silently treating it as
// unspecified would review what the repository excluded.
func parseCheckAttrAnswers(stdout string, answers map[string]AttributeDecision) (map[string]AttributeDecision, *Warning, error) {
	fields := strings.Split(stdout, "\x00")
	if len(fields) > 0 && fields[len(fields)-1] == "" {
		fields = fields[:len(fields)-1]
	}
	if len(fields)%3 != 0 {
		return nil, nil, fmt.Errorf("git check-attr answered %d fields, not a multiple of three", len(fields))
	}
	seen := make(map[string]bool, len(answers))
	for i := 0; i < len(fields); i += 3 {
		path, attr, value := fields[i], fields[i+1], fields[i+2]
		if attr != "linguist-generated" {
			return nil, nil, fmt.Errorf("git check-attr answered for attribute %q, want linguist-generated", attr)
		}
		if _, asked := answers[path]; !asked {
			return nil, nil, fmt.Errorf("git check-attr answered for unqueried path %q", path)
		}
		answers[path] = attributeDecision(value)
		seen[path] = true
	}
	for path := range answers {
		if !seen[path] {
			return nil, nil, fmt.Errorf("git check-attr dropped the record for %q", path)
		}
	}
	return answers, nil, nil
}

// attributeDecision maps check-attr's value. git answers "set" for a bare
// mark, "unset" for a negation, "unspecified" when nothing matches, and the
// literal string for `attr=value`. Any value but false marks the file, which
// is GitHub's own reading of linguist-generated.
func attributeDecision(value string) AttributeDecision {
	switch value {
	case "unspecified":
		return AttributeUnspecified
	case "unset", "false":
		return AttributeNegated
	default:
		return AttributeSet
	}
}
