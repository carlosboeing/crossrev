package archtest_test

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
)

const modulePrefix = "github.com/carlosboeing/crossrev/"

var tier0 = map[string]bool{
	"internal/core": true,
}

var tier1 = map[string]bool{
	"internal/buildinfo": true,
	"internal/policy":    true,
	"internal/prstate":   true,
	"internal/diff":      true,
	"internal/validate":  true,
	"internal/intel":     true,
}

var tier2IntraEdges = map[string][]string{
	"internal/config":           nil,
	"internal/forge":            nil,
	"internal/exec":             nil,
	"internal/runlog":           nil,
	"internal/ui":               nil,
	"internal/prompt":           nil,
	"internal/vcs":              {"internal/exec"},
	"internal/cred":             {"internal/exec"},
	"internal/symbols":          {"internal/exec"},
	"internal/verify":           {"internal/exec"},
	"internal/forge/ghexec":     {"internal/forge", "internal/exec"},
	"internal/verify/ghactions": {"internal/verify", "internal/forge", "internal/exec"},
	"internal/harness":          {"internal/exec", "internal/cred", "internal/runlog"},
	"internal/sandbox":          {"internal/vcs"},
}

var tier3 = map[string]bool{
	"internal/review":    true,
	"internal/resolve":   true,
	"internal/cycle":     true,
	"internal/app":       true,
	"internal/initcmd":   true,
	"internal/preflight": true,
	"internal/cli":       true,
}

var allowedThirdParty = map[string]map[string]bool{
	"internal/config": {
		"go.yaml.in/yaml/v3": true,
	},
	"internal/initcmd": {
		"go.yaml.in/yaml/v3": true,
	},
	"internal/symbols": {
		"github.com/tree-sitter/go-tree-sitter": true,
	},
}

func getAllowedInternalImports(pkgRel string) map[string]bool {
	allowed := make(map[string]bool)

	// Entrypoint: cmd/crossrev is the composition root, so it may import every
	// internal package and no other package may import it.
	//
	// The rule used to name internal/cli alone, which held while the command
	// table was empty. Filling it means opening a forge client, a run log, a
	// harness adapter, the two legs, the cycle driver, the App services,
	// preflight and init — every tier there is. That widening is confined to
	// this one package: the tier-3 rule below is untouched, so no tier-3
	// package gains a peer import from it, and cmd/crossrev is the only path
	// on which two tier-3 packages meet.
	if pkgRel == "cmd/crossrev" {
		for p := range tier0 {
			allowed[p] = true
		}
		for p := range tier1 {
			allowed[p] = true
		}
		for p := range tier2IntraEdges {
			allowed[p] = true
		}
		for p := range tier3 {
			allowed[p] = true
		}
		return allowed
	}

	if tier0[pkgRel] {
		// Tier 0 may import stdlib only (0 internal packages)
		return allowed
	}

	if tier1[pkgRel] {
		// Tier 1 may import Tier 0 only (no peer tier 1 imports)
		for p := range tier0 {
			allowed[p] = true
		}
		return allowed
	}

	if _, ok := tier2IntraEdges[pkgRel]; ok {
		// Tier 2 may import Tier 0 and Tier 1
		for p := range tier0 {
			allowed[p] = true
		}
		for p := range tier1 {
			allowed[p] = true
		}
		// Plus allowed intra-tier edges
		for _, edge := range tier2IntraEdges[pkgRel] {
			allowed[edge] = true
		}
		return allowed
	}

	if tier3[pkgRel] {
		// Tier 3 may import lower tiers (Tier 0, 1, 2) only; NO tier-3 peer imports
		for p := range tier0 {
			allowed[p] = true
		}
		for p := range tier1 {
			allowed[p] = true
		}
		for p := range tier2IntraEdges {
			allowed[p] = true
		}
		return allowed
	}

	return nil
}

func isStdLib(importPath string) bool {
	// Standard library packages do not have a domain with a dot in the first path component
	firstSlash := strings.Index(importPath, "/")
	if firstSlash == -1 {
		return !strings.Contains(importPath, ".")
	}
	return !strings.Contains(importPath[:firstSlash], ".")
}

