package app_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/carlosboeing/crossrev/internal/app"
	"github.com/carlosboeing/crossrev/internal/exec"
	"github.com/carlosboeing/crossrev/internal/harness"
	"github.com/carlosboeing/crossrev/internal/ui"
)

// --- the scaffold every command test shares --------------------------------

// bench is one command's whole world: a config home nothing else shares, one
// buffer standing in for both streams, and a recorder in place of gh.
//
// The two streams are one buffer on purpose. The blocks below were measured
// from the shell with `auth_status 2>&1`, and a warning lands between two body
// lines rather than after them — asserting them apart would lose the ordering
// that measurement proves.
type bench struct {
	env  fakeEnv
	dir  string
	out  *bytes.Buffer
	gh   *recorder
	cmds *app.Commands
}

func newBench(t *testing.T, results ...exec.Result) *bench {
	t.Helper()
	config := t.TempDir()
	env := fakeEnv{"XDG_CONFIG_HOME": config, "HOME": config}
	dir := app.Dir(env)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("creating the apps directory: %v", err)
	}
	out := &bytes.Buffer{}
	gh := &recorder{results: results}
	b := &bench{env: env, dir: dir, out: out, gh: gh}
	b.cmds = &app.Commands{
		IO:        &ui.IO{Out: out, Err: out},
		Env:       env,
		GH:        app.NewGH(gh, app.WithEnv(nil)),
		Harnesses: harnesses(t),
		Now:       func() time.Time { return at },
	}
	return b
}

// at is the instant every command test runs at, so a JWT's claims and a
// ledger's arithmetic are facts rather than ranges.
var at = time.Date(2026, 9, 2, 4, 0, 0, 0, time.UTC)

func harnesses(t *testing.T) harness.Document {
	t.Helper()
	doc, err := harness.Descriptors()
	if err != nil {
		t.Fatalf("reading the harness descriptor: %v", err)
	}
	return doc
}

