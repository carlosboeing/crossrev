package sandbox

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// QuarantineDir is where repository-provided harness configuration is moved to.
// It is CROSSREV_QUARANTINE at lib/sandbox.sh:37.
const QuarantineDir = ".crossrev-quarantine"

// ErrDescriptor is returned when the harness descriptor cannot be read, or
// names a path this package must not act on.
var ErrDescriptor = errors.New("the harness descriptor cannot be used")

// Descriptor is the part of the harness descriptor this package reads.
//
// Deliberately partial. lib/harnesses.sh validates the whole document at load
// and fails closed, and porting that validation belongs with the descriptor
// itself; what is checked here is the one predicate that governs the values
// this package hands to a move and a recursive delete.
type Descriptor struct {
	QuarantineShared []string          `json:"quarantine_shared"`
	Harnesses        []DescriptorEntry `json:"harnesses"`
}

// DescriptorEntry is one harness, as far as the sandbox is concerned.
type DescriptorEntry struct {
	Name        string   `json:"name"`
	Quarantine  []string `json:"quarantine"`
	SandboxArgs []string `json:"sandbox_args"`
}

// LoadDescriptor decodes the descriptor and refuses every quarantine path that
// is absolute, empty, or carries a `..` segment.
//
// The refusal is the `relative($p)` predicate of harness_validate
// (lib/harnesses.sh:31-33), applied here because this is the package that acts
// on the values: lib/harnesses.sh:5-8 states the reason plainly — the
// descriptor names quarantine paths handed to `mv` and `rm -rf`, so a malformed
// entry reaches a side effect.
func LoadDescriptor(raw []byte) (Descriptor, error) {
	var descriptor Descriptor
	if err := json.Unmarshal(raw, &descriptor); err != nil {
		return Descriptor{}, fmt.Errorf("%w: %w", ErrDescriptor, err)
	}
	for _, entry := range descriptor.Harnesses {
		for _, path := range entry.Quarantine {
			if !isRelativePath(path) {
				return Descriptor{}, refuseQuarantinePath(path)
			}
		}
	}
	for _, path := range descriptor.QuarantineShared {
		if !isRelativePath(path) {
			return Descriptor{}, refuseQuarantinePath(path)
		}
	}
	return descriptor, nil
}

func refuseQuarantinePath(path string) error {
	return fmt.Errorf("%w: quarantine path %q is absolute, empty, or contains a .. segment", ErrDescriptor, path)
}

// isRelativePath is jq's `relative($p)`: a non-empty string that does not start
// with a slash and holds no `..` segment.
func isRelativePath(path string) bool {
	if path == "" || strings.HasPrefix(path, "/") {
		return false
	}
	for _, segment := range strings.Split(path, "/") {
		if segment == ".." {
			return false
		}
	}
	return true
}

// Paths is every path a harness is known to load from a working directory,
// sorted and deduplicated. It is _sandbox_paths at lib/sandbox.sh:46-49.
//
// Deliberately over-broad and deliberately not exhaustive. This list is a
// best-effort layer, not the thing standing between an injected hook and the
// App token — that is the credential separation, which leaves the agent process
// holding no GitHub credential at all, so an injection that reaches tool use
// still cannot post as the App, push a commit, or read a secret
// (lib/sandbox.sh:39-45).
//
// The order is jq's `unique`, which sorts by codepoint and drops duplicates.
// Every path in the descriptor is ASCII, so a byte sort is the same sort.
func (d Descriptor) Paths() []string {
	seen := make(map[string]struct{})
	var paths []string
	add := func(path string) {
		if _, already := seen[path]; already {
			return
		}
		seen[path] = struct{}{}
		paths = append(paths, path)
	}
	for _, entry := range d.Harnesses {
		for _, path := range entry.Quarantine {
			add(path)
		}
	}
	for _, path := range d.QuarantineShared {
		add(path)
	}
	sort.Strings(paths)
	return paths
}

// ArgsFor is the arguments that harden a harness invocation without costing the
// billing model. It is sandbox_args_for at lib/sandbox.sh:104-107.
//
// Empty for Claude Code, and that is the isolation the design pays for: `--bare`
// skips hooks, plugin sync, auto-memory and instruction-file auto-discovery,
// which is exactly right — and it also refuses subscription auth, so on that
// harness there is project-config isolation or subscription billing and not
// both (lib/sandbox.sh:11-17).
//
// A list rather than the shell's space-joined string. The join exists because
// the adapters word-split it; an argv runner needs no split, and a joined
// string is how an argument containing a space stops being one argument.
func (d Descriptor) ArgsFor(harness string) []string {
	for _, entry := range d.Harnesses {
		if entry.Name == harness {
			return entry.SandboxArgs
		}
	}
	return nil
}

// resolveInside returns the absolute path a repository-relative entry names
// under root, and refuses one that would land outside it.
//
// The descriptor is validated at load, so nothing the shipped tool carries
// reaches the refusal. It is here because Quarantine and Restore are the two
// functions in this codebase that move and delete on a path they were handed,
// and a guard at the point of the side effect is the one that cannot be
// bypassed by a caller that skipped the loader.
func resolveInside(root, path string) (string, error) {
	if !isRelativePath(path) {
		return "", refuseQuarantinePath(path)
	}
	cleanRoot := filepath.Clean(root)
	target := filepath.Join(cleanRoot, path)
	if target == cleanRoot || !strings.HasPrefix(target, cleanRoot+string(filepath.Separator)) {
		return "", refuseQuarantinePath(path)
	}
	return target, nil
}
