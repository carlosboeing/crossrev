// execute.go — everything `init` changes, once the plan has been agreed to
// (_init_execute, lib/init.sh:436-651).
//
// Nothing here runs before the gate. Resolve reads and Print reads; this file
// is the only one in the package that declares a label, writes a secret or puts
// a file in a repository, and `--dry-run` returns before any of it.

package initcmd

// Execution is every port `init` needs to change something.
//
// It is separate from Request for the reason the plan gate exists: a Request is
// what a run may read, and an Execution is what it may write. A command that
// only prints a plan never builds one, so there is no wiring through which a
// dry run could reach a write.
//
// A nil port is refused by the section that needs it rather than filled in with
// a default, which is the rule Resolve already follows: every default here
// would be a lie an operator then acts on.
type Execution struct {
	// Labels declares the loop's labels and the filed issues' labels.
	Labels LabelWriter
}
