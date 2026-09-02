package app_test

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/app"
	"github.com/carlosboeing/crossrev/internal/exec"
	"github.com/carlosboeing/crossrev/internal/ui"
)

// --- the pieces a login needs ----------------------------------------------

// stateSeed is what `openssl rand -hex 16` is replaced by, so the value
// CrossRev sends is a fact the test can send back.
var stateSeed = []byte{
	0x00, 0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77,
	0x88, 0x99, 0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff,
}

func stateValue() string { return hex.EncodeToString(stateSeed) }

// answers puts a line where a question will read it. ui.Terminal opens the
// path once per question, so every question reads this same first line.
func (b *bench) answers(t *testing.T, line string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "answers")
	if err := os.WriteFile(path, []byte(line+"\n"), 0o600); err != nil {
		t.Fatalf("writing the answers: %v", err)
	}
	b.cmds.IO.Input = ui.Terminal{TTYPath: path}
}

// assumeYes is --yes: every confirmation is answered without asking, and the
// input source is still required (lib/auth.sh:513 runs before ui_confirm does).
func (b *bench) assumeYes(t *testing.T) {
	t.Helper()
	b.answers(t, "")
	b.cmds.IO.AssumeYes = true
}

// redirects binds an ephemeral port and sends the GitHub redirect at it, the
// way a browser would. The connection sits in the accept backlog until Wait
// reaches it.
func (b *bench) redirects(request string) {
	b.cmds.Listen = func() (*app.Listener, error) {
		listener, err := app.ListenOn(0)
		if err != nil {
			return nil, err
		}
		go func() {
			conn, err := net.Dial("tcp", listener.Addr().String())
			if err != nil {
				return
			}
			defer conn.Close()
			fmt.Fprintf(conn, "%s HTTP/1.1\r\nHost: localhost\r\nUser-Agent: test\r\n\r\n", request)
			_, _ = io.Copy(io.Discard, conn)
		}()
		return listener, nil
	}
}

// noListener is the machine with no free port, which is the arm that falls
// through to the paste prompt (lib/auth.sh:556-558).
func (b *bench) noListener() {
	b.cmds.Listen = func() (*app.Listener, error) {
		return nil, errors.New("no free port")
	}
}

// conversion is GitHub's answer to POST app-manifests/<code>/conversions.
func conversion(id, slug, name, pem string) exec.Result {
	return out(`{"id":` + id + `,"slug":"` + slug + `","name":"` + name + `","pem":"` + pem + `"}`)
}

// fixturePEM is a PEM-shaped string that is not a key. It never has to sign
// anything: the registration path only writes it.
const fixturePEM = `-----BEGIN RSA PRIVATE KEY-----\nQUJD\n-----END RSA PRIVATE KEY-----\n`

// --- what happens before anything opens -------------------------------------

// An unknown role is caught while nothing has happened yet, which is why the
// permissions read runs for its exit status alone (lib/auth.sh:511).
func TestLoginRefusesAnUnknownRoleBeforeAnythingOpens(t *testing.T) {
	b := newBench(t)
	o := b.browser(&opener{})
	b.assumeYes(t)

	err := b.cmds.Login(context.Background(), app.LoginRequest{Owner: "ShoreLogic", Role: "bogus"})
	wantRefusal(t, err,
		"unknown App role 'bogus'",
		"Roles are: loop (the review and resolve jobs) and refresher (the credential refresh job).")
	if len(b.gh.specs) != 0 || len(o.urls) != 0 {
		t.Fatalf("gh ran %d times and the browser %d times, want neither", len(b.gh.specs), len(o.urls))
	}
}

// With nowhere to read an answer from, nothing is asked and nothing is
// created — and the check runs before the account lookups, not after them
// (lib/auth.sh:513).
func TestLoginRefusesWithNoTerminalAttached(t *testing.T) {
	b := newBench(t)
	b.browser(&opener{})
	b.cmds.IO.Input = ui.Terminal{TTYPath: filepath.Join(t.TempDir(), "absent")}

	err := b.cmds.Login(context.Background(), app.LoginRequest{Owner: "ShoreLogic", Role: "loop"})
	wantRefusal(t, err,
		"CrossRev needs to ask you something, but no terminal is attached",
		"Run this in a terminal directly. Editor-embedded and captured shells often have no controlling terminal, which is what this is.")
	if len(b.gh.specs) != 0 {
		t.Fatalf("gh ran %d times before the terminal check, want none", len(b.gh.specs))
	}
}

