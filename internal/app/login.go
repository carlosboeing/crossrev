package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/carlosboeing/crossrev/internal/exec"
	"github.com/carlosboeing/crossrev/internal/ui"
)

// LoginRequest is `crossrev auth login`'s parsed options (lib/auth.sh:502-509).
type LoginRequest struct {
	// Owner is --owner. Empty means detect it from the repository the working
	// directory belongs to.
	Owner string
	// Name is --name. Empty means the name CrossRev proposes for the role.
	Name string
	// Role is --role, and empty is the loop's — the shell's parser starts from
	// `local role="loop"`.
	Role string
}

// ErrDeclined is what a refused confirmation returns.
//
// The Bash function prints "Nothing was created." and returns 1
// (lib/auth.sh:608), which is not a failure to report: the reader was asked and
// said no. It carries no ui.FatalError, so nothing prints an error block over
// the top of the sentence that already said what happened.
var ErrDeclined = errors.New("the reader declined")

// Login registers a GitHub App for an owner, and then installs it
// (auth_login, lib/auth.sh:501).
func (c *Commands) Login(ctx context.Context, req LoginRequest) error {
	role := req.Role
	if role == "" {
		role = RoleLoop
	}

	// Rejects an unknown role before anything opens (lib/auth.sh:511). The
	// permissions are read again below; this call is for its refusal alone.
	if _, err := RolePermissions(role); err != nil {
		var refusal *ui.FatalError
		if errors.As(err, &refusal) {
			return c.IO.Die(refusal.Reason, refusal.Action)
		}
		return err
	}

	// `_ui_input_source >/dev/null || _ui_no_input` (lib/auth.sh:513): the
	// check is outside ui_confirm, so --yes does not skip it. A flow that
	// opened a browser and then found it could not ask anything would leave a
	// half-registered App behind.
	if err := c.requireInput(); err != nil {
		return err
	}

	owner := req.Owner
	if owner == "" {
		detected, err := c.GH.DetectOwner(ctx)
		if err != nil {
			return c.IO.Die(
				"could not work out which account this App should belong to",
				"Run this inside a git repository with a GitHub remote, or name it: crossrev auth login --owner <owner>")
		}
		owner = detected
	}

	account, err := c.GH.AccountInfo(ctx, owner)
	if err != nil {
		return c.IO.Die(
			"GitHub does not recognise the account '"+owner+"'",
			"Check the spelling, or pass a different one with --owner")
	}

	dir := Dir(c.Env)
	metaPath := MetaPath(dir, owner, role)
	if isRegularFile(metaPath) {
		meta, err := ReadMetadata(metaPath)
		if err != nil {
			return err
		}
		c.IO.Section("Already configured")
		c.IO.OK(fmt.Sprintf("%s — %s (id %d, role %s)", owner, meta.Name, meta.ID, role))
		c.IO.Gap()
		c.IO.Line("One App per owner per role is the design. Creating a second would mean")
		c.IO.Line("a second private key to protect and rotate, for no extra reach.")
		c.IO.End("See where it is installed with:   crossrev auth status")
		return nil
	}

	// GitHub App names are globally unique, so a bare "crossrev" is very likely
	// taken. Suffixing the owner is likelier to be free and clearer in a list.
	appName := req.Name
	if appName == "" {
		appName = RoleDefaultName(role, owner)
	}

	// The bot account exists exactly when the App does, so this is the probe
	// that finds a name already taken before a browser opens.
	slug := Slug(appName)
	if _, err := c.GH.AccountInfo(ctx, slug+"[bot]"); err == nil {
		return c.IO.Die(
			"a GitHub App named '"+appName+"' already exists",
			"CrossRev cannot reuse an existing App when local metadata is missing. Register a separate App with: crossrev auth login --name <name>")
	}

	state, err := c.stateValue()
	if err != nil {
		return fmt.Errorf("could not generate a state value: %w", err)
	}

	// Bind the port BEFORE building the manifest, so redirect_url names a port
	// we know is free. Hardcoding it first is how you get a redirect to a port
	// something else already owns, with nothing to do about it afterwards.
	//
	// The shell asks two questions here — is nc installed, and is a port free —
	// and this asks one, because binding the socket answers both
	// (lib/auth.sh:553-558).
	port := FallbackPort
	listener, listenErr := c.listen()
	if listenErr == nil {
		defer listener.Close()
		port = listener.Port()
	}
	redirect := fmt.Sprintf("http://localhost:%d/crossrev-auth", port)

	permissions, err := RolePermissions(role)
	if err != nil {
		return err
	}
	manifest := LoginManifest(appName, redirect, permissions)

	postURL := "https://github.com/settings/apps/new"
	if account.Type == "Organization" {
		postURL = "https://github.com/organizations/" + owner + "/settings/apps/new"
	}

	c.IO.Section("Register a GitHub App for " + owner)
	c.IO.Line("Owner        " + owner + " (" + account.Type + ")")
	c.IO.Line("Name         " + appName + " (override with --name)")
	c.IO.Line("Role         " + role)
	c.IO.Line("Permissions  " + RoleSummary(role))
	c.IO.Line("             and nothing else")
	c.IO.Line("Webhook      disabled. GitHub never calls CrossRev; your workflows do")
	c.IO.Line("Visibility   private to " + owner)
	c.IO.Gap()
	if role == RoleRefresher {
		c.IO.Line("This App exists to write one secret, on a schedule, and does nothing")
		c.IO.Line("else. Its workflow never checks out a pull request branch, never runs")
		c.IO.Line("a model and never reads a diff or a comment — there is nothing in it")
		c.IO.Line("to inject into, which is what makes secrets:write safe here and unsafe")
		c.IO.Line("on the App the review jobs use.")
	} else {
		c.IO.Line("issues:write looks surprising and is not optional — GitHub models pull")
		c.IO.Line("request labels under the Issues API, and the loop is label-driven.")
	}
	c.IO.Gap()
	c.IO.Line("Two approvals in the browser: create the App, then install it. CrossRev")
	c.IO.Line("follows along here — nothing to copy back.")
	c.blank()

	agreed, err := c.IO.Confirm("Open GitHub in your browser to create the App?")
	if err != nil {
		return err
	}
	if !agreed {
		c.IO.Say("Nothing was created.")
		return ErrDeclined
	}

	page, err := c.writeLoginPage(LoginPage(appName, postURL, string(manifest), state))
	if err != nil {
		return err
	}
	// The page carries the manifest and the state value, so it goes when the
	// flow does — on every arm, including the refusals below. `trap RETURN` is
	// what the shell uses (lib/auth.sh:613).
	defer os.Remove(page)

	// fromListener records which of the two paths produced the code, because
	// only a redirect can be required to send the state back
	// (lib/auth.sh:664-666).
	var code, returnedState string
	fromListener := false
	if listenErr == nil {
		c.IO.Section("Step 1 of 2: Create the GitHub App")
		c.IO.Line("A browser tab is open on GitHub's App registration page.")
		c.IO.Line("Review the settings and create the App. CrossRev detects the creation")
		c.IO.Line("and continues automatically.")
		c.blank()

		c.openPage(page)

		redirected, err := listener.Wait(ctx, WaitTimeout)
		if err == nil {
			code, returnedState = redirected.Code, redirected.State
			// `if [[ -n "$code" ]]; then from_listener=1; fi`
			// (lib/auth.sh:683). A wait that ended on a decoy request leaves
			// an empty code, and that is the paste path rather than a redirect.
			if code != "" {
				fromListener = true
			}
		} else {
			c.IO.Warn(
				fmt.Sprintf("nothing arrived on localhost:%d within five minutes", port),
				"Falling back to pasting the code by hand. If the browser is showing a page that will not load, the address bar still has what is needed.")
		}
	} else {
		c.openPage(page)
	}

	// Paste fallback. Reached when there is no free port, or the listener timed
	// out. It is the floor, not the plan.
	if code == "" {
		c.IO.Section("Step 1 of 2: Paste the registration code")
		c.IO.Line("GitHub redirected your browser to a localhost address that will not load.")
		c.IO.Line("Copy the full URL from the address bar and paste it below.")
		c.blank()

		pasted, err := c.IO.Prompt("URL or code")
		if err != nil {
			var refusal *ui.FatalError
			if errors.As(err, &refusal) {
				// _ui_no_input already died with its own message, and the `||`
				// after ui_prompt never fires (lib/ui.sh:159).
				return err
			}
			return c.IO.Die(
				"no code was pasted",
				"Re-run: crossrev auth login --owner "+owner)
		}
		if strings.Contains(pasted, "code=") {
			code = pastedQueryValue(pasted, "code")
			returnedState = pastedQueryValue(pasted, "state")
		} else {
			code = pasted
		}
	}

	if code == "" {
		return c.IO.Die(
			"no code found",
			"Paste the full URL from the address bar, or just the value after code=")
	}
	// The two paths make different claims, so they are checked differently
	// (lib/auth.sh:715-736).
	//
	// A request on the listener is a redirect. The listener binds loopback
	// only, so any request to it came from a process on this machine, and
	// separating that from the page CrossRev opened is the whole job of the
	// state. A request without one has not made the claim, so it is refused.
	//
	// The paste is a person reading their own address bar, and the prompt
	// above documents the bare code as an answer. An absent state there is
	// that documented fallback rather than a forgery.
	//
	// Either way, a state that came back wrong is refused.
	stateOK := true
	if fromListener {
		stateOK = returnedState == state
	} else if returnedState != "" && returnedState != state {
		stateOK = false
	}
	if !stateOK {
		// The lowercase `crossrev` is the shell's own byte (lib/auth.sh:735).
		return c.IO.Die(
			"the state value GitHub returned does not match the one CrossRev sent",
			"This request did not come from the page crossrev opened. Start again: crossrev auth login --owner "+owner)
	}

	registration, err := c.GH.ConvertManifest(ctx, code)
	if err != nil {
		return c.IO.Die(
			"GitHub rejected the code",
			"Codes expire one hour after the App is created, and each works once. Re-run: crossrev auth login --owner "+owner)
	}
	if registration.ID == 0 || registration.PEM == "" {
		return c.IO.Die(
			"GitHub's response did not contain an App id and private key",
			"Nothing was stored. Check for a half-created App before retrying")
	}

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("could not create %s: %w", dir, err)
	}
	// chmod after mkdir -p, because mkdir applies the umask and an existing
	// directory keeps whatever mode it already had (lib/auth.sh:701).
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("could not set the mode of %s: %w", dir, err)
	}

	// New registrations always take the roled path, never the legacy one, so
	// the unroled name dies out on its own.
	pemPath := dir + "/" + owner + "." + role + ".pem"
	// 0600 at creation rather than created and then chmodded, so the key is
	// never briefly readable — which is what the shell's umask subshell buys.
	if err := os.WriteFile(pemPath, []byte(registration.PEM), 0o600); err != nil {
		return fmt.Errorf("could not write the private key to %s: %w", pemPath, err)
	}

	metaBytes, err := loginMetadata(owner, account.Type, account.ID,
		registration.ID, registration.Slug, registration.Name, role,
		c.now().UTC().Format(stampLayout))
	if err != nil {
		return err
	}
	if err := os.WriteFile(dir+"/"+owner+"."+role+".json", metaBytes, 0o600); err != nil {
		return fmt.Errorf("could not write the App metadata: %w", err)
	}

	// The shell compares the raw `stat` output here rather than the padded
	// form `auth status` prints, so "600" is the string that matches
	// (lib/auth.sh:719-727).
	storedMode := strings.TrimPrefix(fileMode(pemPath), "0")

	c.IO.Section("Registered")
	c.IO.OK(fmt.Sprintf("App    %s (id %d)", registration.Name, registration.ID))
	if storedMode == "600" {
		c.IO.OK("Key    " + pemPath + " (0600)")
	} else {
		c.IO.No("Key    " + pemPath + " — expected mode 0600, found " + storedMode)
	}

	return c.installFlow(ctx, installFlowRequest{
		Owner:      owner,
		OwnerType:  account.Type,
		OwnerID:    account.ID,
		Slug:       registration.Slug,
		AppID:      registration.ID,
		PEM:        pemPath,
		StepPrefix: "Step 2 of 2: ",
	})
}

