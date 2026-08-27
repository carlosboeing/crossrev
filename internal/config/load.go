package config

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/carlosboeing/crossrev/internal/core"
)

// FileStatus is what a read found at a path.
//
// Three states rather than two, because the Bash implementation asks two
// different questions. Configuration is read behind `[[ -f ]]`
// (lib/config.sh:37), so a directory at `.github/crossrev.yml` states no
// policy; backlog discovery is read behind `[[ -e ]]` (lib/config.sh:443), so
// anything at `backlog/config.yml` counts as the convention being installed.
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
			return nil, refuseUnparsable(operatorPath)
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
	merged.Set("endpoints", deepMerge(objectAt(merged, "endpoints"), objectAt(operatorLayer, "endpoints")))

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
			if base.IsZero() {
				return nil, refuseUnparsable(path)
			}
			return nil, refuseUnparsableAtBase(path, base)
		}
		return layer, nil
	}
	return NewObject(), nil
}

// objectAt reads one key as an object, treating anything else as empty. jq
// would raise a type error on `endpoints:` holding a scalar; Go declines to
// crash over it and merges nothing instead.
func objectAt(layer *Object, key string) *Object {
	if nested := layer.Object(key); nested != nil {
		return nested
	}
	return NewObject()
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