// --yes does not skip the check: the shell tests the input source outside
// ui_confirm, so a runner with --yes still refuses.
func TestLoginStillNeedsATerminalUnderAssumeYes(t *testing.T) {
	b := newBench(t)
	b.browser(&opener{})
	b.cmds.IO.Input = ui.Terminal{TTYPath: filepath.Join(t.TempDir(), "absent")}
	b.cmds.IO.AssumeYes = true

	err := b.cmds.Login(context.Background(), app.LoginRequest{Owner: "ShoreLogic", Role: "loop"})
	if err == nil {
		t.Fatal("Login carried on with no terminal under --yes")
	}
	wantRefusal(t, err,
		"CrossRev needs to ask you something, but no terminal is attached",
		"Run this in a terminal directly. Editor-embedded and captured shells often have no controlling terminal, which is what this is.")
}

func TestLoginRefusesAnAccountGitHubDoesNotRecognise(t *testing.T) {
	b := newBench(t, bad())
	b.browser(&opener{})
	b.assumeYes(t)

	err := b.cmds.Login(context.Background(), app.LoginRequest{Owner: "nonexistent", Role: "loop"})
	wantRefusal(t, err,
		"GitHub does not recognise the account 'nonexistent'",
		"Check the spelling, or pass a different one with --owner")
}

func TestLoginRefusesWhenTheOwnerCannotBeDetected(t *testing.T) {
	b := newBench(t, bad())
	b.browser(&opener{})
	b.assumeYes(t)

	err := b.cmds.Login(context.Background(), app.LoginRequest{Role: "loop"})
	wantRefusal(t, err,
		"could not work out which account this App should belong to",
		"Run this inside a git repository with a GitHub remote, or name it: crossrev auth login --owner <owner>")
}

// One App per owner per role is the design, so a second registration is
// refused rather than made (lib/auth.sh:528-536).
func TestLoginOnAnAlreadyConfiguredAppSaysWhyASecondIsNotOffered(t *testing.T) {
	b := newBench(t, out("Organization 12345\n"))
	b.meta(t, "CrossRev ShoreLogic", "crossrev-shorelogic")
	o := b.browser(&opener{})
	b.assumeYes(t)

	if err := b.cmds.Login(context.Background(), app.LoginRequest{Owner: "ShoreLogic", Role: "loop"}); err != nil {
		t.Fatalf("Login: %v", err)
	}
	wantBlock(t, b.text(), "\n◇  Already configured\n"+
		"│  ✓ ShoreLogic — CrossRev ShoreLogic (id 987, role loop)\n"+
		"│\n"+
		"│  One App per owner per role is the design. Creating a second would mean\n"+
		"│  a second private key to protect and rotate, for no extra reach.\n"+
		"└  See where it is installed with:   crossrev auth status\n\n")
	if len(o.urls) != 0 {
		t.Fatalf("a browser was opened: %q", o.urls)
	}
}

// A name that is taken on GitHub with no local metadata is a collision, caught
// by the bot-account probe before a browser opens (tests/test-auth.sh:238-260).
func TestLoginRefusesWhenAnAppWithThatNameAlreadyExists(t *testing.T) {
	b := newBench(t, out("Organization 12345\n"), out("Bot 99999\n"))
	o := b.browser(&opener{})
	b.assumeYes(t)
	// Neither is reached on the refusal path. They are here so that a port
	// binding and a five-minute poll cannot be what a regression looks like.
	b.noListener()
	b.noWait()

	err := b.cmds.Login(context.Background(), app.LoginRequest{Owner: "ShoreLogic", Role: "loop"})
	wantRefusal(t, err,
		"a GitHub App named 'CrossRev ShoreLogic' already exists",
		"CrossRev cannot reuse an existing App when local metadata is missing. Register a separate App with: crossrev auth login --name <name>")
	if len(o.urls) != 0 {
		t.Fatalf("a browser was opened: %q", o.urls)
	}
	if strings.Contains(b.text(), "Open GitHub") {
		t.Fatalf("prompted to open a browser:\n%s", b.text())
	}
	if got, want := b.gh.specs[1].Args[1], "users/crossrev-shorelogic[bot]"; got != want {
		t.Fatalf("probed %q, want %q", got, want)
	}
}

