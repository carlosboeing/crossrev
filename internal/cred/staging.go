// staging.go — restore, read, discard.
//
// Put a restored credential where the harness looks for it, in a directory that
// is thrown away when the leg finishes. The scratch home is the mechanism, not
// hygiene: the harness may well refresh and write back on its own, and there is
// no flag to stop it. Writing into a throwaway copy means that write reaches a
// directory nobody reads again, instead of a file something later hands back to
// the secret (lib/credentials.sh:99-105).

package cred

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// hostedRunnerValue is what GitHub sets RUNNER_ENVIRONMENT to on its own
// runners (lib/preflight.sh:251).
const hostedRunnerValue = "github-hosted"

// runnerEnvironment is the variable that separates the three environments
// CrossRev runs in.
//
// GITHUB_ACTIONS does not, and the difference is load-bearing rather than
// stylistic: GITHUB_ACTIONS is true on a self-hosted runner too, where the
// harness is logged in on disk and no secret is expected — which is why the
// templates filter those env lines out of the workflow they generate for one
// (lib/preflight.sh:245-250). Reading GITHUB_ACTIONS here would refuse every
// self-hosted run for a secret that was never meant to exist.
const runnerEnvironment = "RUNNER_ENVIRONMENT"

// Environment is the process environment Prepare reads a secret out of and
// exports a staging variable into.
//
// It is an interface so a test can hand Prepare a whole environment without
// mutating the one the test binary runs in, which os.Setenv would do to every
// other test in the package.
type Environment interface {
	// Lookup answers a variable's value and whether it was set at all. The
	// distinction is what Discard restores: `${!staging_env+set}` at
	// lib/credentials.sh:152 tells an unset variable from one holding "".
	Lookup(name string) (string, bool)
	// Set exports a variable for this process and its children.
	Set(name, value string) error
	// Unset removes it.
	Unset(name string) error
}

// OSEnvironment is the real process environment.
//
// The reads are os.LookupEnv, which internal/archtest deliberately leaves
// unconfined: internal/archtest/environment_test.go:32-42 confines the bulk
// read, os.Environ, and says why a named single read is not the boundary. The
// guard on a named credential is exec.Spec.Audience, at the destination.
type OSEnvironment struct{}

func (OSEnvironment) Lookup(name string) (string, bool) { return os.LookupEnv(name) }
func (OSEnvironment) Set(name, value string) error      { return os.Setenv(name, value) }
func (OSEnvironment) Unset(name string) error           { return os.Unsetenv(name) }

// FS is the file system Prepare stages into and Discard removes.
type FS interface {
	// MkdirTemp creates a directory nothing else holds, readable by nobody
	// else. `mktemp -d` at lib/credentials.sh:144 makes it 0700, and
	// os.MkdirTemp makes it 0700 too.
	MkdirTemp(dir, pattern string) (string, error)
	MkdirAll(path string, perm fs.FileMode) error
	WriteFile(name string, data []byte, perm fs.FileMode) error
	RemoveAll(path string) error
}

// OSFileSystem is the real one.
type OSFileSystem struct{}

func (OSFileSystem) MkdirTemp(dir, pattern string) (string, error) {
	return os.MkdirTemp(dir, pattern)
}
func (OSFileSystem) MkdirAll(path string, perm fs.FileMode) error { return os.MkdirAll(path, perm) }
func (OSFileSystem) WriteFile(name string, data []byte, perm fs.FileMode) error {
	return os.WriteFile(name, data, perm)
}
func (OSFileSystem) RemoveAll(path string) error { return os.RemoveAll(path) }

// Options are Prepare's dependencies. Every zero value is the real one.
type Options struct {
	// Env is the environment the secret arrives in and the staging variable is
	// exported into. Nil is OSEnvironment.
	Env Environment
	// FS is where the scratch directory goes. Nil is OSFileSystem.
	FS FS
	// Now decides freshness. Nil is time.Now.
	Now func() time.Time
	// ScratchRoot is the directory the scratch home is created inside. Empty
	// is the operating system's temporary directory, which is what `mktemp -d`
	// uses.
	ScratchRoot string
}

func (o Options) env() Environment {
	if o.Env != nil {
		return o.Env
	}
	return OSEnvironment{}
}

func (o Options) fs() FS {
	if o.FS != nil {
		return o.FS
	}
	return OSFileSystem{}
}

func (o Options) now() time.Time {
	if o.Now != nil {
		return o.Now()
	}
	return time.Now()
}

