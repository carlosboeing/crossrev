package ghexec_test

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/exec"
	"github.com/carlosboeing/crossrev/internal/forge"
	"github.com/carlosboeing/crossrev/internal/forge/ghexec"
)

func testSlug(t *testing.T) core.Slug {
	t.Helper()
	s, err := core.ParseSlug("acme/widget")
	if err != nil {
		t.Fatalf("parsing the fixture slug: %v", err)
	}
	return s
}

// client is a Client over a recorder, with a filter that changes nothing.
func client(t *testing.T, results ...exec.Result) (*ghexec.Client, *recorder) {
	t.Helper()
	r := &recorder{results: results}
	return ghexec.New(r, passthrough{}, ghexec.WithEnv([]string{"PATH=/usr/bin", "GH_TOKEN=redacted"})), r
}

// Every operation the interface declares is the orchestrator's own tool, and
// `gh` is the one child that legitimately holds a GitHub credential.
//
// Driven off forge.Forge by reflection rather than off a list written here. A
// list is a second copy of the interface, and the copy is what goes stale: a
// twenty-fifth operation added to the interface is compiled, shipped and
// unchecked in every respect — including whether it runs `gh` at all — while
// this test goes on passing over the twenty-four it names.
func TestEveryInvocationIsOrchestratorFacing(t *testing.T) {
	env := []string{"PATH=/usr/bin", "GH_TOKEN=redacted"}
	r := &recorder{}
	c := ghexec.New(r, passthrough{}, ghexec.WithEnv(env))

	forgeType := reflect.TypeFor[forge.Forge]()
	client := reflect.ValueOf(c)

	assertOverridesAreLive(t, forgeType)

	for i := range forgeType.NumMethod() {
		name := forgeType.Method(i).Name
		method := client.MethodByName(name)
		before := len(r.specs)

		method.Call(callArgumentsFor(t, name, method.Type()))

		if len(r.specs) == before {
			// A method that reached no process is a method this test says
			// nothing about. The fix is an entry in argumentOverrides that
			// gets it past its own guard, not an exemption.
			t.Errorf("%s started no process, so nothing about it was checked", name)
		}
	}

	if len(r.specs) == 0 {
		t.Fatal("no invocation was recorded")
	}
	for i, spec := range r.specs {
		where := fmt.Sprintf("call %d (%q)", i, strings.Join(spec.Args, " "))
		if spec.Path != "gh" {
			t.Errorf("%s runs %q; the credential travels because the child is gh and nothing else", where, spec.Path)
		}
		if spec.Stdin != nil {
			t.Errorf("%s writes stdin; every argument gh needs travels on the argv", where)
		}
		if !slices.Equal(spec.Env, env) {
			t.Errorf("%s builds its own environment %q rather than the client's", where, spec.Env)
		}
	}
}

// argumentOverrides carries the arguments a method needs to get past a guard of
// its own and reach gh. Everything else is called with a zero value, because
// what this test reads is the Spec and not the answer.
var argumentOverrides = map[string]map[int]any{
	// A run id that is not a number is never asked about (lib/github.sh:62).
	"WorkflowRunStatus": {2: "12345"},
	// A label name that cannot be put in a URL path is never asked for.
	"LabelColour":            {2: "bug"},
	"LabelEnsure":            {2: forge.Label{Name: "bug"}},
	"PullRequestLabelRemove": {3: "bug"},
}

func callArgumentsFor(t *testing.T, name string, signature reflect.Type) []reflect.Value {
	t.Helper()

	args := make([]reflect.Value, 0, signature.NumIn())
	for i := range signature.NumIn() {
		paramType := signature.In(i)
		if override, ok := argumentOverrides[name][i]; ok {
			args = append(args, reflect.ValueOf(override))
			continue
		}
		if i == 0 && paramType == reflect.TypeFor[context.Context]() {
			args = append(args, reflect.ValueOf(context.Background()))
			continue
		}
		args = append(args, reflect.Zero(paramType))
	}
	return args
}

// A stale override is a silent exemption: it names a method that no longer
// exists, or an argument that moved, and the method it was meant to cover goes
// back to being called with a zero value it guards against.
func assertOverridesAreLive(t *testing.T, forgeType reflect.Type) {
	t.Helper()

	for name, positions := range argumentOverrides {
		method, ok := forgeType.MethodByName(name)
		if !ok {
			t.Errorf("argumentOverrides names %s, which forge.Forge does not declare", name)
			continue
		}
		for position, value := range positions {
			if position >= method.Type.NumIn() {
				t.Errorf("argumentOverrides[%s] sets argument %d, which the method does not take", name, position)
				continue
			}
			if want := method.Type.In(position); reflect.TypeOf(value) != want {
				t.Errorf("argumentOverrides[%s][%d] is a %T, and the method takes a %s", name, position, value, want)
			}
		}
	}
}

