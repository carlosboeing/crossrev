package main

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/exec"
	"github.com/carlosboeing/crossrev/internal/forge/ghexec"
	"github.com/carlosboeing/crossrev/internal/harness"
)

// This file pins the ADR 0001 boundary at the composition root: which runner
// every service is handed, and which names every environment allowlist carries.
//
// Both were unheld. A mutation review swapped the runner at ten wiring sites
// and renamed a credential on three allowlists, and every Go test and every
// CLI-driven shell suite still passed.
//
// Two instruments, because one cannot reach everything here. The refusal cases
// below run the boundary: they take the runner a service actually received and
// ask it to start a child holding a forge credential, which is the one
// behaviour the two runners differ on. That reaches the runners open() builds
// and the two legs, and no further — `crossrev auth`, `crossrev init` and
// `crossrev doctor` build their services inside the function that then runs the
// whole command, so there is no value to interrogate without running it. The
// source rule at the end covers those, and covers the reachable sites a second
// time.

// forgeCredentialSentinel is the value the refusal cases put in GH_TOKEN. It is
// not a credential, and no child ever receives it: every case below asserts
// that nothing started.
const forgeCredentialSentinel = "not-a-real-token"

// credentialDecision asks runner to start a program that does not exist, with
// GH_TOKEN in the child's environment, and answers with what the runner decided.
//
// The two runners answer this differently and neither starts anything. The
// model-facing one reads Spec.Env before it builds a command and refuses with
// exec.ErrForgeCredential (internal/exec/osrunner.go:72-80). The orchestrator
// one has no such check, reaches the exec, and fails to find the program. So
// the error type reads which of the two a wiring handed over, by the single
// behaviour the choice between them exists for.
//
// The Bash side is the same fact stated as a strip-list: lib/adapters/claude.sh:72
// runs the harness under `env -u GH_TOKEN -u GITHUB_TOKEN -u GH_ENTERPRISE_TOKEN
// -u GITHUB_ENTERPRISE_TOKEN`, and nothing strips them from git or `gh`.
func credentialDecision(t *testing.T, runner exec.Runner) error {
	t.Helper()
	if runner == nil {
		t.Fatal("the wiring handed over a nil runner")
	}
	result := runner.Run(t.Context(), exec.Spec{
		Path: filepath.Join(t.TempDir(), "no-such-program"),
		Env:  []string{"GH_TOKEN=" + forgeCredentialSentinel},
	})
	if result.Err == nil {
		t.Fatalf("a program that does not exist started and exited %d", result.ExitCode)
	}
	if len(result.Stdout) != 0 || len(result.Stderr) != 0 {
		t.Fatalf("a child ran: stdout %q, stderr %q", result.Stdout, result.Stderr)
	}
	return result.Err
}

// deps.model is the model-facing runner (cmd/crossrev/deps.go:97).
func TestTheModelRunnerInDepsRefusesAForgeCredential(t *testing.T) {
	d := open(newIO(false), harness.Document{})
	if err := credentialDecision(t, d.model); !errors.Is(err, exec.ErrForgeCredential) {
		t.Fatalf("deps.model did not refuse a child holding GH_TOKEN, so it is not the model-facing runner: %v", err)
	}
}

// deps.orchestrator is the runner git and `gh` are started through
// (cmd/crossrev/deps.go:92), and both authenticate with the credential. The
// shell strips it from a harness and from nothing else.
func TestTheOrchestratorRunnerInDepsCarriesAForgeCredential(t *testing.T) {
	d := open(newIO(false), harness.Document{})
	if err := credentialDecision(t, d.orchestrator); errors.Is(err, exec.ErrForgeCredential) {
		t.Fatalf("deps.orchestrator refused a child holding GH_TOKEN, so git and gh cannot authenticate: %v", err)
	}
}

// The review leg starts the reviewing harness, which is the model-facing
// process of ADR 0001 (leg_review, lib/run.sh:913; the strip is at
// lib/adapters/claude.sh:72).
func TestTheReviewLegStartsItsHarnessThroughTheModelRunner(t *testing.T) {
	d := open(newIO(false), harness.Document{})
	if err := credentialDecision(t, reviewLeg(d, nil, nil).Runner); !errors.Is(err, exec.ErrForgeCredential) {
		t.Fatalf("the review leg's runner did not refuse a child holding GH_TOKEN: %v", err)
	}
}

