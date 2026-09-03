package app

import (
	"context"
	"fmt"
	"time"
)

// InstallRequest is `crossrev auth install`'s parsed options
// (lib/auth.sh:753-760).
type InstallRequest struct {
	// Owner is --owner. Empty means detect it from the repository the working
	// directory belongs to.
	Owner string
	// Role is --role. Empty is the loop's, which is the `local role="loop"`
	// the shell's own parser starts from — not `${2:-loop}`, which is a
	// different rule that _auth_role_permissions deliberately does not carry.
	Role string
}

// Install runs the install half on its own (auth_install, lib/auth.sh:752).
//
// `login` does both halves, but the two can be separated by a closed tab, a
// declined permission prompt, or a new repository a year later. Re-running
// `login` is the wrong instrument: it refuses, correctly, because the App
// already exists.
func (c *Commands) Install(ctx context.Context, req InstallRequest) error {
	role := req.Role
	if role == "" {
		role = RoleLoop
	}

	owner := req.Owner
	if owner == "" {
		detected, err := c.GH.DetectOwner(ctx)
		if err != nil {
			return c.IO.Die(
				"could not work out which owner's App to install",
				"Name it: crossrev auth install --owner <owner>")
		}
		// An empty answer with a zero exit is not refused here, because the
		// shell does not refuse one either: it tests gh's status alone, and the
		// empty owner falls through to the metadata check below.
		owner = detected
	}

	dir := Dir(c.Env)
	metaPath := MetaPath(dir, owner, role)
	if !isRegularFile(metaPath) {
		return c.IO.Die(
			fmt.Sprintf("no %s App is configured for %s", role, owner),
			fmt.Sprintf("Register one first: crossrev auth login --owner %s --role %s", owner, role))
	}
	meta, err := ReadMetadata(metaPath)
	if err != nil {
		return err
	}

	ownerID := ownerIDText(meta.OwnerID)
	if ownerID == "" {
		if recovered, err := c.GH.AccountID(ctx, owner); err == nil {
			ownerID = recovered
		}
	}

	pem := PEMPath(dir, owner, role)
	if !isRegularFile(pem) {
		// The lowercase `crossrev` here is the shell's own byte
		// (lib/auth.sh:784), copied rather than restyled: parity wins over the
		// naming rule for a string that already ships.
		return c.IO.Die(
			fmt.Sprintf("the %s private key for %s is missing at %s", role, owner, pem),
			fmt.Sprintf("Without it crossrev cannot confirm the installation. Re-register: crossrev auth login --owner %s --role %s", owner, role))
	}

	return c.installFlow(ctx, installFlowRequest{
		Owner:     owner,
		OwnerType: meta.OwnerType,
		OwnerID:   ownerID,
		Slug:      meta.Slug,
		AppID:     meta.ID,
		PEM:       pem,
	})
}

// installFlowRequest is what the second half of the flow needs to know
// (_auth_install_flow's six positional arguments, lib/auth.sh:794).
type installFlowRequest struct {
	Owner     string
	OwnerType string
	OwnerID   string
	Slug      string
	AppID     int64
	PEM       string
	// StepPrefix is the seventh argument, `${7:-}`. `login` passes
	// "Step 2 of 2: " because it is halfway through a flow; `install` on its
	// own passes nothing, because it is not.
	StepPrefix string
}

// installWaitSeconds is the five minutes at lib/auth.sh:809, and
// installPollInterval the three at :802.
const (
	installWaitSeconds  = 300
	installPollInterval = 3 * time.Second
)

// installFlow is the second half: install the App, then confirm it landed
// (_auth_install_flow, lib/auth.sh:793).
//
// Registering an App that reaches no repository is not a finished job, so this
// is part of `login` rather than a link at the end of it.
func (c *Commands) installFlow(ctx context.Context, req installFlowRequest) error {
	url := InstallURL(req.Slug, req.OwnerType, req.OwnerID)

	c.IO.Section(req.StepPrefix + "Install the App on the repositories you want reviewed")
	c.IO.Line("The App exists on GitHub, but reaches nothing until it is installed.")
	c.IO.Line("Choose 'Only select repositories' unless you mean all of them.")
	c.blank()

	if err := c.Browser.Open(ctx, url); err != nil {
		c.IO.Warn(
			"could not open a browser automatically",
			"Install it here: "+url)
	}

	c.IO.Line("Waiting for the installation to appear...")
	for waited := 0; waited < installWaitSeconds; waited += int(installPollInterval / time.Second) {
		// A token is minted every pass rather than once, because the loop can
		// run for five minutes and a JWT is good for nine.
		if jwt, err := JWT(req.PEM, req.AppID, c.now()); err == nil {
			if installs, err := c.GH.Installations(ctx, jwt); err == nil && len(installs) > 0 {
				c.IO.Gap()
				for _, install := range installs {
					c.IO.OK(fmt.Sprintf("installed on %s (%s repositories)", install.Account, install.Selection))
				}
				c.IO.End("Next:   crossrev init")
				return nil
			}
		}
		if err := c.sleep(ctx, installPollInterval); err != nil {
			return err
		}
	}

	c.IO.Warn(
		"no installation showed up within five minutes",
		"The App is registered and its key is stored, so nothing is lost. Install it at "+url+" and check with: crossrev auth status")
	return nil
}

// blank is `printf '\n'`: a bare newline outside a section, with none of the
// rule or indent every ui helper adds.
//
// ui.IO has no helper for it because the Bash library has no function for it
// either — the four call sites in lib/auth.sh write the newline themselves
// (:606, :638, :664, :784, :869).
func (c *Commands) blank() {
	if c.IO == nil || c.IO.Out == nil {
		return
	}
	fmt.Fprint(c.IO.Out, "\n")
}
