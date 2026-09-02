package archtest_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/app"
	"github.com/carlosboeing/crossrev/internal/exec"
	"github.com/carlosboeing/crossrev/internal/forge/ghexec"
)

// ghAllowlist is the environment `gh` is allowed to inherit, declared here and
// not read from either constructor.
//
// A test that read the list from the code it checks agrees with whatever the
// code says, which is the failure this rule exists to catch: the list is a
// security boundary, and every wrong version of it is still a list. So this is
// the third, independent statement of the seventeen names, and the rule below
// is that both constructors match it exactly.
//
// Why each name is here is documented once, at internal/forge/ghexec's
// ghEnvironment. In short: PATH and HOME find `gh` and its own config;
// XDG_CONFIG_HOME and GH_CONFIG_DIR say where `gh auth login` left the host
// entry; GH_HOST names an enterprise host; the four token names are the
// credential `gh` authenticates with; SSL_CERT_FILE and SSL_CERT_DIR are the
// trust store crypto/x509 reads on Linux; and the proxy pair is read by Go's
// own http.ProxyFromEnvironment.
var ghAllowlist = []string{
	"PATH",
	"HOME",
	"XDG_CONFIG_HOME",
	"GH_CONFIG_DIR",
	"GH_HOST",
	"GH_TOKEN",
	"GITHUB_TOKEN",
	"GH_ENTERPRISE_TOKEN",
	"GITHUB_ENTERPRISE_TOKEN",
	"SSL_CERT_FILE",
	"SSL_CERT_DIR",
	"HTTP_PROXY",
	"HTTPS_PROXY",
	"NO_PROXY",
	"http_proxy",
	"https_proxy",
	"no_proxy",
}

// ghExcluded are the names that must not reach `gh`, and each is excluded for a
// reason rather than by omission.
//
// GH_REPO overrides which repository `gh repo view` answers for, so a run
// carrying it would resolve a repository other than the checkout and land every
// write there. GH_FORCE_TTY makes `gh` render for a terminal, so the JSON these
// calls unmarshal would come back with ANSI escapes in it. The other two are
// ordinary secrets: a name nobody put on the list does not reach the child.
var ghExcluded = []string{
	"GH_REPO",
	"GH_FORCE_TTY",
	"AWS_SECRET_ACCESS_KEY",
	"ANTHROPIC_API_KEY",
}

// ghSpecRecorder is a Runner that keeps the specs it was handed and starts
// nothing.
type ghSpecRecorder struct{ specs []exec.Spec }

func (r *ghSpecRecorder) Run(_ context.Context, spec exec.Spec) exec.Result {
	r.specs = append(r.specs, spec)
	return exec.Result{}
}

// ghPassthrough is the publish filter ghexec.New requires. Nothing here
// publishes a body; a nil filter would make every write refuse, which would be
// a confusing way for this test to fail.
type ghPassthrough struct{}

func (ghPassthrough) Filter(body string) (string, error) { return body, nil }
func (ghPassthrough) Mask(text string) string            { return text }

// Two packages start `gh`, and both must hand it the same environment.
//
// internal/forge/ghexec is the forge provider; internal/app runs `gh` for the
// facts an App's identity is checked against. internal/app held a copy of the
// allowlist, because the original was unexported, and nothing anywhere read
// both. A name added to one side changed one side's environment and no test
// failed — which is the whole defect, because the list is what decides whether
// `gh` authenticates, whether it verifies a certificate, and which repository
// it writes to.
//
// This rule is here rather than in either package because neither can hold it:
// tier-3 internal/app may import tier-2 internal/forge/ghexec, but ghexec may
// never import app, so only a test above both can compare the two.
//
// It reads what each constructor actually passed to a runner, not what either
// declares. A constructor that filtered the list afterwards, or that ignored it
// and built an environment of its own, would still fail here.
func TestBothGhConstructorsPassTheSameAllowlist(t *testing.T) {
	// Set every allowed name plus every excluded one, so what reaches `gh` is
	// decided by the allowlist and not by whatever this process happens to hold.
	for _, name := range slices.Concat(ghAllowlist, ghExcluded) {
		t.Setenv(name, "value-of-"+name)
	}

	// No WithEnv on either: this is each constructor's own answer. What either
	// call returns is another package's business — the recorder answers
	// nothing, so both are reading an empty result. What is read here is the
	// spec each one built, and ghEnvironmentNames fails when there is not
	// exactly one.
	provider := &ghSpecRecorder{}
	_, _ = ghexec.New(provider, ghPassthrough{}).RepoSlug(context.Background())

	identity := &ghSpecRecorder{}
	_, _ = app.NewGH(identity).DetectOwner(context.Background())

	fromProvider := ghEnvironmentNames(t, "internal/forge/ghexec", provider)
	fromIdentity := ghEnvironmentNames(t, "internal/app", identity)

	want := slices.Sorted(slices.Values(ghAllowlist))

	if !slices.Equal(fromProvider, fromIdentity) {
		t.Errorf("the two gh constructors pass different environments:\n  internal/forge/ghexec: %q\n  internal/app:          %q",
			fromProvider, fromIdentity)
	}
	if !slices.Equal(fromProvider, want) {
		t.Errorf("internal/forge/ghexec passes %q, and the allowlist this rule declares is %q", fromProvider, want)
	}
	if !slices.Equal(fromIdentity, want) {
		t.Errorf("internal/app passes %q, and the allowlist this rule declares is %q", fromIdentity, want)
	}
}

// ghEnvironmentNames is the sorted names of the one spec the recorder was
// handed. It fails rather than returning an empty list when there is no spec,
// because two constructors that started nothing pass this rule vacuously.
func ghEnvironmentNames(t *testing.T, who string, r *ghSpecRecorder) []string {
	t.Helper()
	if len(r.specs) != 1 {
		t.Fatalf("%s ran %d children, want exactly 1; with none there is no environment to compare", who, len(r.specs))
	}
	var names []string
	for _, entry := range r.specs[0].Env {
		name, _, ok := strings.Cut(entry, "=")
		if !ok {
			t.Fatalf("%s passed the entry %q, which has no name", who, entry)
		}
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}
