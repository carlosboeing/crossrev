package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/carlosboeing/crossrev/internal/exec"
	"github.com/carlosboeing/crossrev/internal/forge/ghexec"
)

// RoleDefaultName is the App name CrossRev proposes for an owner
// (_auth_role_default_name, lib/auth.sh:97).
//
// The name is display text and takes the product name (ADR 0010): it is what a
// person reads in an organisation's installed Apps list, beside `Claude` and
// `Vercel`.
//
// The spaces are safe, and that is load-bearing rather than incidental. GitHub
// derives the slug by lowercasing and turning spaces into hyphens, so this
// spelling yields the slug the lowercase form did — and the slug is what the
// trusted-author check matches literally.
//
// The owner half keeps its own casing. That is an identity GitHub chose, not
// prose CrossRev gets to restyle.
//
// An unknown role matches no case arm and prints nothing, which is what the
// shell does rather than failing.
func RoleDefaultName(role, owner string) string {
	switch role {
	case RoleLoop:
		return "CrossRev " + owner
	case RoleRefresher:
		return "CrossRev Refresh " + owner
	}
	return ""
}

// RoleSummary is what a role is allowed to do, as `auth status` prints it
// (_auth_role_summary, lib/auth.sh:64).
func RoleSummary(role string) string {
	switch role {
	case RoleLoop:
		return "contents:write, issues:write, pull_requests:write"
	case RoleRefresher:
		return "secrets:write (repository secrets only)"
	}
	return ""
}

// RoleKeySecret is which repository secret carries a role's private key
// (_auth_role_key_secret, lib/auth.sh:77).
//
// Named per role rather than assumed, because the two are not interchangeable
// and the consequence of confusing them is not a broken deploy — it is the
// refresher's key material sitting behind the loop App's identity, which is the
// exact privilege separation the two Apps exist to draw.
func RoleKeySecret(role string) string {
	switch role {
	case RoleLoop:
		return "APP_PRIVATE_KEY"
	case RoleRefresher:
		return "CROSSREV_REFRESH_APP_PRIVATE_KEY"
	}
	return ""
}

// Slug derives an App's slug from its name the way GitHub does: lowercase,
// spaces to hyphens (_auth_slug, lib/auth.sh:105).
//
// The lowercasing is ASCII-only, and that is a decision rather than a
// shortcut. `tr '[:upper:]' '[:lower:]'` answers differently in different
// locales — measured, it lowercases U+00DC under en_AU.UTF-8 and leaves it
// alone under LC_ALL=C, which is what a runner has. The C answer is the one
// that does not move between a laptop and a runner, and GitHub's App names are
// ASCII anyway. strings.ToLower would take the other side and diverge on a
// runner.
//
// Only U+0020 becomes a hyphen. `tr ' '` names one character, so a tab stays a
// tab.
func Slug(name string) string {
	var b strings.Builder
	b.Grow(len(name))
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'A' && c <= 'Z':
			b.WriteByte(c + ('a' - 'A'))
		case c == ' ':
			b.WriteByte('-')
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// Metadata is the file `auth login` writes beside an App's private key
// (lib/auth.sh:708-714), in the order jq wrote its keys.
//
// It is a struct rather than a map so the field order is the file's order, and
// so a caller reading .Slug is reading the field the trusted-author check falls
// back to rather than a string keyed by a name it spelled itself.
type Metadata struct {
	Owner     string `json:"owner"`
	OwnerType string `json:"owner_type"`
	OwnerID   int64  `json:"owner_id"`
	ID        int64  `json:"id"`
	Slug      string `json:"slug"`
	Name      string `json:"name"`
	Role      string `json:"role"`
	Created   string `json:"created"`
}

// ReadMetadata reads one App's cached identity.
//
// An absent role is the loop's: anything registered before roles existed has no
// role key, and `auth status` reads it as `.role // "loop"` (lib/auth.sh:384).
func ReadMetadata(path string) (Metadata, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Metadata{}, fmt.Errorf("could not read the App metadata: %w", err)
	}
	var meta Metadata
	if err := json.Unmarshal(data, &meta); err != nil {
		return Metadata{}, fmt.Errorf("could not read the App metadata at %s: %w", path, err)
	}
	if meta.Role == "" {
		meta.Role = RoleLoop
	}
	return meta, nil
}

