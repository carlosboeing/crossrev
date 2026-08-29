package runlog_test

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
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

// redactionOracle is the frozen redaction vectors.
//
// The bytes are read from the base64 fields and nowhere else. The plain text,
// redacted and published fields beside them are a reading aid for a human
// opening the file, and they are lossy: a vector built from bytes that are not
// valid UTF-8 cannot survive a round trip through a JSON string, and four of
// these are. Reading the aid would assert nothing about exactly the cases the
// filter has to be byte-oriented for.
//
// The base64 fields are pointers so that a missing one is an error rather than
// an empty string. Every case would pass vacuously against a field that decodes
// to nothing on both sides.
type redactionOracle struct {
	Notice string          `json:"notice"`
	Cases  []redactionCase `json:"cases"`
}

type redactionCase struct {
	Name         string  `json:"name"`
	TextB64      *string `json:"text_b64"`
	RedactedB64  *string `json:"redacted_b64"`
	PublishedB64 *string `json:"published_b64"`
	PublishedRC  int     `json:"published_rc"`
}

// bytes decodes one base64 field, and fails the test rather than the decode
// when the field is not there at all.
func (c redactionCase) bytes(t *testing.T, name string, field *string) string {
	t.Helper()
	if field == nil {
		t.Fatalf("%s: the oracle case carries no %s", c.Name, name)
	}
	raw, err := base64.StdEncoding.DecodeString(*field)
	if err != nil {
		t.Fatalf("%s: decoding %s: %v", c.Name, name, err)
	}
	return string(raw)
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
//
// Four of the vectors carry bytes that are not valid UTF-8, and they are the
// reason the filter operates on bytes rather than on runes: that is what a
// failing harness dumps, and a UTF-8 sed aborts on the first such byte with
// "illegal byte sequence", losing the whole line rather than the credential in
// it (lib/log.sh:97-100). Two of the four sit either side of a token prefix, so
// they also pin where a masked body starts and stops.
func TestRedactStringParity(t *testing.T) {
	for _, c := range loadRedaction(t).Cases {
		t.Run(c.Name, func(t *testing.T) {
			text := c.bytes(t, "text_b64", c.TextB64)
			want := c.bytes(t, "redacted_b64", c.RedactedB64)
			if got := runlog.Redact(text); got != want {
				t.Errorf("Redact(%q) = %q, want %q", text, got, want)
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
			text := c.bytes(t, "text_b64", c.TextB64)
			want := c.bytes(t, "published_b64", c.PublishedB64)
			got, err := noLog.Publish(text)
			rc := 0
			if err != nil {
				rc = 1
			}
			if got != want {
				t.Errorf("Publish(%q) = %q, want %q", text, got, want)
			}
			if rc != c.PublishedRC {
				t.Errorf("Publish(%q) rc = %d, want %d", text, rc, c.PublishedRC)
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

// TestLocalRunIDIsThisProcess pins the value the frozen shape cannot.
//
// The oracle froze `local-<pid>` because a process id is not reproducible, and
// a shape is satisfied by any process id at all — the parent's included. A
// parent pid is shared by every sibling a shell starts, so two runs launched
// from one script would claim the same run directory and overwrite each other's
// record. `local-$$` in the shell is the shell's own pid (lib/log.sh:41), and
// this is its counterpart.
func TestLocalRunIDIsThisProcess(t *testing.T) {
	t.Setenv("GITHUB_RUN_ID", "")
	if got, want := runlog.RunID(), "local-"+strconv.Itoa(os.Getpid()); got != want {
		t.Errorf("RunID() = %q, want %q", got, want)
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
