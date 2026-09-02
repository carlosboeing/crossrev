package app_test

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/carlosboeing/crossrev/internal/app"
	"github.com/carlosboeing/crossrev/internal/exec"
	"github.com/carlosboeing/crossrev/internal/ui"
)

// newKey writes a second real RSA key somewhere a rotation can pick it up. It
// is the file GitHub downloads.
func newKey(t *testing.T, path string) string {
	t.Helper()
	block := &pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key(t))}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("creating %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatalf("writing the key: %v", err)
	}
	return path
}

// freshKey is newKey with a modification time the injected clock reads as
// recent, which is what `find -newermt '-5 minutes'` selects on.
func freshKey(t *testing.T, path string) string {
	t.Helper()
	written := newKey(t, path)
	if err := os.Chtimes(written, at, at); err != nil {
		t.Fatalf("stamping the file: %v", err)
	}
	return written
}

// rotatePanel is the block every rotation prints before it does anything, with
// the current key's path and the App's id filled in.
func rotatePanel(name, keyPath, id, role string) string {
	return "\n◇  Rotate the private key for " + name + "\n" +
		"│  Current key   " + keyPath + "\n" +
		"│  App           id " + id + ", role " + role + "\n" +
		"│\n" +
		"│  GitHub has no API for generating an App key, so this part happens in\n" +
		"│  the browser: press 'Generate a private key' and the .pem downloads.\n" +
		"│  CrossRev picks it up, proves it works as this App, and installs it.\n" +
		"│\n" +
		"│  Nothing is replaced until the new key authenticates, and the old one\n" +
		"│  keeps working until you delete it on GitHub — so a failure here leaves\n" +
		"│  you exactly where you started.\n" +
		"\n"
}

// --- the panel, and the confirmation that guards the browser ---------------

func TestRotatePrintsThePanelAndStopsWhenDeclined(t *testing.T) {
	b := newBench(t)
	b.meta(t, "CrossRev ShoreLogic", "crossrev-shorelogic")
	b.pem(t, 0o600)
	o := b.browser(&opener{})
	b.answers(t, "n")

	err := b.cmds.Rotate(context.Background(), app.RotateRequest{Owner: "ShoreLogic", Role: "loop"})
	if !errors.Is(err, app.ErrDeclined) {
		t.Fatalf("error = %v, want ErrDeclined", err)
	}
	wantBlock(t, b.text(), rotatePanel("CrossRev ShoreLogic", filepath.Join(b.dir, "ShoreLogic.loop.pem"), "987", "loop")+
		"◆  Open GitHub?  [y/N] "+
		"  Nothing was changed.\n")
	if len(o.urls) != 0 {
		t.Fatalf("a browser was opened: %q", o.urls)
	}
}