// --- the registration panel -------------------------------------------------

func TestLoginPrintsTheLoopPanelAndStopsWhenDeclined(t *testing.T) {
	b := newBench(t, out("Organization 12345\n"), bad())
	b.browser(&opener{})
	b.answers(t, "n")
	b.cmds.Random = strings.NewReader(string(stateSeed))

	err := b.cmds.Login(context.Background(), app.LoginRequest{Owner: "ShoreLogic", Role: "loop"})
	if !errors.Is(err, app.ErrDeclined) {
		t.Fatalf("error = %v, want ErrDeclined", err)
	}
	wantBlock(t, b.text(), "\n◇  Register a GitHub App for ShoreLogic\n"+
		"│  Owner        ShoreLogic (Organization)\n"+
		"│  Name         CrossRev ShoreLogic (override with --name)\n"+
		"│  Role         loop\n"+
		"│  Permissions  contents:write, issues:write, pull_requests:write\n"+
		"│               and nothing else\n"+
		"│  Webhook      disabled. GitHub never calls CrossRev; your workflows do\n"+
		"│  Visibility   private to ShoreLogic\n"+
		"│\n"+
		"│  issues:write looks surprising and is not optional — GitHub models pull\n"+
		"│  request labels under the Issues API, and the loop is label-driven.\n"+
		"│\n"+
		"│  Two approvals in the browser: create the App, then install it. CrossRev\n"+
		"│  follows along here — nothing to copy back.\n"+
		"\n"+
		"◆  Open GitHub in your browser to create the App?  [y/N] "+
		"  Nothing was created.\n")
}

func TestLoginPrintsTheRefresherPanelWithItsOwnJustification(t *testing.T) {
	b := newBench(t, out("Organization 12345\n"), bad())
	b.browser(&opener{})
	b.answers(t, "n")

	err := b.cmds.Login(context.Background(), app.LoginRequest{Owner: "ShoreLogic", Role: "refresher"})
	if !errors.Is(err, app.ErrDeclined) {
		t.Fatalf("error = %v, want ErrDeclined", err)
	}
	want := "│  Name         CrossRev Refresh ShoreLogic (override with --name)\n" +
		"│  Role         refresher\n" +
		"│  Permissions  secrets:write (repository secrets only)\n" +
		"│               and nothing else\n" +
		"│  Webhook      disabled. GitHub never calls CrossRev; your workflows do\n" +
		"│  Visibility   private to ShoreLogic\n" +
		"│\n" +
		"│  This App exists to write one secret, on a schedule, and does nothing\n" +
		"│  else. Its workflow never checks out a pull request branch, never runs\n" +
		"│  a model and never reads a diff or a comment — there is nothing in it\n" +
		"│  to inject into, which is what makes secrets:write safe here and unsafe\n" +
		"│  on the App the review jobs use.\n"
	if !strings.Contains(b.text(), want) {
		t.Fatalf("printed:\n%s", b.text())
	}
	if strings.Contains(b.text(), "issues:write looks surprising") {
		t.Fatalf("the loop's justification reached the refresher panel:\n%s", b.text())
	}
}

// A user-owned account posts to the personal settings page rather than the
// organisation one (lib/auth.sh:578-582), and the panel says so.
func TestLoginNamesTheOwnerTypeItResolved(t *testing.T) {
	b := newBench(t, out("User 339\n"), bad())
	b.browser(&opener{})
	b.answers(t, "n")

	_ = b.cmds.Login(context.Background(), app.LoginRequest{Owner: "carlosboeing", Role: "loop"})
	if !strings.Contains(b.text(), "│  Owner        carlosboeing (User)\n") {
		t.Fatalf("printed:\n%s", b.text())
	}
	if !strings.Contains(b.text(), "│  Visibility   private to carlosboeing\n") {
		t.Fatalf("printed:\n%s", b.text())
	}
}

