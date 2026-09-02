package cli

import (
	"strings"

	"github.com/carlosboeing/crossrev/internal/ui"
)

// Command names one command, spelled the way a person types it.
type Command string

// Every command bin/crossrev dispatches to.
const (
	CommandHelp          Command = "help"
	CommandVersion       Command = "version"
	CommandCycle         Command = "cycle"
	CommandReview        Command = "review"
	CommandResolve       Command = "resolve"
	CommandStatus        Command = "status"
	CommandInit          Command = "init"
	CommandWatchdog      Command = "watchdog"
	CommandConfigShow    Command = "config show"
	CommandConfigBacklog Command = "config backlog"
	CommandAuthStatus    Command = "auth status"
	CommandAuthLogin     Command = "auth login"
	CommandAuthInstall   Command = "auth install"
	CommandAuthRotate    Command = "auth rotate"
	CommandAuthRefresh   Command = "auth refresh"
	CommandDoctor        Command = "doctor"
)

// AllCommands is every command in the order bin/crossrev lists them in its help
// (bin/crossrev:83-97), with help and version last because that is where the
// help puts them.
func AllCommands() []Command {
	return []Command{
		CommandCycle, CommandReview, CommandResolve, CommandStatus,
		CommandInit, CommandWatchdog, CommandConfigShow, CommandConfigBacklog,
		CommandAuthStatus, CommandAuthLogin, CommandAuthInstall,
		CommandAuthRotate, CommandAuthRefresh, CommandDoctor,
		CommandVersion, CommandHelp,
	}
}

// Invocation is one command line, parsed.
type Invocation struct {
	// Command is which command the arguments named.
	Command Command
	// Request is that command's own request value — CycleRequest,
	// AuthLoginRequest and so on. Dispatch hands it to the matching field of
	// Commands, which is typed on it.
	Request any
}

// Parse turns an argument list into an invocation, and refuses the way the
// shell refuses.
//
// The parse rule is bin/crossrev:111-139, written down there rather than
// discovered later:
//
//   - a first argument beginning with `-` is the default cycle, and the options
//     stay where they are because they are the cycle's own;
//   - anything else is a sub-command, and one that is not recognised is an
//     error rather than the most expensive operation the tool offers;
//   - `help` and `version` are special-cased ahead of the rule, because they are
//     the two dash-arguments nobody means as a cycle, and because they must
//     answer with nothing else installed;
//   - a bare `crossrev` prints help. It does not cycle.
//
// harnesses is the list of installed harness names, which the review and
// resolve usage lines name. An empty list prints the shape of the flag instead.
//
// A refusal is printed before it is returned, the way ui_die prints before it
// exits. The one exception is errNoValue, which the shell does not print.
func Parse(args []string, out *ui.IO, harnesses []string) (Invocation, error) {
	first := ""
	if len(args) > 0 {
		first = args[0]
	}

	switch first {
	case "", "help", "--help", "-h":
		return Invocation{Command: CommandHelp, Request: HelpRequest{}}, nil
	case "version", "--version", "-v":
		return Invocation{Command: CommandVersion, Request: VersionRequest{}}, nil
	}

	// No shift on the dash arm: the options are the cycle's own
	// (bin/crossrev:136).
	name, rest := first, args[1:]
	if strings.HasPrefix(first, "-") {
		name, rest = string(CommandCycle), args
	}

	switch name {
	case "auth":
		return parseAuth(rest, out)
	case "config":
		return parseConfig(rest, out)
	case "doctor":
		// The shell reads none of doctor's arguments (bin/crossrev:163).
		return Invocation{Command: CommandDoctor, Request: DoctorRequest{}}, nil
	case string(CommandCycle):
		return parseCycle(rest, out)
	case string(CommandReview):
		return parseReview(rest, out, harnesses)
	case string(CommandResolve):
		return parseResolve(rest, out, harnesses)
	case string(CommandStatus):
		return parseStatus(rest, out)
	case string(CommandInit):
		return parseInit(rest, out)
	case string(CommandWatchdog):
		return parseWatchdog(rest, out)
	}

	return Invocation{}, out.Die("unknown command: "+name, "Run: crossrev help")
}

