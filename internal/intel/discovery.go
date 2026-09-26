package intel

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/carlosboeing/crossrev/internal/core"
)

// FileBody is what one evidence read found: the content bytes, or the reason
// there are none. Unavailable content stays a required obligation with a
// visible access limit; it is never dropped from the denominator.
type FileBody struct {
	// Data is the evidence bytes. Nil when Unavailable is set.
	Data []byte
	// Unavailable reports that no bytes could be read.
	Unavailable bool
	// Reason names the access limit. Set when Unavailable is set.
	Reason string
}

// FileReader reads one path's evidence at one revision. The tier-3 wiring
// adapts the git read to this interface; discovery itself stays pure and
// imports only core.
type FileReader interface {
	Read(ctx context.Context, revision core.Revision, path string) (FileBody, error)
}

// Exclusion is one path removed from the required denominator, with the
// reason it was removed. Existing backlog exclusions remain exclusions; no
// new exclusion setting ships with this release.
type Exclusion struct {
	// Path names the excluded file or directory.
	Path string
	// Reason says why the path is excluded.
	Reason string
}

// AttributeDecision is the base-tree .gitattributes answer for one changed
// path's linguist-generated attribute. The VCS layer reads it; discovery
// applies it.
type AttributeDecision int

const (
	// AttributeUnspecified means .gitattributes says nothing about the path,
	// so the built-in generated-file rules decide.
	AttributeUnspecified AttributeDecision = iota
	// AttributeSet means the base tree marks the path linguist-generated:
	// repository policy excludes it outright.
	AttributeSet
	// AttributeNegated means the base tree explicitly unmarks the path
	// (-linguist-generated or linguist-generated=false): every built-in
	// rule is suppressed.
	AttributeNegated
)

// Classification source names: what spoke for a path.
const (
	SourceGitattributes = "gitattributes"
	SourceBuiltin       = "built-in"
)

// ClassEffect is what a classification does to a path.
type ClassEffect int

const (
	// EffectPlain reviews the file when it fits and halts the pass when it
	// does not, exactly as an unrecognised file behaves.
	EffectPlain ClassEffect = iota
	// EffectExcluded removes the path from the required set before its body
	// is read.
	EffectExcluded
	// EffectGenerated keeps the path required; packing skips it with a
	// visible warning when it cannot fit one rendered prompt.
	EffectGenerated
)

// Classification is the generated-file decision for one changed path: which
// source spoke, the rule it named, and the effect on the pass. Detectors,
// the base-tree attribute read and any later policy source join here, so one
// function holds the precedence.
type Classification struct {
	// Source names what decided: "gitattributes" or "built-in". Empty for an
	// ordinary file.
	Source string
	// Rule names the deciding rule: "linguist-generated" or a signal name.
	// Empty for an ordinary file.
	Rule string
	// Effect is what happens to the path.
	Effect ClassEffect
}

// GeneratedAttributeReason is the exclusion reason recorded for a path the
// base tree marks linguist-generated.
const GeneratedAttributeReason = "generated: linguist-generated in .gitattributes"

// ClassifyGenerated joins the base-tree attribute answer with the built-in
// evidence signal. A set attribute excludes, whatever the evidence says; a
// negated attribute suppresses every built-in signal; an unspecified
// attribute lets the signal speak.
func ClassifyGenerated(attr AttributeDecision, signal string) Classification {
	switch attr {
	case AttributeSet:
		return Classification{Source: SourceGitattributes, Rule: "linguist-generated", Effect: EffectExcluded}
	case AttributeNegated:
		return Classification{Source: SourceGitattributes, Rule: "linguist-generated", Effect: EffectPlain}
	}
	if signal != "" {
		return Classification{Source: SourceBuiltin, Rule: signal, Effect: EffectGenerated}
	}
	return Classification{Effect: EffectPlain}
}

// FileUnit is one required file: its identity, its change, and the evidence
// the reviewer must account for.
type FileUnit struct {
	// ID is the u1 file identity over the current path.
	ID core.UnitID
	// Path is the current path the required set sorts by.
	Path string
	// OldPath is the previous path for a rename or a deletion.
	OldPath string
	// Change is how the path changed between the two revisions.
	Change core.ChangeKind
	// ContentRevision is where the evidence bytes were read: the base for a
	// deletion, the head for every other kind.
	ContentRevision core.Revision
	// BodyDigest is the full SHA-256 hex over the evidence bytes, or over
	// the empty string when no bytes were available.
	BodyDigest string
	// Body is the evidence bytes. Nil when the unit is unavailable.
	Body []byte
	// Available reports whether evidence bytes were read.
	Available bool
	// Binary reports a NUL byte in the evidence, git's own binary signal.
	Binary bool
	// Generated holds the built-in generated-file signal that matched the
	// evidence, or empty when no rule matched or no bytes were available.
	Generated string
	// Reason names the access limit when the unit is unavailable.
	Reason string
}

