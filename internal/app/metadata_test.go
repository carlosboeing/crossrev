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
)

// --- the name CrossRev proposes, and the slug it implies --------------------
//
// A GitHub App's name is display text and takes the product name (ADR 0010).
// The slug GitHub derives from it is matched literally by the trusted-author
// check, so the two pull in opposite directions: a change to the name that
// shifted the slug would break marker trust on every existing pull request,
// silently. tests/test-auth.sh:44-72 asserts the same pairs against the shell.

func TestRoleDefaultName(t *testing.T) {
	for _, tc := range []struct{ role, owner, want string }{
		{app.RoleLoop, "acme", "CrossRev acme"},
		{app.RoleRefresher, "acme", "CrossRev Refresh acme"},
		// The owner half keeps its own casing: that is an identity GitHub
		// chose, not prose CrossRev gets to restyle.
		{app.RoleLoop, "ShoreLogic", "CrossRev ShoreLogic"},
		// An unknown role matches no case arm, so the shell prints nothing and
		// exits zero. Nothing is invented here either.
		{"bogus", "acme", ""},
	} {
		if got := app.RoleDefaultName(tc.role, tc.owner); got != tc.want {
			t.Errorf("RoleDefaultName(%q, %q) = %q, want %q", tc.role, tc.owner, got, tc.want)
		}
	}
}

