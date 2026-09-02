package initcmd

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/harness"
	"github.com/carlosboeing/crossrev/internal/policy"
)

// The two label policies _init_label_inventory takes (lib/init.sh:354-375).
//
// `recolour` is its own word rather than folded into `exists`, for the reason
// the rest of the plan exists: a run that says "exists" and then quietly
// changes the label's colour has told the reader something false about what it
// would do. The same rule cuts the other way for a set execution leaves alone —
// reporting `recolour` there would promise a change that never comes, which is
// the same lie told backwards. So planning and execution answer from one
// policy: this argument.
const (
	labelRecolour = "recolour"
	labelKeep     = "keep"
)

// Print states the plan: every path, secret and label, the resolved destination
// for deferred work and where that resolution came from, and anything it would
// overwrite (_init_print_plan, lib/init.sh:258-352).
//
// The gate is not politeness. It is the difference between a tool people trust
// with a second repository and one they run once.
//
// It reads GitHub while printing — the label colours and the branch protection
// rule — because the shell does, and the plan must state what execution would
// actually do rather than what it intended to do. It writes nothing.
func (p Plan) Print(ctx context.Context, req Request) {
	out := req.io()

	appLine := "none registered for " + p.Owner + " — run `crossrev auth login` first"
	if app, found := req.Apps.App(p.Owner, RoleLoop); found {
		appLine = fmt.Sprintf("reuse %q (id %s, owner %s)", app.Name, app.ID, p.Owner)
	}

	out.Section("Plan for " + p.Repo.String())
	out.Line("")
	out.Line("GitHub App        " + appLine)
	out.Line("source pin        " + truncate(p.SourceSHA, 40))
	out.Line("                  (" + p.SourceRef + " — the SHA is the pin, the tag is a comment)")

	out.Line("")
	out.Line("runner            " + p.Runner)
	for _, role := range core.Roles() {
		name := p.Config.Get("." + string(role) + ".harness")
		endpoint := p.Config.Get("." + string(role) + ".endpoint")
		if named(endpoint) {
			out.Line("                  " + string(role) + ": " + name + " via the '" + endpoint + "' endpoint, a static token")
			continue
		}
		out.Line("                  " + string(role) + ": " + name + " by subscription")
	}

	if p.NeedsRefresher {
		out.Line("")
		out.Line("refresher App     needed — " + refresherHarness(req.Harness) + " authenticates by subscription on an")
		out.Line("                  ephemeral runner, and its credential rotates, so one")
		out.Line("                  scheduled job refreshes it and the legs only read")
		if app, found := req.Apps.App(p.Owner, RoleRefresher); found {
			out.Line(fmt.Sprintf("                  reuse %q (id %s)", app.Name, app.ID))
		} else {
			out.Line("                  one more browser approval, for an App carrying")
			out.Line("                  secrets:write and nothing else")
		}
	}

	scope := "repository"
	if p.OwnerType == "organization" {
		scope = "organisation"
	}
	out.Line("")
	out.Line("secrets           checked at " + scope + " level, and set only where CrossRev has the value")
	for _, secret := range p.Secrets {
		if secret == "" {
			continue
		}
		if secretSet(p.SecretInventory, secret) {
			out.Line("                  " + secret + " — already set")
			continue
		}
		out.Line("                  " + secret + " — MISSING " + p.secretNote(req, secret))
	}

	out.Line("")
	loop := append(append([]string(nil), p.PassLabels...), p.FixedLabels...)
	out.Line("labels            " + strconv.Itoa(len(p.PassLabels)+len(p.FixedLabels)) + " for the loop:")
	p.labelInventory(ctx, req, loop, labelRecolour)
	if p.BacklogLabels != "" {
		backlog := strings.Fields(p.BacklogLabels)
		out.Line("                  " + strconv.Itoa(len(backlog)) + " for filed issues:")
		p.labelInventory(ctx, req, backlog, labelKeep)
	}
	out.Line("                  init creates these up front so their colours and descriptions")
	out.Line("                  carry the loop's state; GitHub would otherwise create a missing")
	out.Line("                  label later with default metadata")

	out.Line("")
	out.Line("deferred work     " + p.BacklogResolved)
	out.Line("                  " + p.BacklogOrigin)

	out.Line("")
	for index, path := range p.Writes {
		if index == 0 {
			out.Line("write             " + path)
			continue
		}
		out.Line("                  " + path)
	}

	out.Line("")
	if len(p.Overwrites) > 0 {
		out.Line("overwrites        " + strings.Join(p.Overwrites, " "))
	} else {
		out.Line("overwrites        none")
	}

	branch := req.GitHub.DefaultBranch(ctx, p.Repo)
	if !req.GitHub.BranchProtected(ctx, p.Repo, branch) {
		out.Warn(
			"branch protection is off on "+branch+" in "+p.Repo.String(),
			"The orchestrator's own push guard would be the only thing stopping a bad push. It asserts the target equals the pull request's head branch and is not the default branch, but branch protection is the backstop behind it.",
		)
	}
}

// labelInventory prints one row per label, saying what `init` would do to it
// (_init_label_inventory, lib/init.sh:365-375).
//
// The policy is `keep` for a set `init` creates but never repaints, which is
// the GitHub issues destination's: those labels are the repository's own
// taxonomy, and a tool that recoloured somebody's `bug` label because it minted
// one once would be overstepping. The loop's own labels are the other case.
func (p Plan) labelInventory(ctx context.Context, req Request, labels []string, labelPolicy string) {
	for _, label := range labels {
		current := req.GitHub.LabelColour(ctx, p.Repo, label)
		var state string
		switch {
		case current == "":
			state = "create"
		case labelPolicy == labelKeep:
			state = "exists"
		case current == strings.ToLower(policy.LabelColour(label)):
			state = "exists"
		default:
			state = labelRecolour
		}
		req.io().Line(fmt.Sprintf("                    %-8s  %s", state, label))
	}
}

// secretNote says who is going to set a secret that is not set yet
// (_init_secret_note, lib/init.sh:377-394).
//
// The three arms carry the shell's own strings, lowercase `crossrev` and all:
// parity wins over the writing rule for a line already printed by the tool that
// ships.
func (p Plan) secretNote(req Request, secret string) string {
	switch secret {
	case "APP_ID", "APP_PRIVATE_KEY":
		if _, found := req.Apps.App(p.Owner, RoleLoop); found {
			return "— crossrev will set it"
		}
		return "— run `crossrev auth login` first"
	case "CROSSREV_REFRESH_APP_ID", "CROSSREV_REFRESH_APP_PRIVATE_KEY":
		if _, found := req.Apps.App(p.Owner, RoleRefresher); found {
			return "— crossrev will set it"
		}
		return "— crossrev will register the refresher App and set it"
	}
	if hint := seedHint(req.Harness, secret); hint != "" {
		return "— " + hint
	}
	return "— the token an endpoint in the config names; set it yourself with `gh secret set " + secret + "`"
}

// seedHint is the descriptor's `.credential.seed_hint` for the harness whose
// credential lives in this secret, and empty when no harness claims it
// (lib/init.sh:387).
func seedHint(doc harness.Document, secret string) string {
	for _, name := range doc.Names() {
		entry, found := doc.For(name)
		if found && entry.Credential.Secret == secret {
			return entry.Credential.SeedHint
		}
	}
	return ""
}

// truncate is `${value:0:limit}`, counting characters rather than bytes the way
// Bash's substring expansion does.
func truncate(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}
