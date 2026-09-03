package initcmd

import (
	"context"

	"github.com/carlosboeing/crossrev/internal/config"
	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/harness"
	"github.com/carlosboeing/crossrev/internal/ui"
)

// Request is what `crossrev init` was asked for, and everything it needs from
// somewhere it cannot reach on its own (cmd_init, lib/init.sh:34-64).
//
// # Why the ports are here rather than imported
//
// `initcmd` is a tier-3 package. It may import tiers 0 to 2 and no tier-3 peer,
// so it cannot name `internal/app` for the App identity or `internal/preflight`
// for the pairing report. Both arrive as small interfaces the composition root
// implements over those packages. The same rule sends the GitHub reads and the
// git reads through interfaces: nothing here starts a process or opens a
// socket.
//
// # What a nil port means
//
// Nothing. Resolve refuses a Request missing a port it needs rather than
// substituting a default, because every default here would be a lie an operator
// then acts on — no overwrites, no secrets, no App. A missing port is a wiring
// fault in the composition root, and it is reported as one.
type Request struct {
	// Owner is --owner (lib/init.sh:38). Empty means the repository's own
	// owner, which is where Resolve takes it from: the repository's owner is
	// the trust boundary the App's private key should sit on
	// (lib/init.sh:78).
	Owner string

	// Repo is --repo (lib/init.sh:39). The zero Slug means ask the forge
	// which repository the working directory belongs to.
	//
	// The shell holds this as a bare string and accepts anything, deriving
	// the owner with `${INIT_REPO%%/*}`. A Slug refuses a value that is not
	// owner/name, so `--repo garbage` is refused where the flag is parsed
	// rather than here.
	Repo core.Slug

	// DryRun is --dry-run: print the plan and stop (lib/init.sh:40).
	DryRun bool

	// Upgrade is --upgrade: regenerate the workflows and leave the policy
	// file alone (lib/init.sh:41).
	Upgrade bool

	// Yes is --yes or -y (lib/init.sh:42). It answers the plan gate, and it
	// deliberately does not answer the refresher registration, which needs a
	// browser.
	Yes bool

	// Show is how a file is read. Resolve loads the configuration through it
	// with a zero revision, which is the working tree — `cfg_load ""` at
	// lib/init.sh:84, and not the base revision every leg reads policy from,
	// because there is no pull request in play and this is the config `init`
	// is about to write.
	//
	// It is also what the Project Map is read through when the backlog
	// destination is `auto` (lib/init.sh:117).
	Show config.ShowFile

	// Harness is lib/harnesses.json, already loaded.
	Harness harness.Document

	// GitHub is every forge read the plan makes.
	GitHub GitHub

	// Apps is the App identity `crossrev auth login` recorded.
	Apps Apps

	// Pairing is the preflight report.
	Pairing Pairing

	// Source is which commit of CrossRev the generated workflows pin.
	Source Source

	// Files is the working tree, read-only.
	Files FileSystem

	// Out is where the plan and every refusal are printed. A nil Out
	// discards, which is ui.IO's own zero-value rule.
	Out *ui.IO
}

func (r Request) io() *ui.IO { return r.Out }

// The two roles _auth_meta takes (lib/auth.sh:39-45). `loop` is the App the
// review and resolve jobs run as; `refresher` is the second App, carrying
// secrets:write and nothing else, that exists only for a rotating credential on
// an ephemeral runner.
const (
	RoleLoop      = "loop"
	RoleRefresher = "refresher"
)

// App is a GitHub App as its metadata file records it (lib/init.sh:262).
type App struct {
	// Name is `.name`, the App's own name on GitHub.
	Name string
	// ID is `.id`, as jq prints it.
	ID string
}

// Apps answers which App is registered for an owner in a role.
//
// One method, over the file test the shell makes inline: `[[ -f "$(_auth_meta
// "$INIT_OWNER")" ]]` at lib/init.sh:261, and again for the refresher at
// lib/init.sh:294. The false answer is the missing file, so a caller never has
// to know that the identity is a file at all.
type Apps interface {
	App(owner, role string) (App, bool)
}

