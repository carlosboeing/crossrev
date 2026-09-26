package intel

import (
	"bytes"
	pathpkg "path"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Generated-file signals, returned by GeneratedSignal in this fixed
// precedence. The name is part of the recorded reason when a file is
// skipped.
const (
	SignalLockfile   = "lockfile"
	SignalBundleName = "bundle-name"
	SignalHeader     = "header"
	SignalMinified   = "minified"
)

// The lists below are seeded from Linguist's generated.rb (MIT),
// https://github.com/github-linguist/linguist/blob/main/lib/linguist/generated.rb,
// last compared against upstream commit 767853446a7763ee1d1f04c8bdc4d2f92d834037
// (2026-07-27). scripts/refresh-generated-rules.sh prints upstream additions
// the lists lack; a person applies them, because Linguist's Ruby regular
// expressions do not convert mechanically to Go.

// lockfileNames is Linguist's lockfile list plus the common lockfiles it
// leaves out. The match is an exact, case-sensitive basename.
var lockfileNames = map[string]bool{
	"package-lock.json":   true,
	"npm-shrinkwrap.json": true,
	"yarn.lock":           true,
	"pnpm-lock.yaml":      true,
	"bun.lock":            true,
	"bun.lockb":           true,
	"deno.lock":           true,
	"Cargo.lock":          true,
	"go.sum":              true,
	"Gemfile.lock":        true,
	"composer.lock":       true,
	"poetry.lock":         true,
	"pdm.lock":            true,
	"uv.lock":             true,
	"Pipfile.lock":        true,
	"Package.resolved":    true,
	"Podfile.lock":        true,
	"pubspec.lock":        true,
	"mix.lock":            true,
	"packages.lock.json":  true,
	"gradle.lockfile":     true,
	"flake.lock":          true,
	"MODULE.bazel.lock":   true,
	".terraform.lock.hcl": true,
	"pixi.lock":           true,
	"mise.lock":           true,
}

// bundleSuffixes name minified output and source maps.
var bundleSuffixes = []string{".min.js", ".min.css", ".js.map", ".css.map"}

// goGeneratedHeader is Go's code-generation convention, written by protoc,
// gRPC and most Go generators.
var goGeneratedHeader = regexp.MustCompile(`(?m)^// Code generated .* DO NOT EDIT\.$`)

// The header scan window: the first 10 lines or the first 1 KiB, whichever
// is shorter.
const (
	generatedHeaderLines = 10
	generatedHeaderBytes = 1024
)

// minifiedAverageLineBytes is Linguist's average-line-length threshold for
// generated minified files. Here it applies to every extension except
// proseExtensions, because a bundle embedded in another file type has the
// same shape.
const minifiedAverageLineBytes = 110

// proseExtensions are exempt from the minified rule. Prose kept as one line
// per paragraph averages well over 110 bytes a line without being generated,
// and a match would skip a handwritten document instead of halting on it.
// Compared case-insensitively.
var proseExtensions = map[string]bool{
	".md":       true,
	".markdown": true,
	".mdx":      true,
	".rst":      true,
	".adoc":     true,
	".txt":      true,
}

// excerptMaxBytes caps the header excerpt quoted in a PR warning. A marker
// can sit on a minified line of any length, and GitHub refuses a comment
// body over 65,536 characters, so the excerpt is capped before the warning
// line is built rather than by the skip list's own bound afterwards.
const excerptMaxBytes = 120

// GeneratedSignal reports which built-in rule recognises a file as
// generated, reading only the path and the evidence bytes: the head, or the
// base for a deletion. It returns SignalLockfile, SignalBundleName,
// SignalHeader or SignalMinified in that precedence, or an empty string when
// no rule matches. It runs no git command and gives the same answer on every
// call.
func GeneratedSignal(path string, body []byte) string {
	base := pathpkg.Base(path)
	if lockfileNames[base] {
		return SignalLockfile
	}
	for _, suffix := range bundleSuffixes {
		if strings.HasSuffix(base, suffix) {
			return SignalBundleName
		}
	}
	if headerMatches(body) {
		return SignalHeader
	}
	if !proseExtensions[strings.ToLower(pathpkg.Ext(base))] && minifiedMatches(body) {
		return SignalMinified
	}
	return ""
}

// headerWindow returns the bounded scan window: the first
// generatedHeaderLines lines or the first generatedHeaderBytes bytes,
// whichever is shorter.
func headerWindow(body []byte) []byte {
	end := len(body)
	if end > generatedHeaderBytes {
		end = generatedHeaderBytes
	}
	lines := 0
	for i := 0; i < end; i++ {
		if body[i] == '\n' {
			lines++
			if lines == generatedHeaderLines {
				end = i + 1
				break
			}
		}
	}
	return body[:end]
}

// headerMatches reports a generated marker in the header window: Go's
// convention line, the @generated token, or "generated" together with "do
// not edit" or "do not modify" anywhere in the window, case ignored.
func headerMatches(body []byte) bool {
	window := headerWindow(body)
	if goGeneratedHeader.Match(window) {
		return true
	}
	if bytes.Contains(window, []byte("@generated")) {
		return true
	}
	lower := bytes.ToLower(window)
	if !bytes.Contains(lower, []byte("generated")) {
		return false
	}
	return bytes.Contains(lower, []byte("do not edit")) || bytes.Contains(lower, []byte("do not modify"))
}

// minifiedMatches reports an average line length strictly over
// minifiedAverageLineBytes. The body is split on "\n"; a body with no
// newline is one line; an empty body does not match; a body with a NUL byte
// is binary and skips this rule only.
func minifiedMatches(body []byte) bool {
	if len(body) == 0 || bytes.IndexByte(body, 0) >= 0 {
		return false
	}
	newlines := bytes.Count(body, []byte("\n"))
	return len(body)-newlines > minifiedAverageLineBytes*(newlines+1)
}

// HeaderExcerpt quotes the first header-window line carrying a generated
// marker, rendered for a comment: backticks and control bytes removed, cut
// to excerptMaxBytes on a rune boundary with an ellipsis. It is derived from
// the same bounded window the detector read and is never stored in the
// ledger. The resolve leg calls it again on the committed head blob when it
// rewrites the review comment. An empty answer means no marker line is in
// the window.
func HeaderExcerpt(body []byte) string {
	for _, line := range strings.Split(string(headerWindow(body)), "\n") {
		if goGeneratedHeader.MatchString(line) ||
			strings.Contains(line, "@generated") ||
			strings.Contains(strings.ToLower(line), "generated") {
			return sanitizeExcerpt(line)
		}
	}
	return ""
}

// sanitizeExcerpt renders one marker line for a comment: backticks and
// control bytes out, then the byte cap with an ellipsis.
func sanitizeExcerpt(line string) string {
	var b strings.Builder
	for _, r := range line {
		if r == '`' || r < 0x20 || r == 0x7f {
			continue
		}
		b.WriteRune(r)
	}
	s := b.String()
	if len(s) <= excerptMaxBytes {
		return s
	}
	cut := excerptMaxBytes - len("…")
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}
