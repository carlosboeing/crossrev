//go:build !darwin && !linux

package ui_test

import (
	"os"
	"testing"
)

// openPTY has no implementation here, so the arm-ordering test skips. That is
// the same direction IsTerminal takes on a platform it was not written for.
func openPTY(t *testing.T) *os.File {
	t.Helper()
	return nil
}
