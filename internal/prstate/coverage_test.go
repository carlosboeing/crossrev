package prstate_test

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
)

// Every case in an oracle file must be named by a subtest. A fixture case that
// no test reads is a Bash behaviour nothing in Go answers for, and it fails
// silently: the file still parses, the suite still passes, and the divergence
// only shows up on a real pull request.
//
// Each parity subtest records the case it ran through recordCase, and TestMain
// compares the recorded names against the names in the files once the run is
// over.
var (
	coverageMu sync.Mutex
	coverage   = map[string]map[string]bool{}
)

func recordCase(file, name string) {
	coverageMu.Lock()
	defer coverageMu.Unlock()
	if coverage[file] == nil {
		coverage[file] = map[string]bool{}
	}
	coverage[file][name] = true
}

// oracleCaseNames reads the `cases[].name` list out of one oracle file.
func oracleCaseNames(file string) ([]string, error) {
	raw, err := os.ReadFile(filepath.Join(parityFixtureDir, file))
	if err != nil {
		return nil, err
	}
	var f struct {
		Cases []struct {
			Name string `json:"name"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(f.Cases))
	for _, c := range f.Cases {
		names = append(names, c.Name)
	}
	return names, nil
}

// TestMain runs the package's tests and then checks fixture coverage.
//
// The check is skipped under -run, because a filtered run deliberately executes
// a subset and would report every case the filter excluded as uncovered.
func TestMain(m *testing.M) {
	code := m.Run()
	if code == 0 && flag.Lookup("test.run").Value.String() == "" {
		if err := checkCoverage(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			code = 1
		}
	}
	os.Exit(code)
}

func checkCoverage() error {
	for _, file := range prstateOracleFiles {
		names, err := oracleCaseNames(file)
		if err != nil {
			return fmt.Errorf("reading %s: %w", file, err)
		}
		if len(names) == 0 {
			return fmt.Errorf("%s carries no cases", file)
		}
		var missing []string
		coverageMu.Lock()
		ran := coverage[file]
		coverageMu.Unlock()
		for _, name := range names {
			if !ran[name] {
				missing = append(missing, name)
			}
		}
		sort.Strings(missing)
		if len(missing) > 0 {
			return fmt.Errorf("%s: no subtest names %v", file, missing)
		}
	}
	return nil
}
