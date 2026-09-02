// Package initcmd carries what `crossrev init` writes into a repository.
//
// The Bash command lives in lib/init.sh. It provisions the secrets a pairing
// needs, then writes three or four workflow files under .github/workflows/ and
// the policy file .github/crossrev.yml, each rendered from a template in the
// checkout it was installed from (lib/init.sh:568-588).
//
// # Only the templates are here so far
//
// assets.go embeds the six files under templates/ and hands each back byte for
// byte. Nothing renders them yet, and nothing calls this package: the
// substitutions lib/init.sh makes on the way out — the runner labels, the
// refresher scope, the resolved pairing and backlog destination — arrive with
// the step that ports _init_render_workflow and _init_write_config.
//
// # The copies under assets/ are generated
//
// A `go:embed` pattern is package-relative and cannot contain `..`, so this
// package cannot embed templates/ at the repository root directly.
// scripts/sync-embedded-assets.sh keeps a byte-identical copy under
// assets/templates/. Edit the root file and run the script; a hand edit to a
// copy is caught by `--check` in lint and by assets_test.go in the suite.
package initcmd