// Staged is what Prepare left behind, and everything Discard needs to undo it.
//
// lib/credentials.sh keeps this in four globals — CRED_SCRATCH and the three
// CRED_STAGING_ENV_* variables at :20-32 — because a shell function cannot
// return a handle. That is why calling cred_prepare twice without a discard
// leaks the first scratch directory: the second assignment overwrites the only
// record of it. A value cannot be overwritten by somebody else's call.
//
// The zero Staged is a leg that staged nothing, and discarding one is a no-op,
// which is `[[ -n "$CRED_SCRATCH" ]] || return 0` at lib/credentials.sh:188.
type Staged struct {
	// Dir is the scratch home, empty when nothing was staged.
	Dir string
	// File is the credential inside it, empty when nothing was staged.
	File string
	// Env is the variable that was exported to point at Dir.
	Env string

	envWasSet bool
	envWas    string

	options Options
}

// Prepare stages a restored credential and points the harness at it:
// cred_prepare (lib/credentials.sh:136-162), with the cred_assert_present check
// it opens with (:122-134).
//
// endpoint is the leg's configured endpoint, or empty. A leg using a named
// endpoint is exempt from the missing-secret refusal, because its credential is
// a different variable altogether — named by the endpoint block — and the
// adapter resolves it and dies with its own message when it is unset
// (lib/credentials.sh:119-121). The literal string "null" counts as no
// endpoint, because that is what `cfg_get` prints for an absent YAML key.
//
// It returns a zero *Staged on every path that stages nothing: a laptop, a
// self-hosted runner, a harness whose descriptor names no staging variable, and
// a harness whose secret is simply not present locally. All four are the early
// return at lib/credentials.sh:143, and all four cost nothing.
func Prepare(d Descriptor, endpoint string, o Options) (*Staged, error) {
	if err := assertPresent(d, endpoint, o); err != nil {
		return &Staged{}, err
	}

	secret := d.Credential.Secret
	stagingEnv := d.Credential.Staging.Env
	if secret == "" || stagingEnv == "" {
		return &Staged{}, nil
	}
	value, _ := o.env().Lookup(secret)
	if value == "" {
		return &Staged{}, nil
	}

	dir, err := o.fs().MkdirTemp(o.ScratchRoot, "")
	if err != nil {
		return &Staged{}, &Refusal{
			Kind:   ErrStaging,
			Err:    err,
			Reason: fmt.Sprintf("the restored %s credential has nowhere to be staged", d.Harness),
			Action: "CrossRev writes it into a scratch directory it throws away when the leg finishes. Check that the temporary directory is writable.",
		}
	}

	// The staging path may carry a directory of its own — opencode stages under
	// opencode/auth.json — and every shipped path before it was a bare
	// auth.json, which is why the write never needed this until then
	// (lib/credentials.sh:146-148).
	path := d.Credential.Staging.Path
	if path == "" {
		path = "auth.json"
	}
	file := filepath.Join(dir, path)

	// 0700, where `mkdir -p` at lib/credentials.sh:149 runs outside the
	// `(umask 077; …)` subshell below it and so produces 0755 under the default
	// umask. Measured on this platform: the opencode staging directory came out
	// 755 inside a 700 parent. Nothing could reach it either way, and there is
	// no reason for the wider mode, so the tighter one is used.
	if err := o.fs().MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return &Staged{}, stagingFailure(d, dir, o, err)
	}
	// 0600 is `(umask 077; printf … >"$staging_file")` at
	// lib/credentials.sh:150.
	if err := o.fs().WriteFile(file, []byte(value), 0o600); err != nil {
		return &Staged{}, stagingFailure(d, dir, o, err)
	}

	// Freshness is checked on what was written, before anything is exported —
	// the order at lib/credentials.sh:151. The shell's refusal is a ui_die,
	// which exits the process and leaves the scratch directory behind; a Go
	// caller carries on, so the credential is removed rather than left on disk.
	if err := AssertFresh(d, []byte(value), o.now()); err != nil {
		_ = o.fs().RemoveAll(dir)
		return &Staged{}, err
	}

	staged := &Staged{Dir: dir, File: file, Env: stagingEnv, options: o}
	staged.envWas, staged.envWasSet = o.env().Lookup(stagingEnv)
	if err := o.env().Set(stagingEnv, dir); err != nil {
		_ = o.fs().RemoveAll(dir)
		return &Staged{}, &Refusal{
			Kind:   ErrStaging,
			Err:    err,
			Reason: fmt.Sprintf("%s could not be pointed at the staged %s credential", stagingEnv, d.Harness),
			Action: fmt.Sprintf("CrossRev exports %s so the harness reads the scratch copy rather than a login on disk.", stagingEnv),
		}
	}
	return staged, nil
}

