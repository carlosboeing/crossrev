package main

import (
	"context"

	"github.com/carlosboeing/crossrev/internal/app"
	"github.com/carlosboeing/crossrev/internal/cli"
	"github.com/carlosboeing/crossrev/internal/harness"
	"github.com/carlosboeing/crossrev/internal/ui"
)

// authCommands builds the five services `crossrev auth` dispatches to
// (lib/auth.sh, reached at bin/crossrev:142-152).
//
// Both children here are the orchestrator's. `gh` carries the forge credential
// by definition, and the browser opener is `open` or `xdg-open` — a program
// that receives a URL and nothing else, started by the orchestrator because it
// is not a model-facing process and app.NewBrowser panics on a nil runner
// rather than inventing one.
//
// Every remaining field takes its zero value, and app.Commands documents that
// each zero is the real one: time.Now for the clock, a real sleep, the shell's
// port list for the listener, and crypto/rand for the state value.
func authCommands(out *ui.IO, doc harness.Document) *app.Commands {
	d := open(out, doc)
	return &app.Commands{
		IO:        out,
		Env:       osEnv{},
		GH:        app.NewGH(d.orchestrator),
		Browser:   app.NewBrowser(d.orchestrator),
		Harnesses: doc,
	}
}

func authStatus(ctx context.Context, out *ui.IO, doc harness.Document) (int, error) {
	if err := authCommands(out, doc).Status(ctx); err != nil {
		return cli.ExitFailure, reportFatal(out, err)
	}
	return cli.ExitOK, nil
}

func authLogin(ctx context.Context, out *ui.IO, doc harness.Document, req cli.AuthLoginRequest) (int, error) {
	err := authCommands(out, doc).Login(ctx, app.LoginRequest{
		Owner: req.Owner,
		Name:  req.Name,
		Role:  req.Role,
	})
	if err != nil {
		return cli.ExitFailure, reportFatal(out, err)
	}
	return cli.ExitOK, nil
}

func authInstall(ctx context.Context, out *ui.IO, doc harness.Document, req cli.AuthInstallRequest) (int, error) {
	err := authCommands(out, doc).Install(ctx, app.InstallRequest{
		Owner: req.Owner,
		Role:  req.Role,
	})
	if err != nil {
		return cli.ExitFailure, reportFatal(out, err)
	}
	return cli.ExitOK, nil
}

func authRotate(ctx context.Context, out *ui.IO, doc harness.Document, req cli.AuthRotateRequest) (int, error) {
	err := authCommands(out, doc).Rotate(ctx, app.RotateRequest{
		Owner:   req.Owner,
		Role:    req.Role,
		KeyFile: req.KeyFile,
	})
	if err != nil {
		return cli.ExitFailure, reportFatal(out, err)
	}
	return cli.ExitOK, nil
}

func authRefresh(ctx context.Context, out *ui.IO, doc harness.Document, req cli.AuthRefreshRequest) (int, error) {
	err := authCommands(out, doc).Refresh(ctx, app.RefreshRequest{
		Harness: req.Harness,
		Repo:    req.Repo,
		Org:     req.Org,
		Secret:  req.Secret,
	})
	if err != nil {
		return cli.ExitFailure, reportFatal(out, err)
	}
	return cli.ExitOK, nil
}