func TestProductionTierDAG(t *testing.T) {
	cfg := &packages.Config{
		Mode:  packages.NeedName | packages.NeedImports | packages.NeedFiles,
		Tests: false,
	}

	pkgs, err := packages.Load(cfg, "github.com/carlosboeing/crossrev/...")
	if err != nil {
		t.Fatalf("failed to load packages: %v", err)
	}
	// err reports only a driver failure. A package that fails to type-check or
	// parse loads with an empty import list, so every tier rule below would pass
	// on it vacuously and the DAG would report green over a package it never
	// examined. TestWorkerNetworkIsolation already checks this; so must this one.
	if packages.PrintErrors(pkgs) > 0 {
		t.Fatal("packages failed to load; the tier rules cannot be checked against them")
	}

	seenPackages := make(map[string]bool)

	for _, pkg := range pkgs {
		pkgPath := pkg.PkgPath
		if !strings.HasPrefix(pkgPath, "github.com/carlosboeing/crossrev") {
			continue
		}

		var relPath string
		if pkgPath == "github.com/carlosboeing/crossrev" {
			relPath = "."
		} else {
			relPath = strings.TrimPrefix(pkgPath, modulePrefix)
		}

		// Exclude archtest and testgen from production DAG validation
		if strings.HasPrefix(relPath, "internal/archtest") || strings.HasPrefix(relPath, "internal/testgen") {
			continue
		}

		seenPackages[relPath] = true
		// Sorted, so a failure list reads the same on every run.
		for _, violation := range auditPackageImports(relPath, slices.Sorted(maps.Keys(pkg.Imports))) {
			t.Error(violation)
		}
	}

	// Verify all expected packages were found and validated
	for p := range tier0 {
		if !seenPackages[p] {
			t.Errorf("expected package %s not found in loaded packages", p)
		}
	}
	for p := range tier1 {
		if !seenPackages[p] {
			t.Errorf("expected package %s not found in loaded packages", p)
		}
	}
	for p := range tier2IntraEdges {
		if !seenPackages[p] {
			t.Errorf("expected package %s not found in loaded packages", p)
		}
	}
	for p := range tier3 {
		if !seenPackages[p] {
			t.Errorf("expected package %s not found in loaded packages", p)
		}
	}
	if !seenPackages["cmd/crossrev"] {
		t.Errorf("expected package cmd/crossrev not found in loaded packages")
	}
}

// auditPackageImports is the whole verdict for one package: its tier
// classification, and every import it holds judged against that tier.
//
// It takes the import paths rather than a loaded package so that the three
// rules it applies have fixtures of their own. Each was removable with nothing
// failing, because the only caller was the walk over this module and this
// module holds no violation of any of them — a rule that tests the tree with
// nothing testing the rule. TestDependencyAuditVerdict below is what tests it.
func auditPackageImports(relPath string, imports []string) []string {
	allowed := getAllowedInternalImports(relPath)
	if allowed == nil {
		return []string{fmt.Sprintf("package %s is not classified into any architectural tier", relPath)}
	}

	var violations []string
	for _, impPath := range imports {
		if !strings.HasPrefix(impPath, modulePrefix) {
			// Non-module import: must be stdlib or explicitly allowed third-party package
			if !isStdLib(impPath) {
				allowedExternal := allowedThirdParty[relPath]
				isAllowed := false
				for extPrefix := range allowedExternal {
					if impPath == extPrefix || strings.HasPrefix(impPath, extPrefix+"/") {
						isAllowed = true
						break
					}
				}
				if !isAllowed {
					violations = append(violations, fmt.Sprintf("package %s imports unapproved external package %s", relPath, impPath))
				}
			}
			continue
		}

		impRel := strings.TrimPrefix(impPath, modulePrefix)

		// Rule: No production package may import archtest or testgen
		if strings.HasPrefix(impRel, "internal/archtest") || strings.HasPrefix(impRel, "internal/testgen") {
			violations = append(violations, fmt.Sprintf("production package %s imports non-production package %s", relPath, impRel))
			continue
		}

		if !allowed[impRel] {
			violations = append(violations, fmt.Sprintf("illegal dependency: package %s imports %s (not allowed by architectural tier rules)", relPath, impRel))
		}
	}
	return violations
}

