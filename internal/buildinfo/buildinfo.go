package buildinfo

import (
	"errors"
	"fmt"
	"runtime/debug"

	"github.com/carlosboeing/crossrev/internal/core"
)

// Info is what the binary knows about how it was built.
//
// Every field can be empty. go run, go test and a build from an exported
// tarball all produce a binary with no VCS stamp at all, and ordinary commands
// still run from one.
type Info struct {
	// Version is the main module's version, `(devel)` for an untagged build.
	Version string
	// Revision is `vcs.revision`, or empty where the build carries no stamp.
	Revision string
	// Modified is `vcs.modified`: the tree had uncommitted changes.
	Modified bool
	// Time is `vcs.time`, the commit's timestamp.
	Time string
}

// LocalBuild is set by -ldflags when scripts/install-local.sh builds the
// binary, and is empty for every other build including the released ones.
//
// It exists because a clean local build and a release build of the same commit
// are identical to Go: same vcs.revision, same vcs.modified. Nothing at runtime
// can tell them apart, so the local installer says so at build time. QuotaCap
// marks its own output the same way, through QUOTACAP_DEV_BUILD.
var LocalBuild string

// VersionOverride is set by -ldflags from CROSSREV_VERSION_OVERRIDE, and
// replaces the whole version string. It mirrors QUOTACAP_VERSION_OVERRIDE:
// nothing sets it yet, and it exists so the convention has no hole in it.
var VersionOverride string

// Stamped reports whether the build carries a VCS revision.
func (i Info) Stamped() bool { return i.Revision != "" }

// Read returns what this binary was built from.
func Read() Info {
	bi, ok := debug.ReadBuildInfo()
	return read(bi, ok)
}

// ErrRevisionAbsent is returned when the build carries no VCS revision.
var ErrRevisionAbsent = errors.New("this build carries no VCS revision")

// ErrRevisionModified is returned when the build was made from a tree with
// uncommitted changes.
var ErrRevisionModified = errors.New("this build was made from a modified tree")

// Pin returns the revision a generated workflow may pin to.
//
// It refuses an absent revision and a modified one. A workflow pinned to
// nothing is worse than one that was never generated, and a workflow pinned to
// a revision the checkout does not actually hold names code nobody can fetch.
// The two refusals are distinguishable because only one of them is fixed by
// committing.
func Pin() (string, error) { return pin(Read()) }

// read is the pure half of Read, over a build info the caller supplies.
func read(bi *debug.BuildInfo, ok bool) Info {
	if !ok || bi == nil {
		return Info{}
	}
	info := Info{Version: bi.Main.Version}
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			info.Revision = s.Value
		case "vcs.modified":
			// Go stamps the literal strings "true" and "false". Anything else
			// is read as unmodified rather than guessed at.
			info.Modified = s.Value == "true"
		case "vcs.time":
			info.Time = s.Value
		}
	}
	return info
}

// pin is the pure half of Pin.
func pin(info Info) (string, error) {
	if !info.Stamped() {
		return "", ErrRevisionAbsent
	}
	if info.Modified {
		return "", ErrRevisionModified
	}
	// Validated through the domain type rather than a second hex test here, so
	// a pin and a marker's head_sha can never disagree about what a revision is.
	if _, err := core.NewRevision(info.Revision); err != nil {
		return "", fmt.Errorf("the VCS revision is unusable: %w", err)
	}
	return info.Revision, nil
}