// The resolve leg is the same boundary (leg_resolve, lib/run.sh:1730).
func TestTheResolveLegStartsItsHarnessThroughTheModelRunner(t *testing.T) {
	d := open(newIO(false), harness.Document{})
	if err := credentialDecision(t, resolveLeg(d, nil, nil).Runner); !errors.Is(err, exec.ErrForgeCredential) {
		t.Fatalf("the resolve leg's runner did not refuse a child holding GH_TOKEN: %v", err)
	}
}

// `crossrev auth` reads and writes GitHub through `gh`, which cannot
// authenticate without the credential (lib/auth.sh; the shell runs `gh` with
// its own environment and strips nothing).
//
// PATH is emptied so `gh` is not found: with either runner nothing starts, and
// the error says which runner decided. A model-facing one refuses before the
// exec; the orchestrator's reaches it and reports the program missing.
func TestTheAuthGhClientCarriesTheForgeCredential(t *testing.T) {
	t.Setenv("GH_TOKEN", forgeCredentialSentinel)
	t.Setenv("PATH", t.TempDir())

	_, err := authCommands(newIO(false), harness.Document{}).GH.DetectOwner(t.Context())
	if err == nil {
		t.Fatal("gh is not on this PATH, so the read must have failed")
	}
	if errors.Is(err, exec.ErrForgeCredential) {
		t.Fatalf("auth's gh client was handed the model-facing runner, so it cannot authenticate: %v", err)
	}
}

// Env is where an App's key, its metadata and the token ledger are resolved
// from, and every one of those paths is an environment read
// (internal/app/status.go:31-34). A nil reader is not a smaller answer; it is a
// panic on the first `crossrev auth` action that resolves a path.
func TestTheAuthCommandsReadTheProcessEnvironment(t *testing.T) {
	env := authCommands(newIO(false), harness.Document{}).Env
	if env == nil {
		t.Fatal("app.Commands.Env is nil, so no App path can be resolved")
	}
	if _, ok := env.(osEnv); !ok {
		t.Fatalf("app.Commands.Env is %T, and the process environment is osEnv", env)
	}
}

// Harnesses is the descriptor `auth refresh` reads a credential's shape out of
// and `auth status` reads a re-issue command out of
// (internal/app/status.go:39-41). The descriptor the process parsed is the one
// that has to arrive: an empty document names no harness at all.
func TestTheAuthCommandsGetTheParsedDescriptor(t *testing.T) {
	doc, err := harness.Descriptors()
	if err != nil {
		t.Fatalf("the compiled-in descriptor does not parse: %v", err)
	}
	if reflect.DeepEqual(doc, harness.Document{}) {
		t.Fatal("the compiled-in descriptor is empty, so this case would pass vacuously")
	}
	if got := authCommands(newIO(false), doc).Harnesses; !reflect.DeepEqual(got, doc) {
		t.Error("app.Commands.Harnesses is not the descriptor the process parsed")
	}
}

// --- the environment allowlists --------------------------------------------

