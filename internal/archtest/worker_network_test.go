package archtest_test

import (
	"strings"
	"testing"

	"golang.org/x/tools/go/callgraph"
	"golang.org/x/tools/go/callgraph/cha"
	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/ssa/ssautil"
)

func TestWorkerNetworkIsolation(t *testing.T) {
	cfg := &packages.Config{
		Mode: packages.NeedName |
			packages.NeedFiles |
			packages.NeedCompiledGoFiles |
			packages.NeedImports |
			packages.NeedTypes |
			packages.NeedTypesSizes |
			packages.NeedSyntax |
			packages.NeedTypesInfo |
			packages.NeedDeps,
		Tests: false,
	}

	pkgs, err := packages.Load(cfg, "github.com/carlosboeing/crossrev/internal/...")
	if err != nil {
		t.Fatalf("failed to load packages: %v", err)
	}
	if packages.PrintErrors(pkgs) > 0 {
		t.Fatalf("package loading reported errors")
	}

	prog, ssaPkgs := ssautil.AllPackages(pkgs, 0)
	prog.Build()

	var workerMain *ssa.Function
	for _, ssaPkg := range ssaPkgs {
		if ssaPkg != nil && ssaPkg.Pkg.Path() == "github.com/carlosboeing/crossrev/internal/symbols" {
			workerMain = ssaPkg.Func("WorkerMain")
		}
	}

	if workerMain == nil {
		t.Fatalf("symbols.WorkerMain not found in SSA packages")
	}

	cg := cha.CallGraph(prog)
	rootNode := cg.Nodes[workerMain]
	if rootNode == nil {
		// WorkerMain makes no outgoing calls, network isolation trivially holds
		return
	}

	visited := make(map[*callgraph.Node]bool)
	queue := []*callgraph.Node{rootNode}
	visited[rootNode] = true

	for len(queue) > 0 {
		curr := queue[0]
		queue = queue[1:]

		if curr.Func != nil && curr.Func.Pkg != nil && curr.Func.Pkg.Pkg != nil {
			pkgPath := curr.Func.Pkg.Pkg.Path()
			if pkgPath == "net" || strings.HasPrefix(pkgPath, "net/") || strings.Contains(pkgPath, "internal/forge") {
				t.Errorf("reachable forbidden function %s in package %s from symbols.WorkerMain", curr.Func.String(), pkgPath)
			}
		}

		for _, edge := range curr.Out {
			if edge.Callee != nil && !visited[edge.Callee] {
				visited[edge.Callee] = true
				queue = append(queue, edge.Callee)
			}
		}
	}
}
