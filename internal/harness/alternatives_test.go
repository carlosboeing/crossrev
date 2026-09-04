package harness_test

import (
	"os/exec"
	"strings"
	"testing"

	execpkg "github.com/carlosboeing/crossrev/internal/exec"
	"github.com/carlosboeing/crossrev/internal/harness"
)

// jqAlternative runs the adapter's own jq filter, so the expectation below is
// the shell's answer rather than a second reading of the shell.
func jqAlternative(t *testing.T, filter, document string) string {
	t.Helper()
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq is not on PATH, so the shell side cannot be run")
	}
	// The adapters read the filter's answer through `$(…)`, which strips the
	// newline `jq -r` terminates it with (lib/adapters/agy.sh:105).
	out, err := exec.Command("bash", "-c",
		`printf '%s' "$(jq -r "$1" <<<"$2" 2>/dev/null)"`, "bash", filter, document).Output()
	if err != nil {
		t.Fatalf("running the filter: %v", err)
	}
	return string(out)
}

// An empty `error` does not fall through to the next member.
//
// `jq -r '.error // .response // empty'` steps past a null and a false and
// nothing else, so an `error` of "" wins the alternative and answers the empty
// string. The adapter then tests THAT with `[[ -n "$msg" ]]`
// (lib/adapters/agy.sh:106), which sends it to the stderr diagnosis — not to
// `.response`. Reading the two members as "the first non-empty string" put the
// harness's own response text where the stderr belonged, on exactly the runs
// where stderr holds the only diagnosis there is.
func TestAnEmptyErrorFieldFallsThroughToStderrRatherThanTheNextMember(t *testing.T) {
	const stderrLine = "fatal: the credential was rejected"

	tests := []struct {
		name    string
		harness string
		filter  string
		stdout  string
		want    string
	}{
		{
			name: "agy: an empty error takes the stderr", harness: "agy",
			filter: `.error // .response // empty`,
			stdout: `{"status":"ERROR","error":"","response":"the model answered"}`,
			want:   stderrLine,
		},
		{
			name: "agy: a null error takes the response", harness: "agy",
			filter: `.error // .response // empty`,
			stdout: `{"status":"ERROR","error":null,"response":"the model answered"}`,
			want:   "the model answered",
		},
		{
			name: "agy: a false error takes the response", harness: "agy",
			filter: `.error // .response // empty`,
			stdout: `{"status":"ERROR","error":false,"response":"the model answered"}`,
			want:   "the model answered",
		},
		{
			name: "agy: an empty error and an empty response take the stderr", harness: "agy",
			filter: `.error // .response // empty`,
			stdout: `{"status":"ERROR","error":"","response":""}`,
			want:   stderrLine,
		},
		{
			name: "grok: an empty error takes the stderr", harness: "grok",
			filter: `.error // .text // empty`,
			stdout: `{"error":"","text":"the model answered"}`,
			want:   stderrLine,
		},
		{
			name: "grok: a null error takes the text", harness: "grok",
			filter: `.error // .text // empty`,
			stdout: `{"error":null,"text":"the model answered"}`,
			want:   "the model answered",
		},
		{
			name: "claude: an empty result takes the stderr", harness: "claude",
			filter: `.result // empty`,
			stdout: `{"is_error":true,"result":""}`,
			want:   stderrLine,
		},
	}

	doc := descriptors(t)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// The shell's own answer for this document, so the expectation is
			// not a second opinion about what `//` does.
			answered := jqAlternative(t, tt.filter, tt.stdout)
			if answered != "" && answered != tt.want {
				t.Fatalf("the filter answers %q; the expectation says the message is %q", answered, tt.want)
			}
			if answered == "" && tt.want != stderrLine {
				t.Fatalf("the filter answers nothing; the expectation says the message is %q", tt.want)
			}

			adapter, known := harness.For(doc, tt.harness)
			if !known {
				t.Fatalf("no %s adapter", tt.harness)
			}
			envelope := adapter.Envelope(invocation(t, tt.harness, false), execpkg.Result{
				ExitCode: 1,
				Stdout:   []byte(tt.stdout),
				Stderr:   []byte("some banner\n" + stderrLine + "\n"),
			})
			if envelope.OK {
				t.Fatalf("a failing run produced an ok envelope: %+v", envelope)
			}
			if !strings.Contains(deref(envelope.Error), tt.want) {
				t.Errorf("error = %q, want it to carry %q", deref(envelope.Error), tt.want)
			}
		})
	}
}

