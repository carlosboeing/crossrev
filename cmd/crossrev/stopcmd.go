package main

import (
	"context"
	"fmt"

	"github.com/carlosboeing/crossrev/internal/cli"
	"github.com/carlosboeing/crossrev/internal/harness"
	"github.com/carlosboeing/crossrev/internal/ui"
	"github.com/carlosboeing/crossrev/internal/vcs"
)

// stop is `crossrev stop --pr N`: stop the local run holding a pull request.
//
// It needs no forge and no configuration. The lock keys on this checkout's
// shared git directory, and the holder it names is a process on this
// machine, so everything it does happens in internal/vcs beside the lock
// itself. This half only opens the checkout and says what happened.
func stop(ctx context.Context, out *ui.IO, doc harness.Document, req cli.StopRequest) (int, error) {
	_ = doc
	d := open(out, doc)
	result, err := d.repo.StopRunLock(ctx, req.PR)
	if err != nil {
		return cli.ExitFailure, reportFatal(out, err)
	}
	switch result.State {
	case vcs.StopNone:
		out.Say(fmt.Sprintf("No local CrossRev run holds pull request %d.", req.PR))
	case vcs.StopCleared:
		out.Say(fmt.Sprintf("Cleared the lock on pull request %d left by %s: that run is no longer running.", req.PR, result.Holder.Raw))
	case vcs.StopStopped:
		out.Say(fmt.Sprintf("Stopped the CrossRev run holding pull request %d (%s).", req.PR, result.Holder.Raw))
	}
	return cli.ExitOK, nil
}
