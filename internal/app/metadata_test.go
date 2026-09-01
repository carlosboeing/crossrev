package app_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/carlosboeing/crossrev/internal/app"
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
// tests/test-auth.sh:97-107 writes it and lib/auth.sh:708-714 created it.
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
// loop's (lib/auth.sh:382, `.role // "loop"`).
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