// parseAuth is the `auth` arm (bin/crossrev:142-152). With no sub-command the
// shell defaults to `status`.
func parseAuth(args []string, out *ui.IO) (Invocation, error) {
	sub := "status"
	if len(args) > 0 {
		sub, args = args[0], args[1:]
	}
	switch sub {
	case "status":
		// auth_status has no argument loop, so it refuses nothing
		// (lib/auth.sh:373).
		return Invocation{Command: CommandAuthStatus, Request: AuthStatusRequest{}}, nil
	case "login":
		return parseAuthLogin(args, out)
	case "install":
		return parseAuthInstall(args, out)
	case "rotate":
		return parseAuthRotate(args, out)
	case "refresh":
		return parseAuthRefresh(args, out)
	}
	return Invocation{}, out.Die("unknown auth command: "+sub,
		"Try: crossrev auth status | login | install | rotate | refresh")
}

// parseConfig is the `config` arm (bin/crossrev:154-161).
//
// The shell reads `${1:-show}` and nothing after it, so a second argument is
// neither used nor refused. `preflight_require_yq` and `cfg_load` run before
// the sub-command is looked at; here they are the command's own work, so on a
// machine with no yq the two refusals arrive in the other order.
func parseConfig(args []string, out *ui.IO) (Invocation, error) {
	sub := "show"
	if len(args) > 0 {
		sub = args[0]
	}
	switch sub {
	case "show":
		return Invocation{Command: CommandConfigShow, Request: ConfigRequest{}}, nil
	case "backlog":
		return Invocation{Command: CommandConfigBacklog, Request: ConfigRequest{}}, nil
	}
	return Invocation{}, out.Die("unknown config command: "+sub,
		"Try: crossrev config show | backlog")
}

// parseCycle is cmd_cycle's argument loop (lib/run.sh:2896-2916).
func parseCycle(args []string, out *ui.IO) (Invocation, error) {
	req := CycleRequest{Trigger: "human"}
	var pr, repo string
	s := &scanner{args: args}
	for s.more() {
		var err error
		switch flag := s.flag(); flag {
		case "--pr":
			pr, err = s.value()
		case "--repo":
			repo, err = s.value()
		case "--harness":
			req.HarnessOverride, err = s.value()
		case "--trigger":
			req.Trigger, err = s.value()
		case "--no-tips":
			req.NoTips = true
			s.skip()
		case "--keep-transcripts":
			req.KeepTranscripts = true
			s.skip()
		default:
			err = unknownOption(out, "cycle", flag, usageCycle)
		}
		if err != nil {
			return Invocation{}, err
		}
	}
	var err error
	if req.PR, err = requirePR(out, "cycle", pr); err != nil {
		return Invocation{}, err
	}
	if err = requireTrigger(out, "cycle", req.Trigger); err != nil {
		return Invocation{}, err
	}
	if req.Repo, err = optionalSlug(out, repo); err != nil {
		return Invocation{}, err
	}
	return Invocation{Command: CommandCycle, Request: req}, nil
}

// parseReview is leg_review's argument loop (lib/run.sh:914-936).
func parseReview(args []string, out *ui.IO, harnesses []string) (Invocation, error) {
	req := ReviewRequest{Trigger: "human"}
	var pr, repo string
	s := &scanner{args: args}
	for s.more() {
		var err error
		switch flag := s.flag(); flag {
		case "--pr":
			pr, err = s.value()
		case "--repo":
			repo, err = s.value()
		case "--harness":
			req.HarnessOverride, err = s.value()
		case "--trigger":
			req.Trigger, err = s.value()
		case "--continuation":
			req.Continuation = true
			s.skip()
		case "--no-tips":
			req.NoTips = true
			s.skip()
		case "--keep-transcripts":
			req.KeepTranscripts = true
			s.skip()
		case "--pass":
			// Accepted and ignored: the pass comes from the pull request
			// (lib/run.sh:924).
			err = s.discard()
		default:
			err = unknownOption(out, "review", flag, usageReview(harnesses))
		}
		if err != nil {
			return Invocation{}, err
		}
	}
	var err error
	if req.PR, err = requirePR(out, "review", pr); err != nil {
		return Invocation{}, err
	}
	if err = requireTrigger(out, "review", req.Trigger); err != nil {
		return Invocation{}, err
	}
	if req.Repo, err = optionalSlug(out, repo); err != nil {
		return Invocation{}, err
	}
	return Invocation{Command: CommandReview, Request: req}, nil
}

