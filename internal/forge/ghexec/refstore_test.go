package ghexec_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	crexec "github.com/carlosboeing/crossrev/internal/exec"
	"github.com/carlosboeing/crossrev/internal/forge"
	"github.com/carlosboeing/crossrev/internal/forge/ghexec"
	"github.com/carlosboeing/crossrev/internal/prstate"
	"github.com/carlosboeing/crossrev/internal/prstate/storetest"
)

type failingFilter struct{}

func (failingFilter) Filter(string) (string, error) {
	return "", errors.New("the credential filter failed")
}
func (failingFilter) Mask(s string) string { return s }

type rewritingFilter struct {
	from string
	to   string
}

func (r rewritingFilter) Filter(s string) (string, error) {
	return strings.ReplaceAll(s, r.from, r.to), nil
}
func (r rewritingFilter) Mask(s string) string { return s }

func newClient(runner crexec.Runner, filter forge.Publisher) *ghexec.Client {
	return ghexec.New(runner, filter)
}

func mustRefName(t *testing.T, namespace string, ref prstate.SlotRef) string {
	t.Helper()
	name, err := prstate.RefName(namespace, ref)
	if err != nil {
		t.Fatal(err)
	}
	return name
}

func fixtureGeneration(t *testing.T, form string) prstate.Generation {
	t.Helper()
	return storetest.FixtureGeneration(t, form)
}

type stubGh struct {
	client     *ghexec.Client
	dir        string
	stateDir   string
	logPath    string
	routesPath string
}