// openPage hands the local page to a browser, warning rather than failing when
// none can be started (lib/auth.sh:640-642 and :653-655).
func (c *Commands) openPage(path string) {
	url := "file://" + path
	if err := c.Browser.Open(context.Background(), url); err != nil {
		c.IO.Warn(
			"could not open a browser automatically",
			"Open this to continue: "+url)
	}
}

// writeLoginPage puts the page somewhere a browser can open it, which is
// `mktemp "$tmpdir/crossrev-manifest.XXXXXX"` renamed to end in `.html`
// (lib/auth.sh:626-640). os.CreateTemp puts the suffix on for us, so there is
// no rename here and no counterpart to the shell's guard on it.
//
// It is created 0600. The shell's mktemp does the same, and it matters: the
// page holds the state value, and anything that can read it can forge the
// redirect CrossRev is waiting for.
func (c *Commands) writeLoginPage(body string) (string, error) {
	file, err := os.CreateTemp("", "crossrev-manifest-*.html")
	if err != nil {
		// The shell trims one trailing slash off $TMPDIR before naming it
		// (`${tmpdir%/}`, lib/auth.sh:626), so the action line reads the same
		// either way the reader set it.
		dir := strings.TrimSuffix(os.TempDir(), "/")
		return "", c.IO.Die(
			"could not create a temporary file for the registration page",
			"Check that the temporary directory "+dir+" is writable.")
	}
	defer file.Close()
	if _, err := io.WriteString(file, body); err != nil {
		os.Remove(file.Name())
		return "", fmt.Errorf("could not write the registration page: %w", err)
	}
	return file.Name(), nil
}