// --name overrides the proposed name, and the panel reflects it
// (tests/test-auth.sh:280-288).
func TestLoginTakesTheNameItWasGiven(t *testing.T) {
	b := newBench(t, out("Organization 12345\n"), bad())
	b.browser(&opener{})
	b.answers(t, "n")

	_ = b.cmds.Login(context.Background(), app.LoginRequest{Owner: "ShoreLogic", Role: "loop", Name: "My Custom App"})
	if !strings.Contains(b.text(), "│  Name         My Custom App (override with --name)\n") {
		t.Fatalf("printed:\n%s", b.text())
	}
	if got, want := b.gh.specs[1].Args[1], "users/my-custom-app[bot]"; got != want {
		t.Fatalf("probed %q, want %q", got, want)
	}
}

// --- the manifest and the page it is posted from ---------------------------

func TestLoginManifestAsksForTheRolesPermissionsAndNothingElse(t *testing.T) {
	got := app.LoginManifest("CrossRev ShoreLogic", "http://localhost:33517/crossrev-auth",
		[]byte(`{"contents":"write","issues":"write","pull_requests":"write"}`))
	want := `{"name":"CrossRev ShoreLogic","url":"https://github.com/carlosboeing/crossrev",` +
		`"redirect_url":"http://localhost:33517/crossrev-auth","public":false,` +
		`"hook_attributes":{"url":"https://example.com/unused","active":false},` +
		`"default_events":[],` +
		`"default_permissions":{"contents":"write","issues":"write","pull_requests":"write"}}`
	if string(got) != want {
		t.Fatalf("manifest =\n%s\nwant\n%s", got, want)
	}
}

// jq leaves <, > and & alone, so a name carrying one reaches GitHub as it was
// typed rather than as an entity.
func TestLoginManifestEscapesANameTheWayJQDoes(t *testing.T) {
	got := app.LoginManifest(`A&B <x> "q"`, "http://localhost:33517/crossrev-auth", []byte(`{"secrets":"write"}`))
	if !strings.Contains(string(got), `{"name":"A&B <x> \"q\"",`) {
		t.Fatalf("manifest =\n%s", got)
	}
}

func TestLoginPageSubmitsTheEscapedManifestToTheRightSettingsPage(t *testing.T) {
	manifest := app.LoginManifest("CrossRev ShoreLogic", "http://localhost:33517/crossrev-auth",
		[]byte(`{"contents":"write","issues":"write","pull_requests":"write"}`))
	got := app.LoginPage("CrossRev ShoreLogic",
		"https://github.com/organizations/ShoreLogic/settings/apps/new", string(manifest), "deadbeef")

	want := "<!doctype html>\n" +
		"<meta charset=\"utf-8\">\n" +
		"<title>crossrev</title>\n" +
		"<body style=\"font:16px system-ui;margin:4rem auto;max-width:34rem\">\n" +
		"<p>Sending you to GitHub to register <strong>CrossRev ShoreLogic</strong>&hellip;</p>\n" +
		"<p>If nothing happens, press the button.</p>\n" +
		"<form id=\"f\" action=\"https://github.com/organizations/ShoreLogic/settings/apps/new\" method=\"post\">\n" +
		"  <input type=\"hidden\" name=\"manifest\" value=\"" + app.HTMLAttrEscape(string(manifest)) + "\">\n" +
		"  <input type=\"hidden\" name=\"state\" value=\"deadbeef\">\n" +
		"  <button type=\"submit\">Continue to GitHub</button>\n" +
		"</form>\n" +
		"<script>document.getElementById('f').submit()</script>\n" +
		"</body>\n"
	if got != want {
		t.Fatalf("page =\n%q\nwant\n%q", got, want)
	}
	// Every quote in the manifest is an entity, so none of them can end the
	// attribute it sits in.
	if strings.Contains(got[strings.Index(got, `name="manifest"`):], `value="{"`) {
		t.Fatalf("an unescaped quote reached the attribute:\n%s", got)
	}
}

// --- the whole registration -------------------------------------------------