// parseResolve is leg_resolve's argument loop (lib/run.sh:1731-1752).
func parseResolve(args []string, out *ui.IO, harnesses []string) (Invocation, error) {
	req := ResolveRequest{Trigger: "human"}
	var pr, repo string
	s := &scanner{args: args}
	for s.more() {
		var err error
		switch flag := s.flag(); flag {
		case "--pr":
			pr, err = s.value()
		case "--repo":
			repo, err = s.value()
		case "--harness":
			req.HarnessOverride, err = s.value()
		case "--trigger":
			req.Trigger, err = s.value()
		case "--no-tips":
			req.NoTips = true
			s.skip()
		case "--keep-transcripts":
			req.KeepTranscripts = true
			s.skip()
		case "--pass":
			err = s.discard()
		default:
			err = unknownOption(out, "resolve", flag, usageResolve(harnesses))
		}
		if err != nil {
			return Invocation{}, err
		}
	}
	var err error
	if req.PR, err = requirePR(out, "resolve", pr); err != nil {
		return Invocation{}, err
	}
	if err = requireTrigger(out, "resolve", req.Trigger); err != nil {
		return Invocation{}, err
	}
	if req.Repo, err = optionalSlug(out, repo); err != nil {
		return Invocation{}, err
	}
	return Invocation{Command: CommandResolve, Request: req}, nil
}

// parseStatus is cmd_status's argument loop (lib/run.sh:3035-3049).
//
// The three flags it throws away are the ones the composite action forwards to
// whichever leg it was told to run (action.yml:85-88). Status reads the pull
// request rather than acting on it, so none of them has anything to change.
func parseStatus(args []string, out *ui.IO) (Invocation, error) {
	var req StatusRequest
	var pr, repo string
	s := &scanner{args: args}
	for s.more() {
		var err error
		switch flag := s.flag(); flag {
		case "--pr":
			pr, err = s.value()
		case "--repo":
			repo, err = s.value()
		case "--harness", "--trigger":
			err = s.discard()
		case "--no-tips":
			s.skip()
		default:
			err = unknownOption(out, "status", flag, usageStatus)
		}
		if err != nil {
			return Invocation{}, err
		}
	}
	var err error
	if req.PR, err = requirePR(out, "status", pr); err != nil {
		return Invocation{}, err
	}
	if req.Repo, err = optionalSlug(out, repo); err != nil {
		return Invocation{}, err
	}
	return Invocation{Command: CommandStatus, Request: req}, nil
}

// parseWatchdog is cmd_watchdog's argument loop (lib/run.sh:3667-3681).
//
// It sweeps a repository rather than acting on one pull request with one
// harness, and it only ever runs on a schedule, so `--pr`, `--harness`,
// `--trigger` and `--no-tips` are accepted and thrown away.
func parseWatchdog(args []string, out *ui.IO) (Invocation, error) {
	req := WatchdogRequest{Timeout: WatchdogDefaultTimeout}
	var repo string
	s := &scanner{args: args}
	for s.more() {
		var err error
		switch flag := s.flag(); flag {
		case "--repo":
			repo, err = s.value()
		case "--timeout":
			// `timeout="${2:-1800}"` (lib/run.sh:3671): an empty value is
			// the default, and any other value is kept as written.
			if req.Timeout, err = s.value(); err == nil && req.Timeout == "" {
				req.Timeout = WatchdogDefaultTimeout
			}
		case "--pr", "--harness", "--trigger":
			err = s.discard()
		case "--no-tips":
			s.skip()
		default:
			err = unknownOption(out, "watchdog", flag, usageWatchdog)
		}
		if err != nil {
			return Invocation{}, err
		}
	}
	var err error
	if req.Repo, err = optionalSlug(out, repo); err != nil {
		return Invocation{}, err
	}
	return Invocation{Command: CommandWatchdog, Request: req}, nil
}

