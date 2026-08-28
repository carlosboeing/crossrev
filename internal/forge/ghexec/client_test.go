package ghexec_test

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
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

// Every call this package makes is the orchestrator's own tool, and gh is the
// one child that legitimately holds a GitHub credential.
func TestEveryInvocationIsOrchestratorFacing(t *testing.T) {
	c, r := client(t)
	ctx := context.Background()
	repo := testSlug(t)

	// One call per file, so a new spec builder in any of them is caught.
	_, _ = c.RepoSlug(ctx)
	c.DefaultBranch(ctx, repo)
	_, _ = c.PullRequest(ctx, repo, 42)
	c.IssueComments(ctx, repo, 42)
	c.ReviewThreads(ctx, repo, 42)
	_, _ = c.CommentCreate(ctx, repo, 42, "hello")
	c.LabelColour(ctx, repo, "bug")
	c.IssueCandidates(ctx, repo, "app.ts", "")
	c.WorkflowRunStatus(ctx, repo, "12345")
	_ = c.PullRequestLabelAdd(ctx, repo, 42, "crossrev/pass-1")

	if len(r.specs) == 0 {
		t.Fatal("no invocation was recorded")
	}
	for i, spec := range r.specs {
		if spec.Audience != exec.AudienceOrchestrator {
			t.Errorf("call %d (%q) is model-facing; gh needs the credential a model-facing spec is refused",
				i, strings.Join(spec.Args, " "))
		}
		if spec.Path != "gh" {
			t.Errorf("call %d runs %q, want gh resolved on the PATH", i, spec.Path)
		}
		if spec.Stdin != nil {
			t.Errorf("call %d writes stdin; every argument gh needs travels on the argv", i)
		}
	}
}

// A model-facing spec carrying GH_TOKEN is refused by the runner, so the
// audience is not decoration: the same environment through the zero value
// fails closed.
func TestTheEnvironmentThisClientBuildsWouldBeRefusedForAModel(t *testing.T) {
	c, r := client(t, out("acme/widget\n"))
	if _, err := c.RepoSlug(context.Background()); err != nil {
		t.Fatalf("RepoSlug: %v", err)
	}

	spec := r.only(t)
	spec.Audience = exec.AudienceModelFacing
	res := exec.NewOSRunner().Run(context.Background(), spec)
	if res.Err == nil {
		t.Fatal("the same spec was accepted as model-facing; the credential guard did not see GH_TOKEN")
	}
	if !strings.Contains(res.Err.Error(), "GH_TOKEN") {
		t.Errorf("refusal = %v, want it to name GH_TOKEN", res.Err)
	}
}

// One spec builder, so the audience is set once and cannot be forgotten in the
// next method somebody adds.
func TestOnlyOneFileBuildsASpec(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading the package directory: %v", err)
	}

	var found []string
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(filepath.Join(".", name))
		if err != nil {
			t.Fatalf("reading %s: %v", name, err)
		}
		file, err := parser.ParseFile(token.NewFileSet(), name, src, 0)
		if err != nil {
			t.Fatalf("parsing %s: %v", name, err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.CompositeLit)
			if !ok {
				return true
			}
			sel, ok := lit.Type.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Spec" {
				return true
			}
			if ident, ok := sel.X.(*ast.Ident); ok && ident.Name == "exec" && !slices.Contains(found, name) {
				found = append(found, name)
			}
			return true
		})
	}

	if !slices.Equal(found, []string{"client.go"}) {
		t.Errorf("exec.Spec is built in %v, want client.go alone", found)
	}
}

// A Client with no filter refuses every write rather than publishing text
// nothing inspected.
func TestWritesRefuseWithoutAFilter(t *testing.T) {
	r := &recorder{}
	c := ghexec.New(r, nil)

	if _, err := c.CommentCreate(context.Background(), testSlug(t), 42, "hello"); err == nil {
		t.Error("CommentCreate published with no filter")
	}
	if len(r.specs) != 0 {
		t.Errorf("gh was invoked %v, want not at all", r.argvs())
	}
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
