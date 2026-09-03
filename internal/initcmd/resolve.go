package initcmd

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/carlosboeing/crossrev/internal/config"
	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/harness"
)

// Plan is everything `init` settles before it prints, asks or writes anything
// (lib/init.sh:19-28 and _init_resolve, lib/init.sh:70-162).
//
// The order matters more than the contents. `init` registers a GitHub identity,
// writes organisation secrets and adds files to a repository, so every value it
// will act on is resolved first and shown to the operator as one block. A value
// discovered halfway through execution is a value nobody agreed to.
type Plan struct {
	// Repo is the repository being set up.
	Repo core.Slug

	// Owner is whose account or organisation holds the App's private key.
	Owner string

	// OwnerType is `organization` or `user`, and nothing else
	// (lib/init.sh:79-80).
	OwnerType string

	// Runner is `github-hosted` or `self-hosted`, checked against those two
	// exact strings (lib/init.sh:95-99).
	Runner string

	// SourceSHA is the commit of CrossRev the workflows pin, and SourceRef
	// is the described tag that rides in the comment beside it.
	SourceSHA string
	SourceRef string

	// PassLabels is `crossrev/pass-N` for each pass the policy allows, and
	// FixedLabels is the five the loop always needs (lib/init.sh:103-109).
	PassLabels  []string
	FixedLabels []string

	// BacklogLabels is the tracking label and the extras, space-separated,
	// and empty unless deferred work goes to GitHub issues.
	//
	// A string rather than a list because the shell's own value can be one
	// leading space followed by a label, and the plan prints a filed-issues
	// block for that while counting no labels in it (lib/init.sh:130).
	BacklogLabels string

	// BacklogResolved is where deferred work goes, with `auto` already
	// resolved, and BacklogOrigin says where that answer came from.
	BacklogResolved string
	BacklogOrigin   string

	// Workflows are the template names, without the `crossrev-` prefix or
	// the extension (lib/init.sh:149-150).
	Workflows []string

	// Writes is every path `init` would write, and Overwrites is the subset
	// already there, both in the order the plan prints them.
	Writes     []string
	Overwrites []string

	// NeedsRefresher is the one pairing that needs the single-writer
	// refresher workflow and its second App.
	NeedsRefresher bool

	// Secrets is every secret this configuration needs, in the order
	// _init_required_secrets prints them (lib/init.sh:223-252).
	Secrets []string

	// SecretInventory is `gh secret list` output for the scopes that
	// answered, one read per scope rather than two per secret
	// (lib/init.sh:410-421).
	SecretInventory string

	// Config is the configuration this plan was resolved from, so the
	// printer and the renderer read the same merge Resolve did rather than
	// loading a second one.
	Config *config.Config
}

// The five labels the loop always needs, in the order lib/init.sh:109 writes
// them.
var fixedLabels = []string{
	"crossrev/awaiting-resolution",
	"crossrev/awaiting-review",
	"crossrev/converged",
	"crossrev/halted",
	"crossrev/stop",
}

// The three workflows every configuration gets (lib/init.sh:149).
var baseWorkflows = []string{"review", "resolve", "watchdog"}