// --key skips the browser half entirely: there is nothing to confirm, because
// the file it names is already on disk (lib/auth.sh:871).
func TestRotateWithAKeyGivenAsksNothingAndOpensNothing(t *testing.T) {
	b := newBench(t, out("crossrev-shorelogic\n"))
	b.meta(t, "CrossRev ShoreLogic", "crossrev-shorelogic")
	old := b.pem(t, 0o600)
	o := b.browser(&opener{})
	downloaded := newKey(t, filepath.Join(t.TempDir(), "downloaded.pem"))

	if err := b.cmds.Rotate(context.Background(), app.RotateRequest{
		Owner: "ShoreLogic", Role: "loop", KeyFile: downloaded,
	}); err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	if len(o.urls) != 0 {
		t.Fatalf("a browser was opened: %q", o.urls)
	}
	if strings.Contains(b.text(), "Open GitHub?") {
		t.Fatalf("asked a question it had the answer to:\n%s", b.text())
	}

	wantBlock(t, b.text(), rotatePanel("CrossRev ShoreLogic", old, "987", "loop")+
		"\n◇  Rotated\n"+
		"│  ✓ new key installed at "+old+", and it authenticates as this App\n"+
		"│     previous key kept at "+old+".previous\n"+
		"│\n"+
		"│  Two things are still yours to do, and both are outward-facing:\n"+
		"│  → delete the old key on GitHub: https://github.com/organizations/ShoreLogic/settings/apps/crossrev-shorelogic#private-key\n"+
		"│  → update APP_PRIVATE_KEY wherever it is stored: crossrev init --upgrade, or gh secret set\n"+
		"└  Until the secret carries the new key, CI is still authenticating with the old one.\n\n")

	installed, err := os.ReadFile(old)
	if err != nil {
		t.Fatalf("reading the installed key: %v", err)
	}
	source, err := os.ReadFile(downloaded)
	if err != nil {
		t.Fatalf("reading the downloaded key: %v", err)
	}
	if string(installed) != string(source) {
		t.Fatal("the downloaded key was not installed")
	}
	wantMode(t, old, 0o600)
	wantMode(t, old+".previous", 0o600)

	// The proof happens before anything is replaced, and it is a call as the
	// App rather than a parse of the file.
	args := b.gh.specs[0].Args
	if len(args) != 6 || args[0] != "api" || args[1] != "-H" ||
		!strings.HasPrefix(args[2], "Authorization: Bearer ") ||
		args[3] != "/app" || args[4] != "--jq" || args[5] != ".slug" {
		t.Fatalf("argv = %q", args)
	}
}

// --- what is refused, and what is left alone -------------------------------

func TestRotateRefusesWhenNoAppIsConfigured(t *testing.T) {
	b := newBench(t)
	err := b.cmds.Rotate(context.Background(), app.RotateRequest{Owner: "Nobody", Role: "loop"})
	wantRefusal(t, err,
		"no loop App is configured for Nobody",
		"There is nothing to rotate. Register one with: crossrev auth login --owner Nobody --role loop")
}

func TestRotateRefusesWhenTheOwnerCannotBeDetected(t *testing.T) {
	b := newBench(t, bad())
	err := b.cmds.Rotate(context.Background(), app.RotateRequest{Role: "loop"})
	wantRefusal(t, err,
		"could not work out which owner's key to rotate",
		"Name it: crossrev auth rotate --owner <owner>")
}

func TestRotateRefusesAKeyFileThatIsNotThere(t *testing.T) {
	b := newBench(t)
	b.meta(t, "CrossRev ShoreLogic", "crossrev-shorelogic")
	b.pem(t, 0o600)
	b.browser(&opener{})
	missing := filepath.Join(t.TempDir(), "absent.pem")

	err := b.cmds.Rotate(context.Background(), app.RotateRequest{
		Owner: "ShoreLogic", Role: "loop", KeyFile: missing,
	})
	wantRefusal(t, err,
		"there is no file at "+missing,
		"Point --key at the .pem GitHub downloaded.")
}

// A file that is not a key is caught where the fault is, before GitHub is
// asked anything.
func TestRotateRefusesAFileItCannotSignWith(t *testing.T) {
	b := newBench(t)
	b.meta(t, "CrossRev ShoreLogic", "crossrev-shorelogic")
	b.pem(t, 0o600)
	b.browser(&opener{})
	notAKey := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(notAKey, []byte("this is not a key\n"), 0o600); err != nil {
		t.Fatalf("writing the file: %v", err)
	}

	err := b.cmds.Rotate(context.Background(), app.RotateRequest{
		Owner: "ShoreLogic", Role: "loop", KeyFile: notAKey,
	})
	wantRefusal(t, err,
		"could not sign a token with "+notAKey,
		"It has to be the RSA private key GitHub generated for this App. Nothing was changed.")
	if len(b.gh.specs) != 0 {
		t.Fatalf("GitHub was asked about a file that is not a key")
	}
}

