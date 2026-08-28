package vcs

import (
	"context"
	"strings"

	"github.com/carlosboeing/crossrev/internal/exec"
)

// Git is the git command line, reached through the one runner this codebase
// starts processes with.
//
// git stays an external command. Nothing here reimplements an object store: the
// shipped tool shells out to git at every one of these call sites, and a second
// implementation of `git show` would answer differently from the git the
// operator's own hooks, config and credential helpers run under.
type Git struct {
	// Path is the program. Empty means the name `git`, resolved on the PATH of
	// the calling process the way lib/legs.sh:393 resolves it.
	Path string

	// Env is the complete environment every git child receives, as NAME=VALUE
	// entries. Nil means an empty child environment, which is what
	// exec.Spec.Env documents and never the parent's own.
	//
	// There is no default list here, and that is deliberate. The Bash tool runs
	// git with whatever the shell holds, so any allowlist this package invented
	// would be a policy nobody wrote down — one that drops SSH_AUTH_SOCK on the
	// day a push needs it, silently. The orchestrator builds the value with
	// exec.Inherit and owns the names in it.
	Env []string

	// Runner starts the child.
	Runner exec.Runner
}

// New returns a Git that starts children through runner with env.
func New(runner exec.Runner, env []string) *Git {
	return &Git{Runner: runner, Env: env}
}

// Call is one git invocation.
type Call struct {
	// Dir is the working directory. Empty means the calling process's.
	Dir string
	// Args are the arguments after the program name, one argv entry each.
	Args []string
	// ExtraEnv is appended to Git.Env for this call alone. It is how
	// GIT_INDEX_FILE reaches the two index operations that need it
	// (lib/run.sh:638-639 and :667-670) without the whole package holding a
	// mutable environment.
	ExtraEnv []string
}

// Output is what one git invocation produced.
type Output struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// OK reports that git exited zero.
func (o Output) OK() bool { return o.ExitCode == 0 }

// Text is stdout with trailing newlines removed, which is what a Bash command
// substitution hands its caller.
func (o Output) Text() string { return strings.TrimRight(o.Stdout, "\n") }

// Lines is stdout split on newlines with the empty ones dropped.
//
// Every reader of a multi-value git output in the shell is a `while IFS= read`
// loop guarded by `[[ -n "$url" ]] || continue` (lib/legs.sh:397-398), so an
// empty line is skipped rather than carried. A value containing a newline is
// indistinguishable from two values here, exactly as it is there.
func (o Output) Lines() []string {
	var lines []string
	for _, line := range strings.Split(o.Stdout, "\n") {
		if line == "" {
			continue
		}
		lines = append(lines, line)
	}
	return lines
}

// Run starts one git invocation and waits for it.
//
// The spec is orchestrator-facing, and that is a decision rather than a
// default. exec.Spec.Audience defaults to model-facing, which refuses a child
// whose environment names GH_TOKEN, GITHUB_TOKEN or GH_ENTERPRISE_TOKEN — and
// git is the one tool in this port that legitimately holds one. lib/github.sh
// pushes with a plain `git push` (lib/github.sh:443 and :510) over whatever
// credential helper the environment configures, which on a GitHub-hosted
// runner is the ambient token. A model-facing spec here would refuse that push
// with a security message about a leak that is not happening.
//
// git is also not the process the boundary exists for. That process is the
// harness, described at lib/adapters/codex.sh:79-82 as "the process that reads
// attacker-controlled text"; git reads a repository the orchestrator already
// decided to run in.
//
// A non-zero exit is data, not an error: `git cat-file -e` answers a question
// with its status and lib/run.sh:1867 reads it as one. The error return covers
// only the cases exec.Result.Err covers, where no status was produced at all.
func (g *Git) Run(ctx context.Context, call Call) (Output, error) {
	path := g.Path
	if path == "" {
		path = "git"
	}

	env := g.Env
	if len(call.ExtraEnv) > 0 {
		env = make([]string, 0, len(g.Env)+len(call.ExtraEnv))
		env = append(env, g.Env...)
		env = append(env, call.ExtraEnv...)
	}

	result := g.Runner.Run(ctx, exec.Spec{
		Path:     path,
		Args:     call.Args,
		Dir:      call.Dir,
		Env:      env,
		Audience: exec.AudienceOrchestrator,
	})

	output := Output{
		Stdout:   string(result.Stdout),
		Stderr:   string(result.Stderr),
		ExitCode: result.ExitCode,
	}
	return output, result.Err
}

// At returns the repository rooted at dir. An empty dir is the calling
// process's own working directory, which is what every unqualified `git …` in
// lib/ runs against.
func (g *Git) At(dir string) *Repository { return &Repository{git: g, dir: dir} }
