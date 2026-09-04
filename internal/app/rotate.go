package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/carlosboeing/crossrev/internal/ui"
)

// RotateRequest is `crossrev auth rotate`'s parsed options
// (lib/auth.sh:843-852).
type RotateRequest struct {
	// Owner is --owner. Empty means detect it.
	Owner string
	// Role is --role, and empty is the loop's.
	Role string
	// KeyFile is --key: the .pem GitHub downloaded. Empty means open the
	// settings page and watch the downloads folder for it.
	KeyFile string
}

// The two numbers the download watch is built from (lib/auth.sh:898-901).
const (
	rotateWaitSeconds  = 300
	rotatePollInterval = 2 * time.Second
	// downloadFreshness is `-newermt '-5 minutes'`: a file older than this is
	// somebody's previous download, not the one just generated.
	downloadFreshness = 5 * time.Minute
)

// Rotate replaces an App's private key with a freshly generated one
// (auth_rotate, lib/auth.sh:842).
//
// GitHub exposes no API for generating an App private key. It is a web-UI
// action and there is no way around that, so this is a guided flow rather than
// a call: open the right page, wait for the file, prove it works, install it,
// and say what is left to do by hand.
//
// The proof is the part that matters. A rotation that stores an unverified key
// leaves you with a secret nobody has tested and an old key you have been told
// to delete — which is how a repository ends up with no working credential at
// all. So the new key mints a JWT and calls the API as the App before anything
// is replaced, and the old key is kept until that succeeds.
func (c *Commands) Rotate(ctx context.Context, req RotateRequest) error {
	role := req.Role
	if role == "" {
		role = RoleLoop
	}

	owner := req.Owner
	if owner == "" {
		detected, err := c.GH.DetectOwner(ctx)
		if err != nil {
			return c.IO.Die(
				"could not work out which owner's key to rotate",
				"Name it: crossrev auth rotate --owner <owner>")
		}
		owner = detected
	}

	dir := Dir(c.Env)
	metaPath := MetaPath(dir, owner, role)
	if !isRegularFile(metaPath) {
		return c.IO.Die(
			fmt.Sprintf("no %s App is configured for %s", role, owner),
			fmt.Sprintf("There is nothing to rotate. Register one with: crossrev auth login --owner %s --role %s", owner, role))
	}
	meta, err := ReadMetadata(metaPath)
	if err != nil {
		return err
	}
	pem := PEMPath(dir, owner, role)

	settingsURL := "https://github.com/settings/apps/" + meta.Slug + "#private-key"
	if meta.OwnerType == "Organization" {
		settingsURL = "https://github.com/organizations/" + owner + "/settings/apps/" + meta.Slug + "#private-key"
	}

	c.IO.Section("Rotate the private key for " + meta.Name)
	c.IO.Line("Current key   " + pem)
	c.IO.Line(fmt.Sprintf("App           id %d, role %s", meta.ID, role))
	c.IO.Gap()
	c.IO.Line("GitHub has no API for generating an App key, so this part happens in")
	c.IO.Line("the browser: press 'Generate a private key' and the .pem downloads.")
	c.IO.Line("CrossRev picks it up, proves it works as this App, and installs it.")
	c.IO.Gap()
	c.IO.Line("Nothing is replaced until the new key authenticates, and the old one")
	c.IO.Line("keeps working until you delete it on GitHub — so a failure here leaves")
	c.IO.Line("you exactly where you started.")
	c.blank()

	keyfile := req.KeyFile
	if keyfile == "" {
		agreed, err := c.IO.Confirm("Open GitHub?")
		if err != nil {
			return err
		}
		if !agreed {
			c.IO.Say("Nothing was changed.")
			return ErrDeclined
		}
		if err := c.Browser.Open(ctx, settingsURL); err != nil {
			c.IO.Warn(
				"could not open a browser automatically",
				"Generate the key here: "+settingsURL)
		}

		// Watch the downloads folder rather than asking for a path. The file
		// lands with a name GitHub chooses, and typing it out is the step
		// people get wrong.
		downloads := c.Env.Getenv("HOME") + "/Downloads"
		c.IO.Line("Watching " + downloads + " for a new .pem...")
		found := ""
		for waited := 0; waited < rotateWaitSeconds; waited += int(rotatePollInterval / time.Second) {
			if found = c.freshDownload(downloads, meta.Slug); found != "" {
				break
			}
			if err := c.sleep(ctx, rotatePollInterval); err != nil {
				return err
			}
		}
		if found != "" {
			keyfile = found
			c.IO.OK("found " + keyfile)
		} else {
			answer, err := c.IO.Prompt("Path to the downloaded .pem")
			if err != nil {
				var refusal *ui.FatalError
				if errors.As(err, &refusal) {
					return err
				}
				return c.IO.Die(
					"no key file was named",
					fmt.Sprintf("Re-run: crossrev auth rotate --owner %s --role %s --key <path>", owner, role))
			}
			keyfile = answer
		}
	}

	keyfile = expandHome(keyfile, c.Env.Getenv("HOME"))
	if !isRegularFile(keyfile) {
		return c.IO.Die(
			"there is no file at "+keyfile,
			"Point --key at the .pem GitHub downloaded.")
	}

	// Prove it before installing it. The two checks answer different
	// questions: whether the file is a key at all, and whether it is THIS
	// App's key.
	jwt, err := JWT(keyfile, meta.ID, c.now())
	if err != nil {
		return c.IO.Die(
			"could not sign a token with "+keyfile,
			"It has to be the RSA private key GitHub generated for this App. Nothing was changed.")
	}
	if err := c.GH.VerifyApp(ctx, jwt); err != nil {
		return c.IO.Die(
			fmt.Sprintf("GitHub rejected a token signed with %s for App id %d", keyfile, meta.ID),
			"That key belongs to a different App, or the download is incomplete. The existing key is untouched.")
	}

	body, err := os.ReadFile(keyfile)
	if err != nil {
		return fmt.Errorf("could not read %s: %w", keyfile, err)
	}

	dest := dir + "/" + owner + "." + role + ".pem"
	backup := dest + ".previous"
	if isRegularFile(pem) {
		previous, err := os.ReadFile(pem)
		if err != nil {
			return fmt.Errorf("could not read the key being replaced at %s: %w", pem, err)
		}
		// Both writes go through the same helper. A write followed by a chmod
		// left the backup at the source's mode for the width of two calls, and
		// a write onto an existing dest kept whatever mode that file already
		// had (lib/auth.sh:1002-1011).
		if err := write0600(backup, previous); err != nil {
			return c.IO.Die(
				"could not write the backup key to "+backup,
				"Check the directory is writable. The existing key is untouched.")
		}
	}
	if err := write0600(dest, body); err != nil {
		return c.IO.Die(
			"could not write the new key to "+dest,
			"Check the directory is writable. The previous key is at "+backup+".")
	}
	// The legacy unroled path would otherwise keep winning for the loop role
	// and the rotation would look successful while nothing had changed.
	if pem != dest && isRegularFile(pem) {
		if err := os.Remove(pem); err != nil {
			return fmt.Errorf("could not remove the legacy key at %s: %w", pem, err)
		}
	}

	c.IO.Section("Rotated")
	c.IO.OK("new key installed at " + dest + ", and it authenticates as this App")
	if isRegularFile(backup) {
		c.IO.Line("   previous key kept at " + backup)
	}
	c.IO.Gap()
	c.IO.Line("Two things are still yours to do, and both are outward-facing:")
	c.IO.Next("delete the old key on GitHub: " + settingsURL)
	// The role's own secret, never a hardcoded APP_PRIVATE_KEY. Told to update
	// that one after rotating the refresher's key, someone following the
	// instruction literally would put the refresher's key material behind the
	// loop App's identity — handing secrets:write to the job that reads a pull
	// request diff, which is the one thing the two-App split exists to prevent.
	c.IO.Next("update " + RoleKeySecret(role) + " wherever it is stored: crossrev init --upgrade, or gh secret set")
	if role == RoleRefresher {
		c.IO.Line("   repository-scoped: this key can write secrets, so it must never be")
		c.IO.Line("   an organisation secret visible to every workflow in the org")
	}
	c.IO.End("Until the secret carries the new key, CI is still authenticating with the old one.")
	return nil
}

