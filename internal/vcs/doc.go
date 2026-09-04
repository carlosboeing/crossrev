// Package vcs provides git operations, worktree handling, and repository state inspection.
//
// git stays an external command. Every call here builds an argument array and
// hands it to internal/exec, which is the one route from Go to a child process
// in this codebase; nothing in this package reimplements an object store. A
// second implementation would answer differently from the git the operator's
// own configuration, hooks and credential helpers run under, and the whole
// value of these operations is that they agree with it.
//
// # What is in here
//
//   - Revision-scoped reads. Repository.Show reads a path at a revision, which
//     is how policy is read from the pull request's base revision and never its
//     head (ADR 0003).
//   - The push-target guard. GitHubSlug isolates and compares the host whole,
//     and ResolvePushRepo refuses a remote whose entries disagree about where a
//     push would land.
//   - Worktrees. The resolve leg runs in a dedicated one so the checkout the
//     operator is standing in is never mutated.
//   - The local run lock, keyed on the clone's shared git directory so every
//     working tree of one clone finds it.
//   - The working-tree capture a rejected resolve attempt is rolled back to.
//
// # The runner of every child
//
// A real git child is started through exec.NewOrchestratorRunner. git is not the
// process the credential boundary exists for — that process is the harness,
// which reads attacker-controlled text — and git is the one tool here that may
// legitimately hold a forge credential, because a push over https uses whatever
// credential helper the environment configures. Git.New carries the full
// reasoning.
package vcs