// One fixture per clause of the verdict, because each clause was removable with
// the suite still green.
//
// The archtest clause is the sharpest case. Deleting it leaves the tier rule
// below to report the same import, so the count does not change and only the
// sentence does — which is why every row names the words its violation must
// carry rather than counting them.
func TestDependencyAuditVerdict(t *testing.T) {
	tests := []struct {
		name    string
		pkg     string
		imports []string
		// want is every violation, in order. Empty means the package is legal.
		want []string
	}{
		{
			name:    "a tier-3 package importing tier 2 and the standard library",
			pkg:     "internal/cycle",
			imports: []string{"fmt", "github.com/carlosboeing/crossrev/internal/forge"},
		},
		{
			// The clause a production package cannot reach the rules that
			// police it. Without it the tier rule below reports the same
			// import, in different words, which is why the words are asserted.
			name:    "a production package importing the architecture rules",
			pkg:     "internal/cycle",
			imports: []string{"github.com/carlosboeing/crossrev/internal/archtest"},
			want:    []string{"production package internal/cycle imports non-production package internal/archtest"},
		},
		{
			name:    "a production package importing the parity generator",
			pkg:     "internal/cycle",
			imports: []string{"github.com/carlosboeing/crossrev/internal/testgen/policy"},
			want:    []string{"production package internal/cycle imports non-production package internal/testgen/policy"},
		},
		{
			// A third-party import nothing approved for this package.
			name:    "an unapproved external package",
			pkg:     "internal/validate",
			imports: []string{"golang.org/x/sync/errgroup"},
			want:    []string{"package internal/validate imports unapproved external package golang.org/x/sync/errgroup"},
		},
		{
			name:    "the third-party package this one is approved for",
			pkg:     "internal/initcmd",
			imports: []string{"go.yaml.in/yaml/v3"},
		},
		{
			// The allowance is a prefix, so a subpackage of an approved module
			// is approved and a package merely starting with the same letters
			// is not.
			name:    "a subpackage of an approved third-party module",
			pkg:     "internal/symbols",
			imports: []string{"github.com/tree-sitter/go-tree-sitter/bindings"},
		},
		{
			name:    "a module whose path merely starts with an approved one",
			pkg:     "internal/symbols",
			imports: []string{"github.com/tree-sitter/go-tree-sitter-typescript"},
			want:    []string{"package internal/symbols imports unapproved external package github.com/tree-sitter/go-tree-sitter-typescript"},
		},
		{
			name:    "a tier-1 package importing a peer",
			pkg:     "internal/validate",
			imports: []string{"github.com/carlosboeing/crossrev/internal/diff"},
			want:    []string{"illegal dependency: package internal/validate imports internal/diff (not allowed by architectural tier rules)"},
		},
		{
			name:    "a tier-3 package importing a peer",
			pkg:     "internal/review",
			imports: []string{"github.com/carlosboeing/crossrev/internal/resolve"},
			want:    []string{"illegal dependency: package internal/review imports internal/resolve (not allowed by architectural tier rules)"},
		},
		{
			// Tier 0 may import nothing of this module's at all.
			name:    "the base tier importing anything internal",
			pkg:     "internal/core",
			imports: []string{"github.com/carlosboeing/crossrev/internal/policy"},
			want:    []string{"illegal dependency: package internal/core imports internal/policy (not allowed by architectural tier rules)"},
		},
		{
			// Tier 2 has named intra-tier edges, and one that is not named is
			// still a violation.
			name:    "a tier-2 package taking an intra-tier edge it is granted",
			pkg:     "internal/vcs",
			imports: []string{"github.com/carlosboeing/crossrev/internal/exec"},
		},
		{
			name:    "a tier-2 package taking an intra-tier edge it is not granted",
			pkg:     "internal/ui",
			imports: []string{"github.com/carlosboeing/crossrev/internal/exec"},
			want:    []string{"illegal dependency: package internal/ui imports internal/exec (not allowed by architectural tier rules)"},
		},
		{
			// The composition root may import every tier, and nothing may
			// import it.
			name:    "the entrypoint importing every tier",
			pkg:     "cmd/crossrev",
			imports: []string{"github.com/carlosboeing/crossrev/internal/cli", "github.com/carlosboeing/crossrev/internal/core"},
		},
		{
			name:    "a package importing the entrypoint",
			pkg:     "internal/cli",
			imports: []string{"github.com/carlosboeing/crossrev/cmd/crossrev"},
			want:    []string{"illegal dependency: package internal/cli imports cmd/crossrev (not allowed by architectural tier rules)"},
		},
		{
			name:    "a package in no tier at all",
			pkg:     "internal/newthing",
			imports: []string{"fmt"},
			want:    []string{"package internal/newthing is not classified into any architectural tier"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := auditPackageImports(tt.pkg, tt.imports)
			if len(got) != len(tt.want) {
				t.Fatalf("violations = %q, want %q", got, tt.want)
			}
			for i, want := range tt.want {
				if got[i] != want {
					t.Errorf("violation %d = %q, want %q", i, got[i], want)
				}
			}
		})
	}
}
