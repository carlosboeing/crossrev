package cli

import (
	"fmt"
	"os"
)

// Main is the skeleton entry point for the native crossrev CLI.
func Main() int {
	fmt.Fprintln(os.Stderr, "crossrev: native binary not yet ready; use bin/crossrev")
	return 1
}