// The three allowlists cmd/crossrev owns, each spelled out again here rather
// than read from the variable it pins.
//
// A test that read the list from the code it checks agrees with whatever the
// code says, and every wrong version of a list is still a list. This is the
// second, independent statement of each, which is the model
// internal/archtest/ghenv_test.go already uses for the seventeen names `gh`
// inherits.
//
// Why each list holds what it holds:
//
//   - gitEnvironment. The shell runs git with its own environment and strips
//     nothing, so the four forge credential names are on it: a push over https
//     authenticates with whatever credential helper the environment configures.
//     The git configuration and TLS names are the ones the offline suite sets,
//     and a git that inherited neither would read the developer's own config.
//   - ghSecretEnvironment. `gh secret set` is run plainly at lib/init.sh:848,
//     so this is the same seventeen names internal/forge/ghexec gives every
//     other `gh`. The case below also asserts that equality rather than only
//     the spelling.
//   - harnessEnvironment. The Bash child inherits everything minus four names
//     (lib/adapters/claude.sh:72); the port inverts that into an allowlist, for
//     the reason cmd/crossrev/legs.go:27-32 gives. So the names are not a
//     transcription of a shell list — they are the harness homes and vendor
//     credentials lib/harnesses.json declares (the `secret`, `env_names` and
//     `staging.env` members at :41-42, :74-79, :140-145 and :174-178), the
//     endpoint pair lib/adapters/claude.sh:91 sets, the OpenCode pair
//     lib/adapters/opencode.sh:184 sets, and the terminal, locale, proxy and
//     TLS names a CLI renders and reaches a vendor with.
var environmentAllowlists = []struct {
	name string
	got  []string
	want []string
}{
	{
		name: "gitEnvironment",
		got:  gitEnvironment,
		want: []string{
			"PATH", "HOME", "XDG_CONFIG_HOME",
			"GIT_CONFIG_GLOBAL", "GIT_CONFIG_SYSTEM", "GIT_CONFIG_NOSYSTEM",
			"GIT_TERMINAL_PROMPT", "GIT_SSL_CAINFO", "GIT_SSL_CAPATH", "GIT_ASKPASS",
			"SSH_AUTH_SOCK",
			"GH_TOKEN", "GITHUB_TOKEN", "GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN",
			"SSL_CERT_FILE", "SSL_CERT_DIR",
			"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "no_proxy",
		},
	},
	{
		name: "ghSecretEnvironment",
		got:  ghSecretEnvironment,
		want: []string{
			"PATH", "HOME", "XDG_CONFIG_HOME", "GH_CONFIG_DIR", "GH_HOST",
			"GH_TOKEN", "GITHUB_TOKEN", "GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN",
			"SSL_CERT_FILE", "SSL_CERT_DIR",
			"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "no_proxy",
		},
	},
	{
		name: "harnessEnvironment",
		got:  harnessEnvironment,
		want: []string{
			"PATH", "HOME", "SHELL", "USER", "LOGNAME", "TMPDIR",
			"LANG", "LC_ALL", "TERM", "COLORTERM", "NO_COLOR",
			"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME",
			"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_BASE_URL",
			"CLAUDE_CODE_OAUTH_TOKEN",
			"CROSSREV_CODEX_AUTH", "CODEX_HOME",
			"CROSSREV_GROK_AUTH", "GROK_HOME",
			"CROSSREV_OPENCODE_AUTH", "OPENCODE_CONFIG", "OPENCODE_CONFIG_DIR",
			"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "no_proxy",
			"SSL_CERT_FILE", "SSL_CERT_DIR",
		},
	},
}

func TestEveryEnvironmentAllowlistCarriesTheNamesItIsDeclaredWith(t *testing.T) {
	for _, list := range environmentAllowlists {
		t.Run(list.name, func(t *testing.T) {
			got := slices.Sorted(slices.Values(list.got))
			want := slices.Sorted(slices.Values(list.want))
			if slices.Equal(got, want) {
				return
			}
			for _, name := range want {
				if !slices.Contains(got, name) {
					t.Errorf("%s no longer carries %s", list.name, name)
				}
			}
			for _, name := range got {
				if !slices.Contains(want, name) {
					t.Errorf("%s carries %s, which this rule does not declare", list.name, name)
				}
			}
		})
	}
}

// The four credential names are on git's list and on `gh secret`'s, and on no
// harness's. This is the same three lists read for the one property that makes
// them a security boundary rather than a convenience.
func TestTheForgeCredentialsAreOnTheOrchestratorsListsAndNoHarnesss(t *testing.T) {
	for _, credential := range exec.ForgeCredentialNames() {
		if !slices.Contains(gitEnvironment, credential) {
			t.Errorf("gitEnvironment drops %s, so a push over https cannot authenticate", credential)
		}
		if !slices.Contains(ghSecretEnvironment, credential) {
			t.Errorf("ghSecretEnvironment drops %s, so `gh secret` cannot authenticate", credential)
		}
		if slices.Contains(harnessEnvironment, credential) {
			t.Errorf("harnessEnvironment carries %s into a model-facing process", credential)
		}
	}
}

// ghSecretEnvironment says of itself that it is the allowlist
// internal/forge/ghexec builds its client with, spelled again because
// SecretStore takes an environment rather than reading one
// (cmd/crossrev/initcmd.go:79-81). Two copies of one list drift, and
// internal/archtest/ghenv_test.go pins one of them.
func TestTheGhSecretAllowlistIsTheGhAllowlist(t *testing.T) {
	got := slices.Sorted(slices.Values(ghSecretEnvironment))
	want := slices.Sorted(slices.Values(ghexec.EnvironmentNames()))
	if !slices.Equal(got, want) {
		t.Errorf("ghSecretEnvironment is %q, and internal/forge/ghexec passes %q", got, want)
	}
}

// --- which runner every service is handed ----------------------------------

