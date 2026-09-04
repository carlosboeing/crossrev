// Package runlog manages per-run logging, transcripts, redaction, and log aging.
//
// Every run leaves a directory beside the worktrees CrossRev already keeps
// (lib/log.sh:1-24):
//
//	${XDG_STATE_HOME:-$HOME/.local/state}/crossrev/runs/<repo-slug>/pr-<n>/<run id>/
//
// holding a run log and, while a failure is being looked at, the harness
// transcripts. The run id is the same one the pull request marker carries, so
// the file a marker names is the file on disk.
//
// Two rules run through everything written here:
//
//  1. No credential reaches either file. The harness process holds no GitHub
//     token (ADR 0001), and anything captured from it passes the filter before
//     it lands.
//  2. The directory cannot grow without bound. Open sweeps run directories past
//     the retention window, so retention is one rule covering everything
//     written here rather than a policy per file.
//
// Nothing in this package may fail its caller. It is used from the paths whose
// own failure is the thing being recorded, so a nil *Log is a working Log that
// writes nothing, and the helpers that cannot report degrade to a no-op rather
// than to an error nobody can act on.
//
// Nothing here resolves a path from the environment on its way to creating one,
// either. RunDir and RunsBase read the environment and create nothing; Open
// creates and reads nothing. A caller composes the two, and a test hands Open a
// directory of its own and never touches the state directory it is running in.
package runlog