// A key that belongs to another App signs perfectly well and is still refused,
// because the proof is a call as this App (lib/auth.sh:904-906).
func TestRotateRefusesAKeyGitHubWillNotAcceptAndChangesNothing(t *testing.T) {
	b := newBench(t, bad())
	b.meta(t, "CrossRev ShoreLogic", "crossrev-shorelogic")
	old := b.pem(t, 0o600)
	before, err := os.ReadFile(old)
	if err != nil {
		t.Fatalf("reading the key: %v", err)
	}
	b.browser(&opener{})
	downloaded := newKey(t, filepath.Join(t.TempDir(), "downloaded.pem"))

	rotateErr := b.cmds.Rotate(context.Background(), app.RotateRequest{
		Owner: "ShoreLogic", Role: "loop", KeyFile: downloaded,
	})
	wantRefusal(t, rotateErr,
		"GitHub rejected a token signed with "+downloaded+" for App id 987",
		"That key belongs to a different App, or the download is incomplete. The existing key is untouched.")

	after, err := os.ReadFile(old)
	if err != nil {
		t.Fatalf("reading the key: %v", err)
	}
	if string(after) != string(before) {
		t.Fatal("the existing key was replaced by one GitHub rejected")
	}
	if _, err := os.Stat(old + ".previous"); !os.IsNotExist(err) {
		t.Fatal("a backup was taken before the new key was proved")
	}
}

// --- the legacy path -------------------------------------------------------

// The unroled name would otherwise keep winning for the loop role and the
// rotation would look successful while nothing had changed (lib/auth.sh:912-914).
func TestRotateRemovesTheLegacyUnroledKey(t *testing.T) {
	b := newBench(t, out("crossrev-shorelogic\n"))
	b.meta(t, "CrossRev ShoreLogic", "crossrev-shorelogic")
	legacy := filepath.Join(b.dir, "ShoreLogic.pem")
	newKey(t, legacy)
	downloaded := newKey(t, filepath.Join(t.TempDir(), "downloaded.pem"))
	b.browser(&opener{})

	if err := b.cmds.Rotate(context.Background(), app.RotateRequest{
		Owner: "ShoreLogic", Role: "loop", KeyFile: downloaded,
	}); err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Fatal("the legacy key survived the rotation and still wins the lookup")
	}
	roled := filepath.Join(b.dir, "ShoreLogic.loop.pem")
	if _, err := os.Stat(roled); err != nil {
		t.Fatalf("the roled key is not there: %v", err)
	}
	wantMode(t, roled, 0o600)
	// The backup sits beside the ROLED destination, not beside the legacy name
	// it replaced: `backup="$dest.previous"` (lib/auth.sh:908-909).
	if !strings.Contains(b.text(), "previous key kept at "+roled+".previous") {
		t.Fatalf("printed:\n%s", b.text())
	}
	wantMode(t, roled+".previous", 0o600)
}

// --- the role decides which secret to update -------------------------------

// Told to update APP_PRIVATE_KEY after rotating the refresher's key, somebody
// following that literally puts the refresher's key material behind the loop
// App's identity (lib/auth.sh:922-927).
func TestRotateNamesTheRolesOwnSecretAndItsScope(t *testing.T) {
	b := newBench(t, out("crossrev-refresh-shorelogic\n"))
	body := `{"owner":"ShoreLogic","owner_type":"Organization","owner_id":12345,"id":988,` +
		`"slug":"crossrev-refresh-shorelogic","name":"CrossRev Refresh ShoreLogic","role":"refresher"}`
	if err := os.WriteFile(filepath.Join(b.dir, "ShoreLogic.refresher.json"), []byte(body), 0o600); err != nil {
		t.Fatalf("writing the metadata: %v", err)
	}
	b.browser(&opener{})
	downloaded := newKey(t, filepath.Join(t.TempDir(), "downloaded.pem"))

	if err := b.cmds.Rotate(context.Background(), app.RotateRequest{
		Owner: "ShoreLogic", Role: "refresher", KeyFile: downloaded,
	}); err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	want := "│  → update CROSSREV_REFRESH_APP_PRIVATE_KEY wherever it is stored: crossrev init --upgrade, or gh secret set\n" +
		"│     repository-scoped: this key can write secrets, so it must never be\n" +
		"│     an organisation secret visible to every workflow in the org\n"
	if !strings.Contains(b.text(), want) {
		t.Fatalf("printed:\n%s", b.text())
	}
	if strings.Contains(b.text(), "update APP_PRIVATE_KEY") {
		t.Fatalf("named the loop's secret for a refresher rotation:\n%s", b.text())
	}
}