func TestSlug(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"CrossRev acme", "crossrev-acme"},
		{"CrossRev Refresh acme", "crossrev-refresh-acme"},
		{"My Custom App", "my-custom-app"},
		{"CrossRev ShoreLogic", "crossrev-shorelogic"},
		// One hyphen per space, so a doubled space doubles.
		{"A_B  C", "a_b--c"},
		// `tr ' '` names one character. A tab is not it.
		{"tab\tsep", "tab\tsep"},
		{"", ""},
	} {
		if got := app.Slug(tc.in); got != tc.want {
			t.Errorf("Slug(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// The assertion the whole pairing exists for: the trusted author the loop reads
// markers from is "<slug>[bot]".
func TestSlugOfTheProposedNameIsTheInstalledSlug(t *testing.T) {
	if got, want := app.Slug(app.RoleDefaultName(app.RoleLoop, "ShoreLogic")), "crossrev-shorelogic"; got != want {
		t.Fatalf("slug = %q, want %q", got, want)
	}
	if got, want := app.Slug(app.RoleDefaultName(app.RoleLoop, "acme"))+"[bot]", "crossrev-acme[bot]"; got != want {
		t.Fatalf("trusted author = %q, want %q", got, want)
	}
}

// `tr '[:upper:]' '[:lower:]'` answers differently in different locales: under
// en_AU.UTF-8 it lowercases U+00DC, and under LC_ALL=C — which is what a runner
// has — it leaves it alone. Both were measured. The port takes the C answer,
// because it is the one that does not move, and because GitHub's App names are
// ASCII anyway.
func TestSlugLowercasesASCIIOnly(t *testing.T) {
	if got, want := app.Slug("ÜÄ"), "ÜÄ"; got != want {
		t.Fatalf("Slug = %q, want %q", got, want)
	}
}

func TestRoleSummary(t *testing.T) {
	for _, tc := range []struct{ role, want string }{
		{app.RoleLoop, "contents:write, issues:write, pull_requests:write"},
		{app.RoleRefresher, "secrets:write (repository secrets only)"},
		{"bogus", ""},
	} {
		if got := app.RoleSummary(tc.role); got != tc.want {
			t.Errorf("RoleSummary(%q) = %q, want %q", tc.role, got, tc.want)
		}
	}
}

// Which secret carries a role's key is named per role rather than assumed. The
// consequence of confusing them is the refresher's key material sitting behind
// the loop App's identity.
func TestRoleKeySecret(t *testing.T) {
	for _, tc := range []struct{ role, want string }{
		{app.RoleLoop, "APP_PRIVATE_KEY"},
		{app.RoleRefresher, "CROSSREV_REFRESH_APP_PRIVATE_KEY"},
		{"bogus", ""},
	} {
		if got := app.RoleKeySecret(tc.role); got != tc.want {
			t.Errorf("RoleKeySecret(%q) = %q, want %q", tc.role, got, tc.want)
		}
	}
}

// --- the file `auth login` writes ------------------------------------------

// metaFixture is the JSON shape an existing install already has on disk, as
// tests/test-auth.sh:97-107 writes it and lib/auth.sh:722-728 created it.
const metaFixture = `{
  "owner": "ShoreLogic",
  "owner_type": "Organization",
  "owner_id": 12345,
  "id": 987,
  "slug": "revloop-shorelogic",
  "name": "revloop-ShoreLogic",
  "role": "loop",
  "created": "2026-08-13T00:00:00Z"
}
`

// writeMeta puts content at <dir>/ShoreLogic.loop.json with the mode
// registration creates.
func writeMeta(t *testing.T, dir, content string) string {
	t.Helper()
	path := filepath.Join(dir, "ShoreLogic.loop.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("writing the fixture: %v", err)
	}
	return path
}

func TestReadMetadata(t *testing.T) {
	path := writeMeta(t, t.TempDir(), metaFixture)

	meta, err := app.ReadMetadata(path)
	if err != nil {
		t.Fatalf("ReadMetadata: %v", err)
	}
	want := app.Metadata{
		Owner:     "ShoreLogic",
		OwnerType: "Organization",
		OwnerID:   12345,
		ID:        987,
		Slug:      "revloop-shorelogic",
		Name:      "revloop-ShoreLogic",
		Role:      "loop",
		Created:   "2026-08-13T00:00:00Z",
	}
	if meta != want {
		t.Fatalf("ReadMetadata = %+v\nwant           %+v", meta, want)
	}
}

// Anything registered before roles existed has no role key, and it is the
// loop's (lib/auth.sh:396, `.role // "loop"`).
func TestReadMetadataDefaultsAnAbsentRoleToTheLoop(t *testing.T) {
	path := writeMeta(t, t.TempDir(), `{"owner":"ShoreLogic","id":987}`)

	meta, err := app.ReadMetadata(path)
	if err != nil {
		t.Fatalf("ReadMetadata: %v", err)
	}
	if meta.Role != app.RoleLoop {
		t.Fatalf("Role = %q, want %q", meta.Role, app.RoleLoop)
	}
}

func TestReadMetadataRefusesWhatIsNotThere(t *testing.T) {
	if _, err := app.ReadMetadata(filepath.Join(t.TempDir(), "absent.json")); err == nil {
		t.Fatal("ReadMetadata on a missing file returned no error")
	}
	path := writeMeta(t, t.TempDir(), "not json")
	if _, err := app.ReadMetadata(path); err == nil {
		t.Fatal("ReadMetadata on unparseable JSON returned no error")
	}
}

// --- reconciling the cache against the identity GitHub has ------------------

func TestSyncMetaReportsNoDriftAndTouchesNothing(t *testing.T) {
	path := writeMeta(t, t.TempDir(), metaFixture)

	drift, err := app.SyncMeta(path, "revloop-ShoreLogic", "revloop-shorelogic")
	if err != nil {
		t.Fatalf("SyncMeta: %v", err)
	}
	if len(drift) != 0 {
		t.Fatalf("drift = %+v, want none", drift)
	}
	if got := read(t, path); got != metaFixture {
		t.Fatalf("the file was rewritten:\n%s", got)
	}
}

// The 2026-08-13 rename, which is where this was found: revloop-ShoreLogic
// became CrossRev ShoreLogic, and the slug moved with it.
func TestSyncMetaCorrectsARenamedApp(t *testing.T) {
	path := writeMeta(t, t.TempDir(), metaFixture)

	drift, err := app.SyncMeta(path, "CrossRev ShoreLogic", "crossrev-shorelogic")
	if err != nil {
		t.Fatalf("SyncMeta: %v", err)
	}
	want := []app.Drift{
		{Field: "name", Was: "revloop-ShoreLogic", Now: "CrossRev ShoreLogic"},
		{Field: "slug", Was: "revloop-shorelogic", Now: "crossrev-shorelogic"},
	}
	if len(drift) != len(want) {
		t.Fatalf("drift = %+v\nwant   %+v", drift, want)
	}
	for i := range want {
		if drift[i] != want[i] {
			t.Fatalf("drift = %+v\nwant   %+v", drift, want)
		}
	}

	// The whole file, byte for byte as the shell's jq rewrites it: the two
	// fields corrected in place, every other key and its order untouched, two
	// spaces of indent, a space after each colon, and a trailing newline.
	const wantFile = `{
  "owner": "ShoreLogic",
  "owner_type": "Organization",
  "owner_id": 12345,
  "id": 987,
  "slug": "crossrev-shorelogic",
  "name": "CrossRev ShoreLogic",
  "role": "loop",
  "created": "2026-08-13T00:00:00Z"
}
`
	if got := read(t, path); got != wantFile {
		t.Fatalf("file =\n%s\nwant\n%s", got, wantFile)
	}
}

// The slug is the half that matters: the trusted-author check falls back to it,
// so a correction that stopped short of the slug would leave automated mode
// broken while reporting itself fixed.
func TestSyncMetaCorrectsTheSlugAlone(t *testing.T) {
	path := writeMeta(t, t.TempDir(), metaFixture)

	drift, err := app.SyncMeta(path, "revloop-ShoreLogic", "crossrev-shorelogic")
	if err != nil {
		t.Fatalf("SyncMeta: %v", err)
	}
	if len(drift) != 1 || drift[0].Field != "slug" {
		t.Fatalf("drift = %+v, want the slug alone", drift)
	}
	meta, err := app.ReadMetadata(path)
	if err != nil {
		t.Fatalf("ReadMetadata: %v", err)
	}
	if meta.Slug != "crossrev-shorelogic" {
		t.Fatalf("slug = %q, want %q", meta.Slug, "crossrev-shorelogic")
	}
	if meta.Name != "revloop-ShoreLogic" {
		t.Fatalf("name = %q, want it untouched", meta.Name)
	}
}

// A correction rewrites two fields and no others.
func TestSyncMetaLeavesEveryOtherFieldAlone(t *testing.T) {
	path := writeMeta(t, t.TempDir(), metaFixture)

	if _, err := app.SyncMeta(path, "CrossRev ShoreLogic", "crossrev-shorelogic"); err != nil {
		t.Fatalf("SyncMeta: %v", err)
	}
	meta, err := app.ReadMetadata(path)
	if err != nil {
		t.Fatalf("ReadMetadata: %v", err)
	}
	if meta.ID != 987 || meta.Owner != "ShoreLogic" || meta.OwnerType != "Organization" ||
		meta.OwnerID != 12345 || meta.Role != "loop" || meta.Created != "2026-08-13T00:00:00Z" {
		t.Fatalf("a correction moved more than the name and slug: %+v", meta)
	}
}

// A key this version does not know about is another version's, or an
// operator's. jq rewrites two fields and copies the rest, so a round trip
// through Go must not drop one.
func TestSyncMetaKeepsAKeyItDoesNotKnow(t *testing.T) {
	const withExtra = `{
  "owner": "ShoreLogic",
  "slug": "old",
  "name": "OLD",
  "extra": {
    "a": 1
  },
  "zz": "keep"
}
`
	path := writeMeta(t, t.TempDir(), withExtra)

	if _, err := app.SyncMeta(path, "NEW", "new"); err != nil {
		t.Fatalf("SyncMeta: %v", err)
	}
	const want = `{
  "owner": "ShoreLogic",
  "slug": "new",
  "name": "NEW",
  "extra": {
    "a": 1
  },
  "zz": "keep"
}
`
	if got := read(t, path); got != want {
		t.Fatalf("file =\n%s\nwant\n%s", got, want)
	}
}

// `.name = $n` appends when the key is absent, in the order the two
// assignments run. Measured against jq with an empty object.
func TestSyncMetaAppendsAnAbsentField(t *testing.T) {
	path := writeMeta(t, t.TempDir(), "{}")

	drift, err := app.SyncMeta(path, "N", "s")
	if err != nil {
		t.Fatalf("SyncMeta: %v", err)
	}
	if len(drift) != 2 || drift[0].Was != "" || drift[1].Was != "" {
		t.Fatalf("drift = %+v, want both fields moving from empty", drift)
	}
	const want = `{
  "name": "N",
  "slug": "s"
}
`
	if got := read(t, path); got != want {
		t.Fatalf("file =\n%s\nwant\n%s", got, want)
	}
}

// An App name is free text. jq escapes a JSON string and leaves <, > and & as
// they are; encoding/json escapes all three by default, which would rewrite a
// name nobody changed. Measured against jq, name and slug both.
func TestSyncMetaEscapesAStringTheWayJQDoes(t *testing.T) {
	path := writeMeta(t, t.TempDir(), `{"name":"OLD","slug":"old"}`)

	if _, err := app.SyncMeta(path, `A <&> B "q" ü`, "new"); err != nil {
		t.Fatalf("SyncMeta: %v", err)
	}
	const want = `{
  "name": "A <&> B \"q\" ü",
  "slug": "new"
}
`
	if got := read(t, path); got != want {
		t.Fatalf("file =\n%s\nwant\n%s", got, want)
	}
}

// The directory is 0700 and the key beside it is 0600. A correction that
// rewrote the metadata world-readable would widen the one file that names which
// App a machine trusts — so the mode is the umask's, not the original file's.
func TestSyncMetaWritesMode0600(t *testing.T) {
	path := writeMeta(t, t.TempDir(), metaFixture)
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("chmod: %v", err)
	}

	if _, err := app.SyncMeta(path, "CrossRev ShoreLogic", "crossrev-shorelogic"); err != nil {
		t.Fatalf("SyncMeta: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("mode = %04o, want 0600", got)
	}
}

// An unreachable cache is not evidence the identity moved. The shell's jq
// fails, the write fails with it, and nothing is created.
func TestSyncMetaRefusesAMissingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "absent.json")

	if _, err := app.SyncMeta(path, "N", "s"); err == nil {
		t.Fatal("SyncMeta on a missing file returned no error")
	}
	if _, err := os.Stat(path); err == nil {
		t.Fatal("SyncMeta created a file that was not there")
	}
}

func TestSyncMetaRefusesUnparseableJSONAndLeavesItAlone(t *testing.T) {
	path := writeMeta(t, t.TempDir(), "not json")

	if _, err := app.SyncMeta(path, "N", "s"); err == nil {
		t.Fatal("SyncMeta on unparseable JSON returned no error")
	}
	if got := read(t, path); got != "not json" {
		t.Fatalf("the file was rewritten: %q", got)
	}
}

// read returns a file's whole content, failing the test if it cannot.
func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(b)
}

