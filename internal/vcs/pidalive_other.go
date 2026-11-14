//go:build !linux && !darwin

package vcs

import "time"

// isZombie has no probe on this platform, so PIDAlive keeps the signal
// probe's answer. CrossRev ships for Darwin and Linux, where the two files
// beside this one read the state directly; this file exists to keep the
// package buildable rather than to support a host nobody runs it on.
func isZombie(int) bool { return false }

// processStartTime has no probe on this platform either. A stop reads that
// as a holder it cannot identify, and signals nothing.
func processStartTime(int) (time.Time, bool) { return time.Time{}, false }