// Resolve settles every value the plan states, and refuses rather than guessing.
//
// It reproduces `_init_resolve` at lib/init.sh:70-162, in that order, because
// the order is what an operator sees when something is wrong: a runner a
// pairing cannot serve is named before a secret list is read, and a config that
// will not load is named before a GitHub App is looked up.
//
// Nothing here writes. The only questions asked of the disk are whether a path
// is already there and what the configuration says.
func Resolve(ctx context.Context, req Request) (Plan, error) {
	if err := req.wired(); err != nil {
		return Plan{}, err
	}

	var plan Plan
	plan.Repo = req.Repo
	if plan.Repo.Owner() == "" {
		slug, err := req.GitHub.RepoSlug(ctx)
		if err != nil {
			return Plan{}, err
		}
		plan.Repo = slug
	}
	if plan.Repo.Owner() == "" {
		return Plan{}, req.io().Die(
			"could not work out which repository to set up",
			"Run crossrev init from a checkout with a GitHub remote, or pass --repo owner/name.",
		)
	}

	// Detected, not asked. The repository's own owner is the trust boundary
	// the App's private key should sit on (lib/init.sh:76-80).
	plan.Owner = req.Owner
	if plan.Owner == "" {
		plan.Owner = plan.Repo.Owner()
	}
	plan.OwnerType = strings.ToLower(req.GitHub.OwnerType(ctx, plan.Owner))
	if plan.OwnerType != "organization" {
		plan.OwnerType = "user"
	}

	// Config from the working tree: there is no pull request in play, and
	// this is the config `init` is about to write (lib/init.sh:82-84).
	cfg, err := config.Load(ctx, core.Revision{}, req.Show)
	if err != nil {
		return Plan{}, err
	}
	plan.Config = cfg

	// A typo here is the worst kind of wrong: rendering treats anything
	// unrecognised as hosted while the refresher derivation matches the
	// exact string, so `runner: github_hosted` would emit hosted workflows
	// with no refresher (lib/init.sh:90-99).
	plan.Runner = cfg.Get(".runner")
	if plan.Runner != "github-hosted" && plan.Runner != "self-hosted" {
		return Plan{}, req.io().Die(
			"the config sets runner: "+plan.Runner+", which CrossRev does not recognise",
			"It must be exactly github-hosted or self-hosted. Anything else would be treated as hosted while behaving as neither, and the first sign would be a credential expiring weeks later.",
		)
	}
	if err := assertRunnerServesPairing(req, cfg, plan.Runner); err != nil {
		return Plan{}, err
	}
	plan.NeedsRefresher = resolveRefresher(req, cfg, plan.Runner)

	for pass := 1; pass <= passesPerCycle(cfg); pass++ {
		plan.PassLabels = append(plan.PassLabels, "crossrev/pass-"+strconv.Itoa(pass))
	}
	plan.FixedLabels = append([]string(nil), fixedLabels...)

	// `auto` is a bootstrap convenience, not a runtime mode: it is resolved
	// once here and the concrete answer is written into the generated
	// config, so the committed file states plainly where deferred work goes
	// (lib/init.sh:111-124).
	want := cfg.Get(".backlog.destination")
	resolved, err := cfg.ResolveBacklog(ctx, core.Revision{}, want)
	if err != nil {
		return Plan{}, err
	}
	plan.BacklogResolved = resolved.String()
	if want == "auto" {
		_, found, err := config.ProjectMapTracker(ctx, core.Revision{}, req.Show)
		if err != nil {
			return Plan{}, err
		}
		if found {
			plan.BacklogOrigin = "resolved from the ## Project Map section's Tracker field"
		} else {
			plan.BacklogOrigin = "resolved by sniffing for a convention already in use"
		}
	} else {
		plan.BacklogOrigin = "named in the repository config as '" + want + "'"
	}

	if plan.BacklogResolved == string(config.DestinationGitHubIssues) {
		plan.BacklogLabels = backlogLabels(cfg)
	}

	// Which commit of the action the workflows pin, by SHA. A tag only looks
	// immutable: `git tag -f` plus a force push moves it, and the failure
	// mode is a repository whose review behaviour changes with nothing in
	// its own history to show for it (lib/init.sh:133-147).
	if sha, err := req.Source.SHA(ctx); err == nil {
		plan.SourceSHA = sha
	}
	plan.SourceRef = "untagged"
	if ref, err := req.Source.Ref(ctx); err == nil {
		plan.SourceRef = ref
	}
	if plan.SourceSHA == "" {
		return Plan{}, req.io().Die(
			"could not work out which commit of CrossRev to pin the workflows to",
			"init generates workflows that pin the action at a 40-character SHA. Run it from a git checkout of CrossRev.",
		)
	}

	plan.Workflows = append([]string(nil), baseWorkflows...)
	if plan.NeedsRefresher {
		plan.Workflows = append(plan.Workflows, "token-refresh")
	}
	for _, workflow := range plan.Workflows {
		plan.Writes = append(plan.Writes, ".github/workflows/crossrev-"+workflow+".yml")
	}
	plan.Writes = append(plan.Writes, ".github/crossrev.yml")
	for _, path := range plan.Writes {
		if req.Files.Exists(path) {
			plan.Overwrites = append(plan.Overwrites, path)
		}
	}

	plan.Secrets = requiredSecrets(req, cfg, plan.Runner, plan.NeedsRefresher)
	inventory, err := loadSecrets(ctx, req, plan.Repo, plan.Owner, plan.OwnerType)
	if err != nil {
		return Plan{}, err
	}
	plan.SecretInventory = inventory

	return plan, nil
}

// wired refuses a Request the composition root did not finish wiring.
//
// Apps is checked here even though Resolve never asks it anything: Print runs
// straight afterwards and would otherwise report "none registered" for an owner
// that has an App, which is a default an operator then acts on.
func (r Request) wired() error {
	missing := []string{}
	if r.Show == nil {
		missing = append(missing, "Show")
	}
	if r.GitHub == nil {
		missing = append(missing, "GitHub")
	}
	if r.Apps == nil {
		missing = append(missing, "Apps")
	}
	if r.Pairing == nil {
		missing = append(missing, "Pairing")
	}
	if r.Source == nil {
		missing = append(missing, "Source")
	}
	if r.Files == nil {
		missing = append(missing, "Files")
	}
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("initcmd: the request is missing %s", strings.Join(missing, ", "))
}

