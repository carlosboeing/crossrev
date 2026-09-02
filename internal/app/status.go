package app

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/carlosboeing/crossrev/internal/cred"
	"github.com/carlosboeing/crossrev/internal/harness"
	"github.com/carlosboeing/crossrev/internal/ui"
)

// Commands are the five services `crossrev auth` dispatches to: status, login,
// install, rotate and refresh.
//
// Argument parsing is not here. Each method takes a request struct of values a
// CLI already parsed, so the flag spellings live in one place and these stay
// testable without one.
//
// Every field is a dependency rather than a setting, and the zero value of each
// is the real one except the four that cannot have a real default: IO writes
// nowhere, Env answers nothing, and GH and Browser panic on a nil runner rather
// than inventing one.
type Commands struct {
	// IO is the voice: every printed line, every confirmation, every prompt.
	IO *ui.IO
	// Env decides where an App's key, its metadata and the token ledger live,
	// and it is also what Refresh reads a credential out of.
	Env Environment
	// GH is every read and write that goes through the `gh` CLI.
	GH *GH
	// Browser is the handoff to whatever the machine calls a browser.
	Browser *Browser
	// Harnesses is the descriptor `auth refresh` reads a credential's shape
	// out of, and `auth status` reads a re-issue command out of.
	Harnesses harness.Document
	// RefreshOptions are cred.Refresh's dependencies, so a test exercises the
	// vendor exchange without a vendor.
	RefreshOptions cred.RefreshOptions
	// Now is the clock. Nil is time.Now.
	Now func() time.Time
	// Sleep is the wait between polls. Nil sleeps for real.
	Sleep func(ctx context.Context, d time.Duration) error
	// Listen binds the loopback socket the GitHub redirect lands on. Nil is
	// Listen, which walks the shell's port list.
	Listen func() (*Listener, error)
	// Random is where the state value's bytes come from. Nil is crypto/rand.
	Random io.Reader
}