// The loop's rotation carries no organisation-secret warning, because its key
// cannot write a secret at all.
func TestRotateOnTheLoopRoleCarriesNoSecretsScopeNote(t *testing.T) {
	b := newBench(t, out("crossrev-shorelogic\n"))
	b.meta(t, "CrossRev ShoreLogic", "crossrev-shorelogic")
	b.pem(t, 0o600)
	b.browser(&opener{})
	downloaded := newKey(t, filepath.Join(t.TempDir(), "downloaded.pem"))

	if err := b.cmds.Rotate(context.Background(), app.RotateRequest{
		Owner: "ShoreLogic", Role: "loop", KeyFile: downloaded,
	}); err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	if strings.Contains(b.text(), "repository-scoped") {
		t.Fatalf("printed:\n%s", b.text())
	}
}

// A user-owned App's key lives on the personal settings page
// (lib/auth.sh:851-856).
func TestRotatePointsAUserOwnedAppAtItsOwnSettingsPage(t *testing.T) {
	b := newBench(t)
	body := `{"owner":"me","owner_type":"User","owner_id":1,"id":5,` +
		`"slug":"crossrev-me","name":"CrossRev me","role":"loop"}`
	if err := os.WriteFile(filepath.Join(b.dir, "me.loop.json"), []byte(body), 0o600); err != nil {
		t.Fatalf("writing the metadata: %v", err)
	}
	o := b.browser(&opener{})
	b.answers(t, "n")

	_ = b.cmds.Rotate(context.Background(), app.RotateRequest{Owner: "me", Role: "loop"})
	if len(o.urls) != 0 {
		t.Fatalf("a browser was opened after a refusal: %q", o.urls)
	}

	// Say yes this time, and the browser goes to the personal page.
	b2 := newBench(t)
	if err := os.WriteFile(filepath.Join(b2.dir, "me.loop.json"), []byte(body), 0o600); err != nil {
		t.Fatalf("writing the metadata: %v", err)
	}
	o2 := b2.browser(&opener{})
	b2.assumeYes(t)
	b2.noWait()
	b2.answers(t, filepath.Join(t.TempDir(), "nothing.pem"))
	b2.cmds.IO.AssumeYes = true

	_ = b2.cmds.Rotate(context.Background(), app.RotateRequest{Owner: "me", Role: "loop"})
	want := "https://github.com/settings/apps/crossrev-me#private-key"
	if len(o2.urls) != 1 || o2.urls[0] != want {
		t.Fatalf("browser opened %q, want %q", o2.urls, want)
	}
}

// --- watching the downloads folder -----------------------------------------

// The file lands with a name GitHub chooses, and typing it out is the step
// people get wrong (lib/auth.sh:877-885).
func TestRotatePicksUpTheKeyFromTheDownloadsFolder(t *testing.T) {
	b := newBench(t, out("crossrev-shorelogic\n"))
	b.meta(t, "CrossRev ShoreLogic", "crossrev-shorelogic")
	old := b.pem(t, 0o600)
	b.browser(&opener{})
	b.assumeYes(t)
	waits := b.noWait()

	downloaded := freshKey(t, filepath.Join(b.env["HOME"], "Downloads", "crossrev-shorelogic.2026-09-02.private-key.pem"))

	if err := b.cmds.Rotate(context.Background(), app.RotateRequest{Owner: "ShoreLogic", Role: "loop"}); err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	if !strings.Contains(b.text(), "│  Watching "+filepath.Join(b.env["HOME"], "Downloads")+" for a new .pem...\n") {
		t.Fatalf("printed:\n%s", b.text())
	}
	if !strings.Contains(b.text(), "│  ✓ found "+downloaded+"\n") {
		t.Fatalf("printed:\n%s", b.text())
	}
	if len(*waits) != 0 {
		t.Fatalf("waited %d times for a file already there", len(*waits))
	}
	installed, err := os.ReadFile(old)
	if err != nil {
		t.Fatalf("reading the installed key: %v", err)
	}
	source, err := os.ReadFile(downloaded)
	if err != nil {
		t.Fatalf("reading the downloaded key: %v", err)
	}
	if string(installed) != string(source) {
		t.Fatal("the downloaded key was not installed")
	}
}

