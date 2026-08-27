package exec

import (
	"os"
	"strings"
)

// Inherit returns the entries of the process environment whose names appear in
// allow, in the order the process holds them.
//
// The signature is an allowlist rather than a strip-list on purpose. This is the
// ADR 0001 boundary: no GitHub credential may reach a model-facing process, and
// its breach is silent. A strip-list holds only against the names somebody
// remembered to write down, so every credential a harness adds later passes
// through it by default. An allowlist passes nothing nobody named.
//
// A test in internal/archtest fails the build if os.Environ appears in any other
// file, which makes this the only door.
func Inherit(allow []string) []string {
	if len(allow) == 0 {
		return nil
	}

	allowed := make(map[string]struct{}, len(allow))
	for _, name := range allow {
		allowed[name] = struct{}{}
	}

	var inherited []string
	for _, entry := range os.Environ() {
		name, _, ok := strings.Cut(entry, "=")
		if !ok {
			// os.Environ does not produce these. Skipping an entry with no name
			// is the fail-closed answer if some platform ever does.
			continue
		}
		if _, permitted := allowed[name]; permitted {
			inherited = append(inherited, entry)
		}
	}
	return inherited
}
