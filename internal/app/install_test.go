package app_test

import (
	"context"
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

// --- a browser that opens nothing ------------------------------------------

// opener stands in for `open` or `xdg-open`: it records the URL and never
// starts anything.
type opener struct {
	urls    []string
	refuse  bool
	missing bool
}

func (o *opener) Run(_ context.Context, spec exec.Spec) exec.Result {
	o.urls = append(o.urls, spec.Args[0])
	if o.refuse {
		return exec.Result{ExitCode: 1}
	}
	return exec.Result{}
}

func (o *opener) look(name string) (string, error) {
	if o.missing {
		return "", os.ErrNotExist
	}
	return "/usr/bin/" + name, nil
}

// browser wires an opener into the bench and answers it back.
func (b *bench) browser(o *opener) *opener {
	b.cmds.Browser = app.NewBrowser(o, app.WithLookPath(o.look))
	return o
}

// noWait makes every poll instant and records what was waited for, so a
// five-minute loop is a hundred entries rather than five minutes.
func (b *bench) noWait() *[]time.Duration {
	var waits []time.Duration
	b.cmds.Sleep = func(_ context.Context, d time.Duration) error {
		waits = append(waits, d)
		return nil
	}
	return &waits
}

// fatal reads the reason and action off a refusal, failing the test when err is
// not one.
func fatal(t *testing.T, err error) *ui.FatalError {
	t.Helper()
	var refusal *ui.FatalError
	if !errors.As(err, &refusal) {
		t.Fatalf("error = %v, want a ui.FatalError", err)
	}
	return refusal
}

func wantRefusal(t *testing.T, err error, reason, action string) {
	t.Helper()
	refusal := fatal(t, err)
	if refusal.Reason != reason {
		t.Fatalf("reason = %q\nwant     %q", refusal.Reason, reason)
	}
	if refusal.Action != action {
		t.Fatalf("action = %q\nwant     %q", refusal.Action, action)
	}
}

// --- crossrev auth install --------------------------------------------------

func TestInstallReportsWhereTheAppLandedAndWhatComesNext(t *testing.T) {
	b := newBench(t, out("ShoreLogic selected\n"))
	b.meta(t, "CrossRev ShoreLogic", "crossrev-shorelogic")
	b.pem(t, 0o600)
	o := b.browser(&opener{})
	b.noWait()

	if err := b.cmds.Install(context.Background(), app.InstallRequest{Owner: "ShoreLogic", Role: "loop"}); err != nil {
		t.Fatalf("Install: %v", err)
	}

	wantBlock(t, b.text(), "\n◇  Install the App on the repositories you want reviewed\n"+
		"│  The App exists on GitHub, but reaches nothing until it is installed.\n"+
		"│  Choose 'Only select repositories' unless you mean all of them.\n"+
		"\n"+
		"│  Waiting for the installation to appear...\n"+
		"│\n"+
		"│  ✓ installed on ShoreLogic (selected repositories)\n"+
		"└  Next:   crossrev init\n\n")

	// tests/test-auth.sh:310-312: the browser is sent to the prefilled install
	// form, not to the App's settings page.
	want := "https://github.com/apps/crossrev-shorelogic/installations/new/permissions?target_id=12345&target_type=Organization"
	if len(o.urls) != 1 || o.urls[0] != want {
		t.Fatalf("browser opened %q, want %q", o.urls, want)
	}
}

// Running `auth install` on its own is not halfway through a login flow, so it
// must not claim a step count (tests/test-auth.sh:290-309).
func TestInstallOnItsOwnClaimsNoStepCount(t *testing.T) {
	b := newBench(t, out("ShoreLogic selected\n"))
	b.meta(t, "CrossRev ShoreLogic", "crossrev-shorelogic")
	b.pem(t, 0o600)
	b.browser(&opener{})
	b.noWait()

	if err := b.cmds.Install(context.Background(), app.InstallRequest{Owner: "ShoreLogic", Role: "loop"}); err != nil {
		t.Fatalf("Install: %v", err)
	}
	for _, claim := range []string{"Step 1 of 2", "Step 2 of 2"} {
		if strings.Contains(b.text(), claim) {
			t.Fatalf("printed %q:\n%s", claim, b.text())
		}
	}
}

func TestInstallRefusesWhenNoAppIsConfigured(t *testing.T) {
	b := newBench(t)
	err := b.cmds.Install(context.Background(), app.InstallRequest{Owner: "Nobody", Role: "loop"})
	wantRefusal(t, err,
		"no loop App is configured for Nobody",
		"Register one first: crossrev auth login --owner Nobody --role loop")
}

func TestInstallRefusesWhenTheKeyIsMissing(t *testing.T) {
	b := newBench(t)
	b.meta(t, "CrossRev ShoreLogic", "crossrev-shorelogic")

	err := b.cmds.Install(context.Background(), app.InstallRequest{Owner: "ShoreLogic", Role: "loop"})
	wantRefusal(t, err,
		"the loop private key for ShoreLogic is missing at "+filepath.Join(b.dir, "ShoreLogic.loop.pem"),
		"Without it crossrev cannot confirm the installation. Re-register: crossrev auth login --owner ShoreLogic --role loop")
}

// The refresher's refusals name the refresher, because the two roles' keys are
// not interchangeable.
func TestInstallNamesTheRoleItWasAskedFor(t *testing.T) {
	b := newBench(t)
	err := b.cmds.Install(context.Background(), app.InstallRequest{Owner: "ShoreLogic", Role: "refresher"})
	wantRefusal(t, err,
		"no refresher App is configured for ShoreLogic",
		"Register one first: crossrev auth login --owner ShoreLogic --role refresher")
}

// The owner is detected, not asked, because the repository's owner is the trust
// boundary the private key sits on (lib/auth.sh:748).
func TestInstallDetectsTheOwnerWhenNoneIsNamed(t *testing.T) {
	b := newBench(t, out("ShoreLogic\n"), out("ShoreLogic selected\n"))
	b.meta(t, "CrossRev ShoreLogic", "crossrev-shorelogic")
	b.pem(t, 0o600)
	b.browser(&opener{})
	b.noWait()

	if err := b.cmds.Install(context.Background(), app.InstallRequest{Role: "loop"}); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if got, want := b.gh.specs[0].Args, []string{"repo", "view", "--json", "owner", "--jq", ".owner.login"}; !equalArgs(got, want) {
		t.Fatalf("argv = %q, want %q", got, want)
	}
}

func TestInstallRefusesWhenTheOwnerCannotBeDetected(t *testing.T) {
	b := newBench(t, bad())
	err := b.cmds.Install(context.Background(), app.InstallRequest{Role: "loop"})
	wantRefusal(t, err,
		"could not work out which owner's App to install",
		"Name it: crossrev auth install --owner <owner>")
}

// An older metadata file carries no owner_id, and the id is recovered rather
// than the install URL degraded (lib/auth.sh:760).
func TestInstallRecoversAnAbsentOwnerIDFromGitHub(t *testing.T) {
	b := newBench(t, out("12345\n"), out("ShoreLogic selected\n"))
	body := `{"owner":"ShoreLogic","owner_type":"Organization","id":987,` +
		`"slug":"crossrev-shorelogic","name":"CrossRev ShoreLogic","role":"loop"}`
	if err := os.WriteFile(filepath.Join(b.dir, "ShoreLogic.loop.json"), []byte(body), 0o600); err != nil {
		t.Fatalf("writing the metadata: %v", err)
	}
	b.pem(t, 0o600)
	o := b.browser(&opener{})
	b.noWait()

	if err := b.cmds.Install(context.Background(), app.InstallRequest{Owner: "ShoreLogic", Role: "loop"}); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if got, want := b.gh.specs[0].Args, []string{"api", "users/ShoreLogic", "--jq", ".id"}; !equalArgs(got, want) {
		t.Fatalf("argv = %q, want %q", got, want)
	}
	if !strings.Contains(o.urls[0], "target_id=12345") {
		t.Fatalf("browser opened %q", o.urls)
	}
}

// A browser that will not open is a warning naming the URL, never a refusal:
// the install can still be done by hand (lib/auth.sh:786-788).
func TestInstallWarnsRatherThanFailingWhenNoBrowserOpens(t *testing.T) {
	b := newBench(t, out("ShoreLogic selected\n"))
	b.meta(t, "CrossRev ShoreLogic", "crossrev-shorelogic")
	b.pem(t, 0o600)
	b.browser(&opener{missing: true})
	b.noWait()

	if err := b.cmds.Install(context.Background(), app.InstallRequest{Owner: "ShoreLogic", Role: "loop"}); err != nil {
		t.Fatalf("Install: %v", err)
	}
	want := "\n⚠  could not open a browser automatically\n" +
		"   Install it here: https://github.com/apps/crossrev-shorelogic/installations/new/permissions?target_id=12345&target_type=Organization\n\n"
	if !strings.Contains(b.text(), want) {
		t.Fatalf("printed:\n%s", b.text())
	}
	if !strings.Contains(b.text(), "installed on ShoreLogic") {
		t.Fatalf("the flow stopped at the browser:\n%s", b.text())
	}
}

// --- the five-minute wait ---------------------------------------------------

// The loop polls a hundred times, three seconds apart, and then warns without
// undoing anything (lib/auth.sh:792-807).
func TestInstallPollsForFiveMinutesAndThenWarns(t *testing.T) {
	refusals := make([]exec.Result, 200)
	for i := range refusals {
		refusals[i] = bad()
	}
	b := newBench(t, refusals...)
	b.meta(t, "CrossRev ShoreLogic", "crossrev-shorelogic")
	b.pem(t, 0o600)
	b.browser(&opener{})
	waits := b.noWait()

	if err := b.cmds.Install(context.Background(), app.InstallRequest{Owner: "ShoreLogic", Role: "loop"}); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if len(*waits) != 100 {
		t.Fatalf("waited %d times, want 100", len(*waits))
	}
	for _, d := range *waits {
		if d != 3*time.Second {
			t.Fatalf("waited %s, want 3s", d)
		}
	}
	if len(b.gh.specs) != 100 {
		t.Fatalf("gh was invoked %d times, want 100", len(b.gh.specs))
	}
	want := "\n⚠  no installation showed up within five minutes\n" +
		"   The App is registered and its key is stored, so nothing is lost. Install it at https://github.com/apps/crossrev-shorelogic/installations/new/permissions?target_id=12345&target_type=Organization and check with: crossrev auth status\n\n"
	if !strings.Contains(b.text(), want) {
		t.Fatalf("printed:\n%s", b.text())
	}
}

// An App installed nowhere answers with an empty list and no error, and that is
// not "installed": the loop keeps waiting (lib/auth.sh:793-794).
func TestInstallKeepsWaitingWhileTheAppIsInstalledNowhere(t *testing.T) {
	results := []exec.Result{out(""), out(""), out("ShoreLogic all\n")}
	b := newBench(t, results...)
	b.meta(t, "CrossRev ShoreLogic", "crossrev-shorelogic")
	b.pem(t, 0o600)
	b.browser(&opener{})
	waits := b.noWait()

	if err := b.cmds.Install(context.Background(), app.InstallRequest{Owner: "ShoreLogic", Role: "loop"}); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if len(*waits) != 2 {
		t.Fatalf("waited %d times, want 2 before the third answer landed", len(*waits))
	}
	if !strings.Contains(b.text(), "installed on ShoreLogic (all repositories)") {
		t.Fatalf("printed:\n%s", b.text())
	}
}

// Two installations are two lines, which is what `while read -r acct sel`
// produces (lib/auth.sh:796-798).
func TestInstallReportsEveryAccountTheAppLandedOn(t *testing.T) {
	b := newBench(t, out("ShoreLogic selected\nbeta all\n"))
	b.meta(t, "CrossRev ShoreLogic", "crossrev-shorelogic")
	b.pem(t, 0o600)
	b.browser(&opener{})
	b.noWait()

	if err := b.cmds.Install(context.Background(), app.InstallRequest{Owner: "ShoreLogic", Role: "loop"}); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if !strings.Contains(b.text(), "│  ✓ installed on ShoreLogic (selected repositories)\n│  ✓ installed on beta (all repositories)\n") {
		t.Fatalf("printed:\n%s", b.text())
	}
}

// --- what must never be printed --------------------------------------------

func TestInstallKeepsTheJWTOutOfEveryPrintedLine(t *testing.T) {
	b := newBench(t, out("ShoreLogic selected\n"))
	b.meta(t, "CrossRev ShoreLogic", "crossrev-shorelogic")
	b.pem(t, 0o600)
	o := b.browser(&opener{})
	b.noWait()

	if err := b.cmds.Install(context.Background(), app.InstallRequest{Owner: "ShoreLogic", Role: "loop"}); err != nil {
		t.Fatalf("Install: %v", err)
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
	// And it never travels to the browser, which renders pages CrossRev did
	// not write.
	for _, url := range o.urls {
		if strings.Contains(url, jwt) {
			t.Fatal("the JWT reached the browser")
		}
	}
}

// The browser is handed an allowlist, not the process environment: it renders
// pages CrossRev did not write, and a forge credential has no business
// travelling with it.
func TestInstallHandsTheBrowserNoForgeCredential(t *testing.T) {
	b := newBench(t, out("ShoreLogic selected\n"))
	b.meta(t, "CrossRev ShoreLogic", "crossrev-shorelogic")
	b.pem(t, 0o600)
	o := b.browser(&opener{})
	b.noWait()

	t.Setenv("GH_TOKEN", "ghp_never_this")
	t.Setenv("GITHUB_TOKEN", "ghp_nor_this")
	b.cmds.Browser = app.NewBrowser(o, app.WithLookPath(o.look))

	if err := b.cmds.Install(context.Background(), app.InstallRequest{Owner: "ShoreLogic", Role: "loop"}); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if len(o.urls) == 0 {
		t.Fatal("the browser was never started, so this proves nothing")
	}
	// The opener records only URLs, so read the environment off the recorder's
	// own spec by starting one more.
	for _, name := range []string{"GH_TOKEN=", "GITHUB_TOKEN=", "GH_ENTERPRISE_TOKEN=", "GITHUB_ENTERPRISE_TOKEN="} {
		for _, entry := range browserEnvironmentOf(t, o) {
			if strings.HasPrefix(entry, name) {
				t.Fatalf("the browser inherited %s", strings.TrimSuffix(name, "="))
			}
		}
	}
}

// browserEnvironmentOf starts one opener call and answers the environment it
// was handed.
func browserEnvironmentOf(t *testing.T, o *opener) []string {
	t.Helper()
	rec := &recorder{}
	b := app.NewBrowser(rec, app.WithLookPath(o.look))
	if err := b.Open(context.Background(), "https://example.com"); err != nil {
		t.Fatalf("Open: %v", err)
	}
	return rec.specs[0].Env
}

// --- an empty role is the loop's, because the shell's parser starts there ---

func TestInstallReadsAnEmptyRoleAsTheLoops(t *testing.T) {
	b := newBench(t, out("ShoreLogic selected\n"))
	b.meta(t, "CrossRev ShoreLogic", "crossrev-shorelogic")
	b.pem(t, 0o600)
	b.browser(&opener{})
	b.noWait()

	if err := b.cmds.Install(context.Background(), app.InstallRequest{Owner: "ShoreLogic"}); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if !strings.Contains(b.text(), "installed on ShoreLogic") {
		t.Fatalf("printed:\n%s", b.text())
	}
}