// meta writes the file `auth login` writes, with the identity it had at
// creation. It is tests/test-auth.sh:97-106's fixture.
func (b *bench) meta(t *testing.T, name, slug string) string {
	t.Helper()
	path := filepath.Join(b.dir, "ShoreLogic.loop.json")
	body := `{
  "owner": "ShoreLogic",
  "owner_type": "Organization",
  "owner_id": 12345,
  "id": 987,
  "slug": "` + slug + `",
  "name": "` + name + `",
  "role": "loop",
  "created": "2026-08-13T00:00:00Z"
}
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("writing the metadata: %v", err)
	}
	return path
}

// pem puts a real key at the roled path. Real, because JWT signs with it and a
// fixture string would only prove the recorder was reached.
func (b *bench) pem(t *testing.T, mode os.FileMode) string {
	t.Helper()
	source := writePKCS1(t, key(t))
	body, err := os.ReadFile(source)
	if err != nil {
		t.Fatalf("reading the key: %v", err)
	}
	path := filepath.Join(b.dir, "ShoreLogic.loop.pem")
	if err := os.WriteFile(path, body, mode); err != nil {
		t.Fatalf("writing the key: %v", err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("setting the key's mode: %v", err)
	}
	return path
}

func (b *bench) text() string { return b.out.String() }

// wantBlock compares a whole printed block, which is how these were measured.
func wantBlock(t *testing.T, got, want string) {
	t.Helper()
	if got != want {
		t.Fatalf("printed:\n%s\nwant:\n%s", got, want)
	}
}

// identity is the answer `gh api … /app --jq .name, .slug` gives.
func identity(name, slug string) exec.Result { return out(name + "\n" + slug + "\n") }

// --- auth status, with nothing configured ----------------------------------

func TestStatusWithNoAppConfiguredSaysWhatAnAppIsFor(t *testing.T) {
	b := newBench(t)
	if err := b.cmds.Status(context.Background()); err != nil {
		t.Fatalf("Status: %v", err)
	}
	wantBlock(t, b.text(), "\n◇  Apps\n"+
		"│  ○ none configured\n"+
		"│\n"+
		"│  CrossRev needs an App only for automated mode — the loop running on\n"+
		"│  GitHub events. Local runs use your own gh authentication.\n"+
		"└  Set one up with:   crossrev auth login\n\n")
}

func TestStatusStartsNoProcessWhenNothingIsConfigured(t *testing.T) {
	b := newBench(t)
	if err := b.cmds.Status(context.Background()); err != nil {
		t.Fatalf("Status: %v", err)
	}
	if len(b.gh.specs) != 0 {
		t.Fatalf("gh was invoked %d times, want none", len(b.gh.specs))
	}
}

// A directory that does not exist is the same answer as one holding no
// metadata: `[[ ! -d "$dir" ]] || ! compgen -G "$dir/*.json"` (lib/auth.sh:380).
func TestStatusTreatsAnAbsentDirectoryAsNothingConfigured(t *testing.T) {
	b := newBench(t)
	if err := os.RemoveAll(b.dir); err != nil {
		t.Fatalf("removing the apps directory: %v", err)
	}
	if err := b.cmds.Status(context.Background()); err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !strings.Contains(b.text(), "none configured") {
		t.Fatalf("printed:\n%s", b.text())
	}
}

// --- auth status, with an App that was renamed on GitHub -------------------

func TestStatusReportsTheRenameAndCorrectsTheCache(t *testing.T) {
	b := newBench(t,
		identity("CrossRev ShoreLogic", "crossrev-shorelogic"),
		out("ShoreLogic selected\n"))
	path := b.meta(t, "revloop-ShoreLogic", "revloop-shorelogic")
	pem := b.pem(t, 0o600)

	if err := b.cmds.Status(context.Background()); err != nil {
		t.Fatalf("Status: %v", err)
	}

	wantBlock(t, b.text(), "\n◇  Apps\n"+
		"│  ✓ ShoreLogic — CrossRev ShoreLogic (id 987, role loop: contents:write, issues:write, pull_requests:write)\n"+
		"│     name was revloop-ShoreLogic, now CrossRev ShoreLogic\n"+
		"│     slug was revloop-shorelogic, now crossrev-shorelogic\n"+
		"\n⚠  ShoreLogic's App was renamed since CrossRev recorded it — the cached copy has been corrected\n"+
		"   The slug is the half that matters. state_trusted_author falls back to it when CROSSREV_APP_SLUG is unset, so an automated run started from this machine was trusting an author that does not exist: no markers read, pass 1 for ever, nothing reconciled. Generated workflows pass the slug from the token step's app-slug output and were never affected.\n\n"+
		"│     key "+pem+" (0600)\n"+
		"│     installed on ShoreLogic (selected repositories)\n"+
		"└  An App reaches only the repositories it is installed on.\n\n")

	// The assertion the whole thing exists for: marker trust reads this field.
	meta, err := app.ReadMetadata(path)
	if err != nil {
		t.Fatalf("ReadMetadata: %v", err)
	}
	if meta.Slug != "crossrev-shorelogic" {
		t.Fatalf("cached slug = %q, want %q", meta.Slug, "crossrev-shorelogic")
	}
	if meta.Created != "2026-08-13T00:00:00Z" {
		t.Fatalf("the registration date was restamped: %q", meta.Created)
	}
}

func TestStatusOnAnAppThatWasNotRenamedReportsNoDrift(t *testing.T) {
	b := newBench(t,
		identity("CrossRev ShoreLogic", "crossrev-shorelogic"),
		out("ShoreLogic selected\n"))
	b.meta(t, "CrossRev ShoreLogic", "crossrev-shorelogic")
	pem := b.pem(t, 0o600)

	if err := b.cmds.Status(context.Background()); err != nil {
		t.Fatalf("Status: %v", err)
	}
	wantBlock(t, b.text(), "\n◇  Apps\n"+
		"│  ✓ ShoreLogic — CrossRev ShoreLogic (id 987, role loop: contents:write, issues:write, pull_requests:write)\n"+
		"│     key "+pem+" (0600)\n"+
		"│     installed on ShoreLogic (selected repositories)\n"+
		"└  An App reaches only the repositories it is installed on.\n\n")
}

// A rename away from a name with spaces reports the old name whole, which is
// why the shell splits on tabs and this carries three fields
// (tests/test-auth.sh:211-219).
func TestStatusReportsANameWithSpacesWhole(t *testing.T) {
	b := newBench(t,
		identity("CrossRev ShoreLogic", "crossrev-shorelogic"),
		out("ShoreLogic selected\n"))
	b.meta(t, "CrossRev Shore Logic", "crossrev-shore-logic")
	b.pem(t, 0o600)

	if err := b.cmds.Status(context.Background()); err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !strings.Contains(b.text(), "name was CrossRev Shore Logic, now CrossRev ShoreLogic") {
		t.Fatalf("printed:\n%s", b.text())
	}
}

// --- an App installed nowhere ----------------------------------------------

func TestStatusOnAnAppInstalledNowherePrintsTheInstallURL(t *testing.T) {
	b := newBench(t,
		identity("CrossRev ShoreLogic", "crossrev-shorelogic"),
		out(""))
	b.meta(t, "CrossRev ShoreLogic", "crossrev-shorelogic")
	pem := b.pem(t, 0o600)

	if err := b.cmds.Status(context.Background()); err != nil {
		t.Fatalf("Status: %v", err)
	}
	wantBlock(t, b.text(), "\n◇  Apps\n"+
		"│  ✓ ShoreLogic — CrossRev ShoreLogic (id 987, role loop: contents:write, issues:write, pull_requests:write)\n"+
		"│     key "+pem+" (0600)\n"+
		"│  ✗    installed nowhere — it can reach no repository at all\n"+
		"│  → install: https://github.com/apps/crossrev-shorelogic/installations/new/permissions?target_id=12345&target_type=Organization\n"+
		"└  An App reaches only the repositories it is installed on.\n\n")
}

// owner_id was added after the first Apps were registered, so an older file has
// none and the id is recovered rather than the message degraded
// (lib/auth.sh:460-463).
func TestStatusRecoversAnAbsentOwnerIDFromGitHub(t *testing.T) {
	b := newBench(t,
		identity("CrossRev ShoreLogic", "crossrev-shorelogic"),
		out(""),
		out("12345\n"))
	path := filepath.Join(b.dir, "ShoreLogic.loop.json")
	body := `{"owner":"ShoreLogic","owner_type":"Organization","id":987,` +
		`"slug":"crossrev-shorelogic","name":"CrossRev ShoreLogic","role":"loop"}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("writing the metadata: %v", err)
	}
	b.pem(t, 0o600)

	if err := b.cmds.Status(context.Background()); err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !strings.Contains(b.text(), "install: https://github.com/apps/crossrev-shorelogic/installations/new/permissions?target_id=12345&target_type=Organization") {
		t.Fatalf("printed:\n%s", b.text())
	}
	if got, want := b.gh.specs[2].Args, []string{"api", "users/ShoreLogic", "--jq", ".id"}; !equalArgs(got, want) {
		t.Fatalf("argv = %q, want %q", got, want)
	}
}