// A file whose name belongs to another App's slug is not this App's key.
func TestRotateIgnoresAPemForADifferentApp(t *testing.T) {
	b := newBench(t)
	b.meta(t, "CrossRev ShoreLogic", "crossrev-shorelogic")
	b.pem(t, 0o600)
	b.browser(&opener{})
	freshKey(t, filepath.Join(b.env["HOME"], "Downloads", "someone-else.2026-09-02.private-key.pem"))
	waits := b.noWait()
	b.answers(t, "")
	b.cmds.IO.AssumeYes = true

	err := b.cmds.Rotate(context.Background(), app.RotateRequest{Owner: "ShoreLogic", Role: "loop"})
	wantRefusal(t, err,
		"there is no file at ",
		"Point --key at the .pem GitHub downloaded.")
	if len(*waits) != 150 {
		t.Fatalf("waited %d times, want 150 two-second polls", len(*waits))
	}
}

// A file older than five minutes is somebody's previous download, not this
// one: `find -newermt '-5 minutes'` (lib/auth.sh:882).
func TestRotateIgnoresAPemThatIsNotFresh(t *testing.T) {
	b := newBench(t)
	b.meta(t, "CrossRev ShoreLogic", "crossrev-shorelogic")
	b.pem(t, 0o600)
	b.browser(&opener{})
	stale := newKey(t, filepath.Join(b.env["HOME"], "Downloads", "crossrev-shorelogic.old.private-key.pem"))
	old := at.Add(-time.Hour)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatalf("ageing the file: %v", err)
	}
	b.noWait()
	b.answers(t, "")
	b.cmds.IO.AssumeYes = true

	err := b.cmds.Rotate(context.Background(), app.RotateRequest{Owner: "ShoreLogic", Role: "loop"})
	if !strings.HasPrefix(fatal(t, err).Reason, "there is no file at") {
		t.Fatalf("reason = %q", fatal(t, err).Reason)
	}
}

// When nothing lands, the path is asked for rather than guessed at
// (lib/auth.sh:890-892).
func TestRotateAsksForThePathWhenNothingIsDownloaded(t *testing.T) {
	b := newBench(t, out("crossrev-shorelogic\n"))
	b.meta(t, "CrossRev ShoreLogic", "crossrev-shorelogic")
	b.pem(t, 0o600)
	b.browser(&opener{})
	b.noWait()
	downloaded := newKey(t, filepath.Join(t.TempDir(), "elsewhere.pem"))
	b.answers(t, downloaded)
	b.cmds.IO.AssumeYes = true

	if err := b.cmds.Rotate(context.Background(), app.RotateRequest{Owner: "ShoreLogic", Role: "loop"}); err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	if !strings.Contains(b.text(), "Path to the downloaded .pem") {
		t.Fatalf("printed:\n%s", b.text())
	}
}

// `${keyfile/#\~/$HOME}` is a textual replacement of a leading tilde, so a
// typed ~/Downloads/x.pem resolves (lib/auth.sh:895).
func TestRotateExpandsALeadingTilde(t *testing.T) {
	b := newBench(t, out("crossrev-shorelogic\n"))
	b.meta(t, "CrossRev ShoreLogic", "crossrev-shorelogic")
	b.pem(t, 0o600)
	b.browser(&opener{})
	freshKey(t, filepath.Join(b.env["HOME"], "Downloads", "typed.pem"))

	if err := b.cmds.Rotate(context.Background(), app.RotateRequest{
		Owner: "ShoreLogic", Role: "loop", KeyFile: "~/Downloads/typed.pem",
	}); err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	if !strings.Contains(b.text(), "◇  Rotated") {
		t.Fatalf("printed:\n%s", b.text())
	}
}