// --- the `gh` calls the identity is read through ---------------------------

// recorder is a Runner that starts nothing and remembers everything it was
// handed, so a test asserts on the argument array and the environment without a
// process.
type recorder struct {
	specs   []exec.Spec
	results []exec.Result
	calls   int
}

func (r *recorder) Run(_ context.Context, spec exec.Spec) exec.Result {
	r.specs = append(r.specs, spec)
	i := r.calls
	r.calls++
	if i < len(r.results) {
		return r.results[i]
	}
	return exec.Result{Stdout: []byte("{}\n")}
}

// out is a successful invocation printing s.
func out(s string) exec.Result { return exec.Result{Stdout: []byte(s)} }

// bad is an invocation that exited non-zero, which is how the stub reports a
// route declared as `!fail`, and how gh reports a refused API call.
func bad() exec.Result { return exec.Result{ExitCode: 1} }

// errNoStatus is why a child produced no exit status at all.
var errNoStatus = errors.New("gh could not be started")

// unresolved is an invocation that never produced an exit status: an
// unresolvable program, a child that was killed, a context that ended.
func unresolved() exec.Result { return exec.Result{Err: errNoStatus} }

func (r *recorder) only(t *testing.T) exec.Spec {
	t.Helper()
	if len(r.specs) != 1 {
		t.Fatalf("gh was invoked %d times, want once", len(r.specs))
	}
	return r.specs[0]
}