// Scope is the required file set for one revision pair and engine, plus the
// exclusions that were visibly removed from its denominator.
type Scope struct {
	// Base and Head are the revision pair the enumeration ran between.
	Base core.Revision
	Head core.Revision
	// Engine is the file engine version, and EngineID its manifest identity.
	Engine   string
	EngineID string
	// Required holds every non-excluded change, sorted by current path.
	Required []FileUnit
	// Excluded holds every removed path and its reason, sorted by path.
	Excluded []Exclusion
	// Skipped holds the generated units packing skipped, in path order, with
	// their bodies still available for rendering the warning. Each appears in
	// Excluded with its reason. Empty until packing runs: discovery never
	// fills it.
	Skipped []FileUnit
}

// RequiredFiles builds the required file set from one complete enumeration.
// It sorts by current path, reads a deletion's evidence at the base through
// its old path and every other kind at the head, keeps binary and
// unavailable files as obligations with a visible limit, and records
// exclusions by path and reason outside the required denominator.
//
// attrs is the base-tree linguist-generated answer per current path. A set
// answer excludes the path outright, before its body is read, and the
// current path alone decides: a file renamed out of a marked directory is a
// new review obligation. A negated answer suppresses the built-in signal.
//
// A read failure for one path degrades that unit to unavailable; it never
// fails the whole set. Two changes resolving to one UnitID fail the pass
// with both paths named, because a single verdict must never stand for
// two units.
func RequiredFiles(ctx context.Context, changes []core.FileChange, read FileReader, base, head core.Revision, excluded []Exclusion, attrs map[string]AttributeDecision) (Scope, error) {
	scope := Scope{Base: base, Head: head, Engine: core.FileEngineVersion, EngineID: core.FileEngineID()}
	seen := make(map[core.UnitID]string, len(changes))
	for _, change := range changes {
		if matchExclusion(change, excluded) {
			scope.Excluded = append(scope.Excluded, exclusionFor(change, excluded))
			continue
		}
		if ClassifyGenerated(attrs[change.Path], "").Effect == EffectExcluded {
			scope.Excluded = append(scope.Excluded, Exclusion{Path: change.Path, Reason: GeneratedAttributeReason})
			continue
		}
		unit := FileUnit{
			ID:      core.FileUnitID(change.Path),
			Path:    change.Path,
			OldPath: change.OldPath,
			Change:  change.Kind,
		}
		if first, ok := seen[unit.ID]; ok {
			return Scope{}, fmt.Errorf("two required files share unit id %q: %q and %q", unit.ID, first, unit.Path)
		}
		seen[unit.ID] = unit.Path
		revision, path := head, change.Path
		if change.Kind == core.ChangeDeleted {
			revision, path = base, change.OldPath
		}
		unit.ContentRevision = revision
		body, err := read.Read(ctx, revision, path)
		if err != nil {
			body = FileBody{Unavailable: true, Reason: err.Error()}
		}
		if body.Unavailable {
			unit.Reason = body.Reason
			unit.BodyDigest = core.BodyDigestHex(nil)
			scope.Required = append(scope.Required, unit)
			continue
		}
		unit.Available = true
		unit.Body = body.Data
		unit.Binary = bytes.IndexByte(body.Data, 0) >= 0
		unit.BodyDigest = core.BodyDigestHex(body.Data)
		signal := ""
		if attrs[change.Path] == AttributeUnspecified {
			signal = GeneratedSignal(unit.Path, body.Data)
		}
		if classification := ClassifyGenerated(attrs[change.Path], signal); classification.Effect == EffectGenerated {
			unit.Generated = classification.Rule
		}
		scope.Required = append(scope.Required, unit)
	}
	sort.Slice(scope.Required, func(i, j int) bool { return scope.Required[i].Path < scope.Required[j].Path })
	sort.Slice(scope.Excluded, func(i, j int) bool { return scope.Excluded[i].Path < scope.Excluded[j].Path })
	return scope, nil
}

// matchExclusion reports whether either side of a change names an excluded
// path, so a rename out of an excluded directory stays excluded. The
// comparison is literal — a path equal to the pattern or below it as a
// directory — mirroring the diff exclusion rule.
func matchExclusion(change core.FileChange, excluded []Exclusion) bool {
	for _, e := range excluded {
		pattern := strings.TrimRight(e.Path, "/")
		if pattern == "" {
			continue
		}
		for _, path := range []string{change.Path, change.OldPath} {
			if path == pattern || strings.HasPrefix(path, pattern+"/") {
				return true
			}
		}
	}
	return false
}

// exclusionFor records the changed path under the first matching pattern's
// reason.
func exclusionFor(change core.FileChange, excluded []Exclusion) Exclusion {
	for _, e := range excluded {
		pattern := strings.TrimRight(e.Path, "/")
		if pattern == "" {
			continue
		}
		for _, path := range []string{change.Path, change.OldPath} {
			if path == pattern || strings.HasPrefix(path, pattern+"/") {
				return Exclusion{Path: change.Path, Reason: e.Reason}
			}
		}
	}
	return Exclusion{Path: change.Path}
}
