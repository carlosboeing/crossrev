package harness_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/carlosboeing/crossrev/internal/harness"
)

// The rollout that is read is this session's, chosen by correlation and never
// by being newest.
//
// `~/.codex/sessions` is one directory shared by every Codex process on the
// machine. Reading the newest file names another process's model, prices this
// leg at its rates, and fires the substitution warning on a run that never
// substituted anything — which is what usage.go's header argues at length and
// what nothing checked: ten mutations survived across this function, including
// inverting the correlation test so it picks any rollout EXCEPT this session's.
//
// The three fixtures are the three ways the search can land: the id in the
// filename, the id inside the file, and a decoy that is newer than both.
func TestReadCodexRolloutCorrelatesOnTheSession(t *testing.T) {
	const (
		mine   = "0199abcd-1111-2222-3333-444455556666"
		theirs = "0199ffff-9999-8888-7777-666655554444"
	)

	// A rollout line is an envelope, and the fields live on the turn_context
	// record inside `payload` (lib/usage.sh:288-316).
	rollout := func(id, model, effort string) string {
		return `{"timestamp":"2026-08-29T00:00:00Z","type":"session_meta","payload":{"id":"` + id + `"}}` + "\n" +
			`{"timestamp":"2026-08-29T00:00:01Z","type":"turn_context","payload":{"model":"` + model + `","effort":"` + effort + `"}}` + "\n"
	}

	tests := []struct {
		name string
		// files are written in order, so a later name sorts above an earlier
		// one and stands in for "newer" the way `find | sort -r` sees it.
		files      map[string]string
		sessionID  string
		wantModel  string
		wantEffort string
	}{
		{
			name: "the session id is in the filename",
			files: map[string]string{
				"rollout-2026-08-29T00-00-00-" + mine + ".jsonl": rollout(mine, "gpt-5-codex", "high"),
			},
			sessionID: mine, wantModel: "gpt-5-codex", wantEffort: "high",
		},
		{
			name: "the session id is only inside the file",
			files: map[string]string{
				"rollout-2026-08-29T00-00-00-anonymous.jsonl": rollout(mine, "gpt-5-codex", "medium"),
			},
			sessionID: mine, wantModel: "gpt-5-codex", wantEffort: "medium",
		},
		{
			name: "a newer rollout belongs to another process",
			files: map[string]string{
				"rollout-2026-08-29T00-00-00-" + mine + ".jsonl":   rollout(mine, "gpt-5-codex", "high"),
				"rollout-2026-08-30T23-59-59-" + theirs + ".jsonl": rollout(theirs, "some-other-model", "low"),
			},
			sessionID: mine, wantModel: "gpt-5-codex", wantEffort: "high",
		},
		{
			name: "a newer rollout carrying no id at all",
			files: map[string]string{
				"rollout-2026-08-29T00-00-00-anonymous.jsonl": rollout(mine, "gpt-5-codex", "high"),
				"rollout-2026-08-30T23-59-59-decoy.jsonl":     rollout(theirs, "some-other-model", "low"),
			},
			sessionID: mine, wantModel: "gpt-5-codex", wantEffort: "high",
		},
		{
			name: "no rollout correlates, so nothing is reported",
			files: map[string]string{
				"rollout-2026-08-30T23-59-59-" + theirs + ".jsonl": rollout(theirs, "some-other-model", "low"),
			},
			sessionID: mine, wantModel: "", wantEffort: "",
		},
		{
			name: "reasoning_effort is the other spelling of the same value",
			files: map[string]string{
				"rollout-2026-08-29T00-00-00-" + mine + ".jsonl": `{"type":"turn_context","payload":{"model":"gpt-5-codex","reasoning_effort":"xhigh"}}` + "\n",
			},
			sessionID: mine, wantModel: "gpt-5-codex", wantEffort: "xhigh",
		},
		{
			name: "a line carrying no envelope is read at the top level",
			files: map[string]string{
				"rollout-2026-08-29T00-00-00-" + mine + ".jsonl": `{"model":"gpt-5-codex","effort":"high"}` + "\n",
			},
			sessionID: mine, wantModel: "gpt-5-codex", wantEffort: "high",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			sessions := filepath.Join(home, "sessions", "2026", "08")
			if err := os.MkdirAll(sessions, 0o700); err != nil {
				t.Fatalf("making the sessions directory: %v", err)
			}
			for name, body := range tt.files {
				if err := os.WriteFile(filepath.Join(sessions, name), []byte(body), 0o600); err != nil {
					t.Fatalf("writing %s: %v", name, err)
				}
			}

			model, effort := harness.ReadCodexRollout(home, tt.sessionID)
			if model != tt.wantModel || effort != tt.wantEffort {
				t.Errorf("ReadCodexRollout = (%q, %q), want (%q, %q)",
					model, effort, tt.wantModel, tt.wantEffort)
			}
		})
	}
}