// --- what must never be printed --------------------------------------------

func TestRotateKeepsTheKeyAndTheTokenOutOfEveryPrintedLine(t *testing.T) {
	b := newBench(t, out("crossrev-shorelogic\n"))
	b.meta(t, "CrossRev ShoreLogic", "crossrev-shorelogic")
	b.pem(t, 0o600)
	b.browser(&opener{})
	downloaded := newKey(t, filepath.Join(t.TempDir(), "downloaded.pem"))

	if err := b.cmds.Rotate(context.Background(), app.RotateRequest{
		Owner: "ShoreLogic", Role: "loop", KeyFile: downloaded,
	}); err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	body, err := os.ReadFile(downloaded)
	if err != nil {
		t.Fatalf("reading the key: %v", err)
	}
	for _, line := range strings.Split(string(body), "\n") {
		if len(line) < 20 || strings.HasPrefix(line, "-----") {
			continue
		}
		if strings.Contains(b.text(), line) {
			t.Fatal("a line of the private key was printed")
		}
		for _, spec := range b.gh.specs {
			for _, arg := range spec.Args {
				if strings.Contains(arg, line) {
					t.Fatal("a line of the private key reached a gh argument")
				}
			}
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
}

// --- the proof argv ---------------------------------------------------------

func TestRotateProvesTheKeyWithOneCallAsTheApp(t *testing.T) {
	rec := &recorder{results: []exec.Result{out("crossrev-shorelogic\n")}}
	b := newBench(t)
	b.cmds.GH = app.NewGH(rec, app.WithEnv(nil))
	b.gh = rec
	b.meta(t, "CrossRev ShoreLogic", "crossrev-shorelogic")
	b.pem(t, 0o600)
	b.browser(&opener{})
	downloaded := newKey(t, filepath.Join(t.TempDir(), "downloaded.pem"))

	if err := b.cmds.Rotate(context.Background(), app.RotateRequest{
		Owner: "ShoreLogic", Role: "loop", KeyFile: downloaded,
	}); err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	if len(rec.specs) != 1 {
		t.Fatalf("gh was invoked %d times, want once", len(rec.specs))
	}
	args := rec.specs[0].Args
	want := []string{"api", "-H", "", "/app", "--jq", ".slug"}
	if len(args) != len(want) {
		t.Fatalf("argv = %q, want %q with the token in position 2", args, want)
	}
	for i, value := range want {
		if i == 2 {
			if !strings.HasPrefix(args[i], "Authorization: Bearer ") {
				t.Fatalf("argv = %q", args)
			}
			continue
		}
		if args[i] != value {
			t.Fatalf("argv = %q, want %q", args, want)
		}
	}
}

// A source that ends before the reader answers is a failed read, which is what
// `ui_prompt || ui_die` catches (lib/auth.sh:890-891).
func TestRotateRefusesWhenTheAnswerNeverCame(t *testing.T) {
	b := newBench(t)
	b.meta(t, "CrossRev ShoreLogic", "crossrev-shorelogic")
	b.pem(t, 0o600)
	b.browser(&opener{})
	b.noWait()
	path := filepath.Join(t.TempDir(), "truncated")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatalf("writing the answers: %v", err)
	}
	b.cmds.IO.Input = ui.Terminal{TTYPath: path}
	b.cmds.IO.AssumeYes = true

	err := b.cmds.Rotate(context.Background(), app.RotateRequest{Owner: "ShoreLogic", Role: "loop"})
	wantRefusal(t, err,
		"no key file was named",
		"Re-run: crossrev auth rotate --owner ShoreLogic --role loop --key <path>")
}