// wantArgv asserts the whole argument array of the single recorded invocation,
// and that the program was gh.
func (r *recorder) wantArgv(t *testing.T, want ...string) {
	t.Helper()
	spec := r.only(t)
	if spec.Path != "gh" {
		t.Fatalf("program = %q, want %q", spec.Path, "gh")
	}
	if len(spec.Args) != len(want) {
		t.Fatalf("argv = %q\nwant   %q", spec.Args, want)
	}
	for i := range want {
		if spec.Args[i] != want[i] {
			t.Fatalf("argv = %q\nwant   %q", spec.Args, want)
		}
	}
}

func TestDetectOwnerAsksWhichRepositoryThisIs(t *testing.T) {
	rec := &recorder{results: []exec.Result{out("acme\n")}}

	owner, err := app.NewGH(rec).DetectOwner(context.Background())
	if err != nil {
		t.Fatalf("DetectOwner: %v", err)
	}
	if owner != "acme" {
		t.Fatalf("owner = %q, want %q", owner, "acme")
	}
	rec.wantArgv(t, "repo", "view", "--json", "owner", "--jq", ".owner.login")
}

func TestDetectOwnerFailsWhenGhDoes(t *testing.T) {
	for name, res := range map[string]exec.Result{"refused": bad(), "never started": unresolved()} {
		t.Run(name, func(t *testing.T) {
			rec := &recorder{results: []exec.Result{res}}
			if _, err := app.NewGH(rec).DetectOwner(context.Background()); err == nil {
				t.Fatal("DetectOwner returned no error")
			}
		})
	}
}

// The shell checks gh's exit status and not its output, so a successful call
// printing nothing yields an empty owner. Its callers then fail on the empty
// value rather than here. Pinned so the port does not invent a check the
// shipped tool does not make.
func TestDetectOwnerAnswersEmptyWhenGhPrintsNothing(t *testing.T) {
	rec := &recorder{results: []exec.Result{out("")}}

	owner, err := app.NewGH(rec).DetectOwner(context.Background())
	if err != nil {
		t.Fatalf("DetectOwner: %v", err)
	}
	if owner != "" {
		t.Fatalf("owner = %q, want empty", owner)
	}
}

