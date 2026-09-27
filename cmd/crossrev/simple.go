package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/carlosboeing/crossrev/internal/cli"
	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/harness"
	"github.com/carlosboeing/crossrev/internal/preflight"
	"github.com/carlosboeing/crossrev/internal/ui"
)

// doctor is the `doctor` arm of bin/crossrev:163-180.
//
// Four probes and one closing line, and the closing line's wording is the
// verdict: everything installed, or fix what is marked. The pairing report
// runs whenever the configuration loads, which is what `cfg_load ""` inside
// the same branch does: a configuration that refuses ends the command with
// that refusal.
//
// The Runner is the orchestrator's. The three gh identity probes are the
// orchestrator asking GitHub who it is and they carry the forge credential;
// preflight's own nil default is the model-facing runner, which would refuse
// them and report the operator unauthenticated.
func doctor(ctx context.Context, out *ui.IO, doc harness.Document, req cli.DoctorRequest) (int, error) {
	d := open(out, doc)
	checker := &preflight.Checker{
		IO:      out,
		Runner:  d.orchestrator,
		Harness: doc,
	}
	// Empty is the default, and the default is what the shell did.
	level := preflight.NeedHarness
	if req.Level != "" {
		level = req.Level
	}
	ok := checker.Check(ctx, level)
	if !checker.CheckQuarantine() {
		ok = false
	}
	cfg, err := d.loadConfig(ctx, core.Revision{})
	if err != nil {
		return cli.ExitFailure, reportFatal(out, err)
	}
	checker.Config = cfg
	if !checker.ReportPairings(cfg.Get(".runner")) {
		ok = false
	}
	checker.ReportWorktrees()
	if ok {
		out.End("Everything CrossRev needs is installed.")
		return cli.ExitOK, nil
	}
	out.End("Fix what is marked ✗ above, then run this again.")
	return cli.ExitFailure, nil
}

// configShow is `jq . <<<"$CFG_MERGED"` (bin/crossrev:158).
//
// The merge is already compact JSON in the order the layers built it, so this
// is jq's indentation and nothing else: json.Indent re-lays the same tokens
// rather than re-encoding them, which is what keeps the key order and every
// escape as the merge produced it.
func configShow(ctx context.Context, out *ui.IO, doc harness.Document) (int, error) {
	d := open(out, doc)
	if err := requireYq(ctx, out, doc); err != nil {
		return cli.ExitFailure, err
	}
	cfg, err := d.loadConfig(ctx, core.Revision{})
	if err != nil {
		return cli.ExitFailure, reportFatal(out, err)
	}
	compact, err := cfg.MergedJSON()
	if err != nil {
		return cli.ExitFailure, reportFatal(out, err)
	}
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, compact, "", "  "); err != nil {
		return cli.ExitFailure, reportFatal(out, err)
	}
	pretty.WriteByte('\n')
	fmt.Fprint(out.Out, pretty.String())
	return cli.ExitOK, nil
}

// configBacklog is `ui_say "deferred work would go to: …"` (bin/crossrev:159).
func configBacklog(ctx context.Context, out *ui.IO, doc harness.Document) (int, error) {
	d := open(out, doc)
	if err := requireYq(ctx, out, doc); err != nil {
		return cli.ExitFailure, err
	}
	cfg, err := d.loadConfig(ctx, core.Revision{})
	if err != nil {
		return cli.ExitFailure, reportFatal(out, err)
	}
	backlog, err := cfg.ResolveBacklog(ctx, core.Revision{}, cfg.Get(".backlog.destination"))
	if err != nil {
		return cli.ExitFailure, reportFatal(out, err)
	}
	out.Say("deferred work would go to: " + backlog.String())
	return cli.ExitOK, nil
}

// requireYq is preflight_require_yq, which `config` and `init` run before they
// load anything (bin/crossrev:155, lib/init.sh:49).
//
// Only those two commands ask: doctor stopped requiring yq with jq and
// openssl, and every other command loads the configuration without asking.
func requireYq(ctx context.Context, out *ui.IO, doc harness.Document) error {
	_ = ctx
	checker := &preflight.Checker{IO: out, Harness: doc}
	return checker.RequireYq()
}
