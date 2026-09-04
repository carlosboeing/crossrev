package initcmd_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/config"
	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/harness"
	"github.com/carlosboeing/crossrev/internal/initcmd"
	"github.com/carlosboeing/crossrev/internal/ui"
)

// slug is acme/widget, the slug the shell suite's fixtures use.
func slug(t *testing.T) core.Slug {
	t.Helper()
	s, err := core.ParseSlug("acme/widget")
	if err != nil {
		t.Fatalf("parse slug: %v", err)
	}
	return s
}

// descriptor is the shipped lib/harnesses.json, so every credential archetype,
// secret name, install command and seed hint under test is the one that ships
// rather than one a fixture invented.
func descriptor(t *testing.T) harness.Document {
	t.Helper()
	doc, err := harness.Descriptors()
	if err != nil {
		t.Fatalf("load descriptors: %v", err)
	}
	return doc
}

// showing serves the named paths from memory and answers NotFound for anything
// else, which is what config.Load and the Project Map reader see for a
// repository that carries neither file.
func showing(files map[string]string) config.ShowFile {
	return func(_ context.Context, revision core.Revision, path string) ([]byte, config.FileStatus, error) {
		if !revision.IsZero() {
			return nil, config.NotFound, errors.New("init reads the working tree, never a revision")
		}
		if body, found := files[path]; found {
			return []byte(body), config.IsFile, nil
		}
		return nil, config.NotFound, nil
	}
}

// fakeGitHub answers every forge read from a fixed set and records the order it
// was asked in, so a test can assert that a read happened and that nothing else
// did.
type fakeGitHub struct {
	slug      core.Slug
	slugErr   error
	ownerType string
	branch    string
	protected bool
	colours   map[string]string
	orgList   string
	orgOK     bool
	repoList  string
	repoErr   error

	calls []string
}

func (f *fakeGitHub) record(call string) { f.calls = append(f.calls, call) }

func (f *fakeGitHub) RepoSlug(context.Context) (core.Slug, error) {
	f.record("RepoSlug")
	return f.slug, f.slugErr
}

func (f *fakeGitHub) OwnerType(_ context.Context, owner string) string {
	f.record("OwnerType " + owner)
	return f.ownerType
}

func (f *fakeGitHub) DefaultBranch(context.Context, core.Slug) string {
	f.record("DefaultBranch")
	if f.branch == "" {
		return "main"
	}
	return f.branch
}

func (f *fakeGitHub) BranchProtected(_ context.Context, _ core.Slug, branch string) bool {
	f.record("BranchProtected " + branch)
	return f.protected
}

func (f *fakeGitHub) LabelColour(_ context.Context, _ core.Slug, name string) string {
	f.record("LabelColour " + name)
	return f.colours[name]
}

func (f *fakeGitHub) SecretsAtOrg(_ context.Context, owner string) (string, bool) {
	f.record("SecretsAtOrg " + owner)
	return f.orgList, f.orgOK
}

func (f *fakeGitHub) SecretsAtRepo(context.Context, core.Slug) (string, error) {
	f.record("SecretsAtRepo")
	return f.repoList, f.repoErr
}

// fakeApps is the App metadata directory, keyed "<owner>/<role>".
type fakeApps map[string]initcmd.App

func (a fakeApps) App(owner, role string) (initcmd.App, bool) {
	app, found := a[owner+"/"+role]
	return app, found
}

// fakePairing is the preflight report. The zero value supports everything, has
// no secret for any harness and needs no refresher, so a test states only the
// answer it is about.
type fakePairing struct {
	refuse    map[string]string // harness -> reason
	secrets   map[string]string // harness -> secret
	refresher map[string]bool   // harness -> needs one
	legs      map[string]string // "<harness>/<leg>" -> reason
}

func (p fakePairing) Supported(_, name, leg string) (string, bool) {
	if reason, found := p.legs[name+"/"+leg]; found {
		return reason, false
	}
	if reason, found := p.refuse[name]; found {
		return reason, false
	}
	return "", true
}

