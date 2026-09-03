package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/carlosboeing/crossrev/internal/exec"
)

// --- escaping a value into an HTML attribute -------------------------------

// htmlAttrEscaper is the sed pipeline at lib/auth.sh:152-153, which has four
// -e clauses and no fifth.
//
// One pass rather than four is the same answer here and only here: every
// pattern is a single character, and the three characters the replacements
// introduce are & ; and letters, none of which a later clause names. Measured
// against the shell, which turns &amp; into &amp;amp; for the same reason.
var htmlAttrEscaper = strings.NewReplacer(
	"&", "&amp;",
	"<", "&lt;",
	">", "&gt;",
	`"`, "&quot;",
)

// HTMLAttrEscape prepares a value for an HTML attribute (_html_attr_escape,
// lib/auth.sh:151).
//
// The apostrophe is not escaped, and that is the shell's list rather than an
// omission here: every attribute it feeds is double-quoted (lib/auth.sh:636-638),
// so ' cannot end one. A caller that writes a single-quoted attribute would
// need more than this function offers.
func HTMLAttrEscape(s string) string { return htmlAttrEscaper.Replace(s) }

// --- the page the browser lands on -----------------------------------------

// donePage is the heredoc at lib/auth.sh:267-283, byte for byte, trailing
// newline included: `cat` of a heredoc ends with one, and `_auth_done_page |
// wc -c` is 698.
//
// The three lowercase spellings of the product name inside it are the shell's
// own bytes. Parity wins over the naming rule for a string that is copied
// rather than written.
const donePage = `<!doctype html>
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

// DonePage is what _auth_done_page prints (lib/auth.sh:266).
func DonePage() string { return donePage }

// doneResponse is the whole HTTP reply, exactly as the printf at
// lib/auth.sh:312 builds it.
//
// The body is the page without its final newline, because lib/auth.sh:306
// captures the heredoc through command substitution and that strips it; the
// Content-Length at :295 is measured over the stripped bytes, so it is 697 and
// not 698. There is no Date and no Server header: nc sends what it is given and
// nothing more, which is why this writes the bytes rather than using net/http.
func doneResponse() string {
	body := strings.TrimSuffix(donePage, "\n")
	return "HTTP/1.1 200 OK\r\n" +
		"Content-Type: text/html; charset=utf-8\r\n" +
		"Content-Length: " + strconv.Itoa(len(body)) + "\r\n" +
		"Connection: close\r\n" +
		"\r\n" + body
}

// --- the one-shot loopback listener ----------------------------------------

// listenPorts is the list _free_port walks, in order (lib/auth.sh:256).
var listenPorts = []int{33517, 33518, 33519, 33520, 33521, 33522}

// FallbackPort is the port auth_login names in redirect_url when no listener
// could be bound (lib/auth.sh:571). Nothing listens on it: the browser lands on
// a connection refused and the person pastes the address bar.
const FallbackPort = 33517

// WaitTimeout is how long auth_login waits for the redirect (lib/auth.sh:658,
// and the same number as the default at :290).
const WaitTimeout = 300 * time.Second

// ErrNoCode is what Wait reports when no request carrying a code arrived before
// the deadline — the non-zero return of _listen_for_code (lib/auth.sh:328).
//
// It carries nothing from the request. The OAuth code is exchanged for the
// App's private key, so a request line has no business in an error string,
// which reaches a terminal and a run log.
var ErrNoCode = errors.New("no registration code arrived on the loopback listener")

// Redirect is what auth_login pulls out of the first request line
// (lib/auth.sh:659-661).
type Redirect struct {
	// Code is the value of the last ?code= or &code= on the line.
	Code string
	// State is the same for state=. Comparing it against the value CrossRev
	// sent is the caller's (lib/auth.sh:693-696); nothing here checks it,
	// because _listen_for_code does not either.
	State string
}

// Listener is the loopback socket the GitHub redirect lands on
// (_listen_for_code, lib/auth.sh:303).
//
// The Bash version shells out to `nc -k -l`, and _listener_available
// (lib/auth.sh:262) asks whether nc is installed at all. There is no such
// question here — the process binds the socket itself — so the decision
// auth_login makes at lib/auth.sh:568 becomes the error from Listen.
type Listener struct {
	ln net.Listener

	closeOnce sync.Once
	closeErr  error
}

// Listen binds the first free port in the shell's list.
//
// It binds rather than probing, which _port_free could not: `nc -z localhost`
// (lib/auth.sh:252) reports a port free and leaves it free for whatever binds
// it in the next millisecond. The port is held from here on.
func Listen() (*Listener, error) {
	var last error
	for _, port := range listenPorts {
		l, err := ListenOn(port)
		if err == nil {
			return l, nil
		}
		last = err
	}
	return nil, fmt.Errorf("no free port between %d and %d: %w",
		listenPorts[0], listenPorts[len(listenPorts)-1], last)
}

// ListenOn binds one port. Port 0 asks the kernel for a free one.
//
// The address is the IPv4 loopback and never the unspecified address: a
// listener on 0.0.0.0 or :: takes the redirect from any host on the network,
// and the value it receives is the code that mints the App's private key.
func ListenOn(port int) (*Listener, error) {
	ln, err := net.Listen("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return nil, err
	}
	return &Listener{ln: ln}, nil
}

// Addr is the address the socket is bound to.
func (l *Listener) Addr() net.Addr { return l.ln.Addr() }

// Port is the port the socket is bound to, which redirect_url has to name.
func (l *Listener) Port() int {
	if addr, ok := l.ln.Addr().(*net.TCPAddr); ok {
		return addr.Port
	}
	return 0
}

// Close releases the socket. Calling it twice is not an error, because Wait
// closes it on a cancelled context and the caller still has a defer.
func (l *Listener) Close() error {
	l.closeOnce.Do(func() { l.closeErr = l.ln.Close() })
	return l.closeErr
}

// maxRequestBytes caps one connection's request. The Bash version writes every
// connection to a file with no cap; a cap is here because this one holds the
// bytes in memory and the socket is reachable by anything on the machine.
const maxRequestBytes = 64 << 10

// Wait accepts connections until one of them carries a registration code, or
// until timeout elapses or ctx ends.
//
// It is _listen_for_code (lib/auth.sh:303-329) plus the two sed lines its caller
// runs on the result (lib/auth.sh:659-661), and it keeps three properties of the
// shell that are easy to lose:
//
//   - It keeps accepting after the first connection, which is what `nc -k`
//     does and why (lib/auth.sh:289-294): browsers open speculative
//     connections and anything on the machine can probe a listening port.
//     Serving one connection and re-binding loses a request that arrives in
//     the gap, which is the failure the listener exists to remove.
//
//   - Only the first connection receives the page. nc's stdin is consumed
//     once, so the shell answers one connection and no more; lib/auth.sh:295-299
//     records that as a deliberate trade. Measured against the shell: a
//     favicon request followed by the real redirect got 796 bytes and 0.
//
//   - The code comes from the first request line of the whole stream, because
//     auth_login reads `head -1` of the file nc wrote every connection into
//     (lib/auth.sh:659). A decoy arriving first therefore ends the wait with an
//     empty Code and the caller drops to the paste prompt at lib/auth.sh:674.
//     That is a fault in the shipped shell rather than a design, and it is kept
//     here because a person sees the difference.
//
// A timeout of zero or less waits not at all. The shell's `${3:-300}` at
// lib/auth.sh:304 defaults an *absent* argument, and Go has no absent argument;
// inventing a default here would hide a caller that forgot to pass one.
func (l *Listener) Wait(ctx context.Context, timeout time.Duration) (Redirect, error) {
	deadline := time.Now().Add(timeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}

	// Accept does not watch a context. Closing the socket is what unblocks it,
	// so a cancelled context spends the listener; the caller's defer Close then
	// finds it already closed, which Close allows.
	stop := context.AfterFunc(ctx, func() { _ = l.Close() })
	defer stop()

	var raw []byte
	served := false

	for {
		tcp, ok := l.ln.(*net.TCPListener)
		if !ok {
			break
		}
		if err := tcp.SetDeadline(deadline); err != nil {
			break
		}
		conn, err := l.ln.Accept()
		if err != nil {
			break
		}
		request := readRequest(conn, deadline)
		if !served {
			_ = conn.SetDeadline(deadline)
			_, _ = io.WriteString(conn, doneResponse())
			served = true
		}
		_ = conn.Close()

		raw = append(raw, request...)
		// `grep -q 'code='` reads the whole file, headers included
		// (lib/auth.sh:318), so a code= anywhere ends the wait.
		if bytes.Contains(raw, []byte("code=")) {
			line := firstLine(raw)
			return Redirect{
				Code:  lastQueryValue(line, "code"),
				State: lastQueryValue(line, "state"),
			}, nil
		}
	}

	if err := ctx.Err(); err != nil {
		return Redirect{}, err
	}
	return Redirect{}, ErrNoCode
}

// readRequest reads one connection up to the end of its headers.
//
// nc writes the whole connection to the file, body included. Stopping at the
// blank line differs only for a request whose body carries code= and whose
// headers do not, which no browser redirect produces.
func readRequest(conn net.Conn, deadline time.Time) []byte {
	_ = conn.SetDeadline(deadline)
	var buf []byte
	chunk := make([]byte, 4096)
	for len(buf) < maxRequestBytes {
		n, err := conn.Read(chunk)
		if n > 0 {
			buf = append(buf, chunk[:n]...)
		}
		if bytes.Contains(buf, []byte("\r\n\r\n")) || err != nil {
			break
		}
	}
	return buf
}

// firstLine is `head -1`: the bytes before the first newline, or all of them
// when there is none. The carriage return an HTTP request line ends with stays,
// because head does not strip it either.
func firstLine(raw []byte) string {
	if i := bytes.IndexByte(raw, '\n'); i >= 0 {
		return string(raw[:i])
	}
	return string(raw)
}

// lastQueryValue is `sed -n 's/.*[?&]NAME=\([^& ]*\).*/\1/p'`
// (lib/auth.sh:660-661).
//
// Three details of that expression are load-bearing and were measured rather
// than read: the leading .* is greedy, so the LAST ?NAME= or &NAME= on the line
// wins; the value stops at & or a space, which is what keeps ` HTTP/1.1` out of
// it; and nothing is percent-decoded, so A%2FB comes back as A%2FB.
func lastQueryValue(line, name string) string {
	needle := name + "="
	value := ""
	for i := 1; i+len(needle) <= len(line); i++ {
		if line[i:i+len(needle)] != needle {
			continue
		}
		// [?&] has to match the character before the name, so a longer name
		// ending in it — xcode=, for one — does not.
		if line[i-1] != '?' && line[i-1] != '&' {
			continue
		}
		start := i + len(needle)
		end := start
		for end < len(line) && line[end] != '&' && line[end] != ' ' {
			end++
		}
		value = line[start:end]
	}
	return value
}

// --- the browser handoff ---------------------------------------------------

// browserOpeners is the if/elif order at lib/auth.sh:158-159. The shell
// switches on which of the two is installed rather than on the platform, so
// this list is not per-GOOS either.
var browserOpeners = []string{"open", "xdg-open"}

// browserEnvironment is what an opener is allowed to inherit.
//
// The Bash version hands the opener the whole environment, because a shell
// function has no other option. This is an allowlist for the reason
// internal/exec's Inherit documents: the browser renders pages CrossRev did not
// write, and a forge credential has no business travelling with it. Every name
// is one an opener reads — xdg-open is a shell script that resolves the desktop
// from XDG_CURRENT_DESKTOP and DE, finds its helpers on PATH, and reaches the
// session over DBUS_SESSION_BUS_ADDRESS and DISPLAY or WAYLAND_DISPLAY.
var browserEnvironment = []string{
	"PATH",
	"HOME",
	"USER",
	"LOGNAME",
	"LANG",
	"LC_ALL",
	"TMPDIR",
	"DISPLAY",
	"WAYLAND_DISPLAY",
	"XAUTHORITY",
	"DBUS_SESSION_BUS_ADDRESS",
	"XDG_CURRENT_DESKTOP",
	"XDG_SESSION_TYPE",
	"XDG_RUNTIME_DIR",
	"XDG_DATA_HOME",
	"XDG_DATA_DIRS",
	"XDG_CONFIG_HOME",
	"BROWSER",
	"DE",
}

// ErrNoOpener is `else return 1` at lib/auth.sh:160: neither opener is
// installed. auth_login turns it into a warning naming the URL to open by hand
// (lib/auth.sh:654-656), so it never stops the flow.
var ErrNoOpener = errors.New("neither open nor xdg-open is installed")

// Browser opens a URL in whatever the machine calls a browser (_open_browser,
// lib/auth.sh:156).
type Browser struct {
	runner exec.Runner
	env    []string
	look   func(string) (string, error)
}

// BrowserOption adjusts a Browser at construction.
type BrowserOption func(*Browser)

// WithLookPath replaces the PATH search. The default is the process's own PATH.
func WithLookPath(look func(string) (string, error)) BrowserOption {
	return func(b *Browser) { b.look = look }
}

// NewBrowser returns a Browser that starts an opener through runner.
//
// A nil runner panics, for NewGH's reason: inventing one would start a child
// process that a wiring bug never meant to start.
func NewBrowser(runner exec.Runner, opts ...BrowserOption) *Browser {
	if runner == nil {
		panic("app.NewBrowser: runner is nil")
	}
	b := &Browser{runner: runner, env: exec.Inherit(browserEnvironment), look: lookPath}
	for _, opt := range opts {
		opt(b)
	}
	return b
}

// Open hands url to the first opener on PATH.
//
// The opener's own streams are discarded, which is the `>/dev/null 2>&1` on
// both arms, and its exit status is the answer, which is what makes
// _open_browser's status the opener's.
func (b *Browser) Open(ctx context.Context, url string) error {
	for _, opener := range browserOpeners {
		if _, err := b.look(opener); err != nil {
			continue
		}
		res := b.runner.Run(ctx, exec.Spec{Path: opener, Args: []string{url}, Env: b.env})
		if res.Err != nil {
			return fmt.Errorf("%s could not be started: %w", opener, res.Err)
		}
		if res.ExitCode != 0 {
			return fmt.Errorf("%s exited %d", opener, res.ExitCode)
		}
		return nil
	}
	return ErrNoOpener
}

// lookPath reports whether a program is on PATH, the way `command -v` does
// (lib/auth.sh:158-159).
//
// It is the helper internal/review/invoke.go:93 already carries, which this
// package may not import, plus a directory check: `command -v` answers absent
// for a directory on PATH, and os.Stat alone would answer present.
func lookPath(name string) (string, error) {
	if name == "" {
		return "", os.ErrNotExist
	}
	if strings.ContainsRune(name, os.PathSeparator) {
		if info, err := os.Stat(name); err != nil || info.IsDir() {
			return "", os.ErrNotExist
		}
		return name, nil
	}
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if dir == "" {
			continue
		}
		candidate := filepath.Join(dir, name)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, nil
		}
	}
	return "", os.ErrNotExist
}