// Pairing is the preflight report, as `init` reads it.
//
// Three methods rather than three fields holding one function each: they are
// three answers about one subject from one implementation, and separate fields
// would let a composition root wire two of the three and leave the plan quietly
// answering from a nil.
type Pairing interface {
	// Supported answers whether a harness can authenticate by subscription
	// on this runner and serve this leg, and the reason when it cannot
	// (preflight_pairing_supported, lib/preflight.sh:188-233). The leg is
	// the descriptor's word — review or resolve — not the config's.
	Supported(runner, harness, leg string) (reason string, ok bool)

	// Secret is which secret carries a harness's subscription credential,
	// and false for a harness with none (preflight_harness_secret,
	// lib/preflight.sh:236-241).
	Secret(harness string) (string, bool)

	// NeedsRefresher reports the one pairing that needs the single-writer
	// refresher workflow (preflight_needs_refresher,
	// lib/preflight.sh:258-263).
	NeedsRefresher(runner, harness, endpoint string) bool
}

// Source is which commit of CrossRev the generated workflows pin
// (lib/init.sh:141-147).
//
// Two methods, because the two answers fail differently: a SHA that cannot be
// read stops the run, and a ref that cannot be read is the word `untagged`.
// Folding them into one call would put that difference inside the
// implementation, where the shell keeps it in `init`.
type Source interface {
	// SHA is `git -C "$ROOT" rev-parse HEAD` against the CrossRev checkout,
	// not the repository being set up.
	SHA(ctx context.Context) (string, error)

	// Ref is `git -C "$ROOT" describe --tags` against the same checkout.
	Ref(ctx context.Context) (string, error)
}

// FileSystem is the working tree of the repository being set up.
//
// One method, and it reads. `_init_resolve` and `_init_print_plan` run before
// anything has been agreed to, so the only question they may ask of the disk is
// whether a path is already there (lib/init.sh:155-157).
type FileSystem interface {
	// Exists is `[[ -e "$path" ]]`: true for a file, true for a directory,
	// and false for a symbolic link whose target is gone.
	Exists(path string) bool
}

// GitHub is every forge read the plan makes.
//
// Every method is a read. The plan gate is the difference between a tool people
// trust with a second repository and one they run once, so nothing between the
// first flag and the confirmation may change a repository.
//
// The three that answer without an error answer that way in the shell too: it
// sends the failure to /dev/null and carries on with a default, and a caller
// that treated one as fatal would behave differently from the tool that ships.
type GitHub interface {
	// RepoSlug is which repository the working directory belongs to
	// (gh_repo_slug, lib/github.sh:30-34). The error is the shell's refusal
	// there; a zero Slug with no error is the empty answer `init` refuses
	// separately at lib/init.sh:72.
	RepoSlug(ctx context.Context) (core.Slug, error)

	// OwnerType is `.type` from `gh api users/<owner>`, lowercased, and
	// empty when GitHub does not answer (lib/init.sh:79).
	OwnerType(ctx context.Context, owner string) string

	// DefaultBranch answers `main` when GitHub does not answer at all
	// (gh_default_branch, lib/github.sh:46-48).
	DefaultBranch(ctx context.Context, repo core.Slug) string

	// BranchProtected reports whether the branch carries a protection rule
	// (_init_branch_protected, lib/init.sh:427-430). A read that fails
	// answers false, which is what `gh api … >/dev/null 2>&1` does.
	BranchProtected(ctx context.Context, repo core.Slug, branch string) bool

	// LabelColour is the hex a label currently carries, lowercased, or empty
	// if it does not exist (gh_label_colour, lib/github.sh:271-275).
	LabelColour(ctx context.Context, repo core.Slug, name string) string

	// SecretsAtOrg is `gh secret list --org <owner>` (lib/init.sh:412-413).
	// The false answer covers both a failed read and a login without
	// admin:org, which the shell cannot tell apart either and which is a
	// permission state rather than a fault.
	SecretsAtOrg(ctx context.Context, owner string) (string, bool)

	// SecretsAtRepo is `gh secret list --repo <repo>` with stderr folded
	// into stdout (lib/init.sh:416). The string is what GitHub said whether
	// or not the read worked, because the refusal quotes it.
	SecretsAtRepo(ctx context.Context, repo core.Slug) (string, error)
}