// And an id GitHub will not answer for leaves the line out rather than printing
// an install URL with an empty target.
func TestStatusLeavesTheInstallURLOutWithNoOwnerID(t *testing.T) {
	b := newBench(t,
		identity("CrossRev ShoreLogic", "crossrev-shorelogic"),
		out(""),
		bad())
	path := filepath.Join(b.dir, "ShoreLogic.loop.json")
	body := `{"owner":"ShoreLogic","owner_type":"Organization","id":987,` +
		`"slug":"crossrev-shorelogic","name":"CrossRev ShoreLogic","role":"loop"}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("writing the metadata: %v", err)
	}
	b.pem(t, 0o600)

	if err := b.cmds.Status(context.Background()); err != nil {
		t.Fatalf("Status: %v", err)
	}
	if strings.Contains(b.text(), "install:") {
		t.Fatalf("printed an install URL with no target:\n%s", b.text())
	}
	if !strings.Contains(b.text(), "installed nowhere") {
		t.Fatalf("printed:\n%s", b.text())
	}
}

// --- the key's mode --------------------------------------------------------

func TestStatusWarnsAboutAKeyWiderThan0600(t *testing.T) {
	// Two refusals, not one: the identity read and the installations read are
	// separate calls, and a recorder that ran out of answers would invent one.
	b := newBench(t, bad(), bad())
	b.meta(t, "CrossRev ShoreLogic", "crossrev-shorelogic")
	pem := b.pem(t, 0o644)

	if err := b.cmds.Status(context.Background()); err != nil {
		t.Fatalf("Status: %v", err)
	}
	wantBlock(t, b.text(), "\n◇  Apps\n"+
		"│  ✓ ShoreLogic — CrossRev ShoreLogic (id 987, role loop: contents:write, issues:write, pull_requests:write)\n"+
		"│     key "+pem+" (0644)\n"+
		"\n⚠  the private key for ShoreLogic is mode 0644, not 0600\n"+
		"   Any process running as you can read it, and it can mint a token for every repository this App is installed on. Fix with: chmod 600 "+pem+"\n\n"+
		"│     could not check installations — the key may not match this App\n"+
		"└  An App reaches only the repositories it is installed on.\n\n")

	// Both calls were made, which is the ordering at lib/auth.sh:413 and :439:
	// the token is minted before the identity is read, so a refused identity
	// still leaves a token for the installations check to try. Setting the
	// token after the identity read instead would skip this second call and
	// print the same line for a different reason.
	if len(b.gh.specs) != 2 {
		t.Fatalf("gh was invoked %d times, want the identity read and the installations read", len(b.gh.specs))
	}
}

// stat drops the leading zero and the shell pads it back, so a mode is four
// digits whatever it is (lib/auth.sh:445).
func TestStatusPrintsTheModeAsFourDigits(t *testing.T) {
	for _, mode := range []os.FileMode{0o600, 0o644, 0o400, 0o000} {
		b := newBench(t, bad(), bad())
		b.meta(t, "CrossRev ShoreLogic", "crossrev-shorelogic")
		b.pem(t, mode)
		if err := b.cmds.Status(context.Background()); err != nil {
			t.Fatalf("Status: %v", err)
		}
		if !strings.Contains(b.text(), " ("+fourDigits(mode)+")\n") {
			t.Fatalf("mode %o printed:\n%s", mode, b.text())
		}
	}
}

func fourDigits(mode os.FileMode) string {
	digits := "0000"
	octal := ""
	for shift := 9; shift >= 0; shift -= 3 {
		octal += string(rune('0' + byte((mode>>shift)&7)))
	}
	return digits[:4-len(octal)] + octal
}

func TestStatusOnAMissingKeySaysTheAppCannotMintAToken(t *testing.T) {
	b := newBench(t)
	b.meta(t, "CrossRev ShoreLogic", "crossrev-shorelogic")

	if err := b.cmds.Status(context.Background()); err != nil {
		t.Fatalf("Status: %v", err)
	}
	wantBlock(t, b.text(), "\n◇  Apps\n"+
		"│  ✓ ShoreLogic — CrossRev ShoreLogic (id 987, role loop: contents:write, issues:write, pull_requests:write)\n"+
		"│  ✗    key missing at "+filepath.Join(b.dir, "ShoreLogic.loop.pem")+" — this App cannot mint a token\n"+
		"└  An App reaches only the repositories it is installed on.\n\n")
	if len(b.gh.specs) != 0 {
		t.Fatalf("gh was invoked %d times with no key on disk, want none", len(b.gh.specs))
	}
}

// A key that cannot sign never reaches GitHub, and the line an operator sees is
// the same one an unreachable API prints.
func TestStatusOnAKeyThatCannotSignReportsTheInstallationsUnchecked(t *testing.T) {
	b := newBench(t)
	b.meta(t, "CrossRev ShoreLogic", "crossrev-shorelogic")
	path := filepath.Join(b.dir, "ShoreLogic.loop.pem")
	if err := os.WriteFile(path, []byte("not a key\n"), 0o600); err != nil {
		t.Fatalf("writing the key: %v", err)
	}

	if err := b.cmds.Status(context.Background()); err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !strings.Contains(b.text(), "could not check installations — the key may not match this App") {
		t.Fatalf("printed:\n%s", b.text())
	}
	if len(b.gh.specs) != 0 {
		t.Fatalf("gh was invoked %d times with an unusable key, want none", len(b.gh.specs))
	}
}

// --- an unreachable API is not evidence the cache is wrong -----------------

func TestStatusLeavesTheCacheAloneWhenGitHubCannotBeReached(t *testing.T) {
	b := newBench(t, bad(), bad())
	path := b.meta(t, "revloop-ShoreLogic", "revloop-shorelogic")
	b.pem(t, 0o600)

	if err := b.cmds.Status(context.Background()); err != nil {
		t.Fatalf("Status: %v", err)
	}
	if strings.Contains(b.text(), "renamed") {
		t.Fatalf("claimed a rename from an unreachable API:\n%s", b.text())
	}
	meta, err := app.ReadMetadata(path)
	if err != nil {
		t.Fatalf("ReadMetadata: %v", err)
	}
	if meta.Slug != "revloop-shorelogic" {
		t.Fatalf("the cache moved on an unreachable API: %q", meta.Slug)
	}
}

// --- several Apps ----------------------------------------------------------

// The shell iterates `"$dir"/*.json`, which bash expands in sorted order, and
// the role comes out of the file rather than out of its name (lib/auth.sh:394).
func TestStatusReportsEveryAppInPathOrder(t *testing.T) {
	b := newBench(t, bad(), bad())
	write := func(name, owner, role string) {
		body := `{"owner":"` + owner + `","owner_type":"User","owner_id":1,"id":7,` +
			`"slug":"s","name":"n","role":"` + role + `"}`
		if err := os.WriteFile(filepath.Join(b.dir, name), []byte(body), 0o600); err != nil {
			t.Fatalf("writing the metadata: %v", err)
		}
	}
	write("zeta.loop.json", "zeta", "loop")
	write("alpha.refresher.json", "alpha", "refresher")

	if err := b.cmds.Status(context.Background()); err != nil {
		t.Fatalf("Status: %v", err)
	}
	alpha := strings.Index(b.text(), "alpha — n (id 7, role refresher: secrets:write (repository secrets only))")
	zeta := strings.Index(b.text(), "zeta — n (id 7, role loop: contents:write, issues:write, pull_requests:write)")
	if alpha < 0 || zeta < 0 {
		t.Fatalf("printed:\n%s", b.text())
	}
	if alpha > zeta {
		t.Fatalf("the Apps came out in the wrong order:\n%s", b.text())
	}
}

// A file with no role key is the loop's: anything registered before roles
// existed has none, and `.role // "loop"` is what reads it (lib/auth.sh:398).
func TestStatusReadsAnAbsentRoleAsTheLoops(t *testing.T) {
	b := newBench(t)
	body := `{"owner":"acme","owner_type":"User","owner_id":1,"id":7,"slug":"s","name":"n"}`
	if err := os.WriteFile(filepath.Join(b.dir, "acme.json"), []byte(body), 0o600); err != nil {
		t.Fatalf("writing the metadata: %v", err)
	}
	if err := b.cmds.Status(context.Background()); err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !strings.Contains(b.text(), "role loop: contents:write, issues:write, pull_requests:write") {
		t.Fatalf("printed:\n%s", b.text())
	}
}

// --- the token ledger ------------------------------------------------------

func (b *bench) ledger(t *testing.T, body string) {
	t.Helper()
	path := app.TokensPath(b.env)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("creating the ledger's directory: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("writing the ledger: %v", err)
	}
}

// created is a stamp `days` before the instant every test runs at.
func created(days int) string {
	return at.AddDate(0, 0, -days).Format("2006-01-02T15:04:05Z")
}

func TestStatusTokensReportsHealthyExpiringAndExpired(t *testing.T) {
	b := newBench(t)
	b.ledger(t, `{"acme/widget":{`+
		`"CLAUDE_CODE_OAUTH_TOKEN":{"created":"`+created(340)+`","valid_days":365},`+
		`"SOMETHING_ELSE":{"created":"`+created(400)+`","valid_days":365}},`+
		`"beta/thing":{"CLAUDE_CODE_OAUTH_TOKEN":{"created":"`+created(0)+`","valid_days":365}}}`)

	if err := b.cmds.Status(context.Background()); err != nil {
		t.Fatalf("Status: %v", err)
	}
	block := b.text()[strings.Index(b.text(), "\n◇  Long-lived tokens"):]
	wantBlock(t, block, "\n◇  Long-lived tokens\n"+
		"\n⚠  CLAUDE_CODE_OAUTH_TOKEN on acme/widget expires in 25 days\n"+
		"   It cannot be re-read once issued, so nothing recovers it after the fact — the first sign of expiry is a CI failure on a day nobody is looking. Re-issue it with `claude setup-token` and set the secret again.\n\n"+
		"│  ✗ acme/widget — SOMETHING_ELSE expired 35 days ago\n"+
		"│     Every run authenticating with it is failing. Re-issue it and set the secret again.\n"+
		"│  ✓ beta/thing — CLAUDE_CODE_OAUTH_TOKEN, 365 days left\n"+
		"└  Dates only — CrossRev never stores a token, and this one cannot be read back.\n\n")
}

// A secret the descriptor does not name gets the same warning without the
// re-issue command, because there is none to name (lib/auth.sh:499-501).
func TestStatusTokensOmitsASeedCommandItDoesNotHave(t *testing.T) {
	b := newBench(t)
	b.ledger(t, `{"acme/widget":{"MYSTERY_TOKEN":{"created":"`+created(340)+`","valid_days":365}}}`)

	if err := b.cmds.Status(context.Background()); err != nil {
		t.Fatalf("Status: %v", err)
	}
	block := b.text()[strings.Index(b.text(), "\n◇  Long-lived tokens"):]
	wantBlock(t, block, "\n◇  Long-lived tokens\n"+
		"\n⚠  MYSTERY_TOKEN on acme/widget expires in 25 days\n"+
		"   It cannot be re-read once issued, so nothing recovers it after the fact — the first sign of expiry is a CI failure on a day nobody is looking. Re-issue it and set the secret again.\n\n"+
		"└  Dates only — CrossRev never stores a token, and this one cannot be read back.\n\n")
}

// The seed command is looked up by the secret's name, so the refresher's own
// secret names its own command.
func TestStatusTokensNamesTheSeedCommandForTheSecretItBelongsTo(t *testing.T) {
	b := newBench(t)
	b.ledger(t, `{"acme/widget":{"CROSSREV_CODEX_AUTH":{"created":"`+created(340)+`","valid_days":365}}}`)

	if err := b.cmds.Status(context.Background()); err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !strings.Contains(b.text(), "Re-issue it with `codex login` and set the secret again.") {
		t.Fatalf("printed:\n%s", b.text())
	}
}

// An entry whose date cannot be read is skipped rather than reported as
// anything: `auth_token_days_left … || continue` (lib/auth.sh:488).
func TestStatusTokensSkipsAnEntryWithAnUnreadableDate(t *testing.T) {
	b := newBench(t)
	b.ledger(t, `{"acme/widget":{"BAD":{"created":"nope","valid_days":365}}}`)

	if err := b.cmds.Status(context.Background()); err != nil {
		t.Fatalf("Status: %v", err)
	}
	block := b.text()[strings.Index(b.text(), "\n◇  Long-lived tokens"):]
	wantBlock(t, block, "\n◇  Long-lived tokens\n"+
		"└  Dates only — CrossRev never stores a token, and this one cannot be read back.\n\n")
}

func TestStatusTokensPrintsNothingWithoutALedger(t *testing.T) {
	b := newBench(t)
	if err := b.cmds.Status(context.Background()); err != nil {
		t.Fatalf("Status: %v", err)
	}
	if strings.Contains(b.text(), "Long-lived tokens") {
		t.Fatalf("printed a token section with no ledger:\n%s", b.text())
	}
}

// A ledger jq cannot read is `|| return 0`, not a refusal (lib/auth.sh:481).
func TestStatusTokensPrintsNothingForALedgerItCannotRead(t *testing.T) {
	for name, body := range map[string]string{
		"not JSON":     "nope",
		"an array":     "[]",
		"empty object": "{}",
	} {
		t.Run(name, func(t *testing.T) {
			b := newBench(t)
			b.ledger(t, body)
			if err := b.cmds.Status(context.Background()); err != nil {
				t.Fatalf("Status: %v", err)
			}
			if strings.Contains(b.text(), "Long-lived tokens") {
				t.Fatalf("printed a token section for %s:\n%s", name, b.text())
			}
		})
	}
}

// --- what must never be printed --------------------------------------------

// The JWT is in one argument and nowhere else. Whoever holds one can act as the
// App until it expires, and this block reaches a terminal and a run log.
func TestStatusPrintsNoTokenAndNoKeyMaterial(t *testing.T) {
	b := newBench(t,
		identity("CrossRev ShoreLogic", "crossrev-shorelogic"),
		out("ShoreLogic selected\n"))
	b.meta(t, "revloop-ShoreLogic", "revloop-shorelogic")
	pemPath := b.pem(t, 0o600)

	if err := b.cmds.Status(context.Background()); err != nil {
		t.Fatalf("Status: %v", err)
	}

	body, err := os.ReadFile(pemPath)
	if err != nil {
		t.Fatalf("reading the key: %v", err)
	}
	for _, line := range strings.Split(string(body), "\n") {
		if len(line) < 20 || strings.HasPrefix(line, "-----") {
			continue
		}
		if strings.Contains(b.text(), line) {
			t.Fatalf("a line of the private key was printed")
		}
	}

	var jwt string
	for _, spec := range b.gh.specs {
		for _, arg := range spec.Args {
			if strings.HasPrefix(arg, "Authorization: Bearer ") {
				jwt = strings.TrimPrefix(arg, "Authorization: Bearer ")
			}
		}
	}
	if jwt == "" {
		t.Fatal("no Authorization header reached gh, so this proves nothing")
	}
	if strings.Contains(b.text(), jwt) {
		t.Fatal("the JWT was printed")
	}
	for _, spec := range b.gh.specs {
		for _, entry := range spec.Env {
			if strings.Contains(entry, jwt) {
				t.Fatal("the JWT reached gh's environment")
			}
		}
	}
}

// --- the install URL -------------------------------------------------------

func TestInstallURLPrefillsTheTarget(t *testing.T) {
	got := app.InstallURL("crossrev-shorelogic", "Organization", "12345")
	want := "https://github.com/apps/crossrev-shorelogic/installations/new/permissions?target_id=12345&target_type=Organization"
	if got != want {
		t.Fatalf("InstallURL = %q\nwant        %q", got, want)
	}
	got = app.InstallURL("crossrev-me", "User", "339")
	want = "https://github.com/apps/crossrev-me/installations/new/permissions?target_id=339&target_type=User"
	if got != want {
		t.Fatalf("InstallURL = %q\nwant        %q", got, want)
	}
}

func equalArgs(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range want {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
