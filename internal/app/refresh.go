package app

import (
	"context"
	"strings"

	"github.com/carlosboeing/crossrev/internal/cred"
	"github.com/carlosboeing/crossrev/internal/exec"
)

// RefreshRequest is `crossrev auth refresh`'s parsed options
// (lib/auth.sh:945-955).
type RefreshRequest struct {
	// Harness is --harness. Empty means the one harness whose credential
	// rotates, and a refusal when that is not exactly one.
	Harness string
	// Repo is --repo owner/name. Empty with no Org means detect it.
	Repo string
	// Org is --org owner, which writes an organisation secret instead.
	Org string
	// Secret is --secret NAME. Empty means the name the descriptor gives.
	Secret string
}

// Refresh exchanges a rotating harness credential and writes the new one back
// (auth_refresh, lib/auth.sh:944).
//
// Called by the refresher workflow and by nobody else. It is the only place
// that writes a rotating harness credential, which is the whole reason the
// chain holds: using a refresh token consumes it, so several writers means the
// first one silently invalidates the rest.
//
// # Where this keeps the credential
//
// In memory, and in exactly one place outside it: `gh secret set`'s stdin. The
// shell writes it to two temp files under `umask 077` and removes them on the
// way out, with an EXIT trap because every failure below is a ui_die that would
// skip a RETURN one (lib/auth.sh:996-1003 and :1041-1045). None of that is
// needed here — cred.Refresh and cred.SecondsLeft take bytes — so the temp
// files are gone rather than reproduced. Nothing observable depends on them
// existing; a great deal depends on them not being left behind.
func (c *Commands) Refresh(ctx context.Context, req RefreshRequest) error {
	credentials := c.Harnesses.Credentials()

	name := req.Harness
	if name == "" {
		var refreshers []string
		for _, candidate := range credentials.Names() {
			if credentials.For(candidate).Credential.Refresher {
				refreshers = append(refreshers, candidate)
			}
		}
		switch {
		case len(refreshers) > 1:
			return c.IO.Die(
				"more than one harness is configured with a refresher ("+strings.Join(refreshers, ", ")+")",
				"Specify which harness to refresh with --harness <name>.")
		case len(refreshers) == 1:
			name = refreshers[0]
		default:
			return c.IO.Die(
				"no harness is configured with a refresher",
				"CrossRev only refreshes credentials that rotate on ephemeral runners.")
		}
	}

	// An unknown harness answers the same way a non-rotating one does, which is
	// harness_get's own answer: it prints nothing, and nothing is not "true".
	//
	// The lowercase `crossrev` opening this sentence is the shell's own byte
	// (lib/auth.sh:974).
	descriptor := credentials.For(name)
	if !descriptor.Credential.Refresher {
		return c.IO.Die(
			"crossrev only refreshes credentials that rotate on ephemeral runners, and '"+name+"' does not need a refresher",
			"Claude's setup-token is long-lived and needs no refresher; Antigravity uses seed-and-self-refresh; only single-writer rotating credentials use the refresher workflow.")
	}

	secret := req.Secret
	if secret == "" {
		secret = descriptor.Credential.Secret
		if secret == "" {
			// `CROSSREV_${harness^^}_AUTH`: a descriptor entry with no secret
			// name still has a variable a runner could deliver one in.
			secret = "CROSSREV_" + strings.ToUpper(name) + "_AUTH"
		}
	}

	seedHint := descriptor.Credential.SeedHint
	if seedHint == "" {
		seedHint = "re-seed the secret by hand"
	}

	// `${!secret}`: the value of the variable the secret is named after. This
	// is the credential itself, and from here it exists in memory alone.
	credential := []byte(c.Env.Getenv(secret))
	if len(credential) == 0 {
		return c.IO.Die(
			secret+" is not set, so there is no credential to refresh",
			"The refresher workflow passes the secret in as this variable. "+seedHint)
	}

	repo, org := req.Repo, req.Org
	if repo == "" && org == "" {
		// gh_repo_slug dies with its own message (lib/github.sh:30-34); the
		// refusal below is only reached when gh answers with nothing.
		detected, err := c.GH.RepoSlug(ctx)
		if err != nil {
			return c.IO.Die(
				"could not work out which repository this is",
				"Run crossrev from a checkout with a GitHub remote, or pass --repo owner/name.")
		}
		repo = detected
	}
	if repo == "" && org == "" {
		return c.IO.Die(
			"could not work out where to write the refreshed credential",
			"Pass --repo owner/name or --org owner.")
	}

	// An unreadable stored expiry is not a refusal: it makes the comparison
	// below unavailable, and nothing else (lib/auth.sh:1006).
	before, beforeErr := cred.SecondsLeft(descriptor, credential, c.now())

	refreshed, err := cred.Refresh(ctx, descriptor, credential, c.RefreshOptions)
	if err != nil {
		return c.IO.Die(
			"the refresh did not produce a new credential",
			"The stored secret is untouched, so the chain still holds until it expires. Re-seed it by hand if this keeps failing: "+seedHint+".")
	}

	// Rule 5: do not report success for something unverified — and an expiry
	// that cannot be read is unverified, not "probably fine". A vendor response
	// that is HTTP 200 with a malformed access token would otherwise be written
	// back over a working credential, reported as a success, and rejected by
	// every leg from then on.
	after, err := cred.SecondsLeft(descriptor, refreshed, c.now())
	if err != nil {
		return c.IO.Die(
			"the refreshed credential's expiry cannot be read, so CrossRev will not write it back",
			"The vendor answered, but what came back does not parse as a token with an exp claim. The stored secret is untouched and still works until it expires. Re-seed it by hand if this repeats: "+seedHint+".")
	}

	// An expiry no later than the one it replaces means the refresh did not
	// happen, and writing it back would burn a refresh token for nothing.
	if beforeErr == nil && after <= before {
		return c.IO.Die(
			"the refreshed credential expires no later than the one it replaces",
			"The vendor answered but did not issue a new token. The stored secret is untouched. Check the account's session has not been revoked: "+name+" login status")
	}

	if org != "" {
		if err := c.GH.SetOrgSecret(ctx, secret, org, refreshed); err != nil {
			return c.IO.Die(
				"could not write "+secret+" at the "+org+" organisation level",
				"The refresher App needs secrets:write on that organisation. Check: crossrev auth status")
		}
	} else {
		if err := c.GH.SetRepoSecret(ctx, secret, repo, refreshed); err != nil {
			return c.IO.Die(
				"could not write "+secret+" on "+repo,
				"The refresher App needs secrets:write on that repository. Check: crossrev auth status")
		}
	}

	c.IO.Section("Refreshed")
	c.IO.OK(secret + " now holds a credential valid for " + cred.HumanDuration(after))
	c.IO.End("This is the only job that writes it. Every leg restores a copy and discards it.")
	return nil
}

