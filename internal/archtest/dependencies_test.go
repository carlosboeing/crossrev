package archtest_test

import (
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
	"internal/symbols": {
		"github.com/tree-sitter/go-tree-sitter": true,
	},
}

func getAllowedInternalImports(pkgRel string) map[string]bool {
	allowed := make(map[string]bool)

	// Entrypoint: cmd/crossrev may ONLY import internal/cli
	if pkgRel == "cmd/crossrev" {
		allowed["internal/cli"] = true
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
		allowed := getAllowedInternalImports(relPath)
		if allowed == nil {
			t.Errorf("package %s is not classified into any architectural tier", relPath)
			continue
		}

		for impPath := range pkg.Imports {
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
						t.Errorf("package %s imports unapproved external package %s", relPath, impPath)
					}
				}
				continue
			}

			impRel := strings.TrimPrefix(impPath, modulePrefix)

			// Rule: No production package may import archtest or testgen
			if strings.HasPrefix(impRel, "internal/archtest") || strings.HasPrefix(impRel, "internal/testgen") {
				t.Errorf("production package %s imports non-production package %s", relPath, impRel)
				continue
			}

			if !allowed[impRel] {
				t.Errorf("illegal dependency: package %s imports %s (not allowed by architectural tier rules)", relPath, impRel)
			}
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
