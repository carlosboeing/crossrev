//go:build !unix

package exec

import "os"

// isExecutable falls back to the mode where there is no access(2) to ask. The
// unix build asks the kernel; this one keeps the older mode test so the package
// still compiles for a platform the tool has never shipped on.
func isExecutable(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().Perm()&0o111 != 0
}