// Every failure is a miss, and never a failed leg.
//
// The payload has been read by the time this runs, so rollout trouble must not
// change the leg's outcome (usage.go's header). Each case below is a way the
// read can go wrong.
func TestReadCodexRolloutTreatsEveryFailureAsAMiss(t *testing.T) {
	populated := func(t *testing.T) string {
		t.Helper()
		home := t.TempDir()
		sessions := filepath.Join(home, "sessions")
		if err := os.MkdirAll(sessions, 0o700); err != nil {
			t.Fatalf("making the sessions directory: %v", err)
		}
		if err := os.WriteFile(filepath.Join(sessions, "rollout-x.jsonl"),
			[]byte("not json at all\n"), 0o600); err != nil {
			t.Fatalf("writing the rollout: %v", err)
		}
		return home
	}

	tests := []struct {
		name      string
		home      func(t *testing.T) string
		sessionID string
	}{
		{name: "no home", home: func(*testing.T) string { return "" }, sessionID: "ses"},
		{name: "no session id", home: populated, sessionID: ""},
		{name: "the home does not exist", home: func(t *testing.T) string {
			return filepath.Join(t.TempDir(), "absent")
		}, sessionID: "ses"},
		{name: "sessions is a file rather than a directory", home: func(t *testing.T) string {
			home := t.TempDir()
			if err := os.WriteFile(filepath.Join(home, "sessions"), []byte("x"), 0o600); err != nil {
				t.Fatalf("writing the blocking file: %v", err)
			}
			return home
		}, sessionID: "ses"},
		{name: "the rollout is not JSON", home: populated, sessionID: "ses"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if model, effort := harness.ReadCodexRollout(tt.home(t), tt.sessionID); model != "" || effort != "" {
				t.Errorf("ReadCodexRollout = (%q, %q), want a miss", model, effort)
			}
		})
	}
}

// The session id is read from either spelling, at any depth.
//
// `.thread_id? // .session_id? // empty | select(type == "string")`: the
// alternative falls through on null and false, and the select drops whatever
// survived if it is not a string rather than retrying the second spelling. Both
// the second spelling and the descent were dead to the suite.
func TestCodexSessionIDReadsBothSpellings(t *testing.T) {
	tests := []struct {
		name   string
		events string
		want   string
	}{
		{name: "thread_id at the top level",
			events: `{"type":"thread.started","thread_id":"th_1"}` + "\n", want: "th_1"},
		{name: "session_id at the top level",
			events: `{"type":"session.created","session_id":"se_1"}` + "\n", want: "se_1"},
		{name: "thread_id wins over session_id on the same object",
			events: `{"thread_id":"th_1","session_id":"se_1"}` + "\n", want: "th_1"},
		{name: "a null thread_id falls through to session_id",
			events: `{"thread_id":null,"session_id":"se_1"}` + "\n", want: "se_1"},
		{name: "a false thread_id falls through to session_id",
			events: `{"thread_id":false,"session_id":"se_1"}` + "\n", want: "se_1"},
		{name: "a numeric thread_id does not retry session_id on that object",
			events: `{"thread_id":5,"session_id":"se_1"}` + "\n" + `{"session_id":"se_2"}` + "\n", want: "se_2"},
		{name: "nested one level down",
			events: `{"type":"x","msg":{"session_id":"se_deep"}}` + "\n", want: "se_deep"},
		{name: "the first event carrying one wins",
			events: `{"noise":1}` + "\n" + `{"thread_id":"th_first"}` + "\n" + `{"thread_id":"th_second"}` + "\n",
			want:   "th_first"},
		{name: "no event carries one", events: `{"type":"x"}` + "\n", want: ""},
		{name: "the stream is not JSON", events: "garbage\n", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := harness.CodexSessionID([]byte(tt.events)); got != tt.want {
				t.Errorf("CodexSessionID = %q, want %q", got, tt.want)
			}
		})
	}
}