func (g *stubGh) calls() []string {
	raw, err := os.ReadFile(g.logPath)
	if err != nil {
		return nil
	}
	var lines []string
	for line := range strings.SplitSeq(strings.TrimSuffix(string(raw), "\n"), "\n") {
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

func (g *stubGh) publishForeignGeneration(t *testing.T, ref prstate.SlotRef, label string) struct {
	commit        string
	examinedScope string
} {
	t.Helper()
	store := g.client.RefLedger("refs/crossrev")
	gen := fixtureGeneration(t, prstate.GenerationFull)
	gen.ScopeReport.ExaminedScope = label
	handle, err := store.PublishGeneration(context.Background(), ref, prstate.Handle{}, gen)
	if err != nil {
		t.Fatalf("publishForeignGeneration: %v", err)
	}
	return struct {
		commit        string
		examinedScope string
	}{
		commit:        handle.Commit,
		examinedScope: label,
	}
}

func (g *stubGh) moveRef(t *testing.T, refName string, commit string) {
	t.Helper()
	refEnc := strings.ReplaceAll(refName, "/", "_")
	data, err := json.Marshal(map[string]any{
		"ref": refName,
		"object": map[string]string{
			"sha":  commit,
			"type": "commit",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(g.stateDir, "ref-"+refEnc), data, 0o600); err != nil {
		t.Fatal(err)
	}
	trimmed := strings.TrimPrefix(refName, "refs/")
	if err := os.WriteFile(filepath.Join(g.stateDir, "ref-"+strings.ReplaceAll(trimmed, "/", "_")), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func (g *stubGh) deleteRef(t *testing.T, refName string) {
	t.Helper()
	refEnc := strings.ReplaceAll(refName, "/", "_")
	_ = os.Remove(filepath.Join(g.stateDir, "ref-"+refEnc))
	trimmed := strings.TrimPrefix(refName, "refs/")
	_ = os.Remove(filepath.Join(g.stateDir, "ref-"+strings.ReplaceAll(trimmed, "/", "_")))
}

func (g *stubGh) hasRef(t *testing.T, refName string) bool {
	t.Helper()
	refEnc := strings.ReplaceAll(refName, "/", "_")
	_, err := os.Stat(filepath.Join(g.stateDir, "ref-"+refEnc))
	return err == nil
}

func (g *stubGh) setRoute(t *testing.T, routes string) {
	t.Helper()
	if err := os.WriteFile(g.routesPath, []byte(routes), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (g *stubGh) readBlob(t *testing.T, filename string, commitSHA string) []byte {
	t.Helper()
	commitRaw, err := os.ReadFile(filepath.Join(g.stateDir, "commit-"+commitSHA))
	if err != nil {
		t.Fatalf("reading commit file: %v", err)
	}
	var commitObj struct {
		Tree string `json:"tree"`
	}
	if err := json.Unmarshal(commitRaw, &commitObj); err != nil {
		t.Fatalf("unmarshaling commit: %v", err)
	}

	treeRaw, err := os.ReadFile(filepath.Join(g.stateDir, "tree-"+commitObj.Tree))
	if err != nil {
		t.Fatalf("reading tree file: %v", err)
	}
	var treeObj struct {
		Tree []struct {
			Path string `json:"path"`
			SHA  string `json:"sha"`
		} `json:"tree"`
	}
	if err := json.Unmarshal(treeRaw, &treeObj); err != nil {
		t.Fatalf("unmarshaling tree: %v", err)
	}

	var blobSHA string
	for _, entry := range treeObj.Tree {
		if entry.Path == filename {
			blobSHA = entry.SHA
			break
		}
	}
	if blobSHA == "" {
		t.Fatalf("tree missing blob %s", filename)
	}

	blobContent, err := os.ReadFile(filepath.Join(g.stateDir, "blob-"+blobSHA))
	if err != nil {
		t.Fatalf("reading blob file: %v", err)
	}
	return blobContent
}

func newStubbedRefStoreWithFilter(t *testing.T, filter forge.Publisher) (prstate.LedgerStore, *stubGh) {
	t.Helper()

	stub, err := filepath.Abs("../../../tests/stub")
	if err != nil {
		t.Fatalf("locating the stub: %v", err)
	}
	if _, err := os.Stat(filepath.Join(stub, "gh")); err != nil {
		t.Skipf("the stub gh is not in this checkout: %v", err)
	}

	dir := t.TempDir()
	logPath := filepath.Join(dir, "gh.log")
	routesPath := filepath.Join(dir, "routes")
	stateDir := filepath.Join(dir, "state")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatalf("creating state dir: %v", err)
	}
	if err := os.WriteFile(routesPath, nil, 0o600); err != nil {
		t.Fatalf("writing routes: %v", err)
	}
	if err := os.WriteFile(logPath, nil, 0o600); err != nil {
		t.Fatalf("writing log: %v", err)
	}

	t.Setenv("PATH", stub+string(os.PathListSeparator)+os.Getenv("PATH"))

	env := append(crexec.Inherit([]string{"PATH", "HOME"}),
		"CROSSREV_GH_LOG="+logPath,
		"CROSSREV_GH_ROUTES="+routesPath,
		"CROSSREV_GH_STATE="+stateDir,
	)

	client := ghexec.New(crexec.NewOrchestratorRunner(), filter, ghexec.WithEnv(env))
	gh := &stubGh{
		client:     client,
		dir:        dir,
		stateDir:   stateDir,
		logPath:    logPath,
		routesPath: routesPath,
	}
	return client.RefLedger("refs/crossrev"), gh
}

func newStubbedRefStore(t *testing.T) (prstate.LedgerStore, *stubGh) {
	return newStubbedRefStoreWithFilter(t, passthrough{})
}

// Step 2: Regression test 8 — filter abort on raw schema v2 JSON.
func TestRefStoreAbortsOnFilterFailureWithRawSchemaV2JSON(t *testing.T) {
	ctx := context.Background()
	ref := storetest.FixtureSlotRef(t)

	_, records, err := prstate.EncodeGenerationV2(fixtureGeneration(t, prstate.GenerationFull))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(records, []byte(prstate.MarkerPrefix)) {
		t.Fatal("the fixture carries a marker prefix, so this test would pass for the wrong reason")
	}

	rec := &recorder{}
	store := newClient(rec, failingFilter{}).RefLedger("refs/crossrev")
	if _, err := store.PublishGeneration(ctx, ref, prstate.Handle{}, fixtureGeneration(t, prstate.GenerationFull)); err == nil {
		t.Fatal("a ledger write survived a filter failure")
	}
	if len(rec.specs) != 0 {
		t.Fatalf("gh was called %d times after the filter failed; nothing may be written", len(rec.specs))
	}
}

func TestRefStoreFiltersBeforeDigesting(t *testing.T) {
	ctx := context.Background()
	ref := storetest.FixtureSlotRef(t)

	// A filter that rewrites bytes must produce a manifest whose
	// records_digest describes the filtered bytes, not the originals.
	rewriter := rewritingFilter{from: "sensitive-data", to: "redacted-info"}
	store, gh := newStubbedRefStoreWithFilter(t, rewriter)

	gen := fixtureGeneration(t, prstate.GenerationFull)
	gen.Records[0].Reason = prstate.Some("sensitive-data")
	gen.ScopeReport.ExaminedScope = "sensitive-data"

	handle, err := store.PublishGeneration(ctx, ref, prstate.Handle{}, gen)
	if err != nil {
		t.Fatalf("PublishGeneration failed: %v", err)
	}

	readGen, err := store.ReadGeneration(ctx, ref, handle)
	if err != nil {
		t.Fatalf("ReadGeneration failed: %v", err)
	}
	if reason, ok := readGen.Records[0].Reason.Get(); !ok || reason != "redacted-info" {
		t.Fatalf("read record reason = %q, want %q", reason, "redacted-info")
	}

	// Verify the stored records blob in gh has the rewritten text, and the
	// stored manifest's records_digest describes those filtered bytes.
	manifestBytes := gh.readBlob(t, "manifest.json", handle.Commit)
	recordsBytes := gh.readBlob(t, "records.json", handle.Commit)

	unfilteredGen := fixtureGeneration(t, prstate.GenerationFull)
	unfilteredGen.Records[0].Reason = prstate.Some("sensitive-data")
	unfilteredGen.ScopeReport.ExaminedScope = "sensitive-data"
	_, unfilteredRecords, err := prstate.EncodeGenerationV2(unfilteredGen)
	if err != nil {
		t.Fatal(err)
	}
	unfilteredSum := sha256.Sum256(unfilteredRecords)
	filteredSum := sha256.Sum256(recordsBytes)
	if bytes.Equal(unfilteredSum[:], filteredSum[:]) {
		t.Fatal("filtered and unfiltered records produced identical digests")
	}

	var manObj struct {
		RecordsDigest string `json:"records_digest"`
	}
	if err := json.Unmarshal(manifestBytes, &manObj); err != nil {
		t.Fatal(err)
	}
	if manObj.RecordsDigest != hex.EncodeToString(filteredSum[:]) {
		t.Fatalf("manifest records_digest %q != sha256(filteredRecords) %x", manObj.RecordsDigest, filteredSum)
	}
}

// Step 3: Regression test 1 — substitution after publication at store boundary.
func TestRefStoreReadsTheHandleCommitAndNeverTheRef(t *testing.T) {
	ctx := context.Background()
	ref := storetest.FixtureSlotRef(t)
	store, gh := newStubbedRefStore(t)
	ours, err := store.PublishGeneration(ctx, ref, prstate.Handle{}, fixtureGeneration(t, prstate.GenerationFull))
	if err != nil {
		t.Fatal(err)
	}

	theirs := gh.publishForeignGeneration(t, ref, "another-writer") // valid bytes, valid digests
	gh.moveRef(t, mustRefName(t, "refs/crossrev", ref), theirs.commit)

	got, err := store.ReadGeneration(ctx, ref, ours)
	if err != nil {
		t.Fatalf("the legitimate generation stopped reading after the ref moved: %v", err)
	}
	if got.ScopeReport.ExaminedScope == theirs.examinedScope {
		t.Fatal("the reader returned the substituted generation")
	}
	for _, call := range gh.calls() {
		if strings.Contains(call, "git/ref/") && !strings.Contains(call, "--method") {
			t.Fatalf("the read consulted the ref for authority: %s", call)
		}
	}
}

// Step 4: Three-outcome tests at the gh boundary.
func TestRefStoreDeletedRefIsNotALostLedger(t *testing.T) {
	ctx := context.Background()
	ref := storetest.FixtureSlotRef(t)
	store, gh := newStubbedRefStore(t)
	handle, err := store.PublishGeneration(ctx, ref, prstate.Handle{}, fixtureGeneration(t, prstate.GenerationFull))
	if err != nil {
		t.Fatal(err)
	}

	refName := mustRefName(t, "refs/crossrev", ref)
	gh.deleteRef(t, refName)

	got, err := store.ReadGeneration(ctx, ref, handle)
	if err != nil {
		t.Fatalf("reading with deleted ref answered %v, want success", err)
	}
	if got.Gen != handle.Gen {
		t.Fatalf("read Gen = %d, want %d", got.Gen, handle.Gen)
	}
	if !gh.hasRef(t, refName) {
		t.Fatal("read generation succeeded but did not re-create the deleted ref")
	}
}

func TestRefStoreMissingObjectsAreALostLedger(t *testing.T) {
	ctx := context.Background()
	ref := storetest.FixtureSlotRef(t)
	store, _ := newStubbedRefStore(t)
	goneHandle := prstate.Handle{
		Gen:      1,
		Commit:   "0000000000000000000000000000000000000000",
		Location: "refs/crossrev/pr/42/reviewer1/coverage",
	}
	_, err := store.ReadGeneration(ctx, ref, goneHandle)
	if !errors.Is(err, prstate.ErrLedgerLost) {
		t.Fatalf("reading missing commit answered %v, want %v", err, prstate.ErrLedgerLost)
	}
}

func TestRefStoreUnverifiableObjectsAreCorrupt(t *testing.T) {
	ctx := context.Background()
	ref := storetest.FixtureSlotRef(t)
	store, _ := newStubbedRefStore(t)
	corruptHandle := prstate.Handle{
		Gen:      1,
		Commit:   "corrupt00000000000000000000000000000000",
		Location: "refs/crossrev/pr/42/reviewer1/coverage",
	}
	_, err := store.ReadGeneration(ctx, ref, corruptHandle)
	if !errors.Is(err, prstate.ErrLedgerCorrupt) {
		t.Fatalf("reading unverifiable commit answered %v, want %v", err, prstate.ErrLedgerCorrupt)
	}
}

func TestRefStoreUnreadableStoreIsNeitherSentinel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ref := storetest.FixtureSlotRef(t)
	store, _ := newStubbedRefStore(t)
	handle := prstate.Handle{
		Gen:      1,
		Commit:   "1111111111111111111111111111111111111111",
		Location: "refs/crossrev/pr/42/reviewer1/coverage",
	}
	_, err := store.ReadGeneration(ctx, ref, handle)
	if err == nil {
		t.Fatal("read with canceled context succeeded")
	}
	if errors.Is(err, prstate.ErrLedgerLost) {
		t.Fatalf("an unreadable store was reported as ErrLedgerLost: %v", err)
	}
	if errors.Is(err, prstate.ErrLedgerCorrupt) {
		t.Fatalf("an unreadable store was reported as ErrLedgerCorrupt: %v", err)
	}
}

func TestRefStoreRefusedWriteReportsRefusal(t *testing.T) {
	ctx := context.Background()
	ref := storetest.FixtureSlotRef(t)
	store, gh := newStubbedRefStore(t)
	gh.setRoute(t, "api --method POST repos/*/git/refs*\t!fail\napi --method PATCH repos/*/git/refs/*\t!fail\n")

	_, err := store.PublishGeneration(ctx, ref, prstate.Handle{}, fixtureGeneration(t, prstate.GenerationFull))
	if err == nil {
		t.Fatal("a refused write succeeded")
	}
	if !strings.Contains(err.Error(), "ref write refused") {
		t.Fatalf("error %q does not report refusal", err)
	}
}

// Step 8: Contract test across both backends.
func TestRefStoreMeetsTheContract(t *testing.T) {
	storetest.Contract(t, "refs", func(t *testing.T) prstate.LedgerStore {
		store, _ := newStubbedRefStore(t)
		return store
	})
}

// Moved from the retired comment-ledger suite (its filter-failure refusal
// test): a generation the filter could not process stops the write, and gh
// is never invoked — marker-carrying content refuses the same as unmarked
// content.
func TestRefStoreRefusesTheBlobWriteWhenTheFilterFails(t *testing.T) {
	ctx := context.Background()
	ref := storetest.FixtureSlotRef(t)
	gen := fixtureGeneration(t, prstate.GenerationFull)
	gen.Records[0].Reason = prstate.Some("Shard.\n\n<!-- crossrev:c {\"v\":1,\"kind\":\"shard\"} -->")

	rec := &recorder{}
	store := newClient(rec, failingFilter{}).RefLedger("refs/crossrev")
	if _, err := store.PublishGeneration(ctx, ref, prstate.Handle{}, gen); err == nil {
		t.Error("a generation was published unfiltered")
	}
	if len(rec.specs) != 0 {
		t.Errorf("gh was invoked %d times, want not at all", len(rec.specs))
	}
}

// Moved from the retired comment-ledger suite (its publishes-the-notice
// test), with the behaviour inverted on purpose: the comment path published
// a notice when unmarked content failed filtering, but the blob path has no
// notice — the write refuses, nothing is stored, and the error names
// filtering as the cause rather than masking it.
func TestRefStoreRefusesUnmarkedContentWhenTheFilterFails(t *testing.T) {
	ctx := context.Background()
	ref := storetest.FixtureSlotRef(t)

	_, records, err := prstate.EncodeGenerationV2(fixtureGeneration(t, prstate.GenerationFull))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(records, []byte(prstate.MarkerPrefix)) {
		t.Fatal("the fixture carries a marker prefix, so this test would pass for the wrong reason")
	}

	rec := &recorder{}
	store := newClient(rec, failingFilter{}).RefLedger("refs/crossrev")
	if _, err := store.PublishGeneration(ctx, ref, prstate.Handle{}, fixtureGeneration(t, prstate.GenerationFull)); err == nil {
		t.Fatal("unmarked content was published despite the filter failing")
	} else if !strings.Contains(err.Error(), "filtering generation") {
		t.Fatalf("error %q does not name filtering as the cause", err)
	}
	if len(rec.specs) != 0 {
		t.Fatalf("gh was called %d times; nothing may be written", len(rec.specs))
	}
}

// Moved from the retired comment-ledger suite (its filtered-body test): the
// filtered bytes are what reach storage, not the bytes the caller handed
// over — in both blobs, since the scope report travels in the manifest.
func TestRefStoreStoresTheFilteredBytes(t *testing.T) {
	ctx := context.Background()
	ref := storetest.FixtureSlotRef(t)
	store, gh := newStubbedRefStoreWithFilter(t, masking{})

	gen := fixtureGeneration(t, prstate.GenerationFull)
	gen.Records[0].Reason = prstate.Some("sk-ant-secret")
	gen.ScopeReport.ExaminedScope = "sk-ant-secret"

	handle, err := store.PublishGeneration(ctx, ref, prstate.Handle{}, gen)
	if err != nil {
		t.Fatalf("PublishGeneration: %v", err)
	}
	for _, name := range []string{"manifest.json", "records.json"} {
		stored := gh.readBlob(t, name, handle.Commit)
		if bytes.Contains(stored, []byte("sk-ant-secret")) {
			t.Errorf("%s holds the unfiltered secret", name)
		}
		if !bytes.Contains(stored, []byte("masked")) {
			t.Errorf("%s holds %q, want the filtered bytes", name, stored)
		}
	}

	read, err := store.ReadGeneration(ctx, ref, handle)
	if err != nil {
		t.Fatalf("ReadGeneration: %v", err)
	}
	if reason, ok := read.Records[0].Reason.Get(); !ok || reason != "masked" {
		t.Errorf("read record reason = %q, want %q", reason, "masked")
	}
	if read.ScopeReport.ExaminedScope != "masked" {
		t.Errorf("read examined scope = %q, want %q", read.ScopeReport.ExaminedScope, "masked")
	}
}