// freshDownload is `find "$downloads" -maxdepth 1 -name "$slug*.private-key.pem"
// -newermt '-5 minutes' | head -1` (lib/auth.sh:899).
//
// The freshness window is re-read on every poll, because the shell re-runs find
// on every poll and `-5 minutes` is relative to the moment it runs.
//
// find walks the directory in whatever order the filesystem hands it back, and
// `head -1` takes the first of those. This sorts instead, so two candidate
// downloads give the same answer twice rather than an answer that depends on
// the directory's internal layout.
func (c *Commands) freshDownload(dir, slug string) string {
	matches, err := filepath.Glob(filepath.Join(dir, slug+"*.private-key.pem"))
	if err != nil {
		return ""
	}
	sort.Strings(matches)
	cutoff := c.now().Add(-downloadFreshness)
	for _, path := range matches {
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		if info.ModTime().After(cutoff) {
			return path
		}
	}
	return ""
}

// VerifyApp asks GitHub to answer as this App, which is the proof a rotation
// turns on (lib/auth.sh:921).
//
// The answer is discarded: `>/dev/null` at the shell. What is being tested is
// that GitHub accepted a token signed with the key, and nothing about the
// response adds to that.
func (g *GH) VerifyApp(ctx context.Context, jwt string) error {
	res := g.run(ctx, "api", "-H", "Authorization: Bearer "+jwt, "/app", "--jq", ".slug")
	if !answered(res) {
		return ghFailure("GitHub did not accept a token signed with this key", res)
	}
	return nil
}
