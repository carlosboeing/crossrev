package config

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/carlosboeing/crossrev/internal/core"
)

// FileStatus is what a read found at a path.
//
// Three states rather than two, because the Bash implementation asks three
// different questions of the same kind of path.
//
//   - Working-tree configuration is read behind `[[ -f ]]` (lib/config.sh:37),
//     so a directory at `.github/crossrev.yml` states no policy and the next
//     file is tried.
//   - Base-revision configuration is read with `git show`
//     (lib/config.sh:93-95), which succeeds on a tree and prints its listing.
//     yq reads that listing as a plain multi-line string, so the file is found
//     and then refused for its shape rather than skipped.
//   - Backlog discovery is read behind `[[ -e ]]` (lib/config.sh:443), so
//     anything at `backlog/config.yml` counts as the convention being
//     installed.
type FileStatus int

const (
	// NotFound is nothing at the path.
	NotFound FileStatus = iota
	// IsFile is a regular file at the path, or a blob at the revision. The
	// content is its bytes, which may be empty.
	IsFile
	// IsOther is a path that exists but holds no file content, such as a
	// directory or a tree.
	IsOther
)

// ShowFile reads one path, at a revision or from the working tree.
//
// It is a function type owned by this package so that `config` imports no other
// tier-2 package: the git-backed implementation lands in Task 2.3 and the unit
// tests here supply bytes directly.
//
// A zero revision means the working tree, which is what `init` and `doctor`
// read (lib/config.sh:134-136). A non-zero revision means the pull request's
// base revision, which is where every leg reads policy from and never the head
// (ADR 0003, lib/config.sh:50-55).
//
// A zero-revision read is therefore a plain filesystem read, and the path it is
// given is not always repository-relative: Load reads the operator layer at
// OperatorPath(), an absolute path outside the repository, with a zero revision
// however the run was invoked (lib/config.sh:166). An implementation that
// resolved every path against the checkout would drop that layer silently, and
// every operator-only endpoint would then refuse with "defined nowhere" —
// which reads as a config error rather than the plumbing error it is.
type ShowFile func(ctx context.Context, revision core.Revision, path string) ([]byte, FileStatus, error)

// Config is one loaded configuration: the two layers that produced it, and the
// merge every reader asks questions of.
//
// The three are kept separately as well as merged for the reason
// lib/config.sh:12-19 gives: `init` needs to know what the repository itself
// declared rather than what it inherited.
type Config struct {
	// Repo is the repository layer, as decoded.
	Repo *Object
	// Operator is the operator file layer, as decoded.
	Operator *Object
	// Merged is the defaults, the repository layer over them, and the
	// operator file's endpoints over those.
	Merged *Object

	show ShowFile
}

// repoConfigPaths are the two repository configuration files, in the order
// cfg_load tries them at lib/config.sh:147-160. The first one found wins, and a
// broken first file is named rather than skipped in favour of the second.
var repoConfigPaths = []string{".github/crossrev.yml", ".crossrev.yml"}

// Load reads both configuration layers, checks their versions, merges them and
// refuses every value family the Bash implementation refuses.
//
// It reproduces cfg_load at lib/config.sh:137-191. A zero base revision reads
// the working tree, which is what `init` and `doctor` do; every leg passes the
// pull request's base revision instead.
func Load(ctx context.Context, base core.Revision, show ShowFile) (*Config, error) {
	repoLayer, err := loadRepoLayer(ctx, base, show)
	if err != nil {
		return nil, err
	}

	operatorPath := OperatorPath()
	operatorSource, status, err := show(ctx, core.Revision{}, operatorPath)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", operatorPath, err)
	}
	operatorLayer := NewObject()
	if status == IsFile {
		operatorLayer, err = decodeDocument(operatorSource)
		if err != nil {
			// The operator file is always read from the working tree, so it
			// takes the working-tree form of every refusal in the family,
			// under its own path (lib/config.sh:213-214).
			return nil, refuseDocument(err, operatorPath, core.Revision{})
		}
	}

	// Both layer versions are checked before the merge, so a future shape is
	// refused rather than merged into this one. The repository layer is named
	// `.github/crossrev.yml` whichever of the two files it came from, which is
	// what lib/config.sh:168 composes.
	where := repoConfigPaths[0]
	if !base.IsZero() {
		where = "base revision " + where
	}
	if err := checkVersion(repoLayer, where); err != nil {
		return nil, err
	}
	if err := checkVersion(operatorLayer, operatorPath); err != nil {
		return nil, err
	}

	// Repository policy over defaults; then endpoints merged by name with the
	// operator file winning, so a repository can declare a public endpoint
	// while you point the same name at your own instance locally without
	// touching the repository (lib/config.sh:174-184).
	merged := deepMerge(Defaults(), repoLayer)
	repoEndpoints, err := endpointsOf(merged, "endpoints")
	if err != nil {
		return nil, err
	}
	operatorEndpoints, err := endpointsOf(operatorLayer, "endpoints")
	if err != nil {
		return nil, err
	}
	merged.Set("endpoints", deepMerge(repoEndpoints, operatorEndpoints))

	loaded := &Config{Repo: repoLayer, Operator: operatorLayer, Merged: merged, show: show}
	for _, assert := range []func() error{
		loaded.assertMinFixSeverity,
		loaded.assertMaxPassesPerCycle,
		loaded.assertGitHooks,
		loaded.assertLogs,
		loaded.assertBacklog,
	} {
		if err := assert(); err != nil {
			return nil, err
		}
	}
	return loaded, nil
}

