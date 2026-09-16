package vcs

import (
	"context"
	"fmt"
	"strings"

	"github.com/carlosboeing/crossrev/internal/core"
)

// ChangedFiles enumerates every changed path between base and head in one
// complete `git diff --name-status -z --find-renames <base>...<head>` call.
//
// The output is NUL-delimited and parsed as NUL-delimited: a path carrying a
// space is one field, not two. Each record is a status field, then one path,
// except a rename which carries the old and the new path as two fields after
// a status of the form R100. The rename's OldPath names the base evidence;
// every other kind names its content at the head, and a deletion names its
// base content through both path fields.
func (r *Repository) ChangedFiles(ctx context.Context, base, head core.Revision) ([]core.FileChange, error) {
	output, err := r.Run(ctx, "diff", "--name-status", "-z", "--find-renames", base.SHA()+"..."+head.SHA())
	if err != nil {
		return nil, err
	}
	if !output.OK() {
		return nil, fmt.Errorf("git diff --name-status exited %d: %s", output.ExitCode, output.Stderr)
	}
	return parseNameStatus(output.Stdout)
}

// parseNameStatus reads NUL-delimited --name-status output without splitting
// valid path bytes on whitespace. A trailing NUL leaves an empty final field
// which carries no record.
func parseNameStatus(stdout string) ([]core.FileChange, error) {
	fields := strings.Split(stdout, "\x00")
	var changes []core.FileChange
	for i := 0; i < len(fields); {
		status := fields[i]
		i++
		if status == "" {
			continue
		}
		kind, rename := parseStatus(status)
		if kind == "" {
			return nil, fmt.Errorf("git diff reported an unknown change status %q", status)
		}
		if rename {
			if i+1 >= len(fields) {
				return nil, fmt.Errorf("git diff ended inside a rename record for status %q", status)
			}
			oldPath, newPath := fields[i], fields[i+1]
			i += 2
			changes = append(changes, core.FileChange{OldPath: oldPath, Path: newPath, Kind: kind})
			continue
		}
		if i >= len(fields) {
			return nil, fmt.Errorf("git diff ended inside a %q record", status)
		}
		path := fields[i]
		i++
		change := core.FileChange{Path: path, Kind: kind}
		if kind == core.ChangeDeleted {
			change.OldPath = path
		}
		changes = append(changes, change)
	}
	if changes == nil {
		return nil, nil
	}
	return changes, nil
}

// parseStatus maps a --name-status letter onto the domain vocabulary. The
// rename letter carries its similarity score (R100), so only the first byte
// decides; anything else is refused rather than guessed at.
func parseStatus(status string) (core.ChangeKind, bool) {
	switch status[0] {
	case 'A':
		return core.ChangeAdded, false
	case 'M':
		return core.ChangeModified, false
	case 'D':
		return core.ChangeDeleted, false
	case 'R':
		return core.ChangeRenamed, true
	case 'T':
		return core.ChangeTypeChanged, false
	}
	return "", false
}
