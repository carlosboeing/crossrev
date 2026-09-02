package app_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/carlosboeing/crossrev/internal/app"
	"github.com/carlosboeing/crossrev/internal/exec"
)

// --- the escaping the manifest page needs ----------------------------------

// The four substitutions, measured by running _html_attr_escape (lib/auth.sh:137)
// on each input. The apostrophe row is the one that matters: the sed pipeline
// has four -e clauses and none of them names ', so it survives.
func TestHTMLAttrEscapeReplacesFourCharactersAndLeavesTheApostrophe(t *testing.T) {
	cases := []struct{ in, want string }{
		{`a&b<c>d"e'f`, `a&amp;b&lt;c&gt;d&quot;e'f`},
		{`&`, `&amp;`},
		{`<`, `&lt;`},
		{`>`, `&gt;`},
		{`"`, `&quot;`},
		{`'`, `'`},
		{`it's`, `it's`},
		{``, ``},
	}
	for _, c := range cases {
		if got := app.HTMLAttrEscape(c.in); got != c.want {
			t.Errorf("HTMLAttrEscape(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// sed replaces every & before it replaces anything else, so the ampersands it
// introduces are never seen again. Measured: &amp; -> &amp;amp;.
func TestHTMLAttrEscapeDoesNotEscapeItsOwnOutput(t *testing.T) {
	if got, want := app.HTMLAttrEscape(`&amp;`), `&amp;amp;`; got != want {
		t.Fatalf("HTMLAttrEscape = %q, want %q", got, want)
	}
	if got, want := app.HTMLAttrEscape(`&lt;`), `&amp;lt;`; got != want {
		t.Fatalf("HTMLAttrEscape = %q, want %q", got, want)
	}
}

// Everything the sed clauses do not name passes through byte for byte,
// including a newline, a backslash and non-ASCII text. Measured on each.
func TestHTMLAttrEscapeLeavesEverythingElseAlone(t *testing.T) {
	for _, in := range []string{"a\nb", `a\b`, "café — ü", "a\tb", "%2F"} {
		if got := app.HTMLAttrEscape(in); got != in {
			t.Errorf("HTMLAttrEscape(%q) = %q, want it unchanged", in, got)
		}
	}
}

// --- the page the browser lands on -----------------------------------------

// wantDonePage is the heredoc at lib/auth.sh:253-269, written out again rather
// than derived, so a change to either side fails here.
const wantDonePage = `<!doctype html>
<html><head><meta charset="utf-8"><title>crossrev</title><style>
:root{color-scheme:light dark}
body{font:16px/1.6 system-ui,-apple-system,sans-serif;margin:0;min-height:100vh;
display:grid;place-items:center;background:#fbfbfa;color:#1f1b16}
@media (prefers-color-scheme:dark){body{background:#16130f;color:#f0ece4}}
.c{max-width:26rem;padding:2rem;text-align:center}
h1{font-size:1.25rem;margin:0 0 .5rem}
p{margin:.5rem 0;opacity:.75}
.t{margin-top:1.5rem;font-size:.875rem;opacity:.55}
</style></head><body><div class="c">
<h1>Registered</h1>
<p>crossrev has the App details and is carrying on in your terminal.</p>
<p class="t">You can close this tab.</p>
</div></body></html>
`

func TestDonePageIsTheHeredocByteForByte(t *testing.T) {
	if got := app.DonePage(); got != wantDonePage {
		t.Fatalf("DonePage() =\n%q\nwant\n%q", got, wantDonePage)
	}
	// `_auth_done_page | wc -c` is 698: cat of the heredoc keeps the last
	// newline.
	if got, want := len(app.DonePage()), 698; got != want {
		t.Fatalf("len(DonePage()) = %d, want %d", got, want)
	}
}

// --- the one-shot loopback listener ----------------------------------------

// wantResponseHead is the printf at lib/auth.sh:298, with the length the shell
// computes at :295 — `printf '%s' "$body" | wc -c` over a body that command
// substitution has already stripped the heredoc's final newline from, so 697
// rather than 698.
const wantResponseHead = "HTTP/1.1 200 OK\r\n" +
	"Content-Type: text/html; charset=utf-8\r\n" +
	"Content-Length: 697\r\n" +
	"Connection: close\r\n" +
	"\r\n"

func wantResponse() string {
	return wantResponseHead + strings.TrimSuffix(wantDonePage, "\n")
}

// listen opens a listener on a kernel-chosen port and closes it when the test
// ends.
func listen(t *testing.T) *app.Listener {
	t.Helper()
	l, err := app.ListenOn(0)
	if err != nil {
		t.Fatalf("ListenOn(0): %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l
}

// request opens one connection, sends a GET for target, and returns everything
// the listener wrote back before closing.
func request(t *testing.T, port int, target string) string {
	t.Helper()
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 5*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("SetDeadline: %v", err)
	}
	head := fmt.Sprintf("GET %s HTTP/1.1\r\nHost: 127.0.0.1:%d\r\nUser-Agent: crossrev-test\r\nAccept: */*\r\n\r\n", target, port)
	if _, err := io.WriteString(conn, head); err != nil {
		t.Fatalf("write: %v", err)
	}
	body, err := io.ReadAll(conn)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return string(body)
}

// waiting runs Wait in the background and hands back a function that collects
// its answer.
func waiting(t *testing.T, l *app.Listener, timeout time.Duration) func() (app.Redirect, error) {
	t.Helper()
	return waitingCtx(t, context.Background(), l, timeout)
}

func waitingCtx(t *testing.T, ctx context.Context, l *app.Listener, timeout time.Duration) func() (app.Redirect, error) {
	t.Helper()
	type answer struct {
		redirect app.Redirect
		err      error
	}
	done := make(chan answer, 1)
	go func() {
		r, err := l.Wait(ctx, timeout)
		done <- answer{r, err}
	}()
	return func() (app.Redirect, error) {
		t.Helper()
		select {
		case a := <-done:
			return a.redirect, a.err
		case <-time.After(20 * time.Second):
			t.Fatal("Wait did not return")
			return app.Redirect{}, nil
		}
	}
}

// The listener must not be reachable from another host. 0.0.0.0 and :: are both
// wrong answers, and neither is visible from the port number alone.
func TestTheListenerBindsTheIPv4LoopbackAlone(t *testing.T) {
	l := listen(t)
	addr, ok := l.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("Addr() is %T, want *net.TCPAddr", l.Addr())
	}
	if !addr.IP.Equal(net.IPv4(127, 0, 0, 1)) {
		t.Fatalf("bound to %s, want 127.0.0.1", addr.IP)
	}
	if addr.IP.IsUnspecified() {
		t.Fatalf("bound to the unspecified address %s", addr.IP)
	}
	if l.Port() != addr.Port {
		t.Fatalf("Port() = %d, addr says %d", l.Port(), addr.Port)
	}
}

// ListenOn(0) is the kernel-chosen port; it is what the tests here bind and
// what a caller with no port of its own gets.
func TestListenOnZeroAsksTheKernelForAPort(t *testing.T) {
	l := listen(t)
	if l.Port() <= 0 || l.Port() > 65535 {
		t.Fatalf("Port() = %d, want a real port", l.Port())
	}
}

// _free_port walks 33517 33518 33519 33520 33521 33522 in order and answers the
// first free one (lib/auth.sh:242).
func TestListenWalksTheShellsPortListInOrder(t *testing.T) {
	held, err := net.Listen("tcp", "127.0.0.1:33517")
	if err != nil {
		t.Skipf("33517 is not available to hold: %v", err)
	}
	defer func() { _ = held.Close() }()

	l, err := app.Listen()
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = l.Close() }()

	if l.Port() == 33517 {
		t.Fatal("Listen took a port something else already holds")
	}
	if l.Port() < 33518 || l.Port() > 33522 {
		t.Fatalf("Port() = %d, want one of 33518..33522", l.Port())
	}
}

// With every candidate taken the shell's `_free_port` returns 1 and auth_login
// falls back to the paste flow on port 33517 (lib/auth.sh:554-558). The port
// reports that as an error rather than binding something else.
func TestListenRefusesWhenEveryCandidatePortIsHeld(t *testing.T) {
	var held []net.Listener
	defer func() {
		for _, h := range held {
			_ = h.Close()
		}
	}()
	for p := 33517; p <= 33522; p++ {
		h, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p))
		if err != nil {
			t.Skipf("port %d is not available to hold: %v", p, err)
		}
		held = append(held, h)
	}
	l, err := app.Listen()
	if err == nil {
		_ = l.Close()
		t.Fatalf("Listen bound port %d with every candidate held", l.Port())
	}
	if app.FallbackPort != 33517 {
		t.Fatalf("FallbackPort = %d, want 33517", app.FallbackPort)
	}
}

// The whole point of the listener: the redirect arrives, the browser gets the
// page, and the code and state come back without anyone pasting.
func TestWaitAnswersTheRedirectWithTheDonePage(t *testing.T) {
	l := listen(t)
	collect := waiting(t, l, 10*time.Second)

	got := request(t, l.Port(), "/crossrev-auth?code=ABC123&state=deadbeef")
	if got != wantResponse() {
		t.Fatalf("response =\n%q\nwant\n%q", got, wantResponse())
	}
	if len(got) != 796 {
		t.Fatalf("response is %d bytes, want 796", len(got))
	}

	redirect, err := collect()
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if redirect.Code != "ABC123" || redirect.State != "deadbeef" {
		t.Fatalf("redirect = %+v, want {Code:ABC123 State:deadbeef}", redirect)
	}
}

// The sed at lib/auth.sh:646-647 reads the last [?&]code= on the line, stops the
// value at & or a space, and decodes nothing. Every row was measured against it.
func TestWaitReadsTheCodeTheWayTheShellsSedDoes(t *testing.T) {
	cases := []struct {
		target     string
		code       string
		state      string
		wantAnswer bool
	}{
		{"/crossrev-auth?code=ABC&state=XY", "ABC", "XY", true},
		{"/crossrev-auth?state=XY&code=ABC", "ABC", "XY", true},
		{"/a?code=one&code=two", "two", "", true},
		{"/a?code=A%2FB", "A%2FB", "", true},
		// grep matches the substring, so xcode= ends the wait; the sed
		// requires a ? or & before the name, so it extracts nothing. Measured
		// against the shell: _listen_for_code returned 0 on this request line.
		{"/a?xcode=nope", "", "", true},
		{"/a?code=", "", "", true},
	}
	for _, c := range cases {
		t.Run(c.target, func(t *testing.T) {
			l := listen(t)
			collect := waiting(t, l, 2*time.Second)
			request(t, l.Port(), c.target)
			redirect, err := collect()
			if c.wantAnswer && err != nil {
				t.Fatalf("Wait: %v", err)
			}
			if !c.wantAnswer && !errors.Is(err, app.ErrNoCode) {
				t.Fatalf("err = %v, want ErrNoCode", err)
			}
			if redirect.Code != c.code || redirect.State != c.state {
				t.Fatalf("redirect = %+v, want {Code:%q State:%q}", redirect, c.code, c.state)
			}
		})
	}
}

// A request with no code at all leaves the wait running until the deadline, the
// way `grep -q 'code='` never matching does (lib/auth.sh:303-310).
func TestWaitIgnoresARequestCarryingNoCode(t *testing.T) {
	l := listen(t)
	collect := waiting(t, l, 700*time.Millisecond)

	if got := request(t, l.Port(), "/favicon.ico"); got != wantResponse() {
		t.Fatalf("the first connection got %d bytes, want the page", len(got))
	}
	redirect, err := collect()
	if !errors.Is(err, app.ErrNoCode) {
		t.Fatalf("err = %v, want ErrNoCode", err)
	}
	if redirect != (app.Redirect{}) {
		t.Fatalf("redirect = %+v, want the zero value", redirect)
	}
}

// `nc -k` keeps accepting after the first connection, and only the first
// receives the body because nc's stdin is consumed once (lib/auth.sh:281-285).
// Measured against the shell: a favicon request followed a second later by the
// real redirect gets 796 bytes and 0 bytes respectively.
//
// The second half of the row is the shell's defect, kept because a person can
// see it: auth_login reads `head -1` of the concatenated requests
// (lib/auth.sh:645), so the code on the *second* request line is never
// extracted and the flow drops to the paste prompt with an empty code.
func TestWaitAnswersOnlyTheFirstConnection(t *testing.T) {
	l := listen(t)
	collect := waiting(t, l, 5*time.Second)

	first := request(t, l.Port(), "/favicon.ico")
	if first != wantResponse() {
		t.Fatalf("the first connection got %d bytes, want the page", len(first))
	}
	second := request(t, l.Port(), "/crossrev-auth?code=REAL&state=st")
	if second != "" {
		t.Fatalf("the second connection got %q, want nothing", second)
	}

	redirect, err := collect()
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if redirect.Code != "" || redirect.State != "" {
		t.Fatalf("redirect = %+v, want the zero value: the shell reads head -1", redirect)
	}
}

// `grep -q 'code='` reads the whole request, headers included, so a code= in a
// header ends the wait even though the request line the caller parses has none.
func TestWaitStopsOnACodeAnywhereInTheRequest(t *testing.T) {
	l := listen(t)
	collect := waiting(t, l, 5*time.Second)

	conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", l.Port()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	head := fmt.Sprintf("GET /favicon.ico HTTP/1.1\r\nHost: 127.0.0.1:%d\r\nReferer: http://x/?code=HEADER\r\n\r\n", l.Port())
	if _, err := io.WriteString(conn, head); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := io.ReadAll(conn); err != nil {
		t.Fatalf("read: %v", err)
	}
	_ = conn.Close()

	redirect, err := collect()
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if redirect.Code != "" {
		t.Fatalf("Code = %q, want empty: the request line carries none", redirect.Code)
	}
}

// The deadline is the caller's. lib/auth.sh:644 passes 300, and the function's
// own default at :290 is the same number.
func TestWaitGivesUpAtTheDeadlineWithNothingConnecting(t *testing.T) {
	l := listen(t)
	start := time.Now()
	redirect, err := l.Wait(context.Background(), 200*time.Millisecond)
	elapsed := time.Since(start)

	if !errors.Is(err, app.ErrNoCode) {
		t.Fatalf("err = %v, want ErrNoCode", err)
	}
	if redirect != (app.Redirect{}) {
		t.Fatalf("redirect = %+v, want the zero value", redirect)
	}
	if elapsed > 10*time.Second {
		t.Fatalf("Wait took %s for a 200ms deadline", elapsed)
	}
	if app.WaitTimeout != 300*time.Second {
		t.Fatalf("WaitTimeout = %s, want 5m0s", app.WaitTimeout)
	}
}

// `${3:-300}` at lib/auth.sh:290 fills in an absent argument, not a zero one:
// `_listen_for_code p f 0` runs no iteration of the wait loop. Go has no absent
// argument, so a zero here waits not at all rather than silently becoming five
// minutes.
func TestWaitWithNoTimeGivesUpAtOnce(t *testing.T) {
	l := listen(t)
	start := time.Now()
	if _, err := l.Wait(context.Background(), 0); !errors.Is(err, app.ErrNoCode) {
		t.Fatalf("err = %v, want ErrNoCode", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("a zero timeout waited %s", elapsed)
	}
}

func TestWaitStopsWhenTheContextIsCancelled(t *testing.T) {
	l := listen(t)
	ctx, cancel := context.WithCancel(context.Background())
	collect := waitingCtx(t, ctx, l, time.Minute)
	cancel()

	redirect, err := collect()
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if redirect != (app.Redirect{}) {
		t.Fatalf("redirect = %+v, want the zero value", redirect)
	}
}

// The code is a bearer of the App's private key for one hour. Nothing this
// package returns on a failure may carry any part of the request.
func TestWaitKeepsTheRequestOutOfItsError(t *testing.T) {
	l := listen(t)
	collect := waiting(t, l, 700*time.Millisecond)

	conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", l.Port()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	head := fmt.Sprintf("GET /crossrev-auth?token=s3cr3t-value HTTP/1.1\r\nHost: 127.0.0.1:%d\r\nAuthorization: Bearer s3cr3t-value\r\n\r\n", l.Port())
	if _, err := io.WriteString(conn, head); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := io.ReadAll(conn); err != nil {
		t.Fatalf("read: %v", err)
	}
	_ = conn.Close()

	_, err = collect()
	if err == nil {
		t.Fatal("Wait answered a request with no code")
	}
	for _, secret := range []string{"s3cr3t-value", "Authorization", "Bearer", "crossrev-auth"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("error %q carries %q", err, secret)
		}
	}
}

func TestCloseTwiceIsNotAnError(t *testing.T) {
	l, err := app.ListenOn(0)
	if err != nil {
		t.Fatalf("ListenOn(0): %v", err)
	}
	if err := l.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := l.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

// --- the browser handoff ---------------------------------------------------

// stubLook answers for exactly the names given and refuses everything else,
// which is what `command -v` does with a PATH holding only those.
func stubLook(names ...string) func(string) (string, error) {
	set := map[string]bool{}
	for _, n := range names {
		set[n] = true
	}
	return func(name string) (string, error) {
		if set[name] {
			return "/stub/" + name, nil
		}
		return "", os.ErrNotExist
	}
}

// lib/auth.sh:144 — `open "$url"`, and nothing else on the command line.
func TestOpenBrowserRunsOpenWithTheURLAlone(t *testing.T) {
	rec := &recorder{}
	b := app.NewBrowser(rec, app.WithLookPath(stubLook("open", "xdg-open")))
	if err := b.Open(context.Background(), "file:///tmp/x.html"); err != nil {
		t.Fatalf("Open: %v", err)
	}
	rec.wantArgvFor(t, "open", "file:///tmp/x.html")
}

// lib/auth.sh:145 — the elif arm, reached only when open is absent.
func TestOpenBrowserFallsBackToXdgOpen(t *testing.T) {
	rec := &recorder{}
	b := app.NewBrowser(rec, app.WithLookPath(stubLook("xdg-open")))
	if err := b.Open(context.Background(), "https://github.com/apps/x"); err != nil {
		t.Fatalf("Open: %v", err)
	}
	rec.wantArgvFor(t, "xdg-open", "https://github.com/apps/x")
}

// `else return 1` at lib/auth.sh:146. auth_login turns it into a ui_warn naming
// the file to open by hand (lib/auth.sh:640-642), so the error has to be
// distinguishable and nothing may be started.
func TestOpenBrowserRefusesWhenNeitherOpenerIsInstalled(t *testing.T) {
	rec := &recorder{}
	b := app.NewBrowser(rec, app.WithLookPath(stubLook()))
	err := b.Open(context.Background(), "file:///tmp/x.html")
	if !errors.Is(err, app.ErrNoOpener) {
		t.Fatalf("err = %v, want ErrNoOpener", err)
	}
	if len(rec.specs) != 0 {
		t.Fatalf("started %d processes with no opener installed", len(rec.specs))
	}
}

// _open_browser's status is the opener's own, so a non-zero exit is a failure
// the caller warns about rather than an ignored one.
// TestErrNoOpenerText pins the sentinel's bytes: a caller that prints the
// error directly would otherwise print changed text with no test failing.
func TestErrNoOpenerText(t *testing.T) {
	if got := app.ErrNoOpener.Error(); got != "neither open nor xdg-open is installed" {
		t.Errorf("ErrNoOpener = %q", got)
	}
}

func TestOpenBrowserReportsTheOpenersOwnFailure(t *testing.T) {
	rec := &recorder{results: []exec.Result{{ExitCode: 1}}}
	b := app.NewBrowser(rec, app.WithLookPath(stubLook("open")))
	if err := b.Open(context.Background(), "file:///tmp/x.html"); err == nil {
		t.Fatal("Open reported success for an opener that exited 1")
	}
}

func TestOpenBrowserReportsAnOpenerThatNeverStarted(t *testing.T) {
	rec := &recorder{results: []exec.Result{unresolved()}}
	b := app.NewBrowser(rec, app.WithLookPath(stubLook("open")))
	if err := b.Open(context.Background(), "file:///tmp/x.html"); err == nil {
		t.Fatal("Open reported success for an opener that never started")
	}
}

// The URL reaches the opener as one argv entry whatever it holds. A manifest
// page's path comes from mktemp and a settings URL carries & and ?.
func TestOpenBrowserPassesOneArgumentWhateverTheURLHolds(t *testing.T) {
	for _, url := range []string{
		"https://github.com/apps/s/installations/new/permissions?target_id=1&target_type=User",
		"file:///tmp/crossrev manifest.html",
		"file:///tmp/x?a=b&c=d",
	} {
		rec := &recorder{}
		b := app.NewBrowser(rec, app.WithLookPath(stubLook("open")))
		if err := b.Open(context.Background(), url); err != nil {
			t.Fatalf("Open: %v", err)
		}
		rec.wantArgvFor(t, "open", url)
	}
}

// The browser renders attacker-reachable pages. No forge credential goes with
// it, whatever this process holds.
func TestOpenBrowserCarriesNoForgeCredential(t *testing.T) {
	for _, name := range exec.ForgeCredentialNames() {
		t.Setenv(name, "must-not-travel")
	}
	rec := &recorder{}
	b := app.NewBrowser(rec, app.WithLookPath(stubLook("open")))
	if err := b.Open(context.Background(), "file:///tmp/x.html"); err != nil {
		t.Fatalf("Open: %v", err)
	}
	spec := rec.only(t)
	for _, entry := range spec.Env {
		name, _, _ := strings.Cut(entry, "=")
		for _, credential := range exec.ForgeCredentialNames() {
			if name == credential {
				t.Fatalf("the opener was handed %s", name)
			}
		}
		if strings.Contains(entry, "must-not-travel") {
			t.Fatalf("the opener was handed %q", name)
		}
	}
}

// The default lookup is the process's own PATH. Every test above injects one,
// and an injected lookup that always answers would hide a broken default, so
// this one goes through it with a PATH holding two real files.
func TestOpenBrowserSearchesTheProcessPathWhenNoLookupIsInjected(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"open", "xdg-open"} {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
	}
	t.Setenv("PATH", dir)

	rec := &recorder{}
	b := app.NewBrowser(rec)
	if err := b.Open(context.Background(), "file:///tmp/x.html"); err != nil {
		t.Fatalf("Open: %v", err)
	}
	// open first, xdg-open second: the if/elif order at lib/auth.sh:144-145.
	rec.wantArgvFor(t, "open", "file:///tmp/x.html")
}

func TestOpenBrowserFindsNoOpenerOnAnEmptyPath(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	rec := &recorder{}
	b := app.NewBrowser(rec)
	if err := b.Open(context.Background(), "file:///tmp/x.html"); !errors.Is(err, app.ErrNoOpener) {
		t.Fatalf("err = %v, want ErrNoOpener", err)
	}
}

func TestNewBrowserWithNoRunnerPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("NewBrowser(nil) returned instead of panicking")
		}
	}()
	_ = app.NewBrowser(nil)
}

// wantArgvFor asserts the single recorded invocation's program and arguments.
func (r *recorder) wantArgvFor(t *testing.T, program string, want ...string) {
	t.Helper()
	spec := r.only(t)
	if spec.Path != program {
		t.Fatalf("program = %q, want %q", spec.Path, program)
	}
	if !bytes.Equal([]byte(strings.Join(spec.Args, "\x00")), []byte(strings.Join(want, "\x00"))) {
		t.Fatalf("argv = %q\nwant   %q", spec.Args, want)
	}
}