func TestAccountInfoResolvesAnAccount(t *testing.T) {
	for _, tc := range []struct {
		login    string
		stdout   string
		wantType string
		wantID   string
	}{
		{"ShoreLogic", "Organization 12345\n", "Organization", "12345"},
		{"carlosboeing", "User 3394597\n", "User", "3394597"},
		// A bot login carries brackets, and /users/ resolves it like any other.
		{"crossrev-acme[bot]", "Bot 99999\n", "Bot", "99999"},
	} {
		rec := &recorder{results: []exec.Result{out(tc.stdout)}}

		account, err := app.NewGH(rec).AccountInfo(context.Background(), tc.login)
		if err != nil {
			t.Fatalf("AccountInfo(%q): %v", tc.login, err)
		}
		if account.Type != tc.wantType || account.ID != tc.wantID {
			t.Fatalf("AccountInfo(%q) = %+v, want {%s %s}", tc.login, account, tc.wantType, tc.wantID)
		}
		rec.wantArgv(t, "api", "users/"+tc.login, "--jq", `"\(.type // empty) \(.id // empty)"`)
	}
}

// jq's `\(empty)` produces no output at all, so a response missing either half
// prints an empty line rather than half an answer. The shell reads that as a
// failure, and so does this.
func TestAccountInfoRefusesAHalfAnswer(t *testing.T) {
	for name, stdout := range map[string]string{
		"nothing":         "",
		"a newline alone": "\n",
		// Both fields present and empty is the one case that reaches the
		// shell's `!= " "` guard.
		"two empty fields": " \n",
	} {
		t.Run(name, func(t *testing.T) {
			rec := &recorder{results: []exec.Result{out(stdout)}}
			if _, err := app.NewGH(rec).AccountInfo(context.Background(), "nobody"); err == nil {
				t.Fatal("AccountInfo returned no error")
			}
		})
	}
}

func TestAccountInfoFailsWhenTheAccountDoesNotExist(t *testing.T) {
	rec := &recorder{results: []exec.Result{bad()}}
	if _, err := app.NewGH(rec).AccountInfo(context.Background(), "nonexistent"); err == nil {
		t.Fatal("AccountInfo returned no error")
	}
}

// GET /app is authoritative and reachable with the key already on disk: the
// name on the first line, the slug on the second.
func TestAppIdentityReadsNameThenSlug(t *testing.T) {
	rec := &recorder{results: []exec.Result{out("CrossRev ShoreLogic\ncrossrev-shorelogic\n")}}

	identity, err := app.NewGH(rec).AppIdentity(context.Background(), "the-jwt")
	if err != nil {
		t.Fatalf("AppIdentity: %v", err)
	}
	if identity.Name != "CrossRev ShoreLogic" || identity.Slug != "crossrev-shorelogic" {
		t.Fatalf("identity = %+v", identity)
	}
	rec.wantArgv(t, "api", "-H", "Authorization: Bearer the-jwt", "/app", "--jq", ".name, .slug")
}

// A reachable API that answered with neither is not evidence of anything, and
// an empty slug written back would be worse than the stale one it replaced.
func TestAppIdentityRefusesAnIncompleteAnswer(t *testing.T) {
	for name, stdout := range map[string]string{
		"nothing":        "",
		"the name alone": "CrossRev ShoreLogic\n",
		"an empty name":  "\ncrossrev-shorelogic\n",
	} {
		t.Run(name, func(t *testing.T) {
			rec := &recorder{results: []exec.Result{out(stdout)}}
			if _, err := app.NewGH(rec).AppIdentity(context.Background(), "the-jwt"); err == nil {
				t.Fatal("AppIdentity returned no error")
			}
		})
	}
}

func TestAppIdentityFailsWhenTheAPICannotBeReached(t *testing.T) {
	rec := &recorder{results: []exec.Result{bad()}}
	if _, err := app.NewGH(rec).AppIdentity(context.Background(), "the-jwt"); err == nil {
		t.Fatal("AppIdentity returned no error")
	}
}

// The JWT is the App's proof that it is the App: whoever holds one can act as
// it until it expires. It travels in an argument, so it must not travel into an
// error string, a terminal or a run log.
func TestAppIdentityKeepsTheJWTOutOfItsError(t *testing.T) {
	const jwt = "header.payload.signature-that-must-not-be-printed"
	rec := &recorder{results: []exec.Result{bad()}}

	_, err := app.NewGH(rec).AppIdentity(context.Background(), jwt)
	if err == nil {
		t.Fatal("AppIdentity returned no error")
	}
	if strings.Contains(err.Error(), jwt) || strings.Contains(err.Error(), "signature-that-must-not-be-printed") {
		t.Fatalf("the error carries the JWT: %v", err)
	}
}