// runnerWiring is every place in this package that names one of the two
// runners, and which one it must name.
//
// The refusal cases above reach four of these. The rest are built inside the
// function that then runs the whole command, so this rule reads the source
// instead: for each site it records the enclosing function and what the runner
// is being handed to, which is a name that survives an edit anywhere else in
// the file. A site that changes runner fails on the value; a site added without
// a row here fails as undeclared, which is the case a table cannot otherwise
// see.
//
// Every value is the security decision of ADR 0001 restated once per site:
// `git` and `gh` hold the forge credential and a harness may never see one.
var runnerWiring = map[string]string{
	// `gh` for `crossrev auth`, and the opener it hands a URL to. The opener
	// is not model-facing (lib/auth.sh:142-146).
	"authCommands: app.NewGH()":      "orchestrator",
	"authCommands: app.NewBrowser()": "orchestrator",

	// The forge provider: every read and write is `gh`.
	"forgeClient: ghexec.New()": "orchestrator",

	// `crossrev init`. The first three are `gh` — the account read, the
	// secret and branch-protection reads, and `gh secret set` at
	// lib/init.sh:848. The fourth starts a harness to seed its credential,
	// and is the one model-facing child in the command.
	"initCommand: app.NewGH()":                 "orchestrator",
	"initCommand: secretLister.runner":         "orchestrator",
	"initCommand: initcmd.SecretStore.Runner":  "orchestrator",
	"initCommand: initcmd.SeedCommands.Runner": "model",

	// The two legs: each starts the harness that reviews or resolves.
	"reviewLeg: review.Leg.Runner":   "model",
	"resolveLeg: resolve.Leg.Runner": "model",

	// `crossrev doctor`. The three `gh` identity probes are the orchestrator
	// asking GitHub who it is; a model-facing runner would refuse them and
	// report the operator unauthenticated (cmd/crossrev/simple.go:26-29).
	"doctor: preflight.Checker.Runner": "orchestrator",
}

func TestEveryServiceIsHandedTheRunnerItIsDeclaredWith(t *testing.T) {
	found := runnerWiringSites(t)

	for site, want := range runnerWiring {
		got, declared := found[site]
		if !declared {
			t.Errorf("%s is gone; this rule declares it takes the %s runner", site, want)
			continue
		}
		if got != want {
			t.Errorf("%s takes the %s runner, and it must take the %s one", site, got, want)
		}
	}
	for site, got := range found {
		if _, declared := runnerWiring[site]; !declared {
			t.Errorf("%s takes the %s runner and no rule declares which it may take", site, got)
		}
	}
}

// runnerWiringSites reads every `.orchestrator` and `.model` selector in this
// package's own source, keyed by where it is.
func runnerWiringSites(t *testing.T) map[string]string {
	t.Helper()

	sources, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("the package's own sources could not be listed: %v", err)
	}

	found := make(map[string]string)
	for _, source := range sources {
		if strings.HasSuffix(source, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), source, nil, 0)
		if err != nil {
			t.Fatalf("%s does not parse: %v", source, err)
		}

		var ancestors []ast.Node
		ast.Inspect(file, func(node ast.Node) bool {
			if node == nil {
				ancestors = ancestors[:len(ancestors)-1]
				return false
			}
			if runner := runnerName(node); runner != "" {
				site := describeWiring(ancestors)
				if site == "" {
					t.Errorf("%s names the %s runner somewhere this rule cannot describe", source, runner)
				} else if was, seen := found[site]; seen && was != runner {
					t.Errorf("%s names both runners", site)
				} else {
					found[site] = runner
				}
			}
			ancestors = append(ancestors, node)
			return true
		})
	}
	return found
}

// runnerName answers "orchestrator" or "model" for a selector naming one of the
// two deps fields, and the empty string for anything else.
func runnerName(node ast.Node) string {
	selector, ok := node.(*ast.SelectorExpr)
	if !ok {
		return ""
	}
	switch selector.Sel.Name {
	case "orchestrator", "model":
		return selector.Sel.Name
	}
	return ""
}

// describeWiring names the site a runner was handed to, out of the ancestors of
// the selector that names it: the enclosing function, then either the struct
// field it initialises or the function it is an argument to.
func describeWiring(ancestors []ast.Node) string {
	var enclosing string
	for _, node := range ancestors {
		if function, ok := node.(*ast.FuncDecl); ok {
			enclosing = function.Name.Name
		}
	}
	if enclosing == "" || len(ancestors) < 2 {
		return ""
	}

	switch parent := ancestors[len(ancestors)-1].(type) {
	case *ast.KeyValueExpr:
		literal, ok := ancestors[len(ancestors)-2].(*ast.CompositeLit)
		if !ok || literal.Type == nil {
			return ""
		}
		return enclosing + ": " + types.ExprString(literal.Type) + "." + types.ExprString(parent.Key)
	case *ast.CallExpr:
		return enclosing + ": " + types.ExprString(parent.Fun) + "()"
	}
	return ""
}