func TestLoginWritesTheKeyAndTheMetadataAndReportsBoth(t *testing.T) {
	b := newBench(t,
		out("Organization 12345\n"),
		bad(),
		conversion("4242", "crossrev-shorelogic", "CrossRev ShoreLogic", fixturePEM),
		out("ShoreLogic selected\n"))
	o := b.browser(&opener{})
	b.assumeYes(t)
	b.noWait()
	b.cmds.Random = strings.NewReader(string(stateSeed))
	b.redirects("GET /crossrev-auth?code=abc123&state=" + stateValue())

	if err := b.cmds.Login(context.Background(), app.LoginRequest{Owner: "ShoreLogic", Role: "loop"}); err != nil {
		t.Fatalf("Login: %v", err)
	}

	pem := filepath.Join(b.dir, "ShoreLogic.loop.pem")
	body, err := os.ReadFile(pem)
	if err != nil {
		t.Fatalf("reading the key: %v", err)
	}
	if want := "-----BEGIN RSA PRIVATE KEY-----\nQUJD\n-----END RSA PRIVATE KEY-----\n"; string(body) != want {
		t.Fatalf("key = %q, want %q", body, want)
	}
	wantMode(t, pem, 0o600)
	wantMode(t, b.dir, 0o700)

	meta, err := os.ReadFile(filepath.Join(b.dir, "ShoreLogic.loop.json"))
	if err != nil {
		t.Fatalf("reading the metadata: %v", err)
	}
	want := "{\n" +
		"  \"owner\": \"ShoreLogic\",\n" +
		"  \"owner_type\": \"Organization\",\n" +
		"  \"owner_id\": 12345,\n" +
		"  \"id\": 4242,\n" +
		"  \"slug\": \"crossrev-shorelogic\",\n" +
		"  \"name\": \"CrossRev ShoreLogic\",\n" +
		"  \"role\": \"loop\",\n" +
		"  \"created\": \"2026-09-02T04:00:00Z\"\n" +
		"}\n"
	if string(meta) != want {
		t.Fatalf("metadata =\n%s\nwant\n%s", meta, want)
	}
	wantMode(t, filepath.Join(b.dir, "ShoreLogic.loop.json"), 0o600)

	if !strings.Contains(b.text(), "\n◇  Registered\n"+
		"│  ✓ App    CrossRev ShoreLogic (id 4242)\n"+
		"│  ✓ Key    "+pem+" (0600)\n") {
		t.Fatalf("printed:\n%s", b.text())
	}
	// The second half follows on from the first, so it says so.
	if !strings.Contains(b.text(), "◇  Step 2 of 2: Install the App on the repositories you want reviewed") {
		t.Fatalf("printed:\n%s", b.text())
	}
	if !strings.Contains(b.text(), "◇  Step 1 of 2: Create the GitHub App") {
		t.Fatalf("printed:\n%s", b.text())
	}
	// The browser is sent at the local page first, then at the install form.
	if len(o.urls) != 2 || !strings.HasPrefix(o.urls[0], "file://") {
		t.Fatalf("browser opened %q", o.urls)
	}
	if !strings.Contains(o.urls[1], "/apps/crossrev-shorelogic/installations/new/permissions") {
		t.Fatalf("browser opened %q", o.urls)
	}
	// And the page it was pointed at is gone once the flow ends.
	if _, err := os.Stat(strings.TrimPrefix(o.urls[0], "file://")); !os.IsNotExist(err) {
		t.Fatalf("the manifest page was left behind at %s", o.urls[0])
	}

	// The whole point of the POST: the code goes in the path and nothing else.
	if got, want := b.gh.specs[2].Args, []string{"api", "--method", "POST", "app-manifests/abc123/conversions"}; !equalArgs(got, want) {
		t.Fatalf("argv = %q, want %q", got, want)
	}
}

func wantMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s is mode %04o, want %04o", path, got, want)
	}
}

// A state value that is not the one CrossRev sent means the request did not
// come from the page it opened (lib/auth.sh:733-736).
func TestLoginRefusesAStateItDidNotSend(t *testing.T) {
	b := newBench(t, out("Organization 12345\n"), bad())
	b.browser(&opener{})
	b.assumeYes(t)
	b.cmds.Random = strings.NewReader(string(stateSeed))
	b.redirects("GET /crossrev-auth?code=abc123&state=somebodyelses")

	err := b.cmds.Login(context.Background(), app.LoginRequest{Owner: "ShoreLogic", Role: "loop"})
	wantRefusal(t, err,
		"the state value GitHub returned does not match the one CrossRev sent",
		"This request did not come from the page crossrev opened. Start again: crossrev auth login --owner ShoreLogic")
	if len(b.gh.specs) != 2 {
		t.Fatalf("gh ran %d times, so the code was exchanged despite the mismatch", len(b.gh.specs))
	}
}