// assertRunnerServesPairing refuses a pairing before anything is installed
// (_init_assert_runner_serves_pairing, lib/init.sh:168-199).
//
// Caught here it is a config error. Caught at invocation it is a workflow that
// installs cleanly, fires on the first pull request, and dies every time.
func assertRunnerServesPairing(req Request, cfg *config.Config, runner string) error {
	for _, role := range core.Roles() {
		name := cfg.Get("." + string(role) + ".harness")
		endpoint := cfg.Get("." + string(role) + ".endpoint")

		// An endpoint means a static token in a secret, which never
		// rotates and so never cares what kind of runner it is on. It
		// still has to resolve, and this is where that should surface.
		if named(endpoint) {
			if _, err := cfg.Endpoint(endpoint); err != nil {
				return err
			}
			host := req.Harness.EndpointHost()
			if name != host {
				return req.io().Die(
					fmt.Sprintf("the %s names the endpoint '%s' but runs on '%s', which cannot use one", role, endpoint, name),
					fmt.Sprintf("Named endpoints are Anthropic-compatible and reached through the %s adapter. Use harness: %s with endpoint: %s, or drop the endpoint for this leg.", host, host, endpoint),
				)
			}
			continue
		}

		// The loop names the config keys; the pairing report speaks the
		// descriptor's vocabulary. Without the leg, a review-only
		// harness passes on its credential archetype and init installs
		// workflows that die on every resolve (lib/init.sh:189-194).
		leg, err := role.Leg()
		if err != nil {
			return err
		}
		reason, ok := req.Pairing.Supported(runner, name, leg.String())
		if ok {
			continue
		}
		return req.io().Die(
			fmt.Sprintf("the %s is configured to run %s by subscription, and a %s runner cannot serve that", role, name, runner),
			reason+". Two fixes: set runner: self-hosted in the config, where every harness refreshes its own credential on disk; or name a different harness for this leg.",
		)
	}
	return nil
}

// resolveRefresher derives whether this configuration needs the refresher
// (_init_resolve_refresher, lib/init.sh:208-216).
//
// Derived, never asked. The refresher exists for exactly one situation: a
// harness whose credential rotates, authenticating by subscription, on an
// ephemeral runner. Change any one of those three and it disappears, so asking
// would be asking someone to restate a consequence of a pairing they already
// chose.
func resolveRefresher(req Request, cfg *config.Config, runner string) bool {
	needs := false
	for _, role := range core.Roles() {
		if req.Pairing.NeedsRefresher(runner,
			cfg.Get("."+string(role)+".harness"),
			cfg.Get("."+string(role)+".endpoint")) {
			needs = true
		}
	}
	return needs
}

// requiredSecrets is the secrets this configuration actually needs
// (_init_required_secrets, lib/init.sh:223-252).
//
// Derived rather than fixed, because half of them exist only for one runner or
// one pairing. A hosted runner needs a credential for each subscription
// harness; a self-hosted one needs none of them, since the machine is already
// logged in.
func requiredSecrets(req Request, cfg *config.Config, runner string, needsRefresher bool) []string {
	secrets := []string{"APP_ID", "APP_PRIVATE_KEY"}
	seen := map[string]bool{}
	add := func(name string) {
		if seen[name] {
			return
		}
		seen[name] = true
		secrets = append(secrets, name)
	}

	for _, role := range core.Roles() {
		name := cfg.Get("." + string(role) + ".harness")
		endpoint := cfg.Get("." + string(role) + ".endpoint")
		if named(endpoint) {
			// An endpoint carries its own token variable, so the
			// secret it needs is whatever the endpoint block names.
			// Naming the variable rather than a vendor is what keeps
			// this from going stale every time the pairing changes.
			resolved, err := cfg.Endpoint(endpoint)
			if err != nil {
				continue
			}
			add(resolved.TokenEnv)
			continue
		}
		if runner == "self-hosted" {
			continue
		}
		// The same mapping the leg path checks against, read from one
		// place so `init` cannot ask for a secret no leg reads.
		secret, found := req.Pairing.Secret(name)
		if !found {
			continue
		}
		add(secret)
	}

	if needsRefresher {
		secrets = append(secrets, "CROSSREV_REFRESH_APP_ID", "CROSSREV_REFRESH_APP_PRIVATE_KEY")
	}
	return secrets
}

