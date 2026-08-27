package config

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/carlosboeing/crossrev/internal/core"
)

// projectMapHeading opens a `## Project Map` or `## Project Context` section.
// The Bash awk lowercases the whole line before matching, so the heading text
// is matched case-insensitively and two or more hashes open a section
// (lib/config.sh:351).
var projectMapHeading = regexp.MustCompile(`^##+[[:space:]]*project (map|context)`)

// projectMapClose is the awk rule that closes the section, `/^##[[:space:]]/`
// at lib/config.sh:352. Written as the same character class rather than as two
// literal prefixes, because POSIX [[:space:]] holds a carriage return and a
// CRLF instruction file otherwise leaves a `##` line failing to close it.
var projectMapClose = regexp.MustCompile(`^##[[:space:]]`)

// trackerField is the Tracker line inside that section. The Bash sed carries
// the `I` flag, so the field name is matched case-insensitively
// (lib/config.sh:354).
var trackerField = regexp.MustCompile(`(?i)^[[:space:]]*-[[:space:]]*\*\*Tracker\*\*:[[:space:]]*`)

// projectMapFiles are the instruction files a Project Map may live in, in the
// order lib/config.sh:344 reads them.
var projectMapFiles = []string{"AGENTS.md", "CLAUDE.md", "GEMINI.md"}

// ProjectMapTracker reads the Tracker field out of a `## Project Map` section.
//
// The convention declares where project-tracking information lives so tools
// read it instead of guessing. It is read from the base revision like every
// other policy: a pull request that could edit it from the head could repoint
// where the loop writes (lib/config.sh:336-366, ADR 0003).
func ProjectMapTracker(ctx context.Context, base core.Revision, show ShowFile) (string, bool, error) {
	for _, path := range projectMapFiles {
		source, status, err := show(ctx, base, path)
		if err != nil {
			return "", false, fmt.Errorf("read %s: %w", path, err)
		}
		if status != IsFile || len(source) == 0 {
			continue
		}
		// A field that strips to nothing does not end the search. The Bash
		// returns only when the stripped value is non-empty and otherwise
		// moves to the next file (lib/config.sh:363-365), so `- **Tracker**:
		// (see the board)` in AGENTS.md leaves CLAUDE.md to decide.
		if tracker, found := trackerIn(string(source)); found && tracker != "" {
			return tracker, true, nil
		}
	}
	return "", false, nil
}

// trackerIn scans one document for the first Tracker field inside a Project Map
// section.
func trackerIn(document string) (string, bool) {
	inMap := false
	for _, line := range strings.Split(document, "\n") {
		if projectMapHeading.MatchString(strings.ToLower(line)) {
			inMap = true
			continue
		}
		// Exactly two hashes and a space closes the section, so a `###`
		// sub-heading inside it does not (lib/config.sh:352).
		if inMap && projectMapClose.MatchString(line) {
			inMap = false
		}
		if !inMap {
			continue
		}
		if location := trackerField.FindStringIndex(line); location != nil {
			return stripGloss(line[location[1]:]), true
		}
	}
	return "", false
}

// stripGloss drops a parenthetical gloss before returning the value.
//
// Project Map fields routinely carry one — `none (ROADMAP.md is the single
// source of truth)` is how the convention's own example reads — and an
// unstripped gloss makes `none` stop matching `none`, so the caller falls
// through to the sniff and picks a destination the repository just declared it
// does not have. Nothing legitimate here holds a parenthesis: the value is a
// path, a tracker name, or `none` (lib/config.sh:356-362).
func stripGloss(value string) string {
	if at := strings.Index(value, "("); at >= 0 {
		value = value[:at]
	}
	// The Bash sed is `s/[[:space:]]*$//` at lib/config.sh:362, and POSIX
	// [[:space:]] holds the carriage return. Trimming space and tab alone
	// leaves the CR of a CRLF instruction file on the value, `none` stops
	// matching `none`, and the caller picks a backlog location the repository
	// just declared it does not have.
	return strings.TrimRight(value, " \t\r\v\f")
}