// Sync is the whole of what `auth status` does before it prints a line: read
// the authoritative identity, and correct the cache against it.
func TestSyncCorrectsTheCacheFromTheAuthoritativeIdentity(t *testing.T) {
	path := writeMeta(t, t.TempDir(), metaFixture)
	rec := &recorder{results: []exec.Result{out("CrossRev ShoreLogic\ncrossrev-shorelogic\n")}}

	drift, err := app.NewGH(rec).Sync(context.Background(), path, "the-jwt")
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if len(drift) != 2 {
		t.Fatalf("drift = %+v, want the name and the slug", drift)
	}
	meta, err := app.ReadMetadata(path)
	if err != nil {
		t.Fatalf("ReadMetadata: %v", err)
	}
	if meta.Slug != "crossrev-shorelogic" || meta.Name != "CrossRev ShoreLogic" {
		t.Fatalf("the cache was not corrected: %+v", meta)
	}
}

// An unreachable API is not evidence the cached identity is wrong, so nothing
// is corrected and nothing is claimed.
func TestSyncLeavesTheCacheAloneWhenTheAPIFails(t *testing.T) {
	path := writeMeta(t, t.TempDir(), metaFixture)
	rec := &recorder{results: []exec.Result{bad()}}

	if _, err := app.NewGH(rec).Sync(context.Background(), path, "the-jwt"); err == nil {
		t.Fatal("Sync returned no error")
	}
	if got := read(t, path); got != metaFixture {
		t.Fatalf("the cache was rewritten:\n%s", got)
	}
}

// A GH with no runner is a wiring bug, not a default: inventing one would start
// a real child holding a real credential.
func TestNewGHPanicsWithoutARunner(t *testing.T) {
	defer func() {
		if recovered := recover(); recovered == nil {
			t.Fatal("NewGH(nil) did not panic")
		}
	}()
	app.NewGH(nil)
}

// The environment gh receives is an allowlist read from this process, not this
// process's whole environment.
func TestNewGHPassesAnAllowlistedEnvironment(t *testing.T) {
	t.Setenv("GH_HOST", "github.example.com")
	t.Setenv("CROSSREV_NOT_ON_THE_LIST", "present")

	rec := &recorder{results: []exec.Result{out("acme\n")}}
	if _, err := app.NewGH(rec).DetectOwner(context.Background()); err != nil {
		t.Fatalf("DetectOwner: %v", err)
	}

	env := rec.only(t).Env
	var sawHost, sawPath, sawStray bool
	for _, entry := range env {
		switch {
		case entry == "GH_HOST=github.example.com":
			sawHost = true
		case strings.HasPrefix(entry, "PATH="):
			sawPath = true
		case strings.HasPrefix(entry, "CROSSREV_NOT_ON_THE_LIST="):
			sawStray = true
		}
	}
	if !sawHost {
		t.Error("gh did not receive GH_HOST")
	}
	if !sawPath && os.Getenv("PATH") != "" {
		t.Error("gh did not receive PATH")
	}
	if sawStray {
		t.Error("gh received a variable that is not on the allowlist")
	}
}

// WithEnv is what a caller uses to hand gh an environment of its own, which is
// the only route this package offers to one.
func TestWithEnvReplacesTheEnvironment(t *testing.T) {
	rec := &recorder{results: []exec.Result{out("acme\n")}}

	client := app.NewGH(rec, app.WithEnv([]string{"PATH=/only/this"}))
	if _, err := client.DetectOwner(context.Background()); err != nil {
		t.Fatalf("DetectOwner: %v", err)
	}
	env := rec.only(t).Env
	if len(env) != 1 || env[0] != "PATH=/only/this" {
		t.Fatalf("env = %q, want the one entry it was given", env)
	}
}

var _ exec.Runner = (*recorder)(nil)

// --- the ledger of long-lived tokens ---------------------------------------
//
// `claude setup-token` prints a token valid for a year and says plainly that
// you will not see it again, so there is nothing to inspect eleven months later
// and the first sign of expiry is a CI failure on a day nobody is looking.
// Recording the creation date at the moment it is set is the only point at
// which the information exists. The ledger holds dates, never tokens.

// stamped is a fixed instant, so a recorded date is an assertion rather than a
// moving target.
var stamped = time.Date(2026, 9, 1, 22, 28, 33, 0, time.UTC)

func TestTokenRecordWritesTheLedger(t *testing.T) {
	env := fakeEnv{"XDG_CONFIG_HOME": t.TempDir()}

	if err := app.TokenRecord(env, "acme/repo", "CLAUDE_CODE_OAUTH_TOKEN", 365, stamped); err != nil {
		t.Fatalf("TokenRecord: %v", err)
	}

	// Compact, with a trailing newline: `jq -c` writes it.
	const want = `{"acme/repo":{"CLAUDE_CODE_OAUTH_TOKEN":{"created":"2026-09-01T22:28:33Z","valid_days":365}}}` + "\n"
	if got := read(t, app.TokensPath(env)); got != want {
		t.Fatalf("ledger = %s\nwant     %s", got, want)
	}
}