// Drift is one field of the cached identity that no longer matches the one
// GitHub has.
//
// The shell prints these as tab-separated text and the caller splits it, with
// tabs rather than spaces because an App name is free text and routinely
// contains spaces — CrossRev proposes one that does. Three fields make the
// split unnecessary rather than careful.
type Drift struct {
	Field string
	Was   string
	Now   string
}

// SyncMeta reconciles the cached identity at path against the authoritative
// one, correcting it (_auth_sync_meta, lib/auth.sh:207).
//
// It returns one entry per field that moved, and nothing at all when the two
// agree — in which case the file is not touched.
//
// It writes, which a status command otherwise does not. The justification is
// that this file is CrossRev's own cache of a fact GitHub owns, not operator
// config, and the cached slug is what the trusted-author check falls back to.
// Diagnosing that drift and then leaving it in place would report a fault it
// had already found and could have fixed, and the only repair left would be
// editing JSON by hand.
//
// The rewrite goes to a sibling and is renamed over the original, created 0600
// rather than created and then chmodded, so nothing ever reads a half-written
// file or one briefly wider than the 0600 the original was created with.
func SyncMeta(path, name, slug string) ([]Drift, error) {
	// The shell reads the two fields with jq and discards its diagnostics, so
	// an unreadable or unparseable file yields two empty strings rather than a
	// refusal here. What refuses is the write below, which cannot rewrite what
	// it could not read.
	obj, readErr := readObject(path)
	was := func(field string) string {
		if readErr != nil {
			return ""
		}
		return obj.stringValue(field)
	}

	var drift []Drift
	if wasName := was("name"); wasName != name {
		drift = append(drift, Drift{Field: "name", Was: wasName, Now: name})
	}
	if wasSlug := was("slug"); wasSlug != slug {
		drift = append(drift, Drift{Field: "slug", Was: wasSlug, Now: slug})
	}
	if len(drift) == 0 {
		return nil, nil
	}
	if readErr != nil {
		return nil, fmt.Errorf("could not correct the cached App identity at %s: %w", path, readErr)
	}

	obj.set("name", mustEncodeString(name))
	obj.set("slug", mustEncodeString(slug))
	if err := writeFile0600(path, obj.indented()); err != nil {
		return nil, fmt.Errorf("could not correct the cached App identity at %s: %w", path, err)
	}
	return drift, nil
}

// writeFile0600 replaces path with data, through a sibling named the way the
// shell names it.
//
// The temporary file is removed when the write fails. The shell leaves it,
// which is a wart rather than a contract: nothing reads it and the next attempt
// truncates it.
func writeFile0600(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// --- an object that keeps its key order ------------------------------------
//
// jq rewrites the two fields it was given and copies every other key through in
// the order it found them. A Go map does neither, and a struct drops a key this
// version has not heard of — an operator's, or a later version's. This is the
// smallest thing that behaves the way the file's existing readers already do.

type object struct {
	keys   []string
	values map[string]json.RawMessage
}

// readObject parses a JSON object from a file, keeping key order.
func readObject(path string) (*object, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return decodeObject(data)
}

func decodeObject(data []byte) (*object, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if delim, ok := tok.(json.Delim); !ok || delim != '{' {
		return nil, fmt.Errorf("expected a JSON object")
	}
	obj := &object{values: make(map[string]json.RawMessage)}
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, ok := keyTok.(string)
		if !ok {
			return nil, fmt.Errorf("expected a JSON object key")
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, err
		}
		obj.set(key, raw)
	}
	if _, err := dec.Token(); err != nil {
		return nil, err
	}
	return obj, nil
}

// set replaces a key's value in place, or appends it when the key is new. That
// is `.k = v` at the shell: measured, an existing key keeps its position and a
// new one lands at the end.
func (o *object) set(key string, raw json.RawMessage) {
	if _, held := o.values[key]; !held {
		o.keys = append(o.keys, key)
	}
	o.values[key] = raw
}

func (o *object) get(key string) (json.RawMessage, bool) {
	raw, held := o.values[key]
	return raw, held
}

// stringValue is `jq -r '.k // empty'` for a string field: the value when it is
// a string, and empty for anything else — absent, null, or another type.
func (o *object) stringValue(key string) string {
	raw, held := o.get(key)
	if !held {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return ""
	}
	return s
}

// compact is the object as `jq -c` writes it.
func (o *object) compact() []byte {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, key := range o.keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		buf.Write(mustEncodeString(key))
		buf.WriteByte(':')
		// The value is re-compacted rather than copied, so a file somebody
		// pretty-printed by hand comes back out the way jq would write it.
		var value bytes.Buffer
		if err := json.Compact(&value, o.values[key]); err != nil {
			value.Write(o.values[key])
		}
		buf.Write(value.Bytes())
	}
	buf.WriteByte('}')
	buf.WriteByte('\n')
	return buf.Bytes()
}