// loadRepoLayer reads the repository layer from the base revision when there is
// one, and from the working tree when there is not.
func loadRepoLayer(ctx context.Context, base core.Revision, show ShowFile) (*Object, error) {
	for _, path := range repoConfigPaths {
		source, status, err := show(ctx, base, path)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", path, err)
		}
		if status == IsOther {
			// A tree is absent to `[[ -f ]]` and present to `git show`, which
			// prints its listing. yq reads the listing as one multi-line
			// string, the shape test refuses it, and the second file is never
			// reached (lib/config.sh:93-95, 48-81).
			if base.IsZero() {
				continue
			}
			return nil, refuseNotMappingAtBase(path, base)
		}
		if status != IsFile {
			continue
		}
		// Which file was found has to be tracked separately from its bytes.
		// A file that exists at the base revision and holds nothing states no
		// policy, which is the same answer as no file at all, so the text
		// alone cannot say whether there was a file to parse
		// (lib/config.sh:142-145 and lib/config.sh:67-68).
		layer, err := decodeDocument(source)
		if err != nil {
			// Every refusal in the family names the file that was actually
			// read rather than the composed `.github/crossrev.yml` the version
			// refusal uses, because this one is about the bytes on the other
			// end of that path (lib/config.sh:199-208).
			return nil, refuseDocument(err, path, base)
		}
		return layer, nil
	}
	return NewObject(), nil
}

// endpointsOf reads one layer's `endpoints` as the mapping the merge needs, and
// refuses anything else by name.
//
// It is `($x.endpoints // {})` with the type error the multiplication after it
// raises. jq's `//` reads null and false as absent, so both become an empty
// mapping and the layer contributes nothing; a string, a list, a number or
// `true` reaches `*` and stops the run there with jq's own message
// (lib/config.sh:181-184).
//
// Reading it leniently is the worst of the family this refusal covers. Bash
// will not run at all, and Go ran with every named endpoint dropped — so the
// next Endpoint call reported the name as defined nowhere, which reads as a
// config error when the truth is that a whole layer was discarded. That is the
// substitution endpoint.go says must never happen.
func endpointsOf(layer *Object, where string) (*Object, error) {
	value := layer.Value("endpoints")
	if boolean, ok := value.(bool); ok && !boolean {
		return NewObject(), nil
	}
	if err := requireMapping(value, where); err != nil {
		return nil, err
	}
	if nested, _ := value.(*Object); nested != nil {
		return nested, nil
	}
	return NewObject(), nil
}

// requireMapping refuses a key that holds something jq cannot read keys out of.
//
// Every nested value the loader reaches is read by a jq path expression, and
// jq raises a type error rather than answering null when the container is not
// one: `.policy.min_fix_severity` against `policy: "x"` is `Cannot index string
// with string`, and the run stops with nothing loaded. Go answered null, so a
// configuration Bash refuses to run loaded here — silently for `backlog`, and
// under a refusal naming the wrong key for `policy`, `git` and `logs`.
//
// null is not refused, because `null.foo` is null in jq and every one of these
// keys has a default underneath it.
//
// The message names the key rather than reproducing jq's, which says what type
// it found and never where it found it. In a config of any length that is the
// half a reader needs, and it is the half the shell does not give them.
func requireMapping(value any, where string) error {
	if value == nil {
		return nil
	}
	if _, ok := value.(*Object); ok {
		return nil
	}
	return &Refusal{
		Message: where + " is " + shapeOf(value) + ", which is not a mapping",
		Hint:    "CrossRev reads configuration keys out of " + where + ", so it must hold keys of its own rather than a list or a single value. Correct it where it is set, or remove it to take the defaults.",
	}
}

// requireMappingAt is requireMapping over a dotted path into the merge.
func requireMappingAt(root *Object, path string) error {
	return requireMapping(lookup(root, path), strings.TrimPrefix(path, "."))
}

// shapeOf names a value's shape in the words a config file is written in.
func shapeOf(value any) string {
	switch value.(type) {
	case []any:
		return "a list"
	case string:
		return "a string"
	case Number:
		return "a number"
	case bool:
		return "true or false"
	default:
		return "a single value"
	}
}