// A model-facing runner refuses the environment this client builds, so the
// orchestrator runner is not decoration: the same Spec through NewOSRunner
// fails closed.
func TestTheEnvironmentThisClientBuildsWouldBeRefusedForAModel(t *testing.T) {
	c, r := client(t, out("acme/widget\n"))
	if _, err := c.RepoSlug(context.Background()); err != nil {
		t.Fatalf("RepoSlug: %v", err)
	}

	spec := r.only(t)
	res := exec.NewOSRunner().Run(context.Background(), spec)
	if res.Err == nil {
		t.Fatal("the same spec was accepted as model-facing; the credential guard did not see GH_TOKEN")
	}
	if !strings.Contains(res.Err.Error(), "GH_TOKEN") {
		t.Errorf("refusal = %v, want it to name GH_TOKEN", res.Err)
	}
}

// A nil runner is a wiring bug. Substituting NewOrchestratorRunner would start
// a real child rather than fail at the constructor.
func TestNewPanicsOnANilRunner(t *testing.T) {
	defer func() {
		rec := recover()
		if rec == nil {
			t.Fatal("ghexec.New(nil, …) accepted a nil runner; want a panic that names the constructor")
		}
		msg := fmt.Sprint(rec)
		if !strings.Contains(msg, "ghexec.New") {
			t.Errorf("panic %q does not name ghexec.New", msg)
		}
	}()
	ghexec.New(nil, passthrough{})
}

// A Client with no filter refuses every write rather than publishing text
// nothing inspected.
//
// Every write that publishes a body, not one of them: the five that can report
// the refusal do, and IssueCommentCreate — which returns nothing and so cannot
// — is the one where only the absence of a call says so. Its guard is the
// difference between posting nothing and posting an empty body.
func TestWritesRefuseWithoutAFilter(t *testing.T) {
	writes := map[string]func(t *testing.T, c *ghexec.Client) error{
		"CommentCreate": func(t *testing.T, c *ghexec.Client) error {
			_, err := c.CommentCreate(context.Background(), testSlug(t), 42, "hello")
			return err
		},
		"CommentEdit": func(t *testing.T, c *ghexec.Client) error {
			return c.CommentEdit(context.Background(), testSlug(t), 9001, "hello")
		},
		"ReviewCommentCreate": func(t *testing.T, c *ghexec.Client) error {
			_, err := c.ReviewCommentCreate(context.Background(), reviewComment(t, "hello"))
			return err
		},
		"ReviewReply": func(t *testing.T, c *ghexec.Client) error {
			return c.ReviewReply(context.Background(), testSlug(t), 42, 5000, "hello")
		},
		"IssueCreate": func(t *testing.T, c *ghexec.Client) error {
			_, err := c.IssueCreate(context.Background(), testSlug(t), "title", "hello", nil)
			return err
		},
	}

	for name, write := range writes {
		t.Run(name, func(t *testing.T) {
			r := &recorder{}
			if err := write(t, ghexec.New(r, nil)); err == nil {
				t.Errorf("%s published with no filter", name)
			}
			if len(r.specs) != 0 {
				t.Errorf("gh was invoked %v, want not at all", r.argvs())
			}
		})
	}

	// The write with no return value. Nothing reports the refusal, so the
	// absence of a call is the whole assertion — and without the guard this
	// posts an empty body rather than not posting.
	t.Run("IssueCommentCreate", func(t *testing.T) {
		r := &recorder{}
		ghexec.New(r, nil).IssueCommentCreate(context.Background(), testSlug(t), 31, "hello")
		if len(r.specs) != 0 {
			t.Errorf("gh was invoked %v, want not at all", r.argvs())
		}
	})
}

// The filtered body is what reaches gh, not the body the caller handed over.
func TestTheFilteredBodyIsWhatIsPublished(t *testing.T) {
	r := &recorder{results: []exec.Result{out("9001\n")}}
	c := ghexec.New(r, masking{})

	if _, err := c.CommentCreate(context.Background(), testSlug(t), 42, "sk-ant-secret"); err != nil {
		t.Fatalf("CommentCreate: %v", err)
	}
	if got := strings.Join(r.specs[0].Args, " "); strings.Contains(got, "sk-ant-secret") {
		t.Errorf("argv = %q, want the filtered body", got)
	}
	r.wantArgs(t, 0, "api", "--method", "POST", "repos/acme/widget/issues/42/comments",
		"-f", "body=masked", "--jq", ".id")
}

var _ forge.Forge = (*ghexec.Client)(nil)