// indented is the object as jq writes it without -c: two spaces of indent, a
// space after each colon, and a trailing newline. Verified byte for byte
// against jq, nested values and empty containers included.
func (o *object) indented() []byte {
	compact := o.compact()
	var buf bytes.Buffer
	if err := json.Indent(&buf, bytes.TrimRight(compact, "\n"), "", "  "); err != nil {
		return compact
	}
	buf.WriteByte('\n')
	return buf.Bytes()
}

// mustEncodeString is one JSON string, escaped the way jq escapes it.
//
// SetEscapeHTML(false) is the whole point: encoding/json escapes <, > and & by
// default and jq does not, so an App name carrying one would come back changed.
func mustEncodeString(s string) json.RawMessage {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(s); err != nil {
		// encoding a string cannot fail; a bare quoted empty string is the
		// fail-closed answer if it ever does.
		return json.RawMessage(`""`)
	}
	return json.RawMessage(bytes.TrimRight(buf.Bytes(), "\n"))
}

// --- the `gh` calls the identity is read through ---------------------------

// program is the CLI these reads drive. A bare name, resolved on the PATH of
// the calling process, which is what the offline suite relies on: it puts
// tests/stub/gh earlier on the PATH.
const program = "gh"

// The environment `gh` is allowed to inherit is ghexec.EnvironmentNames, and
// this package reads it rather than keeping a list of its own. A second copy
// drifts: a name added to either one changed only that side's environment, and
// every test in both packages stayed green.
//
// The reasoning for each name is documented once, at that list. The short form
// is that every one is either documented by `gh help environment` or read by
// the Go runtime `gh` is built on, and that GH_REPO and GH_FORCE_TTY are left
// out on purpose.
//
// This is the orchestrator side of the ADR 0001 boundary, so the four
// credential names are on the list rather than stripped from it: the child is
// `gh`, not a model, and `gh` cannot authenticate without one. Nothing here
// reads attacker-controlled text — every argument is built in this package.

// GH reads GitHub through the `gh` CLI, for the facts an App's identity is
// checked against.
type GH struct {
	runner exec.Runner
	env    []string
}

// GHOption adjusts a GH at construction.
type GHOption func(*GH)

// WithEnv replaces the environment `gh` receives. The default is
// ghexec.EnvironmentNames, read from this process.
func WithEnv(env []string) GHOption {
	return func(g *GH) { g.env = env }
}

// NewGH returns a GH that runs `gh` through runner.
//
// A nil runner panics. Inventing one would start a child that may hold a forge
// credential, which is a wiring bug and not a default. A real child is started
// through exec.NewOrchestratorRunner, because these calls are the orchestrator's
// and the credential is what `gh` authenticates with; a test injects a fake.
func NewGH(runner exec.Runner, opts ...GHOption) *GH {
	if runner == nil {
		panic("app.NewGH: runner is nil")
	}
	g := &GH{runner: runner, env: exec.Inherit(ghexec.EnvironmentNames())}
	for _, opt := range opts {
		opt(g)
	}
	return g
}

// run invokes `gh` with exactly these arguments.
func (g *GH) run(ctx context.Context, args ...string) exec.Result {
	return g.runner.Run(ctx, exec.Spec{Path: program, Args: args, Env: g.env})
}

// answered reports that gh ran and exited zero.
//
// The two halves are one question: a non-zero exit is how gh reports a refused
// API call, and Result.Err is how the runner reports that no child produced a
// status at all. The shell cannot tell them apart either — `2>/dev/null ||
// return 1` fires for both.
func answered(res exec.Result) bool { return res.Err == nil && res.ExitCode == 0 }

// output is gh's stdout with the trailing newlines command substitution would
// have stripped.
func output(res exec.Result) string {
	return strings.TrimRight(string(res.Stdout), "\n")
}

// ghFailure turns a refused invocation into an error carrying summary.
//
// Neither the arguments nor the captured streams are in it, and here that is a
// requirement rather than a convention: one of these calls carries the App's
// JWT in an argument, and whoever holds one can act as the App until it
// expires. An error string reaches a terminal and a run log.
func ghFailure(summary string, res exec.Result) error {
	if res.Err != nil {
		return fmt.Errorf("%s: %w", summary, res.Err)
	}
	return fmt.Errorf("%s: gh exited %d", summary, res.ExitCode)
}