// refuseDocument names the file a decodeDocument error came from, and picks the
// refusal that says which of the four faults it was.
//
// Four rather than two, because a reader has to be told what to change. A file
// that will not parse, a file that parses into something other than a mapping,
// a file whose anchor names itself and a file that nests past what any reader
// follows are four different edits, and the last two used to arrive under the
// first one's text — which offers a `yq '.'` command that answers the file
// perfectly well.
func refuseDocument(err error, path string, base core.Revision) *Refusal {
	switch {
	case errors.Is(err, errAliasCycle):
		return refuseAliasCycle(path, base)
	case errors.Is(err, errTooDeep):
		return refuseTooDeep(path, base)
	case errors.Is(err, errNotMapping):
		if base.IsZero() {
			return refuseNotMapping(path)
		}
		return refuseNotMappingAtBase(path, base)
	case base.IsZero():
		return refuseUnparsable(path)
	default:
		return refuseUnparsableAtBase(path, base)
	}
}

// refuseAliasCycle refuses a file holding an anchor that is named from inside
// its own value.
//
// This is a deliberate divergence, and the only one in the family. yq loads
// some of these files: `m: &a` / `  b: *a` prints a two-level expansion and
// exits 0. It refuses others written the same way — a second `*a` beside the
// first, or a `<<: *a` one level down, overflow yq's own stack and it exits 2,
// which the Bash reads as unparsable. The loaded answer is an artefact of yq
// rewriting the anchored node while it expands it rather than a value the
// document states, so there is nothing here to match and every cycle is refused
// instead.
//
// The message says what is wrong rather than borrowing the unparsable text,
// because that refusal's hint would send the reader to a `yq '.'` that prints a
// mapping and tells them nothing.
func refuseAliasCycle(path string, base core.Revision) *Refusal {
	return &Refusal{
		Message: "an anchor in " + path + atRevision(base) + " refers to itself",
		Hint:    "An anchor named from inside its own value has no end, so it states no configuration that can be read back. Break the loop where the anchor is defined.",
	}
}

// refuseTooDeep refuses a file nested past what this reader follows.
func refuseTooDeep(path string, base core.Revision) *Refusal {
	return &Refusal{
		Message: fmt.Sprintf("%s%s nests more than %d levels deep", path, atRevision(base), maxDecodeDepth),
		Hint:    "CrossRev follows a bounded depth of nesting, anchors and merge keys included. Flatten it.",
	}
}

// atRevision is the clause a base-revision refusal adds to say which bytes it
// read.
func atRevision(base core.Revision) string {
	if base.IsZero() {
		return ""
	}
	return " at base revision " + base.SHA()
}

// refuseUnparsable refuses a working-tree file that will not parse
// (_cfg_refuse_unparsable, lib/config.sh:43-46).
func refuseUnparsable(path string) *Refusal {
	return &Refusal{
		Message: "could not parse " + path,
		Hint:    "It must be valid YAML. Check it with: yq '.' " + path,
	}
}

// refuseUnparsableAtBase refuses a base-revision file that will not parse.
//
// Separate from refuseUnparsable because the hint has to read the revision
// rather than the working tree. The file on disk may parse, may differ from
// what the base revision holds, or may not be there at all, so `yq '.'
// .github/crossrev.yml` would check the wrong bytes
// (_cfg_refuse_unparsable_at_base, lib/config.sh:75-85).
func refuseUnparsableAtBase(path string, base core.Revision) *Refusal {
	return &Refusal{
		Message: "could not parse " + path + " at base revision " + base.SHA(),
		Hint:    "It must be valid YAML. Check it with: git show " + base.SHA() + ":" + path + " | yq '.'",
	}
}

// refuseNotMapping refuses a working-tree file that parses and holds something
// other than a mapping (_cfg_refuse_not_mapping, lib/config.sh:78-81).
//
// Separate from refuseUnparsable because the two are different faults and a
// reader has to be told which one they have: the file is valid YAML, and what
// is wrong is that its top level is a list or a single value rather than
// configuration keys.
func refuseNotMapping(path string) *Refusal {
	return &Refusal{
		Message: path + " is not a mapping",
		Hint:    "It must hold configuration keys at its top level, not a list or a single value. Check it with: yq '.' " + path,
	}
}

// refuseNotMappingAtBase refuses a base-revision file that is not a mapping.
//
// Separate from refuseNotMapping for the reason refuseUnparsableAtBase gives:
// the hint has to read the revision rather than the working tree
// (_cfg_refuse_not_mapping_at_base, lib/config.sh:124-129).
func refuseNotMappingAtBase(path string, base core.Revision) *Refusal {
	return &Refusal{
		Message: path + " is not a mapping at base revision " + base.SHA(),
		Hint:    "It must hold configuration keys at its top level, not a list or a single value. Check it with: git show " + base.SHA() + ":" + path + " | yq '.'",
	}
}

// OperatorPath is the operator configuration file, which is cross-project and
// deliberately outside the repository (_cfg_operator_path, lib/config.sh:23).
//
// Two files, because endpoints and policy live at different layers. Policy is
// repository-specific and belongs in the repository. Endpoint URLs are
// cross-project, and some of them are meaningless on a GitHub-hosted runner, so
// committing those asserts something false for half the places the config is
// read (lib/config.sh:4-8).
func OperatorPath() string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		base = filepath.Join(os.Getenv("HOME"), ".config")
	}
	return filepath.Join(base, "crossrev", "config.yml")
}