// The session id search stops where the shell's `head -n 1` stops.
//
// `jq -Rr 'fromjson? | .sessionID // empty' | head -n 1` prints an empty LINE
// for an event whose sessionID is "", because only null and false are falsy in
// jq. head takes that line, the id is empty, and no export runs. Scanning on to
// the next event found an id the shell would never have exported against.
func TestOpencodeSessionIDStopsWhereTheShellStops(t *testing.T) {
	tests := []struct {
		name   string
		stdout string
		want   string
	}{
		{
			name:   "an empty id on the first event ends the search",
			stdout: "{\"type\":\"text\",\"sessionID\":\"\"}\n{\"type\":\"text\",\"sessionID\":\"ses_real\"}\n",
			want:   "",
		},
		{
			name:   "an absent id is skipped",
			stdout: "{\"type\":\"text\"}\n{\"type\":\"text\",\"sessionID\":\"ses_real\"}\n",
			want:   "ses_real",
		},
		{
			name:   "a null id is skipped",
			stdout: "{\"type\":\"text\",\"sessionID\":null}\n{\"type\":\"text\",\"sessionID\":\"ses_real\"}\n",
			want:   "ses_real",
		},
		{
			name:   "a false id is skipped",
			stdout: "{\"sessionID\":false}\n{\"sessionID\":\"ses_real\"}\n",
			want:   "ses_real",
		},
		{
			name:   "a line that is not JSON is skipped",
			stdout: "not json at all\n{\"sessionID\":\"ses_real\"}\n",
			want:   "ses_real",
		},
		{
			// jq raises a type error for that input alone, reports it on the
			// stderr the adapter discards, and carries on. Measured.
			name:   "a line that parses to a number is skipped",
			stdout: "5\n{\"sessionID\":\"ses_real\"}\n",
			want:   "ses_real",
		},
		{
			name:   "no event carries one",
			stdout: "{\"type\":\"text\"}\n",
			want:   "",
		},
	}

	doc := descriptors(t)
	adapter, known := harness.For(doc, "opencode")
	if !known {
		t.Fatal("no opencode adapter")
	}
	reader, isReader := adapter.(interface {
		SessionID(execpkg.Result) string
	})
	if !isReader {
		t.Fatal("the opencode adapter no longer answers a session id")
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// The shell's own answer, run over the same bytes.
			want := opencodeShellSessionID(t, tt.stdout)
			if want != tt.want {
				t.Fatalf("the shell answers %q where this case expects %q", want, tt.want)
			}
			if got := reader.SessionID(execpkg.Result{Stdout: []byte(tt.stdout)}); got != want {
				t.Errorf("SessionID = %q, and the shell answers %q", got, want)
			}
		})
	}
}

// opencodeShellSessionID is lib/adapters/opencode.sh:264, run over the bytes.
func opencodeShellSessionID(t *testing.T, stdout string) string {
	t.Helper()
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq is not on PATH, so the shell side cannot be run")
	}
	const script = `printf '%s' "$(jq -Rr 'fromjson? | .sessionID // empty' <<<"$1" 2>/dev/null | head -n 1)"`
	out, err := exec.Command("bash", "-c", script, "bash", strings.TrimSuffix(stdout, "\n")).Output()
	if err != nil {
		t.Fatalf("running the session-id filter: %v", err)
	}
	return string(out)
}