// parseInit is cmd_init's argument loop (lib/init.sh:35-46).
func parseInit(args []string, out *ui.IO) (Invocation, error) {
	var req InitRequest
	var repo string
	s := &scanner{args: args}
	for s.more() {
		var err error
		switch flag := s.flag(); flag {
		case "--owner":
			req.Owner, err = s.required(out, "--owner", usageInit)
		case "--repo":
			repo, err = s.required(out, "--repo", usageInit)
		case "--dry-run":
			req.DryRun = true
			s.skip()
		case "--upgrade":
			req.Upgrade = true
			s.skip()
		case "--yes", "-y":
			req.Yes = true
			s.skip()
		default:
			err = unknownOption(out, "init", flag, usageInit)
		}
		if err != nil {
			return Invocation{}, err
		}
	}
	var err error
	if req.Repo, err = optionalSlug(out, repo); err != nil {
		return Invocation{}, err
	}
	return Invocation{Command: CommandInit, Request: req}, nil
}

// parseAuthLogin is auth_login's argument loop (lib/auth.sh:512-520).
func parseAuthLogin(args []string, out *ui.IO) (Invocation, error) {
	req := AuthLoginRequest{Role: "loop"}
	s := &scanner{args: args}
	for s.more() {
		var err error
		switch flag := s.flag(); flag {
		case "--owner":
			req.Owner, err = s.required(out, "--owner", usageAuthLogin)
		case "--name":
			req.Name, err = s.required(out, "--name", usageAuthLogin)
		case "--role":
			req.Role, err = s.required(out, "--role", usageAuthLogin)
		default:
			err = unknownOption(out, "auth login", flag, usageAuthLogin)
		}
		if err != nil {
			return Invocation{}, err
		}
	}
	return Invocation{Command: CommandAuthLogin, Request: req}, nil
}

// parseAuthInstall is auth_install's argument loop (lib/auth.sh:793-800).
func parseAuthInstall(args []string, out *ui.IO) (Invocation, error) {
	req := AuthInstallRequest{Role: "loop"}
	s := &scanner{args: args}
	for s.more() {
		var err error
		switch flag := s.flag(); flag {
		case "--owner":
			req.Owner, err = s.required(out, "--owner", usageAuthInstall)
		case "--role":
			req.Role, err = s.required(out, "--role", usageAuthInstall)
		default:
			err = unknownOption(out, "auth install", flag, usageAuthInstall)
		}
		if err != nil {
			return Invocation{}, err
		}
	}
	return Invocation{Command: CommandAuthInstall, Request: req}, nil
}

// parseAuthRotate is auth_rotate's argument loop (lib/auth.sh:880-889).
func parseAuthRotate(args []string, out *ui.IO) (Invocation, error) {
	req := AuthRotateRequest{Role: "loop"}
	s := &scanner{args: args}
	for s.more() {
		var err error
		switch flag := s.flag(); flag {
		case "--owner":
			req.Owner, err = s.required(out, "--owner", usageAuthRotate)
		case "--role":
			req.Role, err = s.required(out, "--role", usageAuthRotate)
		case "--key":
			req.KeyFile, err = s.required(out, "--key", usageAuthRotate)
		default:
			err = unknownOption(out, "auth rotate", flag, usageAuthRotate)
		}
		if err != nil {
			return Invocation{}, err
		}
	}
	return Invocation{Command: CommandAuthRotate, Request: req}, nil
}

// parseAuthRefresh is auth_refresh's argument loop (lib/auth.sh:999-1009).
func parseAuthRefresh(args []string, out *ui.IO) (Invocation, error) {
	var req AuthRefreshRequest
	s := &scanner{args: args}
	for s.more() {
		var err error
		switch flag := s.flag(); flag {
		case "--harness":
			req.Harness, err = s.required(out, "--harness", usageAuthRefresh)
		case "--repo":
			req.Repo, err = s.required(out, "--repo", usageAuthRefresh)
		case "--secret":
			req.Secret, err = s.required(out, "--secret", usageAuthRefresh)
		case "--org":
			req.Org, err = s.required(out, "--org", usageAuthRefresh)
		default:
			err = unknownOption(out, "auth refresh", flag, usageAuthRefresh)
		}
		if err != nil {
			return Invocation{}, err
		}
	}
	return Invocation{Command: CommandAuthRefresh, Request: req}, nil
}
