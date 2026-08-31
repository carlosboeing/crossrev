// Package sandbox isolates the workspace by quarantining repository-provided harness configuration.
//
// A pull request branch does not only contain content to review. It contains
// files that configure the thing reviewing it: settings, instruction files,
// hooks, MCP server definitions, agents. A hook is arbitrary code execution
// before the model ever sees a token.
//
// The mechanism is to move those files out of the way for the length of an
// invocation and put them back before anything is committed. It is
// harness-agnostic, which is what makes it hold: a flag that changes name in
// the next release fails open, whereas a file that is not there cannot be read
// by anything.
//
// It is a best-effort layer and not the security boundary. That is the
// credential separation — the agent process holds no GitHub credential at all,
// so an injection that reaches tool use still cannot post as the App, push a
// commit, or read a secret (ADR 0001, SECURITY.md).
//
// The files are quarantined rather than deleted, because a pull request that
// adds a hook is exactly the pull request a reviewer should be flagging.
package sandbox
