//go:build !linux && !darwin

package vcs

// isZombie has no probe on this platform, so PIDAlive keeps the signal
// probe's answer. CrossRev ships for Darwin and Linux, where the two files
// beside this one read the state directly; this file exists to keep the
// package buildable rather than to support a host nobody runs it on.
func isZombie(int) bool { return false }