// The ledger names which repositories hold a credential and when each stops
// working. It is created 0600 in a directory `mkdir -p` leaves at the umask's
// default, which is what the shell does.
func TestTokenRecordCreatesTheFile0600(t *testing.T) {
	env := fakeEnv{"XDG_CONFIG_HOME": t.TempDir()}

	if err := app.TokenRecord(env, "acme/repo", "TOKEN", 365, stamped); err != nil {
		t.Fatalf("TokenRecord: %v", err)
	}
	info, err := os.Stat(app.TokensPath(env))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("mode = %04o, want 0600", got)
	}
	dir, err := os.Stat(filepath.Dir(app.TokensPath(env)))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if !dir.IsDir() {
		t.Fatal("the ledger's directory was not created")
	}
}

// A second secret in the same repository, and a second repository, land after
// what is already there. jq appends a key it has not seen.
func TestTokenRecordAppends(t *testing.T) {
	env := fakeEnv{"XDG_CONFIG_HOME": t.TempDir()}

	for _, r := range []struct {
		repo, name string
		days       int
	}{
		{"acme/repo", "FIRST", 365},
		{"acme/repo", "SECOND", 30},
		{"other/repo", "THIRD", 1},
	} {
		if err := app.TokenRecord(env, r.repo, r.name, r.days, stamped); err != nil {
			t.Fatalf("TokenRecord: %v", err)
		}
	}

	const want = `{"acme/repo":{"FIRST":{"created":"2026-09-01T22:28:33Z","valid_days":365},` +
		`"SECOND":{"created":"2026-09-01T22:28:33Z","valid_days":30}},` +
		`"other/repo":{"THIRD":{"created":"2026-09-01T22:28:33Z","valid_days":1}}}` + "\n"
	if got := read(t, app.TokensPath(env)); got != want {
		t.Fatalf("ledger = %s\nwant     %s", got, want)
	}
}

// Re-recording a secret restamps it where it already sits. Reordering the
// ledger on every rotation would make a diff of it unreadable.
func TestTokenRecordUpdatesInPlace(t *testing.T) {
	env := fakeEnv{"XDG_CONFIG_HOME": t.TempDir()}
	seedLedger(t, env, `{"r":{"a":{"created":"x","valid_days":1},"b":{"created":"y","valid_days":2}}}`)

	if err := app.TokenRecord(env, "r", "a", 9, stamped); err != nil {
		t.Fatalf("TokenRecord: %v", err)
	}
	const want = `{"r":{"a":{"created":"2026-09-01T22:28:33Z","valid_days":9},"b":{"created":"y","valid_days":2}}}` + "\n"
	if got := read(t, app.TokensPath(env)); got != want {
		t.Fatalf("ledger = %s\nwant     %s", got, want)
	}
}

// An empty file is `{}`, which is what `[[ -n "$existing" ]] || existing='{}'`
// says. A file somebody truncated is not a reason to refuse.
func TestTokenRecordTreatsAnEmptyLedgerAsEmpty(t *testing.T) {
	env := fakeEnv{"XDG_CONFIG_HOME": t.TempDir()}
	seedLedger(t, env, "")

	if err := app.TokenRecord(env, "r", "n", 5, stamped); err != nil {
		t.Fatalf("TokenRecord: %v", err)
	}
	const want = `{"r":{"n":{"created":"2026-09-01T22:28:33Z","valid_days":5}}}` + "\n"
	if got := read(t, app.TokensPath(env)); got != want {
		t.Fatalf("ledger = %s\nwant     %s", got, want)
	}
}

// jq refuses both of these and the write never runs, so the ledger on disk is
// the one that was there. Overwriting it would lose every date it holds.
func TestTokenRecordRefusesALedgerItCannotRead(t *testing.T) {
	for name, seed := range map[string]string{
		"unparseable":                  "garbage",
		"a repo that is not an object": `{"r":"str"}`,
	} {
		t.Run(name, func(t *testing.T) {
			env := fakeEnv{"XDG_CONFIG_HOME": t.TempDir()}
			seedLedger(t, env, seed)

			if err := app.TokenRecord(env, "r", "n", 5, stamped); err == nil {
				t.Fatal("TokenRecord returned no error")
			}
			if got := read(t, app.TokensPath(env)); got != seed {
				t.Fatalf("the ledger was rewritten: %s", got)
			}
		})
	}
}

