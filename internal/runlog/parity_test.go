package runlog_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/carlosboeing/crossrev/internal/runlog"
)

// parityFixtureDir is the single package-relative route to the frozen Bash
// oracle. Go reads those files and never writes them, and no test here invokes
// the capture script: a Go run that could recapture would freeze Go's answer
// rather than Bash's.
const parityFixtureDir = "../../tests/fixtures/parity"

// runlogOracleFiles are the two oracle files this package answers for. Only the
// run_dirs and local_run_id_shape parts of paths.json belong here; the
// quarantine and sandbox parts of the same file belong to the sandbox port.
var runlogOracleFiles = []string{"redaction.json", "paths.json"}

// captured is the provenance block every oracle file carries. The filter is sed
// under LC_ALL=C, so the platform and the locale the Bash run happened under
// decide whether the vectors are byte-oriented.
type captured struct {
	Platform         string `json:"platform"`
	TrImplementation string `json:"tr_implementation"`
	Locale           string `json:"locale"`
}

func loadFixture(t *testing.T, name string, v any) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(parityFixtureDir, name))
	if err != nil {
		t.Fatalf("reading the %s oracle: %v", name, err)
	}
	if err := json.Unmarshal(raw, v); err != nil {
		t.Fatalf("decoding the %s oracle: %v", name, err)
	}
}

// TestParityFixturesCarryProvenance fails if an oracle file lost the block that
// says which machine produced it. A fixture with no provenance cannot be
// re-derived, and a divergence found against it could not be attributed.
func TestParityFixturesCarryProvenance(t *testing.T) {
	for _, name := range runlogOracleFiles {
		var f struct {
			Captured captured `json:"captured"`
			Function string   `json:"function"`
		}
		loadFixture(t, name, &f)
		if f.Captured.Platform == "" || f.Captured.TrImplementation == "" || f.Captured.Locale == "" {
			t.Errorf("%s: incomplete provenance %+v", name, f.Captured)
		}
		if f.Function == "" {
			t.Errorf("%s: names no Bash function", name)
		}
	}
}

type redactionOracle struct {
	Notice string `json:"notice"`
	Cases  []struct {
		Name        string `json:"name"`
		Text        string `json:"text"`
		Redacted    string `json:"redacted"`
		Published   string `json:"published"`
		PublishedRC int    `json:"published_rc"`
	} `json:"cases"`
}

func loadRedaction(t *testing.T) redactionOracle {
	t.Helper()
	var oracle redactionOracle
	loadFixture(t, "redaction.json", &oracle)
	if len(oracle.Cases) == 0 {
		t.Fatal("the redaction oracle holds no cases")
	}
	return oracle
}

// TestRedactNoticeParity pins the sentence appended to a masked body. It is
// published text, so a reworded copy in Go would change what a pull request
// says without changing a single test that looks only at behaviour.
func TestRedactNoticeParity(t *testing.T) {
	if got, want := runlog.RedactNotice, loadRedaction(t).Notice; got != want {
		t.Errorf("RedactNotice = %q, want %q", got, want)
	}
}

// TestRedactStringParity replays every credential shape the oracle froze
// through the string filter (log_redact_str, lib/log.sh:116).
func TestRedactStringParity(t *testing.T) {
	for _, c := range loadRedaction(t).Cases {
		t.Run(c.Name, func(t *testing.T) {
			if got := runlog.Redact(c.Text); got != c.Redacted {
				t.Errorf("Redact(%q) = %q, want %q", c.Text, got, c.Redacted)
			}
		})
	}
}

// TestRedactPublishParity replays the same shapes through the publish filter
// (log_redact_publish, lib/log.sh:148), which appends the notice when it
// changed anything and reports whether it could filter at all.
//
// The receiver is a nil *Log on purpose: the Bash function runs whether or not
// log_init has made a run directory, and a nil Log is that state.
func TestRedactPublishParity(t *testing.T) {
	var noLog *runlog.Log
	for _, c := range loadRedaction(t).Cases {
		t.Run(c.Name, func(t *testing.T) {
			got, err := noLog.Publish(c.Text)
			rc := 0
			if err != nil {
				rc = 1
			}
			if got != c.Published {
				t.Errorf("Publish(%q) = %q, want %q", c.Text, got, c.Published)
			}
			if rc != c.PublishedRC {
				t.Errorf("Publish(%q) rc = %d, want %d", c.Text, rc, c.PublishedRC)
			}
		})
	}
}

type pathsOracle struct {
	LocalRunIDShape string `json:"local_run_id_shape"`
	RunDirs         []struct {
		Name         string `json:"name"`
		Repo         string `json:"repo"`
		PR           string `json:"pr"`
		XDGStateHome string `json:"xdg_state_home"`
		Home         string `json:"home"`
		GitHubRunID  string `json:"github_run_id"`
		Dir          string `json:"dir"`
	} `json:"run_dirs"`
}

func loadPaths(t *testing.T) pathsOracle {
	t.Helper()
	var oracle pathsOracle
	loadFixture(t, "paths.json", &oracle)
	if len(oracle.RunDirs) == 0 {
		t.Fatal("the paths oracle holds no run directories")
	}
	return oracle
}

// TestRunDirParity replays log_run_dir (lib/log.sh:44) over the frozen
// environments. The trailing-slash case is the one that decides the
// implementation: the Bash function concatenates, so /state/ yields a doubled
// separator, and filepath.Join would quietly clean it away.
func TestRunDirParity(t *testing.T) {
	for _, c := range loadPaths(t).RunDirs {
		t.Run(c.Name, func(t *testing.T) {
			t.Setenv("XDG_STATE_HOME", c.XDGStateHome)
			t.Setenv("HOME", c.Home)
			t.Setenv("GITHUB_RUN_ID", c.GitHubRunID)
			if got := runlog.RunDir(c.Repo, c.PR); got != c.Dir {
				t.Errorf("RunDir(%q, %q) = %q, want %q", c.Repo, c.PR, got, c.Dir)
			}
		})
	}
}

var localRunID = regexp.MustCompile(`^local-[0-9]+$`)

// TestLocalRunIDShapeParity checks the id a run carries when no workflow gave
// it one (log_run_id, lib/log.sh:41). The oracle froze the shape rather than
// the value, because the value is a process id.
func TestLocalRunIDShapeParity(t *testing.T) {
	t.Setenv("GITHUB_RUN_ID", "")
	got := runlog.RunID()
	shape := localRunID.ReplaceAllString(got, "local-<pid>")
	if want := loadPaths(t).LocalRunIDShape; shape != want {
		t.Errorf("RunID() = %q, shape %q, want shape %q", got, shape, want)
	}
}

// TestRunIDPrefersTheWorkflowRun checks the other arm: the marker on a pull
// request names the workflow run, so the directory on disk has to as well.
func TestRunIDPrefersTheWorkflowRun(t *testing.T) {
	t.Setenv("GITHUB_RUN_ID", "12345")
	if got := runlog.RunID(); got != "12345" {
		t.Errorf("RunID() = %q, want %q", got, "12345")
	}
}