func stagingFailure(d Descriptor, dir string, o Options, err error) error {
	_ = o.fs().RemoveAll(dir)
	return &Refusal{
		Kind:   ErrStaging,
		Err:    err,
		Reason: fmt.Sprintf("the restored %s credential could not be written to its scratch home", d.Harness),
		Action: "CrossRev stages it in a directory it throws away when the leg finishes. Check that the temporary directory is writable.",
	}
}

// Discard removes the scratch home and puts the environment back: cred_discard
// (lib/credentials.sh:187-201).
//
// The variable is restored rather than unset. Prepare exports whatever name the
// descriptor gives it — CODEX_HOME, GROK_HOME or XDG_DATA_HOME — and unsetting
// one name written out by hand worked for codex and for no other harness:
// GROK_HOME stayed exported at a directory that had just been removed, and
// XDG_DATA_HOME, which is opencode's staging lever and not CrossRev's variable
// at all, reached everything XDG-aware the same process ran afterwards
// (lib/credentials.sh:22-29). One process runs both legs under
// `crossrev cycle`, so that is the ordinary path.
//
// It is idempotent and nil-safe: a second call, or a call on a leg that staged
// nothing, does nothing at all.
func Discard(s *Staged) error {
	if s == nil || s.Dir == "" {
		return nil
	}
	dir, name, was, wasSet, options := s.Dir, s.Env, s.envWas, s.envWasSet, s.options

	// Cleared first, so a failure below cannot be retried into a second removal
	// of a path this handle no longer describes.
	s.Dir, s.File, s.Env, s.envWas, s.envWasSet = "", "", "", "", false

	removeErr := options.fs().RemoveAll(dir)

	var envErr error
	if name != "" {
		if wasSet {
			envErr = options.env().Set(name, was)
		} else {
			envErr = options.env().Unset(name)
		}
	}

	if removeErr != nil {
		return fmt.Errorf("removing the scratch credential home: %w", removeErr)
	}
	if envErr != nil {
		return fmt.Errorf("restoring %s: %w", name, envErr)
	}
	return nil
}

// assertPresent refuses when the secret that should have arrived did not:
// cred_assert_present (lib/credentials.sh:122-134).
//
// Returning quietly is right on a laptop and on a self-hosted runner, where the
// harness keeps its own login on disk. On a GitHub-hosted runner it is a silent
// pass: a secret that is unset, misnamed, or scoped to the wrong place leaves
// exactly the same empty variable as a laptop, so the leg starts the harness
// with no credential and the first sign of trouble is an authentication error
// from the vendor, minutes later and one billed pass in
// (lib/credentials.sh:108-113).
//
// GitHub expands a reference to a secret that does not exist into the empty
// string rather than dropping the variable, so empty and unset are the same
// fault and both are checked (lib/credentials.sh:115-117).
func assertPresent(d Descriptor, endpoint string, o Options) error {
	if runner, _ := o.env().Lookup(runnerEnvironment); runner != hostedRunnerValue {
		return nil
	}
	if endpoint != "" && endpoint != "null" {
		return nil
	}
	secret := d.Credential.Secret
	if secret == "" {
		// preflight_harness_secret returns non-zero for a harness the
		// descriptor gives no secret, and cred_assert_present returns 0 on that
		// (lib/preflight.sh:236-241, lib/credentials.sh:126). An unknown
		// harness lands here too.
		return nil
	}
	if value, _ := o.env().Lookup(secret); value != "" {
		return nil
	}

	hint := d.Credential.SeedHint
	if hint == "" {
		hint = "Seed it with the vendor CLI"
	}

	return &Refusal{
		Kind: ErrSecretMissing,
		Reason: fmt.Sprintf("%s is not set, and this github-hosted runner has no other way to authenticate %s",
			secret, d.Harness),
		Action: fmt.Sprintf("A hosted runner is a fresh container with no login on disk, so this secret is the only credential %s has. "+
			"It is empty here, which means it was never set, is named differently, or is scoped to an organisation this repository cannot read — "+
			"GitHub expands a missing secret to an empty string rather than failing. "+
			"CrossRev stops now instead of starting %s unauthenticated, which surfaces as a vendor authentication error with nothing pointing back here. "+
			"%s. Check what is set with: gh secret list. A self-hosted runner needs none of this.",
			d.Harness, d.Harness, hint),
	}
}