// requireInput is `_ui_input_source >/dev/null || _ui_no_input`
// (lib/auth.sh:513): it resolves the answer source without asking anything, so
// a flow that cannot ask is stopped before it opens a browser.
//
// The two sentences are internal/ui/input.go:105-108's, repeated because
// _ui_no_input is what the shell calls here and ui.IO offers no exported way to
// resolve the source without a question.
func (c *Commands) requireInput() error {
	if c.IO == nil || c.IO.Input == nil {
		return c.noInput()
	}
	source, err := c.IO.Input.Open()
	if err != nil {
		return c.noInput()
	}
	return source.Close()
}

func (c *Commands) noInput() error {
	return c.IO.Die(
		"CrossRev needs to ask you something, but no terminal is attached",
		"Run this in a terminal directly. Editor-embedded and captured shells often have no controlling terminal, which is what this is.")
}

// listen binds the loopback socket, through the injected function when there is
// one.
func (c *Commands) listen() (*Listener, error) {
	if c.Listen != nil {
		return c.Listen()
	}
	return Listen()
}

// stateValue is `openssl rand -hex 16` (lib/auth.sh:548): sixteen random bytes
// as thirty-two hex characters.
//
// It is what ties the redirect to the page CrossRev opened, so the bytes come
// from a cryptographic source and an error stops the flow rather than falling
// back to a weaker one.
func (c *Commands) stateValue() (string, error) {
	source := c.Random
	if source == nil {
		source = rand.Reader
	}
	buf := make([]byte, 16)
	if _, err := io.ReadFull(source, buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// pastedQueryValue is the sed the paste fallback runs:
// `s/.*[?&]NAME=\([^&]*\).*/\1/p` (lib/auth.sh:668-669).
//
// It is one character different from the listener's, and the difference is
// deliberate on the shell's part: a pasted line has no ` HTTP/1.1` on the end
// to stop at, so the value runs to the next & or to the end. A code with a
// space in it therefore survives here and would be truncated there.
func pastedQueryValue(line, name string) string {
	needle := name + "="
	value := ""
	for i := 1; i+len(needle) <= len(line); i++ {
		if line[i:i+len(needle)] != needle {
			continue
		}
		if line[i-1] != '?' && line[i-1] != '&' {
			continue
		}
		start := i + len(needle)
		end := start
		for end < len(line) && line[end] != '&' {
			end++
		}
		value = line[start:end]
	}
	return value
}

// --- the manifest, and the page that posts it -------------------------------

// LoginManifest is the App manifest GitHub creates the App from
// (lib/auth.sh:562-575).
//
// The keys are in jq's order because they are written in it, and the whole
// thing is one line because `jq -cn` is compact — it goes into an HTML
// attribute, where a newline would end nothing but would still be there.
//
// `public: false` and a disabled webhook are the two facts the panel above
// promises. GitHub never calls CrossRev; the workflows do.
func LoginManifest(name, redirect string, permissions []byte) []byte {
	manifest := &object{values: map[string]json.RawMessage{}}
	manifest.set("name", mustEncodeString(name))
	manifest.set("url", mustEncodeString("https://github.com/carlosboeing/crossrev"))
	manifest.set("redirect_url", mustEncodeString(redirect))
	manifest.set("public", json.RawMessage("false"))
	manifest.set("hook_attributes", json.RawMessage(`{"url":"https://example.com/unused","active":false}`))
	manifest.set("default_events", json.RawMessage("[]"))
	manifest.set("default_permissions", json.RawMessage(permissions))
	// jq -c ends with a newline; this is a value handed to a page, not a file.
	return []byte(strings.TrimSuffix(string(manifest.compact()), "\n"))
}

// LoginPage is the local page the browser is sent to (the heredoc at
// lib/auth.sh:615-629), byte for byte, trailing newline included.
//
// It exists because GitHub's App-manifest flow takes a POST and a browser
// cannot be pointed at one. The form submits itself; the button is for when
// scripting is off.
//
// Every attribute is double-quoted, which is why HTMLAttrEscape does not escape
// the apostrophe: it cannot end one of these.
//
// The lowercase `crossrev` in the title is the shell's own byte, copied rather
// than restyled.
func LoginPage(appName, postURL, manifest, state string) string {
	return "<!doctype html>\n" +
		"<meta charset=\"utf-8\">\n" +
		"<title>crossrev</title>\n" +
		"<body style=\"font:16px system-ui;margin:4rem auto;max-width:34rem\">\n" +
		"<p>Sending you to GitHub to register <strong>" + HTMLAttrEscape(appName) + "</strong>&hellip;</p>\n" +
		"<p>If nothing happens, press the button.</p>\n" +
		"<form id=\"f\" action=\"" + HTMLAttrEscape(postURL) + "\" method=\"post\">\n" +
		"  <input type=\"hidden\" name=\"manifest\" value=\"" + HTMLAttrEscape(manifest) + "\">\n" +
		"  <input type=\"hidden\" name=\"state\" value=\"" + HTMLAttrEscape(state) + "\">\n" +
		"  <button type=\"submit\">Continue to GitHub</button>\n" +
		"</form>\n" +
		"<script>document.getElementById('f').submit()</script>\n" +
		"</body>\n"
}

// loginMetadata is the file `auth login` writes beside the key
// (lib/auth.sh:708-714): jq's pretty format, keys in the order they are named.
//
// owner_id and id are numbers, because the shell passes both with `--argjson`.
// An owner id that is not a number is refused here; jq would error and the
// redirect would leave nothing on disk either way.
func loginMetadata(owner, ownerType, ownerID string, id int64, slug, name, role, created string) ([]byte, error) {
	numericOwnerID, err := strconv.ParseInt(ownerID, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("GitHub answered a non-numeric id for the account %s: %q", owner, ownerID)
	}
	meta := &object{values: map[string]json.RawMessage{}}
	meta.set("owner", mustEncodeString(owner))
	meta.set("owner_type", mustEncodeString(ownerType))
	meta.set("owner_id", json.RawMessage(strconv.FormatInt(numericOwnerID, 10)))
	meta.set("id", json.RawMessage(strconv.FormatInt(id, 10)))
	meta.set("slug", mustEncodeString(slug))
	meta.set("name", mustEncodeString(name))
	meta.set("role", mustEncodeString(role))
	meta.set("created", mustEncodeString(created))
	return meta.indented(), nil
}

// --- exchanging the code for the App ----------------------------------------

// Registration is what GitHub answers when a manifest is converted
// (lib/auth.sh:691-694).
type Registration struct {
	// ID is the App's numeric id, zero for a response carrying none.
	ID int64
	// Slug is what the App is installed as, and what marker trust matches.
	Slug string
	// Name is what GitHub called it, which is not always what was asked for.
	Name string
	// PEM is the private key, in full. It is written to disk and never
	// printed, logged or put in an argument.
	PEM string
}

// ConvertManifest exchanges a registration code for the App
// (lib/auth.sh:685).
//
// The code works once and expires an hour after the App is created, so a failed
// exchange is not retried here — the caller says so and stops.
//
// Nothing about the response reaches the error. The body carries the private
// key, and an error string reaches a terminal and a run log.
func (g *GH) ConvertManifest(ctx context.Context, code string) (Registration, error) {
	const summary = "could not exchange the registration code"

	res := g.runner.Run(ctx, exec.Spec{
		Path: program,
		Args: []string{"api", "--method", "POST", "app-manifests/" + code + "/conversions"},
		Env:  g.env,
		// `2>&1` at lib/auth.sh:685: the shell captures both streams into the
		// value it parses, so a warning on stderr is part of what jq reads.
		Streams: exec.StreamsCombined,
	})
	if !answered(res) {
		return Registration{}, ghFailure(summary, res)
	}

	var answer struct {
		ID   int64  `json:"id"`
		Slug string `json:"slug"`
		Name string `json:"name"`
		PEM  string `json:"pem"`
	}
	if err := json.Unmarshal(res.Stdout, &answer); err != nil {
		// The parse error would quote the body, which holds the key.
		return Registration{}, fmt.Errorf("%s: GitHub's answer is not the JSON object this expects", summary)
	}
	return Registration(answer), nil
}
