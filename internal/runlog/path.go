package runlog

import (
	"os"
	"strconv"
	"strings"
)

// dirMode and fileMode are the two permissions everything under the run
// directory is created with.
//
// They are passed to the create call rather than applied afterwards, which is
// the property lib/log.sh:51-59 buys with a subshell umask instead of
// create-then-chmod: mkdir(2) and open(2) both apply the mode as the inode
// appears, so nothing ever reads a file or a directory that was briefly wider.
// A umask can only clear bits, so a hostile umask makes these narrower and
// never wider.
const (
	dirMode  os.FileMode = 0o700
	fileMode os.FileMode = 0o600
)

// RunID is the id this run is known by, and it is the same one the pull request
// markers carry, so `crossrev status` naming a run and the directory on disk
// agree (log_run_id, lib/log.sh:41).
func RunID() string {
	if id := os.Getenv("GITHUB_RUN_ID"); id != "" {
		return id
	}
	return "local-" + strconv.Itoa(os.Getpid())
}

// StateHome is where CrossRev keeps state that survives a run, resolved the way
// the XDG base directory specification says (lib/log.sh:48). An unset and an
// empty XDG_STATE_HOME are the same answer, because Bash's `${X:-default}`
// treats them alike.
func StateHome() string {
	if base := os.Getenv("XDG_STATE_HOME"); base != "" {
		return base
	}
	return os.Getenv("HOME") + "/.local/state"
}

// RunsBase is the directory every run record lives under, and the one the
// retention sweep walks (lib/log.sh:195).
func RunsBase() string {
	return StateHome() + "/crossrev/runs"
}

// Slug turns a repository name into one path segment by replacing every
// separator, so `owner/repo` names one directory rather than two, and a name
// with more than one separator still names one (lib/log.sh:46).
func Slug(repo string) string {
	return strings.ReplaceAll(repo, "/", "-")
}

// RunDir is the per-run directory for a repository and pull request. Printed,
// not created — Open is what creates it (log_run_dir, lib/log.sh:44).
//
// The segments are concatenated rather than joined. The Bash function builds
// the path with printf, so an XDG_STATE_HOME that ends in a separator produces
// a doubled one, and path.Join or filepath.Join would clean that away and
// disagree with the frozen vector that records it.
func RunDir(repo, pr string) string {
	return RunsBase() + "/" + Slug(repo) + "/pr-" + pr + "/" + RunID()
}

// CreatePrivate creates or truncates a file that is 0600 from birth, so a
// caller that then writes into it never widens it and never finds it wider
// (log_create_private, lib/log.sh:55).
//
// An existing file keeps the mode it already has, exactly as the Bash
// redirection does: open(2) applies the mode on creation only.
func CreatePrivate(path string) error {
	if path == "" {
		return nil
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, fileMode)
	if err != nil {
		return err
	}
	return f.Close()
}
