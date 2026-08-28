package sandbox

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/carlosboeing/crossrev/internal/vcs"
)

// Restore puts everything back, so the checkout is the pull request's own again
// before anything is committed. It is sandbox_restore at lib/sandbox.sh:77-100.
//
// Without it the resolver commits the quarantine. It therefore runs before any
// commit snapshot is taken, and the shell runs it from the cleanup trap as well
// (lib/run.sh:94) so an interrupted leg hands the checkout back intact.
//
// # The clobber warning, and why it is not silent
//
// Anything sitting at a quarantined path when the restore runs was written
// blind: the quarantine moved the real file away before the harness started, so
// the agent never read it. Discarding that write is the correct outcome —
// letting a pull request's own instructions survive the quarantine is precisely
// what the quarantine exists to stop — but it must not be silent. A finding the
// resolver "fixed" by writing there is reported as fixed, lands in no commit,
// and the "reported fixes but changed no files" guard stays quiet because other
// files did change (lib/sandbox.sh:83-90).
//
// Returns the clobbered paths in descriptor order and the warning that names
// them, or nil when there were none.
func Restore(root string, paths []string) ([]string, *vcs.Warning, error) {
	quarantine := filepath.Join(root, QuarantineDir)
	if info, err := os.Stat(quarantine); err != nil || !info.IsDir() {
		// Nothing was quarantined here. Stranded quarantine elsewhere stays
		// where it is and stays discoverable; this removes only what it
		// restored from.
		return nil, nil, nil
	}

	var clobbered []string
	for _, path := range paths {
		held, err := resolveInside(quarantine, path)
		if err != nil {
			return clobbered, nil, err
		}
		if !existsExactly(held) {
			continue
		}
		destination, err := resolveInside(root, path)
		if err != nil {
			return clobbered, nil, err
		}
		if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
			return clobbered, nil, err
		}
		if existsExactly(destination) {
			clobbered = append(clobbered, path)
		}
		if err := os.RemoveAll(destination); err != nil {
			return clobbered, nil, err
		}
		if err := os.Rename(held, destination); err != nil {
			return clobbered, nil, err
		}
	}

	if err := os.RemoveAll(quarantine); err != nil {
		return clobbered, nil, err
	}
	if len(clobbered) == 0 {
		return nil, nil, nil
	}
	return clobbered, &vcs.Warning{
		Message: "the harness wrote to quarantined path(s): " + strings.Join(clobbered, " "),
		Hint:    "Those writes were discarded when the checkout was restored, so any finding reported as fixed by editing them is not fixed and is in no commit. Check those findings by hand.",
	}, nil
}
