package app

import "os"

// Environment reads the two variables that decide where an App's key and
// metadata live.
//
// It is an interface so a test can name a whole config home without setting a
// variable in the process every other test shares. Nothing in this package
// reads XDG_CONFIG_HOME or HOME by any other route.
type Environment interface {
	// Getenv answers a variable's value, empty when it is unset. The shell
	// makes no distinction here: `${XDG_CONFIG_HOME:-$HOME/.config}` takes the
	// fallback for an empty value as readily as for a missing one.
	Getenv(name string) string
}

// OSEnvironment is the real process environment.
//
// The read is os.Getenv, which internal/archtest deliberately leaves
// unconfined: internal/archtest/environment_test.go:32-42 confines the bulk
// read, os.Environ, and says why a named single read is not the boundary.
type OSEnvironment struct{}

// Getenv answers this process's environment.
func (OSEnvironment) Getenv(name string) string { return os.Getenv(name) }

// The two roles an App is registered under (lib/auth.sh:56-84).
//
// One App per owner became one App per owner per role. The loop App is
// referenced by the jobs that check out a pull request branch and run a model
// over a diff; the refresher App can write a repository secret. Putting
// secrets:write on the loop App would put secret rewriting one injection away
// from attacker-controlled text.
const (
	RoleLoop      = "loop"
	RoleRefresher = "refresher"
)

// configHome is `${XDG_CONFIG_HOME:-$HOME/.config}`, the base both files sit
// under (lib/auth.sh:23, lib/auth.sh:329).
//
// The pieces are concatenated rather than joined with filepath.Join, and that
// is deliberate. `auth status` prints the key's path beside its mode, so the
// path is operator-visible text: with XDG_CONFIG_HOME=/x/ the shell prints
// /x//crossrev/apps and filepath.Join would print /x/crossrev/apps, a path the
// shipped tool never printed. Measured against the shell rather than reasoned
// about.
func configHome(env Environment) string {
	if base := env.Getenv("XDG_CONFIG_HOME"); base != "" {
		return base
	}
	return env.Getenv("HOME") + "/.config"
}

// Dir is where every App's key and metadata are kept (_auth_dir,
// lib/auth.sh:23).
func Dir(env Environment) string { return configHome(env) + "/crossrev/apps" }

// TokensPath is the ledger of long-lived tokens and the dates they stop
// working (_auth_tokens_file, lib/auth.sh:329).
//
// It sits beside the apps directory rather than inside it: the ledger is about
// harness tokens in a repository's secrets, not about an App.
func TokensPath(env Environment) string { return configHome(env) + "/crossrev/tokens.json" }

// PEMPath is where a role's private key lives, under dir (_auth_pem,
// lib/auth.sh:31).
//
// Keys registered before roles existed sit at <owner>.pem and belong to the
// loop, so the loop role reads that path when the roled one is absent. New
// registrations always write the roled path, so the legacy name dies out on its
// own rather than needing a migration step nobody would run.
//
// An empty role is the loop, which is `${2:-loop}` at the shell.
func PEMPath(dir, owner, role string) string {
	return rolePath(dir, owner, role, "pem")
}

// MetaPath is where a role's App metadata lives, under dir (_auth_meta,
// lib/auth.sh:39). It carries the same legacy fallback as PEMPath.
func MetaPath(dir, owner, role string) string {
	return rolePath(dir, owner, role, "json")
}

// rolePath is the shape both paths share: <dir>/<owner>.<role>.<ext>, or the
// legacy <dir>/<owner>.<ext> when the role is the loop's and only the legacy
// file is on disk.
func rolePath(dir, owner, role, ext string) string {
	if role == "" {
		role = RoleLoop
	}
	roled := dir + "/" + owner + "." + role + "." + ext
	if role != RoleLoop {
		return roled
	}
	legacy := dir + "/" + owner + "." + ext
	if !isRegularFile(roled) && isRegularFile(legacy) {
		return legacy
	}
	return roled
}

// isRegularFile is `[[ -f "$path" ]]`: a file that exists, is regular, and is
// reached through any symlinks on the way.
func isRegularFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

// write0600 writes body to dest so that the file is 0600 once the write
// finishes, whatever mode the path held before (_auth_write_0600,
// lib/auth.sh:47-57).
//
// os.WriteFile applies its mode argument on create only, exactly like the
// shell's umask. A write onto an existing file truncates it and keeps the mode
// it already had, so writing a key over a path somebody had widened to 0644
// left it at 0644. The rename replaces the inode, so the mode comes from the
// file this created rather than from whatever was there. It also means nothing
// ever reads a half-written key.
//
// A write it cannot make returns an error and removes its own temporary file,
// so a failed call leaves the destination and its sibling as they were.
func write0600(dest string, body []byte) error {
	tmp := dest + ".tmp"
	// A temporary left behind by an interrupted run would be written onto
	// rather than created, which is the defect this helper exists to close.
	// Removing it first means the write below always creates.
	os.Remove(tmp)
	if err := os.WriteFile(tmp, body, 0o600); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, dest); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}