// DetectOwner is the owner of the repository the working directory belongs to
// (_auth_detect_owner, lib/auth.sh:111).
//
// The owner is detected, not asked, because the repository's owner is the trust
// boundary the private key should sit on.
//
// An empty answer is not refused here, because the shell does not refuse one:
// it checks gh's exit status alone, and its callers fail on the empty value.
func (g *GH) DetectOwner(ctx context.Context) (string, error) {
	res := g.run(ctx, "repo", "view", "--json", "owner", "--jq", ".owner.login")
	if !answered(res) {
		return "", ghFailure("could not work out which owner this repository belongs to", res)
	}
	return output(res), nil
}

// Account is what GitHub says an account is: a user, an organisation or a bot,
// and the numeric id that prefills the install page with the right target.
//
// The id stays a string. It is read as text, printed as text into an install
// URL, and handed back to GitHub as text.
type Account struct {
	Type string
	ID   string
}

// AccountInfo resolves an account by login (_auth_account_info,
// lib/auth.sh:118).
//
// /users/ resolves all three kinds. The `\(…)` interpolation is jq's, so a
// response missing either half prints nothing at all rather than half an
// answer: `"\(empty)"` produces no output. That is why an empty line is a
// failure here and not an account with a missing field.
func (g *GH) AccountInfo(ctx context.Context, login string) (Account, error) {
	summary := "could not resolve the account " + login

	res := g.run(ctx, "api", "users/"+login, "--jq", `"\(.type // empty) \(.id // empty)"`)
	if !answered(res) {
		return Account{}, ghFailure(summary, res)
	}
	info := output(res)
	// `[[ "$info" != " " && -n "$info" ]]`: a single space is both fields
	// present and empty, which jq can produce and which answers nothing.
	if info == "" || info == " " {
		return Account{}, fmt.Errorf("%s: GitHub answered with neither a type nor an id", summary)
	}
	accountType, id, _ := strings.Cut(info, " ")
	return Account{Type: accountType, ID: id}, nil
}

// Identity is what an App calls itself, as GitHub has it.
type Identity struct {
	Name string
	Slug string
}

// AppIdentity reads the authoritative identity with the App's own JWT
// (_auth_app_identity, lib/auth.sh:183).
//
// Authoritative, and reachable with the key already on disk. A reachable API
// that answered with neither field is not evidence of anything, and an empty
// slug written back would be worse than the stale one it replaced.
func (g *GH) AppIdentity(ctx context.Context, jwt string) (Identity, error) {
	const summary = "could not read the App's identity from GitHub"

	res := g.run(ctx, "api", "-H", "Authorization: Bearer "+jwt, "/app", "--jq", ".name, .slug")
	if !answered(res) {
		return Identity{}, ghFailure(summary, res)
	}
	// `{ read -r name; read -r slug; }`: the first line and the second, and
	// nothing if there is no second line.
	lines := strings.Split(strings.TrimRight(string(res.Stdout), "\n"), "\n")
	var name, slug string
	if len(lines) > 0 {
		name = lines[0]
	}
	if len(lines) > 1 {
		slug = lines[1]
	}
	if name == "" || slug == "" {
		return Identity{}, fmt.Errorf("%s: GitHub answered with no name or no slug", summary)
	}
	return Identity{Name: name, Slug: slug}, nil
}

// Sync reads the authoritative identity and corrects the cache at metaPath
// against it, returning one entry per field that moved.
//
// It is the pair `auth status` performs before it prints a line
// (lib/auth.sh:398-403). An unreachable API returns an error and leaves the
// cache alone: it is not evidence the cached identity is wrong.
func (g *GH) Sync(ctx context.Context, metaPath, jwt string) ([]Drift, error) {
	identity, err := g.AppIdentity(ctx, jwt)
	if err != nil {
		return nil, err
	}
	return SyncMeta(metaPath, identity.Name, identity.Slug)
}

// --- the ledger of long-lived tokens ---------------------------------------
//
// `claude setup-token` prints a token valid for a year and says plainly that
// you will not see it again. So there is nothing to inspect eleven months later
// and no way to recover it — the first sign of expiry is a CI failure on a day
// nobody is looking. Recording the creation date at the moment it is set is the
// only point at which the information exists.
//
// The ledger holds dates, never tokens.

