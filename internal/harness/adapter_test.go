package harness_test

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/exec"
	"github.com/carlosboeing/crossrev/internal/harness"
)

// Every harness the descriptor names has an adapter, and nothing else does.
//
// lib/harnesses.sh:106-121 checks this against the filesystem in both
// directions: a named harness with no lib/adapters/<name>.sh is a refusal, and
// an adapter file with no descriptor entry is a refusal. A compiled binary has
// no directory to read, so the registry in adapter.go is the directory and this
// is the check.
func TestEveryNamedHarnessHasAnAdapter(t *testing.T) {
	doc := descriptors(t)

	adapters, err := harness.Adapters(doc)
	if err != nil {
		t.Fatalf("building the adapters: %v", err)
	}
	var names []string
	for _, adapter := range adapters {
		names = append(names, adapter.Name())
	}
	if !reflect.DeepEqual(names, doc.Names()) {
		t.Errorf("Adapters() = %v, want %v in descriptor order", names, doc.Names())
	}
}

func TestUnknownHarnessHasNoAdapter(t *testing.T) {
	doc := descriptors(t)

	if _, known := harness.For(doc, "not-a-harness"); known {
		t.Error("a name the descriptor does not carry has no adapter")
	}
	// The one name the descriptor carries and does not drive.
	if _, known := harness.For(doc, "kimi"); known {
		t.Error("kimi is reached through another adapter, so it has none of its own")
	}
}

// Every adapter refuses a CLI it cannot find, with the two halves a ui_die
// prints and the sentinel a caller can ask about.
func TestNotInstalledRefusalsNameTheHarnessAndTheFix(t *testing.T) {
	doc := descriptors(t)
	adapters, err := harness.Adapters(doc)
	if err != nil {
		t.Fatalf("building the adapters: %v", err)
	}

	for _, adapter := range adapters {
		t.Run(adapter.Name(), func(t *testing.T) {
			refusal := adapter.NotInstalled()
			entry, _ := doc.For(adapter.Name())
			want := "the " + entry.Binary + " CLI is not installed, and this leg is configured to use it"
			if refusal.Reason != want {
				t.Errorf("Reason = %q, want %q", refusal.Reason, want)
			}
			if !strings.Contains(refusal.Action, "point this leg at another harness with --harness.") {
				t.Errorf("Action does not offer the other harnesses: %q", refusal.Action)
			}
			if !errorIs(refusal, harness.ErrNotInstalled) {
				t.Error("the refusal does not report itself as ErrNotInstalled")
			}
		})
	}
}

// The codex adapter is the one that reads its install hint out of the
// descriptor rather than spelling a URL (lib/adapters/codex.sh:24).
func TestCodexInstallHintComesFromTheDescriptor(t *testing.T) {
	doc := descriptors(t)
	entry, _ := doc.For("codex")
	adapter, _ := harness.For(doc, "codex")

	if !strings.Contains(adapter.NotInstalled().Action, entry.Install.Hint) {
		t.Errorf("the codex refusal does not name %q: %q", entry.Install.Hint, adapter.NotInstalled().Action)
	}
}