// A redirect carrying a code and no state at all is refused, because the
// listener path is the one path that can be required to send the state back
// (lib/auth.sh:727-736).
//
// The listener binds loopback only, so any request reaching it came from a
// process on this machine — which is exactly what the state exists to tell
// apart from the page CrossRev opened. A request that sent no state has not
// made that claim.
//
// `attacker-code` is the shape the probe used: a request the operator's
// browser never made, arriving with a code and nothing else
// (tests/test-auth.sh:444-451).
func TestLoginRefusesAListenerRedirectCarryingNoState(t *testing.T) {
	b := newBench(t, out("Organization 12345\n"), bad())
	b.browser(&opener{})
	b.assumeYes(t)
	b.cmds.Random = strings.NewReader(string(stateSeed))
	b.redirects("GET /crossrev-auth?code=attacker-code")

	err := b.cmds.Login(context.Background(), app.LoginRequest{Owner: "ShoreLogic", Role: "loop"})
	wantRefusal(t, err,
		"the state value GitHub returned does not match the one CrossRev sent",
		"This request did not come from the page crossrev opened. Start again: crossrev auth login --owner ShoreLogic")
	if len(b.gh.specs) != 2 {
		t.Fatalf("gh ran %d times, so the code was exchanged despite the missing state", len(b.gh.specs))
	}
	if _, statErr := os.Stat(filepath.Join(b.dir, "ShoreLogic.loop.pem")); !os.IsNotExist(statErr) {
		t.Fatal("a key was written for a redirect that carried no state")
	}
}

// The positive control for the arm above. Without it, "refused" could just as
// well mean the listener path never completes at all (tests/test-auth.sh:435-440).
func TestLoginAcceptsAListenerRedirectCarryingTheStateItSent(t *testing.T) {
	b := newBench(t,
		out("Organization 12345\n"),
		bad(),
		conversion("4242", "crossrev-shorelogic", "CrossRev ShoreLogic", fixturePEM),
		out("ShoreLogic selected\n"))
	b.browser(&opener{})
	b.assumeYes(t)
	b.noWait()
	b.cmds.Random = strings.NewReader(string(stateSeed))
	b.redirects("GET /crossrev-auth?code=abc123&state=" + stateValue())

	if err := b.cmds.Login(context.Background(), app.LoginRequest{Owner: "ShoreLogic", Role: "loop"}); err != nil {
		t.Fatalf("Login: %v", err)
	}
	if got, want := b.gh.specs[2].Args[3], "app-manifests/abc123/conversions"; got != want {
		t.Fatalf("exchanged %q, want %q", got, want)
	}
	wantMode(t, filepath.Join(b.dir, "ShoreLogic.loop.pem"), 0o600)
}

// --- the paste fallback -----------------------------------------------------

// With no listener the flow goes straight to the paste, which is the floor
// rather than the plan (lib/auth.sh:658-673).
func TestLoginFallsBackToPastingWithNoListener(t *testing.T) {
	b := newBench(t,
		out("Organization 12345\n"),
		bad(),
		conversion("4242", "crossrev-shorelogic", "CrossRev ShoreLogic", fixturePEM),
		out("ShoreLogic selected\n"))
	b.browser(&opener{})
	b.noWait()
	b.noListener()
	b.cmds.Random = strings.NewReader(string(stateSeed))
	b.answers(t, "http://localhost:33517/crossrev-auth?code=pasted123&state="+stateValue())
	b.cmds.IO.AssumeYes = true

	if err := b.cmds.Login(context.Background(), app.LoginRequest{Owner: "ShoreLogic", Role: "loop"}); err != nil {
		t.Fatalf("Login: %v", err)
	}
	if !strings.Contains(b.text(), "\n◇  Step 1 of 2: Paste the registration code\n"+
		"│  GitHub redirected your browser to a localhost address that will not load.\n"+
		"│  Copy the full URL from the address bar and paste it below.\n") {
		t.Fatalf("printed:\n%s", b.text())
	}
	if got, want := b.gh.specs[2].Args[3], "app-manifests/pasted123/conversions"; got != want {
		t.Fatalf("exchanged %q, want %q", got, want)
	}
	// The redirect URL names the fallback port, which nothing is listening on.
	page := app.LoginManifest("CrossRev ShoreLogic", "http://localhost:33517/crossrev-auth", []byte(`{}`))
	if !strings.Contains(string(page), "localhost:33517") {
		t.Fatal("the fallback port moved")
	}
}