func TestTokenDaysLeft(t *testing.T) {
	env := fakeEnv{"XDG_CONFIG_HOME": t.TempDir()}
	if err := app.TokenRecord(env, "acme/repo", "TOKEN", 365, stamped); err != nil {
		t.Fatalf("TokenRecord: %v", err)
	}

	for _, tc := range []struct {
		name string
		now  time.Time
		want int
	}{
		{"on the day it was recorded", stamped, 365},
		// Integer division truncates, so a partial day counts for nothing
		// until it completes.
		{"most of a day later", stamped.Add(23 * time.Hour), 365},
		{"a day later", stamped.Add(24 * time.Hour), 364},
		// Elapsed 24-hour periods, not calendar years: two years from this
		// instant is 731 days because 2028 has a leap day. Cross-checked
		// against the shell's own arithmetic.
		{"long past expiry", stamped.AddDate(2, 0, 0), 365 - 731},
	} {
		t.Run(tc.name, func(t *testing.T) {
			left, err := app.TokenDaysLeft(env, "acme/repo", "TOKEN", tc.now)
			if err != nil {
				t.Fatalf("TokenDaysLeft: %v", err)
			}
			if left != tc.want {
				t.Fatalf("days left = %d, want %d", left, tc.want)
			}
		})
	}
}

func TestTokenDaysLeftFailsWhenNothingWasRecorded(t *testing.T) {
	env := fakeEnv{"XDG_CONFIG_HOME": t.TempDir()}

	// No ledger at all.
	if _, err := app.TokenDaysLeft(env, "acme/repo", "TOKEN", stamped); err == nil {
		t.Fatal("TokenDaysLeft returned no error with no ledger")
	}

	seedLedger(t, env, `{"acme/repo":{"OTHER":{"created":"2026-09-01T22:28:33Z","valid_days":365}}}`)
	if _, err := app.TokenDaysLeft(env, "acme/repo", "TOKEN", stamped); err == nil {
		t.Fatal("TokenDaysLeft returned no error for a secret nobody recorded")
	}
	if _, err := app.TokenDaysLeft(env, "other/repo", "OTHER", stamped); err == nil {
		t.Fatal("TokenDaysLeft returned no error for a repository nobody recorded")
	}
}

// The shell parses the date with BSD `date -j -f`, which takes this layout and
// no other, before falling back to GNU `date -d`. A date it cannot read is a
// failure rather than a guess.
func TestTokenDaysLeftRefusesADateItCannotRead(t *testing.T) {
	for name, created := range map[string]string{
		"not a date":              `"nope"`,
		"absent":                  ``,
		"an offset rather than Z": `"2026-01-01T00:00:00+10:00"`,
	} {
		t.Run(name, func(t *testing.T) {
			env := fakeEnv{"XDG_CONFIG_HOME": t.TempDir()}
			entry := `{"valid_days":5}`
			if created != "" {
				entry = `{"created":` + created + `,"valid_days":5}`
			}
			seedLedger(t, env, `{"r":{"n":`+entry+`}}`)

			if _, err := app.TokenDaysLeft(env, "r", "n", stamped); err == nil {
				t.Fatal("TokenDaysLeft returned no error")
			}
		})
	}
}

// `$(( 0.5 - 3 ))` is a bash syntax error, so a fractional validity refuses
// rather than rounding.
func TestTokenDaysLeftRefusesAFractionalValidity(t *testing.T) {
	env := fakeEnv{"XDG_CONFIG_HOME": t.TempDir()}
	seedLedger(t, env, `{"r":{"n":{"created":"2026-09-01T22:28:33Z","valid_days":0.5}}}`)

	if _, err := app.TokenDaysLeft(env, "r", "n", stamped); err == nil {
		t.Fatal("TokenDaysLeft returned no error")
	}
}

// An entry with no validity counts as zero days: bash reads the bare word
// `null` as an unset variable and arithmetic on one is zero. Measured, not
// assumed — the answer is the elapsed days, negated.
func TestTokenDaysLeftReadsAnAbsentValidityAsZero(t *testing.T) {
	env := fakeEnv{"XDG_CONFIG_HOME": t.TempDir()}
	seedLedger(t, env, `{"r":{"n":{"created":"2026-09-01T22:28:33Z"}}}`)

	left, err := app.TokenDaysLeft(env, "r", "n", stamped.AddDate(0, 0, 10))
	if err != nil {
		t.Fatalf("TokenDaysLeft: %v", err)
	}
	if left != -10 {
		t.Fatalf("days left = %d, want -10", left)
	}
}

// seedLedger puts content at the ledger's path, creating its directory.
func seedLedger(t *testing.T, env app.Environment, content string) {
	t.Helper()
	path := app.TokensPath(env)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("writing the ledger: %v", err)
	}
}
