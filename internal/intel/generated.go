package intel

import (
	"bytes"
	pathpkg "path"
	"regexp"
	"strings"
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
// generated minified files. Here it applies to every extension, because a
// bundle embedded in another file type has the same shape.
const minifiedAverageLineBytes = 110

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
	if minifiedMatches(body) {
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
	lines := bytes.Count(body, []byte("\n")) + 1
	return len(body) > minifiedAverageLineBytes*lines
}
