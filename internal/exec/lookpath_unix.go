//go:build unix

package exec

import "syscall"

// isExecutable asks the kernel whether this caller may execute the path, which
// is the question bash's search asks — access(2) — rather than the question the
// mode answers. A file whose only execute bit belongs to a class the caller is
// not in is not executable here, and bash does not prefer it either: measured,
// a mode-0010 file the caller owns is skipped for the preferred slot and takes
// the fallback slot instead.
//
// Root is the exception, and it is bash's exception too: access(2) grants X_OK
// to root whenever any of the three execute bits is set, so a root session
// finds programs an ordinary caller cannot run.
//
// syscall.Access asks about the real user where bash asks about the effective
// one. The two differ only under a setuid binary, which nothing here is.
func isExecutable(path string) bool {
	const executeOK = 0x1 // X_OK
	return syscall.Access(path, executeOK) == nil
}
