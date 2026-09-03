package main

import (
	"context"

	"github.com/carlosboeing/crossrev/internal/cli"
	"github.com/carlosboeing/crossrev/internal/harness"
	"github.com/carlosboeing/crossrev/internal/ui"
)

// compose is the command table, and the harness names the usage lines carry.
//
// Every field is a closure over dependencies opened when the command runs
// rather than when the table is built. `crossrev version` must answer on a
// machine with no git, no gh and no repository, so nothing here may open one
// eagerly.
//
// The names come from the descriptor. An empty list prints the shape of the
// flag instead, which is the arm the shell takes when jq is missing
// (bin/crossrev:68-72).
func compose(out *ui.IO, doc harness.Document) (cli.Commands, []string) {
	names := doc.Names()

	return cli.Commands{
		Help: func(context.Context, cli.HelpRequest) (int, error) {
			return cli.Help(out, names)
		},
		Version: func(context.Context, cli.VersionRequest) (int, error) {
			return cli.Version(out, cli.InstalledVersion())
		},

		Doctor: func(ctx context.Context, _ cli.DoctorRequest) (int, error) {
			return doctor(ctx, out, doc)
		},
		ConfigShow: func(ctx context.Context, _ cli.ConfigRequest) (int, error) {
			return configShow(ctx, out, doc)
		},
		ConfigBacklog: func(ctx context.Context, _ cli.ConfigRequest) (int, error) {
			return configBacklog(ctx, out, doc)
		},

		Status: func(ctx context.Context, req cli.StatusRequest) (int, error) {
			return status(ctx, out, doc, req)
		},
		Watchdog: func(ctx context.Context, req cli.WatchdogRequest) (int, error) {
			return watchdog(ctx, out, doc, req)
		},

		Cycle: func(ctx context.Context, req cli.CycleRequest) (int, error) {
			return cycleCommand(ctx, out, doc, req)
		},
		Review: func(ctx context.Context, req cli.ReviewRequest) (int, error) {
			return reviewCommand(ctx, out, doc, req)
		},
		Resolve: func(ctx context.Context, req cli.ResolveRequest) (int, error) {
			return resolveCommand(ctx, out, doc, req)
		},

		Init: func(ctx context.Context, req cli.InitRequest) (int, error) {
			return initCommand(ctx, doc, req)
		},

		AuthStatus: func(ctx context.Context, _ cli.AuthStatusRequest) (int, error) {
			return authStatus(ctx, out, doc)
		},
		AuthLogin: func(ctx context.Context, req cli.AuthLoginRequest) (int, error) {
			return authLogin(ctx, out, doc, req)
		},
		AuthInstall: func(ctx context.Context, req cli.AuthInstallRequest) (int, error) {
			return authInstall(ctx, out, doc, req)
		},
		AuthRotate: func(ctx context.Context, req cli.AuthRotateRequest) (int, error) {
			return authRotate(ctx, out, doc, req)
		},
		AuthRefresh: func(ctx context.Context, req cli.AuthRefreshRequest) (int, error) {
			return authRefresh(ctx, out, doc, req)
		},
	}, names
}