func (p fakePairing) Secret(name string) (string, bool) {
	secret, found := p.secrets[name]
	return secret, found
}

func (p fakePairing) NeedsRefresher(_, name, endpoint string) bool {
	if endpoint != "" && endpoint != "null" {
		return false
	}
	return p.refresher[name]
}

// livePairing is internal/preflight's answer, reproduced here because initcmd
// may not import that package. Only the tests use it, and only so the secret
// names and refusals in a whole-plan assertion are the shipped ones.
type livePairing struct{ doc harness.Document }

func (p livePairing) Supported(runner, name, leg string) (string, bool) {
	if leg != "" && !p.doc.ServesLeg(name, leg) {
		return name + " is limited to another leg", false
	}
	if runner == "self-hosted" {
		return "", true
	}
	entry, known := p.doc.For(name)
	if !known {
		return "CrossRev has no adapter for '" + name + "'", false
	}
	credential := entry.Credential
	switch {
	case credential.Archetype == "A",
		credential.Archetype == "B" && credential.Refresher,
		credential.Archetype == "C" && credential.Provenance == "measured" && credential.Secret != "":
		return "", true
	}
	return entry.ProductName + " cannot be seeded into a hosted runner yet", false
}

func (p livePairing) Secret(name string) (string, bool) {
	entry, found := p.doc.For(name)
	if !found || entry.Credential.Secret == "" {
		return "", false
	}
	return entry.Credential.Secret, true
}

func (p livePairing) NeedsRefresher(runner, name, endpoint string) bool {
	if runner != "github-hosted" || (endpoint != "" && endpoint != "null") {
		return false
	}
	entry, found := p.doc.For(name)
	return found && entry.Credential.Refresher
}

// fakeSource is the CrossRev checkout's own git.
type fakeSource struct {
	sha    string
	shaErr error
	ref    string
	refErr error
}

func (s fakeSource) SHA(context.Context) (string, error) { return s.sha, s.shaErr }
func (s fakeSource) Ref(context.Context) (string, error) { return s.ref, s.refErr }

// diskFS is a real directory. It is deliberately not a map: the tests that
// prove nothing is written compare the directory before and after, and a map
// would only prove that the fake was never asked to write.
type diskFS struct{ root string }

func (d diskFS) Exists(path string) bool {
	_, err := os.Stat(filepath.Join(d.root, path))
	return err == nil
}

// tree is every path under root with its bytes, for a before-and-after
// comparison.
func tree(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if entry.IsDir() {
			out[relative+"/"] = ""
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		out[relative] = string(body)
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return out
}

// capture is an IO writing into one buffer with no colour, which is the shell
// measured under NO_COLOR=1.
func capture() (*ui.IO, *bytes.Buffer) {
	var buffer bytes.Buffer
	return &ui.IO{Out: &buffer, Err: &buffer, Palette: ui.Plain()}, &buffer
}

// loaded builds the Request the rest of the tests vary. Every port is wired,
// because a nil one is a wiring fault Resolve refuses rather than a default it
// fills in.
func request(t *testing.T, configuration string) initcmd.Request {
	t.Helper()
	out, _ := capture()
	return initcmd.Request{
		Repo:    slug(t),
		Show:    showing(map[string]string{".github/crossrev.yml": configuration}),
		Harness: descriptor(t),
		GitHub:  &fakeGitHub{slug: slug(t), ownerType: "User"},
		Apps:    fakeApps{},
		Pairing: fakePairing{},
		Source:  fakeSource{sha: strings.Repeat("a", 40), ref: "v9.9.9"},
		Files:   diskFS{root: t.TempDir()},
		Out:     out,
	}
}

// sorted is a stable rendering for a set assertion.
func sorted(values []string) string {
	copied := append([]string(nil), values...)
	sort.Strings(copied)
	return strings.Join(copied, " ")
}
