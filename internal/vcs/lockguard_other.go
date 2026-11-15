//go:build !linux && !darwin

package vcs

// withLockGuard has no guard on this platform and runs fn as it is. CrossRev
// ships for Darwin and Linux, where the file beside this one holds flock(2);
// this file exists to keep the package buildable.
func withLockGuard(_ string, fn func() error) error { return fn() }