func (c *Commands) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Commands) sleep(ctx context.Context, d time.Duration) error {
	if c.Sleep != nil {
		return c.Sleep(ctx, d)
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// --- crossrev auth status ---------------------------------------------------

// Status reports every App on this machine, and what is true of it right now
// (auth_status, lib/auth.sh:363).
//
// It revalidates before it prints rather than after. The name and slug in the
// metadata file were written once, at creation; renaming the App in its
// settings moves both and nothing local notices. The command whose whole job is
// answering "is my credential set up correctly" would otherwise answer
// confidently out of a file that can be wrong (lib/auth.sh:392-397).
func (c *Commands) Status(ctx context.Context) error {
	dir := Dir(c.Env)

	// `[[ ! -d "$dir" ]] || ! compgen -G "$dir/*.json"` (lib/auth.sh:366): a
	// directory that is not there and one holding no metadata are one answer.
	metas, err := filepath.Glob(dir + "/*.json")
	if err != nil {
		return fmt.Errorf("could not list the configured Apps in %s: %w", dir, err)
	}
	if !isDirectory(dir) || len(metas) == 0 {
		c.IO.Section("Apps")
		c.IO.Opt("none configured")
		c.IO.Gap()
		c.IO.Line("CrossRev needs an App only for automated mode — the loop running on")
		c.IO.Line("GitHub events. Local runs use your own gh authentication.")
		c.IO.End("Set one up with:   crossrev auth login")
		return c.statusTokens()
	}

	c.IO.Section("Apps")
	for _, path := range metas {
		if err := c.statusApp(ctx, dir, path); err != nil {
			return err
		}
	}
	c.IO.End("An App reaches only the repositories it is installed on.")
	return c.statusTokens()
}

// statusApp is one iteration of the loop at lib/auth.sh:380-457.
func (c *Commands) statusApp(ctx context.Context, dir, path string) error {
	// The shell reads each field with `jq -r`, which answers the literal `null`
	// for an absent key. This refuses the file instead: a row reading
	// "null — null (id null, role loop: …)" reports nothing an operator can
	// act on, and every file here was written by `auth login` in one piece.
	meta, err := ReadMetadata(path)
	if err != nil {
		return err
	}
	// Owner and role come out of the file rather than out of its name
	// (lib/auth.sh:381-382). ReadMetadata already reads an absent role as the
	// loop's, which is `.role // "loop"`.
	name, slug := meta.Name, meta.Slug
	pem := PEMPath(dir, meta.Owner, meta.Role)

	// The revalidation, and the order of the three calls is the shell's: the
	// token is minted before the identity is read, so a token that exists but
	// was refused still counts as one at the installations check below
	// (lib/auth.sh:399-400 and :439).
	var jwt string
	var drift []Drift
	if isRegularFile(pem) {
		if token, err := JWT(pem, meta.ID, c.now()); err == nil {
			jwt = token
			if identity, err := c.GH.AppIdentity(ctx, jwt); err == nil {
				// A correction that cannot be written reports no drift, which
				// is what the shell's discarded return code amounts to. There
				// is nothing to say: the file on disk is unchanged and the
				// identity read is still the one printed below.
				drift, _ = SyncMeta(path, identity.Name, identity.Slug)
				if len(drift) > 0 {
					// Report the identity GitHub has, in this line and in the
					// install URL below, which was being built from the stale
					// slug too (lib/auth.sh:404-406).
					name, slug = identity.Name, identity.Slug
				}
			}
		}
	}

	c.IO.OK(fmt.Sprintf("%s — %s (id %d, role %s: %s)",
		meta.Owner, name, meta.ID, meta.Role, RoleSummary(meta.Role)))

	if len(drift) > 0 {
		for _, moved := range drift {
			c.IO.Line(fmt.Sprintf("   %s was %s, now %s", moved.Field, moved.Was, moved.Now))
		}
		c.IO.Warn(
			meta.Owner+"'s App was renamed since CrossRev recorded it — the cached copy has been corrected",
			"The slug is the half that matters. state_trusted_author falls back to it when CROSSREV_APP_SLUG is unset, so an automated run started from this machine was trusting an author that does not exist: no markers read, pass 1 for ever, nothing reconciled. Generated workflows pass the slug from the token step's app-slug output and were never affected.")
	}

	if !isRegularFile(pem) {
		c.IO.No("   key missing at " + pem + " — this App cannot mint a token")
		return nil
	}

	mode := fileMode(pem)
	if mode == "" {
		c.IO.Line("   key " + pem)
	} else {
		c.IO.Line("   key " + pem + " (" + mode + ")")
	}
	if mode != "0600" {
		c.IO.Warn(
			fmt.Sprintf("the private key for %s is mode %s, not 0600", meta.Owner, mode),
			"Any process running as you can read it, and it can mint a token for every repository this App is installed on. Fix with: chmod 600 "+pem)
	}

	// Rule 5: report what is true, not what was configured. An App installed
	// nowhere looks identical to a working one until the first API call fails.
	if jwt == "" {
		c.IO.Line("   could not check installations — the key may not match this App")
		return nil
	}
	installs, err := c.GH.Installations(ctx, jwt)
	if err != nil {
		c.IO.Line("   could not check installations — the key may not match this App")
		return nil
	}
	if len(installs) > 0 {
		for _, install := range installs {
			c.IO.Line(fmt.Sprintf("   installed on %s (%s repositories)", install.Account, install.Selection))
		}
		return nil
	}

	c.IO.No("   installed nowhere — it can reach no repository at all")
	// owner_id was added after the first Apps were registered, so recover it
	// rather than degrading the message for anything created earlier
	// (lib/auth.sh:446-448). A failed recovery drops the line: an install URL
	// with an empty target lands on a page that cannot prefill anything.
	ownerID := ownerIDText(meta.OwnerID)
	if ownerID == "" {
		if recovered, err := c.GH.AccountID(ctx, meta.Owner); err == nil {
			ownerID = recovered
		}
	}
	if ownerID != "" {
		c.IO.Next("install: " + InstallURL(slug, meta.OwnerType, ownerID))
	}
	return nil
}

// ownerIDText is `jq -r '.owner_id // empty'`: the number as text, and empty
// when the file carries none.
//
// Zero is read as absent. `// empty` answers for a missing key and for a null,
// and Metadata.OwnerID cannot tell either from a literal 0 — which is not an
// account id GitHub issues.
func ownerIDText(id int64) string {
	if id == 0 {
		return ""
	}
	return fmt.Sprintf("%d", id)
}

// --- the other half of "is this still going to work tomorrow" --------------

// statusTokens reports the long-lived tokens whose creation dates were recorded
// (_auth_status_tokens, lib/auth.sh:463).
//
// Every failure here is silent, which is the shell's `|| return 0` at :465 and
// :467: this is a trailer on a command that has already reported something, and
// a ledger nobody can read is not a reason to fail the report above it.
func (c *Commands) statusTokens() error {
	path := TokensPath(c.Env)
	if !isRegularFile(path) {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	ledger, err := decodeObject(data)
	if err != nil {
		return nil
	}
	repos := sortedKeys(ledger)
	if len(repos) == 0 {
		return nil
	}

	c.IO.Section("Long-lived tokens")
	for _, repo := range repos {
		raw, _ := ledger.get(repo)
		secrets, err := decodeObject(raw)
		if err != nil {
			// `jq '.[$r] | keys[]'` errors on a value that is not an object,
			// leaving the name loop nothing to read.
			continue
		}
		for _, name := range sortedKeys(secrets) {
			left, err := TokenDaysLeft(c.Env, repo, name, c.now())
			if err != nil {
				continue
			}
			switch {
			case left < 0:
				c.IO.No(fmt.Sprintf("%s — %s expired %d days ago", repo, name, -left))
				c.IO.Line("   Every run authenticating with it is failing. Re-issue it and set the secret again.")
			case left < 60:
				reissue := "Re-issue it and set the secret again."
				if command := c.seedCommand(name); command != "" {
					reissue = "Re-issue it with `" + command + "` and set the secret again."
				}
				c.IO.Warn(
					fmt.Sprintf("%s on %s expires in %d days", name, repo, left),
					"It cannot be re-read once issued, so nothing recovers it after the fact — the first sign of expiry is a CI failure on a day nobody is looking. "+reissue)
			default:
				c.IO.OK(fmt.Sprintf("%s — %s, %d days left", repo, name, left))
			}
		}
	}
	c.IO.End("Dates only — CrossRev never stores a token, and this one cannot be read back.")
	return nil
}

// seedCommand is what an operator runs to mint the token this secret carries,
// found by the secret's name (lib/auth.sh:480-481).
//
// The first match wins. jq's `select(.credential.secret == $s)` prints every
// match, and two would leave the shell with a two-line harness name that
// harness_get answers nothing for; the descriptor's names are unique per
// secret, so neither implementation has ever met a second.
func (c *Commands) seedCommand(secret string) string {
	for _, name := range c.Harnesses.Names() {
		descriptor, found := c.Harnesses.For(name)
		if found && descriptor.Credential.Secret == secret {
			return descriptor.Credential.SeedCommand
		}
	}
	return ""
}

// sortedKeys is `jq 'keys[]'`, which sorts. Go compares strings by byte and
// UTF-8 orders bytes the way it orders code points, so this is jq's order.
func sortedKeys(o *object) []string {
	keys := append([]string(nil), o.keys...)
	sort.Strings(keys)
	return keys
}

// --- filesystem answers the shell gets from the shell ----------------------

// isDirectory is `[[ -d "$path" ]]`.
func isDirectory(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// fileMode is the four-digit mode `auth status` prints beside the key
// (lib/auth.sh:431).
//
// `stat` drops the leading zero and the shell pads it back with `printf '%04d'`,
// because "600" next to a sentence about 0600 reads as a mismatch. The
// setuid, setgid and sticky bits are carried too: BSD's `%Lp` and GNU's `%a`
// both print all twelve bits, so a key somebody had made setgid prints 2600
// rather than 600.
func fileMode(path string) string {
	info, err := os.Stat(path)
	if err != nil {
		return ""
	}
	perm := info.Mode().Perm()
	if info.Mode()&fs.ModeSetuid != 0 {
		perm |= 0o4000
	}
	if info.Mode()&fs.ModeSetgid != 0 {
		perm |= 0o2000
	}
	if info.Mode()&fs.ModeSticky != 0 {
		perm |= 0o1000
	}
	return fmt.Sprintf("%04o", perm)
}

// --- the install URL, and the one gh read only status makes ----------------

// InstallURL is where to install an App (_auth_install_url, lib/auth.sh:131).
//
// /apps/<slug>/installations/new/permissions with target_id and target_type
// lands directly on the install form with the account already chosen. It works
// for private Apps, which the owner-settings path also does but without the
// prefill — one fewer decision for someone who has just approved permissions.
//
// Nothing is escaped, which is the shell's `printf '%s'`. A slug is
// [a-z0-9-], an owner type is one of three words, and an id is digits.
func InstallURL(slug, ownerType, ownerID string) string {
	return "https://github.com/apps/" + slug +
		"/installations/new/permissions?target_id=" + ownerID +
		"&target_type=" + ownerType
}

// AccountID is an account's numeric id, as text (`gh api "users/$owner" --jq
// .id`, lib/auth.sh:448 and :760).
//
// It is the narrower half of AccountInfo, and it is separate because both call
// sites already know the owner type and want the id alone. Its failure is not
// reported: both callers fall back rather than refuse.
func (g *GH) AccountID(ctx context.Context, login string) (string, error) {
	res := g.run(ctx, "api", "users/"+login, "--jq", ".id")
	if !answered(res) {
		return "", ghFailure("could not read the numeric id of the account "+login, res)
	}
	id := output(res)
	if id == "" {
		return "", fmt.Errorf("could not read the numeric id of the account %s: GitHub answered with none", login)
	}
	return id, nil
}

// expandHome is `${path/#\~/$HOME}`: a leading tilde and nothing else.
//
// Bash's parameter replacement is textual, so `~other` becomes `$HOME` followed
// by `other` rather than another user's home. This copies that rather than
// improving on it, because the string is a path an operator typed and the shell
// is what they typed it into.
func expandHome(path, home string) string {
	if strings.HasPrefix(path, "~") {
		return home + path[1:]
	}
	return path
}
