//go:build !unix

package exec_test

import (
	"fmt"
	"os"
)

func helperRaise(string)      { fmt.Fprintln(os.Stderr, "helper: signals need a unix host"); os.Exit(2) }
func helperProcessGroup() int { return 0 }
func helperSpawn(string)      { fmt.Fprintln(os.Stderr, "helper: spawn needs a unix host"); os.Exit(2) }
func helperOrphan(string)     { fmt.Fprintln(os.Stderr, "helper: orphan needs a unix host"); os.Exit(2) }