// loadSecrets is one read per scope, cached for the run (_init_load_secrets,
// lib/init.sh:410-421).
//
// This used to query GitHub twice per secret and decide from grep's exit status
// alone, with stderr discarded, so a call that failed was indistinguishable
// from a secret that was absent. The organisation read is still allowed to
// fail: a login without admin:org gets 403 there, which is a permission state
// rather than a fault, and the repository read is what decides the answer
// anyway. That one stops the run rather than guessing.
func loadSecrets(ctx context.Context, req Request, repo core.Slug, owner, ownerType string) (string, error) {
	inventory := ""
	if ownerType == "organization" {
		if out, ok := req.GitHub.SecretsAtOrg(ctx, owner); ok {
			inventory = out
		}
	}
	out, err := req.GitHub.SecretsAtRepo(ctx, repo)
	if err != nil {
		return "", req.io().Die(
			"could not read the secrets on "+repo.String(),
			"The plan says which secrets are already set, and a failed read would report every one of them missing. Check the login reaches that repository, then run it again. GitHub said: "+out,
		)
	}
	return inventory + "\n" + out, nil
}

// secretSet reports whether a secret is already there
// (_init_secret_exists, lib/init.sh:423-425). The name is anchored and followed
// by whitespace or the end of the line, so APP_ID never matches APP_ID_OLD.
func secretSet(inventory, name string) bool {
	pattern := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(name) + `([[:space:]]|$)`)
	return pattern.MatchString(inventory)
}

// named is `[[ -n "$endpoint" && "$endpoint" != "null" ]]`, the guard every
// endpoint read in `init` carries. `cfg_get` reads through `// empty` and
// cannot produce the literal `null`, but the shell tests for it and so does
// this.
func named(endpoint string) bool { return endpoint != "" && endpoint != "null" }

// passesPerCycle is `.policy.max_passes_per_cycle` as a count.
//
// A value that is not a whole number yields no pass labels, which is what the
// Bash `for (( i = 1; i <= max; i++ ))` does with one: arithmetic reads the
// word as an unset name and evaluates it to zero. Config validation refuses
// such a value before `init` runs.
func passesPerCycle(cfg *config.Config) int {
	passes, err := strconv.Atoi(cfg.Get(".policy.max_passes_per_cycle"))
	if err != nil {
		return 0
	}
	return passes
}

// backlogLabels is the tracking label followed by the configured extras
// (lib/init.sh:126-131).
//
// The shell builds it with `printf '%s %s' … | tr -s ' ' | sed 's/ *$//'`, so
// repeated spaces collapse and trailing ones go. A single leading space does
// not: `tr -s` squeezes a repeat, and one space is not a repeat. That is
// reproduced rather than tidied, because the plan tests the result for
// emptiness and would print a filed-issues block either way.
func backlogLabels(cfg *config.Config) string {
	tracking := cfg.Get(".backlog.github_issues.tracking_label")
	joined := tracking + " " + joinLabels(cfg.GetJSON(".backlog.github_issues.labels"))
	joined = squeezeSpaces(joined)
	return strings.TrimRight(joined, " ")
}

// joinLabels is jq's `join(" ")` over the configured label list: a string
// element bare, null as nothing, and anything else as its JSON text. A value
// that is not a list joins to nothing, which is what the shell's failed jq
// leaves in the substitution.
func joinLabels(raw []byte) string {
	var values []any
	if err := json.Unmarshal(raw, &values); err != nil {
		return ""
	}
	parts := make([]string, 0, len(values))
	for _, value := range values {
		switch typed := value.(type) {
		case nil:
			parts = append(parts, "")
		case string:
			parts = append(parts, typed)
		default:
			encoded, err := json.Marshal(typed)
			if err != nil {
				return ""
			}
			parts = append(parts, string(encoded))
		}
	}
	return strings.Join(parts, " ")
}

// squeezeSpaces is `tr -s ' '`: every run of spaces becomes one.
func squeezeSpaces(value string) string {
	var out strings.Builder
	previousWasSpace := false
	for _, r := range value {
		if r == ' ' && previousWasSpace {
			continue
		}
		previousWasSpace = r == ' '
		out.WriteRune(r)
	}
	return out.String()
}

// refresherHarness is the one harness the descriptor marks as needing the
// refresher (`harness_field '.harnesses[] | select(.credential.refresher ==
// true) | .name'`, lib/init.sh:289).
func refresherHarness(doc harness.Document) string {
	for _, name := range doc.Names() {
		if entry, found := doc.For(name); found && entry.Credential.Refresher {
			return name
		}
	}
	return ""
}