// stampLayout is the timestamp the ledger records and reads back, which is what
// `date -u +%Y-%m-%dT%H:%M:%SZ` prints (lib/auth.sh:338).
//
// Reading it back is BSD `date -j -f`, which takes this layout and no other,
// with GNU `date -d` as the fallback. time.Parse is the strict reading: an
// offset in place of the Z is refused rather than guessed at.
const stampLayout = "2006-01-02T15:04:05Z"

// TokenRecord writes down that a token was set, and how long it is good for
// (auth_token_record, lib/auth.sh:332).
//
// now is injected rather than read, so what the ledger says is a fact a caller
// chose and a test can assert.
func TokenRecord(env Environment, repo, name string, days int, now time.Time) error {
	path := TokensPath(env)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("could not create the token ledger's directory: %w", err)
	}

	// `existing="$(cat "$file" 2>/dev/null)"; [[ -n "$existing" ]] || existing='{}'`:
	// a missing or empty ledger is an empty object, and anything else jq
	// cannot read refuses below rather than being overwritten.
	data, readErr := os.ReadFile(path)
	if readErr != nil || len(bytes.TrimSpace(data)) == 0 {
		data = []byte("{}")
	}
	ledger, err := decodeObject(data)
	if err != nil {
		return fmt.Errorf("could not read the token ledger at %s: %w", path, err)
	}

	// `.[$r] = ((.[$r] // {}) | .[$n] = {...})`: a repository that is absent or
	// null starts empty, and one holding anything but an object refuses.
	secrets := &object{values: make(map[string]json.RawMessage)}
	if raw, held := ledger.get(repo); held && !isJSONNull(raw) {
		secrets, err = decodeObject(raw)
		if err != nil {
			return fmt.Errorf("could not read the token ledger at %s: %w", path, err)
		}
	}

	entry := &object{values: make(map[string]json.RawMessage)}
	entry.set("created", mustEncodeString(now.UTC().Format(stampLayout)))
	entry.set("valid_days", json.RawMessage(strconv.Itoa(days)))
	secrets.set(name, bytes.TrimRight(entry.compact(), "\n"))
	ledger.set(repo, bytes.TrimRight(secrets.compact(), "\n"))

	if err := writeFile0600(path, ledger.compact()); err != nil {
		return fmt.Errorf("could not write the token ledger at %s: %w", path, err)
	}
	return nil
}

// TokenDaysLeft is how many days a recorded token has before it expires
// (auth_token_days_left, lib/auth.sh:344).
//
// It fails when nothing was recorded for that repository and secret, which is
// what its caller treats as "no date to report" rather than as an error.
func TokenDaysLeft(env Environment, repo, name string, now time.Time) (int, error) {
	path := TokensPath(env)
	summary := fmt.Sprintf("no recorded date for %s in %s", name, repo)

	data, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", summary, err)
	}
	ledger, err := decodeObject(data)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", summary, err)
	}
	rawRepo, held := ledger.get(repo)
	if !held || isJSONNull(rawRepo) {
		return 0, fmt.Errorf("%s", summary)
	}
	secrets, err := decodeObject(rawRepo)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", summary, err)
	}
	rawEntry, held := secrets.get(name)
	if !held || isJSONNull(rawEntry) {
		return 0, fmt.Errorf("%s", summary)
	}
	entry, err := decodeObject(rawEntry)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", summary, err)
	}

	start, err := time.Parse(stampLayout, entry.stringValue("created"))
	if err != nil {
		return 0, fmt.Errorf("the recorded date for %s in %s cannot be read: %w", name, repo, err)
	}

	// `$(( days - ... ))`: an absent or null validity is the bare word `null`,
	// which bash arithmetic reads as an unset variable and therefore as zero. A
	// fractional one is a syntax error, so it refuses here too.
	days := 0
	if raw, held := entry.get("valid_days"); held && !isJSONNull(raw) {
		days, err = strconv.Atoi(string(bytes.TrimSpace(raw)))
		if err != nil {
			return 0, fmt.Errorf("the recorded validity for %s in %s is not a whole number of days", name, repo)
		}
	}

	// Integer division truncates, so a partial day counts for nothing until it
	// completes.
	return days - int(now.UTC().Sub(start)/(24*time.Hour)), nil
}

// isJSONNull reports that a raw value is JSON null, which `//` treats as
// absent.
func isJSONNull(raw json.RawMessage) bool {
	return string(bytes.TrimSpace(raw)) == "null"
}