// A bare value with no code= in it is the code itself (lib/auth.sh:671).
func TestLoginTakesABarePastedValueAsTheCode(t *testing.T) {
	b := newBench(t,
		out("Organization 12345\n"),
		bad(),
		conversion("4242", "crossrev-shorelogic", "CrossRev ShoreLogic", fixturePEM),
		out("ShoreLogic selected\n"))
	b.browser(&opener{})
	b.noWait()
	b.noListener()
	b.answers(t, "just-the-code")
	b.cmds.IO.AssumeYes = true

	if err := b.cmds.Login(context.Background(), app.LoginRequest{Owner: "ShoreLogic", Role: "loop"}); err != nil {
		t.Fatalf("Login: %v", err)
	}
	if got, want := b.gh.specs[2].Args[3], "app-manifests/just-the-code/conversions"; got != want {
		t.Fatalf("exchanged %q, want %q", got, want)
	}
}

// The paste is held to the state it carries. An absent one is the documented
// fallback; one that came back wrong is a mismatch on either path
// (lib/auth.sh:730-731, tests/test-auth.sh:472-479).
func TestLoginRefusesAPastedURLCarryingTheWrongState(t *testing.T) {
	b := newBench(t, out("Organization 12345\n"), bad())
	b.browser(&opener{})
	b.noListener()
	b.cmds.Random = strings.NewReader(string(stateSeed))
	b.answers(t, "http://localhost:33517/crossrev-auth?code=c&state=not-the-one")
	b.cmds.IO.AssumeYes = true

	err := b.cmds.Login(context.Background(), app.LoginRequest{Owner: "ShoreLogic", Role: "loop"})
	wantRefusal(t, err,
		"the state value GitHub returned does not match the one CrossRev sent",
		"This request did not come from the page crossrev opened. Start again: crossrev auth login --owner ShoreLogic")
	if _, statErr := os.Stat(filepath.Join(b.dir, "ShoreLogic.loop.pem")); !os.IsNotExist(statErr) {
		t.Fatal("a key was written for a paste carrying the wrong state")
	}
}

// The paste expression stops the value at & alone, not at a space, which is
// the one character that separates it from the listener's
// (lib/auth.sh:646 against :668).
func TestLoginReadsAPastedValueUpToTheNextAmpersandOnly(t *testing.T) {
	b := newBench(t,
		out("Organization 12345\n"),
		bad(),
		conversion("4242", "crossrev-shorelogic", "CrossRev ShoreLogic", fixturePEM),
		out("ShoreLogic selected\n"))
	b.browser(&opener{})
	b.noWait()
	b.noListener()
	b.cmds.Random = strings.NewReader(string(stateSeed))
	b.answers(t, "?code=one two&state="+stateValue())
	b.cmds.IO.AssumeYes = true

	if err := b.cmds.Login(context.Background(), app.LoginRequest{Owner: "ShoreLogic", Role: "loop"}); err != nil {
		t.Fatalf("Login: %v", err)
	}
	if got, want := b.gh.specs[2].Args[3], "app-manifests/one two/conversions"; got != want {
		t.Fatalf("exchanged %q, want %q", got, want)
	}
}

func TestLoginRefusesWhenNothingUsableWasPasted(t *testing.T) {
	b := newBench(t, out("Organization 12345\n"), bad())
	b.browser(&opener{})
	b.noListener()
	b.answers(t, "")
	b.cmds.IO.AssumeYes = true

	err := b.cmds.Login(context.Background(), app.LoginRequest{Owner: "ShoreLogic", Role: "loop"})
	wantRefusal(t, err,
		"no code found",
		"Paste the full URL from the address bar, or just the value after code=")
}

// --- what GitHub answered with ----------------------------------------------