// --- the two writes, and the read that decides where they go ---------------

// RepoSlug is which repository the working directory belongs to (gh_repo_slug,
// lib/github.sh:30).
//
// It answers whatever gh printed, unvalidated, because that is what the shell
// hands on. The one caller here puts it in a `--repo` argument, which gh parses
// itself.
func (g *GH) RepoSlug(ctx context.Context) (string, error) {
	res := g.run(ctx, "repo", "view", "--json", "nameWithOwner", "--jq", ".nameWithOwner")
	if !answered(res) {
		return "", ghFailure("could not work out which repository this is", res)
	}
	return output(res), nil
}

// SetRepoSecret writes a repository secret (lib/auth.sh:1036).
func (g *GH) SetRepoSecret(ctx context.Context, name, repo string, value []byte) error {
	return g.setSecret(ctx, value, "secret", "set", name, "--repo", repo)
}

// SetOrgSecret writes an organisation secret, readable by every repository in
// it (lib/auth.sh:1032).
//
// `--visibility all` is not a widening for convenience: an organisation secret
// no workflow can read is not a credential, and the refresher writes exactly
// one of these on purpose. The refresher App is the only thing that can write
// it, and its own key is repository-scoped for the reason `auth rotate` prints.
func (g *GH) SetOrgSecret(ctx context.Context, name, org string, value []byte) error {
	return g.setSecret(ctx, value, "secret", "set", name, "--org", org, "--visibility", "all")
}

// setSecret hands the value to gh on stdin, which is `printf '%s' "$new" | gh
// secret set …` (lib/auth.sh:1032 and :1036).
//
// Stdin rather than an argument, and that is the point of the helper: an
// argument is in the process table, readable by anything on the machine for as
// long as the child runs. The value is not in the error either, and gh's own
// output is discarded the way `>/dev/null` discards it.
func (g *GH) setSecret(ctx context.Context, value []byte, args ...string) error {
	res := g.runner.Run(ctx, exec.Spec{
		Path:  program,
		Args:  args,
		Env:   g.env,
		Stdin: value,
	})
	if !answered(res) {
		return ghFailure("gh could not write the secret", res)
	}
	return nil
}