// The default environment is the allowlist, and nothing else read it.
//
// Every other test in this package overrides it with WithEnv, so the list
// itself was unexercised: it could be emptied of all four credential names, or
// gain the one name that is excluded on purpose, and the suite would not
// notice. This is the test that reads it.
func TestTheDefaultEnvironmentIsTheAllowlist(t *testing.T) {
	// Set the whole allowlist plus the two names it must not carry, so what
	// reaches gh is decided by the allowlist and not by this process.
	required := []string{
		"PATH", "HOME",
		"XDG_CONFIG_HOME", "GH_CONFIG_DIR", "GH_HOST",
		"GH_TOKEN", "GITHUB_TOKEN", "GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN",
		"SSL_CERT_FILE", "SSL_CERT_DIR",
		"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY",
		"http_proxy", "https_proxy", "no_proxy",
	}
	// GH_REPO would retarget RepoSlug and every write after it; GH_FORCE_TTY
	// would put ANSI escapes in JSON these reads unmarshal.
	excluded := []string{"GH_REPO", "GH_FORCE_TTY", "AWS_SECRET_ACCESS_KEY", "ANTHROPIC_API_KEY"}

	for _, name := range append(append([]string{}, required...), excluded...) {
		t.Setenv(name, "value-of-"+name)
	}

	r := &recorder{}
	// No WithEnv: this is the constructor's own answer.
	c := ghexec.New(r, passthrough{})
	if _, err := c.RepoSlug(context.Background()); err == nil {
		t.Fatal("the recorder answered a slug it was never given")
	}

	got := map[string]string{}
	for _, entry := range r.only(t).Env {
		name, value, _ := strings.Cut(entry, "=")
		got[name] = value
	}

	for _, name := range required {
		if got[name] != "value-of-"+name {
			t.Errorf("%s is not in the environment gh receives; without it gh cannot authenticate, resolve its host or verify a certificate", name)
		}
	}
	for _, name := range excluded {
		if _, present := got[name]; present {
			t.Errorf("%s reached gh; it is excluded on purpose", name)
		}
	}
	if len(got) != len(required) {
		t.Errorf("gh received %d names and the allowlist declares %d; a name added to the allowlist is declared in this test too",
			len(got), len(required))
	}
}

// EnvironmentNames is the allowlist itself, for the one other caller that
// builds `gh` invocations.
//
// internal/app runs `gh` too, and it held a second copy of these seventeen
// names. Two lists of the same thing drift: a name added to one widens or
// narrows only that side's environment, nothing errors, and no test on either
// side reads the other. This accessor is what makes there be one list, and
// internal/archtest is where the two constructors are compared.
//
// The list is read from the constructor rather than written out here, so that
// this test cannot agree with a wrong answer that a matching copy would.
func TestEnvironmentNamesIsTheDefaultTheConstructorReads(t *testing.T) {
	declared := ghexec.EnvironmentNames()
	if len(declared) == 0 {
		t.Fatal("EnvironmentNames answered nothing; gh would inherit an empty environment")
	}

	// Set every declared name, plus two that are not on the list, so what
	// reaches gh is decided by the allowlist and not by this process.
	for _, name := range declared {
		t.Setenv(name, "value-of-"+name)
	}
	t.Setenv("GH_REPO", "acme/other")
	t.Setenv("CROSSREV_NOT_ON_THE_LIST", "present")

	r := &recorder{}
	// No WithEnv: this is the constructor's own answer.
	c := ghexec.New(r, passthrough{})
	if _, err := c.RepoSlug(context.Background()); err == nil {
		t.Fatal("the recorder answered a slug it was never given")
	}

	var got []string
	for _, entry := range r.only(t).Env {
		name, _, _ := strings.Cut(entry, "=")
		got = append(got, name)
	}
	slices.Sort(got)
	want := slices.Clone(declared)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("gh received %q and EnvironmentNames declares %q; the accessor is not the list the constructor reads", got, want)
	}
}

// The accessor answers a fresh slice each time, for the reason
// exec.ForgeCredentialNames does: an exported slice variable is writable from
// any package in the binary, and a package that shortened this one would narrow
// the environment `gh` receives everywhere at once — silently, because a `gh`
// that cannot find its config or its trust store fails as a network or auth
// error rather than as a missing name.
func TestEnvironmentNamesCannotBeWrittenThrough(t *testing.T) {
	got := ghexec.EnvironmentNames()
	first := got[0]
	got[0] = "OVERWRITTEN"
	if second := ghexec.EnvironmentNames(); second[0] != first {
		t.Errorf("EnvironmentNames()[0] = %q after a caller wrote to an earlier answer, want %q", second[0], first)
	}
}

// A call gh answered with no id at all is not a refusal.
//
// gh_comment_create dies when `gh` fails and never when the id is unreadable:
// it carries the empty string through (lib/github.sh:187-195). Refusing here
// stopped the watchdog's halt, which discards the id — the shell posts that
// comment with `>/dev/null` (lib/run.sh:3747). The two callers that need an id
// refuse for themselves on id == 0.
func TestCommentCreateAnswersNoIdRatherThanRefusing(t *testing.T) {
	r := &recorder{results: []exec.Result{out("")}}
	c := ghexec.New(r, masking{})

	id, err := c.CommentCreate(context.Background(), testSlug(t), 42, "hello")
	if err != nil {
		t.Fatalf("CommentCreate refused an unreadable id: %v", err)
	}
	if id != 0 {
		t.Errorf("id = %d, want 0", id)
	}
}