func TestLoginRefusesAConversionWithNoIDOrKey(t *testing.T) {
	for name, answer := range map[string]exec.Result{
		"no id":        out(`{"slug":"s","name":"n","pem":"` + fixturePEM + `"}`),
		"a null id":    out(`{"id":null,"slug":"s","name":"n","pem":"` + fixturePEM + `"}`),
		"no key":       out(`{"id":4242,"slug":"s","name":"n"}`),
		"a null key":   out(`{"id":4242,"slug":"s","name":"n","pem":null}`),
		"an empty key": out(`{"id":4242,"slug":"s","name":"n","pem":""}`),
	} {
		t.Run(name, func(t *testing.T) {
			b := newBench(t, out("Organization 12345\n"), bad(), answer)
			b.browser(&opener{})
			b.noWait()
			b.noListener()
			b.answers(t, "the-code")
			b.cmds.IO.AssumeYes = true

			err := b.cmds.Login(context.Background(), app.LoginRequest{Owner: "ShoreLogic", Role: "loop"})
			wantRefusal(t, err,
				"GitHub's response did not contain an App id and private key",
				"Nothing was stored. Check for a half-created App before retrying")
			if _, statErr := os.Stat(filepath.Join(b.dir, "ShoreLogic.loop.pem")); !os.IsNotExist(statErr) {
				t.Fatal("a key was written for a response that carried none")
			}
		})
	}
}

func TestLoginRefusesACodeGitHubRejected(t *testing.T) {
	b := newBench(t, out("Organization 12345\n"), bad(), bad())
	b.browser(&opener{})
	b.noListener()
	b.answers(t, "expired-code")
	b.cmds.IO.AssumeYes = true

	err := b.cmds.Login(context.Background(), app.LoginRequest{Owner: "ShoreLogic", Role: "loop"})
	wantRefusal(t, err,
		"GitHub rejected the code",
		"Codes expire one hour after the App is created, and each works once. Re-run: crossrev auth login --owner ShoreLogic")
}

// --- what must never be printed ---------------------------------------------

func TestLoginKeepsThePrivateKeyOutOfEveryPrintedLineAndEveryArgument(t *testing.T) {
	b := newBench(t,
		out("Organization 12345\n"),
		bad(),
		conversion("4242", "crossrev-shorelogic", "CrossRev ShoreLogic", fixturePEM),
		out("ShoreLogic selected\n"))
	o := b.browser(&opener{})
	b.assumeYes(t)
	b.noWait()
	b.cmds.Random = strings.NewReader(string(stateSeed))
	b.redirects("GET /crossrev-auth?code=abc123&state=" + stateValue())

	if err := b.cmds.Login(context.Background(), app.LoginRequest{Owner: "ShoreLogic", Role: "loop"}); err != nil {
		t.Fatalf("Login: %v", err)
	}
	const body = "QUJD"
	if strings.Contains(b.text(), body) {
		t.Fatal("the private key was printed")
	}
	for _, spec := range b.gh.specs {
		for _, arg := range spec.Args {
			if strings.Contains(arg, body) {
				t.Fatal("the private key reached a gh argument")
			}
		}
	}
	for _, url := range o.urls {
		if strings.Contains(url, body) {
			t.Fatal("the private key reached the browser")
		}
	}
	// The registration code is exchanged once and never printed: it converts
	// to the App's private key, and a terminal is not the place for it.
	if strings.Contains(b.text(), "abc123") {
		t.Fatal("the registration code was printed")
	}
}

// The page carrying the manifest is opened with file:// and lives in a
// directory the process owns, so nothing it holds is served to a network.
func TestLoginOpensTheManifestPageFromDisk(t *testing.T) {
	b := newBench(t, out("Organization 12345\n"), bad(), bad())
	o := b.browser(&opener{})
	b.noListener()
	b.answers(t, "the-code")
	b.cmds.IO.AssumeYes = true

	_ = b.cmds.Login(context.Background(), app.LoginRequest{Owner: "ShoreLogic", Role: "loop"})
	if len(o.urls) != 1 {
		t.Fatalf("browser opened %q, want one page", o.urls)
	}
	if !strings.HasPrefix(o.urls[0], "file://") || !strings.HasSuffix(o.urls[0], ".html") {
		t.Fatalf("browser opened %q", o.urls[0])
	}
}