// Only one adapter can reach a named endpoint, because a named endpoint is
// Anthropic-compatible. The other four refuse and say where to take it.
func TestOnlyOneAdapterReachesANamedEndpoint(t *testing.T) {
	doc := descriptors(t)
	endpoint := harness.Endpoint{Name: "an-endpoint", URL: "https://example.invalid", TokenVar: "A_TOKEN", Token: "t"}

	for _, name := range doc.Names() {
		t.Run(name, func(t *testing.T) {
			adapter, _ := harness.For(doc, name)
			inv := invocation(t, adapter.Name(), false)
			inv.Endpoint = endpoint

			_, err := adapter.Spec(inv)
			if name == doc.EndpointHost() {
				if err != nil {
					t.Fatalf("the endpoint host refused a named endpoint: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("the adapter accepted a named endpoint it cannot reach")
			}
			if !errorIs(err, harness.ErrEndpointUnsupported) {
				t.Fatalf("err = %v, want ErrEndpointUnsupported", err)
			}
			var refusal *harness.Refusal
			if !asRefusal(err, &refusal) {
				t.Fatal("the error is not a Refusal")
			}
			want := "the " + name + " adapter cannot use the endpoint 'an-endpoint'"
			if refusal.Reason != want {
				t.Errorf("Reason = %q, want %q", refusal.Reason, want)
			}
			if !strings.Contains(refusal.Action, "Use harness: "+doc.EndpointHost()+" with endpoint: an-endpoint") {
				t.Errorf("Action does not say where to take it: %q", refusal.Action)
			}
		})
	}
}

// A model-facing Spec that still carried a forge credential would be refused by
// exec.Run. childEnv is what stops a leg getting there, and it takes the list
// from internal/cred rather than keeping a second one.
func TestEveryAdapterStripsTheForgeCredentialsAndForeignVendorCredentials(t *testing.T) {
	doc := descriptors(t)
	credentials := doc.Credentials()

	// Everything a workflow might hand one process, plus the four forge names.
	var supplied []string
	supplied = append(supplied, "PATH=/usr/bin", "HOME=/tmp")
	for _, name := range exec.ForgeCredentialNames() {
		supplied = append(supplied, name+"=secret")
	}
	for _, name := range credentials.VendorNames() {
		supplied = append(supplied, name+"=secret")
	}

	for _, name := range doc.Names() {
		t.Run(name, func(t *testing.T) {
			adapter, _ := harness.For(doc, name)
			inv := invocation(t, name, false)
			inv.Env = supplied

			spec, err := adapter.Spec(inv)
			if err != nil {
				t.Fatalf("building the spec: %v", err)
			}

			held := map[string]bool{}
			for _, entry := range spec.Env {
				variable, _, _ := strings.Cut(entry, "=")
				held[variable] = true
			}
			for _, forge := range exec.ForgeCredentialNames() {
				if held[forge] {
					t.Errorf("the child would hold %s, and it reads attacker-controlled text", forge)
				}
			}
			keep := credentials.For(name).Credential.EnvKeep
			for _, vendor := range credentials.VendorNames() {
				if held[vendor] && !slices.Contains(keep, vendor) {
					t.Errorf("the child would hold %s, which belongs to a harness that is not running", vendor)
				}
				if !held[vendor] && slices.Contains(keep, vendor) {
					t.Errorf("the child lost %s, which this harness keeps", vendor)
				}
			}
			if !held["PATH"] || !held["HOME"] {
				t.Error("the child lost a variable nothing asked to strip")
			}
			if spec.Stdin != nil {
				t.Error("stdin has to be at EOF; the CLIs block on an open one")
			}
			if spec.Dir != inv.Workdir {
				t.Errorf("Dir = %q, want the checkout", spec.Dir)
			}
			entry, _ := doc.For(name)
			if spec.Path != entry.Binary {
				t.Errorf("Path = %q, want the descriptor's binary %q", spec.Path, entry.Binary)
			}
		})
	}
}

// The descriptor says how each harness is handed a schema, and each adapter has
// to do what its own entry says.
func TestSchemaStyleMatchesTheDescriptor(t *testing.T) {
	doc := descriptors(t)

	for _, name := range doc.Names() {
		t.Run(name, func(t *testing.T) {
			adapter, _ := harness.For(doc, name)
			inv := invocation(t, name, false)

			spec, err := adapter.Spec(inv)
			if err != nil {
				t.Fatalf("building the spec: %v", err)
			}
			entry, _ := doc.For(name)
			switch entry.SchemaStyle {
			case "inline":
				if !slices.Contains(spec.Args, inv.Schema.Text) {
					t.Error("an inline harness has to be handed the schema text")
				}
				if slices.Contains(spec.Args, inv.Schema.Path) {
					t.Error("an inline harness must not be handed the schema path")
				}
			case "path":
				if !slices.Contains(spec.Args, inv.Schema.Path) {
					t.Error("a path harness has to be handed the schema path")
				}
				if slices.Contains(spec.Args, inv.Schema.Text) {
					t.Error("a path harness must not be handed the schema text")
				}
			case "prompt":
				if slices.Contains(spec.Args, inv.Schema.Path) {
					t.Error("a prompt harness takes no schema flag")
				}
				last := spec.Args[len(spec.Args)-1]
				if !strings.Contains(last, inv.Schema.Text) {
					t.Error("a prompt harness carries the schema inside the prompt")
				}
			default:
				t.Fatalf("the descriptor names an unknown schema style %q", entry.SchemaStyle)
			}
		})
	}
}

// Every adapter invokes a writing leg differently from a reading one, and none
// of the write grants reaches for a blanket bypass.
//
// This is the class assertion of tests/test-permissions.sh:121-137, over every
// adapter rather than over a list, so an adapter added without the distinction
// is caught here rather than in review.
func TestEveryAdapterDistinguishesAWritingLegFromAReadingOne(t *testing.T) {
	doc := descriptors(t)

	for _, name := range doc.Names() {
		t.Run(name, func(t *testing.T) {
			adapter, _ := harness.For(doc, name)

			writing := legInvocation(t, adapter, name, true)
			reading := legInvocation(t, adapter, name, false)
			if reflect.DeepEqual(writing, reading) {
				t.Error("a writing leg and a reading leg are invoked identically")
			}

			// The line worth holding is between editing files and running
			// arbitrary commands. Every harness offers a mode on the wrong side
			// of it, and none of them is what a resolve leg needs.
			for _, forbidden := range []string{"bypassPermissions", "danger-full-access", "--dangerously"} {
				if strings.Contains(strings.Join(writing, " "), forbidden) {
					t.Errorf("the write grant reaches for %s", forbidden)
				}
			}
		})
	}
}

// legInvocation is one adapter's whole invocation as text: its arguments, plus
// the permission block for the one harness whose grant rides in a config file
// rather than in a flag (tests/test-permissions.sh:78-82).
func legInvocation(t *testing.T, adapter harness.Adapter, name string, write bool) []string {
	t.Helper()
	inv := invocation(t, name, write)
	spec, err := adapter.Spec(inv)
	if err != nil {
		t.Fatalf("building the spec: %v", err)
	}
	shape := slices.Clone(spec.Args)
	for _, entry := range spec.Env {
		variable, value, _ := strings.Cut(entry, "=")
		if variable != "OPENCODE_CONFIG" {
			continue
		}
		config, err := os.ReadFile(value) //nolint:gosec // the adapter wrote this path
		if err != nil {
			t.Fatalf("reading the isolation config: %v", err)
		}
		shape = append(shape, string(config))
	}
	return shape
}

// --- the shared fixture ------------------------------------------------------

// invocation is a leg every adapter can serve: a review prompt the stubs
// recognise, a schema in both forms, and a real checkout.
func invocation(t *testing.T, name string, write bool) harness.Invocation {
	t.Helper()
	dir := t.TempDir()

	prompt := reviewPrompt
	if write {
		prompt = resolvePrompt
	}
	promptPath := filepath.Join(dir, "prompt.txt")
	if err := os.WriteFile(promptPath, []byte(prompt), 0o600); err != nil {
		t.Fatalf("writing the prompt: %v", err)
	}
	schemaPath := filepath.Join(dir, "schema.json")
	if err := os.WriteFile(schemaPath, []byte(schemaText), 0o600); err != nil {
		t.Fatalf("writing the schema: %v", err)
	}
	workdir := filepath.Join(dir, "checkout")
	if err := os.MkdirAll(workdir, 0o700); err != nil {
		t.Fatalf("making the checkout: %v", err)
	}
	scratch := filepath.Join(dir, "scratch")
	if err := os.MkdirAll(scratch, 0o700); err != nil {
		t.Fatalf("making the scratch directory: %v", err)
	}

	model := "a-model"
	if name == "opencode" {
		// The real CLI takes provider/model rather than a bare id, and
		// tests/stub/opencode refuses a bare one.
		model = "opencode/a-model"
	}
	return harness.Invocation{
		Prompt:      harness.File{Path: promptPath, Text: prompt},
		Schema:      harness.File{Path: schemaPath, Text: schemaText},
		Workdir:     workdir,
		Model:       model,
		Effort:      "high",
		Write:       write,
		Env:         []string{"PATH=" + os.Getenv("PATH")},
		Scratch:     scratch,
		PayloadPath: filepath.Join(dir, "payload.json"),
		CodexHome:   filepath.Join(dir, "codex-home"),
	}
}

// The two sentences the stubs match on to tell a review leg from a resolve leg.
const (
	reviewPrompt  = "You are the review leg. Read the diff."
	resolvePrompt = "You are the resolve leg. Fix what is real."
	schemaText    = `{"type":"object","properties":{"verdict":{"type":"string"}}}`
)

// stubDir is tests/stub, where the offline suite keeps a fake CLI for every
// harness. They are executable specifications: each asserts the flag order and
// the permission shape its real CLI needs, and exits 96 on a violation.
const stubDir = repoRoot + "/tests/stub"

// runAgainstStub starts a spec with tests/stub ahead of the PATH.
//
// The stub is the specification, so a Spec that reaches it and comes back with
// exit 96 has failed the same contract a real CLI would have failed silently.
func runAgainstStub(t *testing.T, spec exec.Spec, extraEnv ...string) exec.Result {
	t.Helper()

	stubs, err := filepath.Abs(stubDir)
	if err != nil {
		t.Fatalf("locating the stubs: %v", err)
	}
	// os/exec resolves Spec.Path on the PATH of the CALLING process, which is
	// what `env … <cli>` does too: env strips names from the child's
	// environment and still resolves the program from its own.
	t.Setenv("PATH", stubs+string(os.PathListSeparator)+os.Getenv("PATH"))
	spec.Env = append(slices.Clone(spec.Env), extraEnv...)

	return exec.NewOSRunner().Run(context.Background(), spec)
}

// payloadFile writes a canned harness answer and answers the environment entry
// naming it.
func payloadFile(t *testing.T, variable, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "payload.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("writing the canned payload: %v", err)
	}
	return variable + "=" + path
}

// cannedPayload is what every stub hands back when a test names one.
const cannedPayload = `{"verdict":"issues-remain"}`

func errorIs(err error, target error) bool {
	type isser interface{ Is(error) bool }
	if matcher, ok := err.(isser); ok {
		return matcher.Is(target)
	}
	return err == target
}

func asRefusal(err error, into **harness.Refusal) bool {
	refusal, ok := err.(*harness.Refusal)
	if ok {
		*into = refusal
	}
	return ok
}
