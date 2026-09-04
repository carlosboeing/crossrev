package cli

import (
	"context"

	"github.com/carlosboeing/crossrev/internal/ui"
)

// Commands is the table of everything bin/crossrev can dispatch to.
//
// The fields are function values rather than the command packages themselves,
// for two reasons. The tier rule forbids this package importing a tier-3 peer,
// and internal/cycle, internal/review, internal/resolve, internal/app,
// internal/initcmd and internal/preflight are all tier 3. And a table that is
// handed in is a table a test can fill with recorders, so the parser's own
// tests never start a leg, open a lock or reach GitHub.
//
// Each field takes a context and the request its command's flags parse into,
// and answers a process status and an error. Run folds the pair into one of
// three statuses; see exitFor.
type Commands struct {
	Cycle    func(context.Context, CycleRequest) (int, error)
	Review   func(context.Context, ReviewRequest) (int, error)
	Resolve  func(context.Context, ResolveRequest) (int, error)
	Status   func(context.Context, StatusRequest) (int, error)
	Init     func(context.Context, InitRequest) (int, error)
	Watchdog func(context.Context, WatchdogRequest) (int, error)

	ConfigShow    func(context.Context, ConfigRequest) (int, error)
	ConfigBacklog func(context.Context, ConfigRequest) (int, error)

	AuthStatus  func(context.Context, AuthStatusRequest) (int, error)
	AuthLogin   func(context.Context, AuthLoginRequest) (int, error)
	AuthInstall func(context.Context, AuthInstallRequest) (int, error)
	AuthRotate  func(context.Context, AuthRotateRequest) (int, error)
	AuthRefresh func(context.Context, AuthRefreshRequest) (int, error)

	Doctor func(context.Context, DoctorRequest) (int, error)

	// Help and Version are the two special cases ahead of the parse rule
	// (bin/crossrev:123-124). They are in the table rather than rendered here
	// because the usage block names the installed harnesses and the version
	// comes off disk, and neither belongs to a parser.
	Help    func(context.Context, HelpRequest) (int, error)
	Version func(context.Context, VersionRequest) (int, error)
}

// Dispatch runs the command an invocation named.
//
// A nil field is a composition-root fault with no counterpart in the shell,
// where a missing `case` arm cannot be built. It is refused rather than left to
// panic: a panic on a terminal reads as a crash, and says nothing about which
// command was not wired.
func Dispatch(ctx context.Context, inv Invocation, cmds Commands, out *ui.IO) (int, error) {
	switch inv.Command {
	case CommandCycle:
		return call(ctx, inv, cmds.Cycle, out)
	case CommandReview:
		return call(ctx, inv, cmds.Review, out)
	case CommandResolve:
		return call(ctx, inv, cmds.Resolve, out)
	case CommandStatus:
		return call(ctx, inv, cmds.Status, out)
	case CommandInit:
		return call(ctx, inv, cmds.Init, out)
	case CommandWatchdog:
		return call(ctx, inv, cmds.Watchdog, out)
	case CommandConfigShow:
		return call(ctx, inv, cmds.ConfigShow, out)
	case CommandConfigBacklog:
		return call(ctx, inv, cmds.ConfigBacklog, out)
	case CommandAuthStatus:
		return call(ctx, inv, cmds.AuthStatus, out)
	case CommandAuthLogin:
		return call(ctx, inv, cmds.AuthLogin, out)
	case CommandAuthInstall:
		return call(ctx, inv, cmds.AuthInstall, out)
	case CommandAuthRotate:
		return call(ctx, inv, cmds.AuthRotate, out)
	case CommandAuthRefresh:
		return call(ctx, inv, cmds.AuthRefresh, out)
	case CommandDoctor:
		return call(ctx, inv, cmds.Doctor, out)
	case CommandHelp:
		return call(ctx, inv, cmds.Help, out)
	case CommandVersion:
		return call(ctx, inv, cmds.Version, out)
	}
	return ExitFailure, notWired(inv.Command, out)
}

// call hands the parsed request to one field of the table, or refuses when the
// field is nil or the request is not the type that field takes.
func call[T any](ctx context.Context, inv Invocation, fn func(context.Context, T) (int, error), out *ui.IO) (int, error) {
	req, ok := inv.Request.(T)
	if !ok || fn == nil {
		return ExitFailure, notWired(inv.Command, out)
	}
	return fn(ctx, req)
}

func notWired(name Command, out *ui.IO) error {
	return out.Die("crossrev "+string(name)+" is not wired into this build",
		"This is a defect in CrossRev itself. Please report it with the command you ran.")
}
