package buildinfo

import (
	"errors"
	"runtime/debug"
	"testing"
)

const stampedRevision = "0123456789abcdef0123456789abcdef01234567"

func TestReadTakesVersionAndVCSSettingsFromTheBuildInfo(t *testing.T) {
	bi := &debug.BuildInfo{
		Main: debug.Module{Version: "v0.5.0"},
		Settings: []debug.BuildSetting{
			{Key: "vcs", Value: "git"},
			{Key: "vcs.revision", Value: stampedRevision},
			{Key: "vcs.modified", Value: "false"},
			{Key: "vcs.time", Value: "2026-08-27T00:00:00Z"},
		},
	}
	got := read(bi, true)
	if got.Version != "v0.5.0" {
		t.Fatalf("Version = %q, want v0.5.0", got.Version)
	}
	if got.Revision != stampedRevision {
		t.Fatalf("Revision = %q, want the stamped revision", got.Revision)
	}
	if got.Modified {
		t.Fatal("Modified = true for vcs.modified false")
	}
	if got.Time != "2026-08-27T00:00:00Z" {
		t.Fatalf("Time = %q", got.Time)
	}
	if !got.Stamped() {
		t.Fatal("Stamped() = false for a build carrying a revision")
	}
}

func TestReadReportsAnUnstampedBuildRatherThanFailing(t *testing.T) {
	// go run, go test and a build from an exported tarball all reach here.
	got := read(&debug.BuildInfo{Main: debug.Module{Version: "(devel)"}}, true)
	if got.Revision != "" {
		t.Fatalf("Revision = %q, want empty", got.Revision)
	}
	if got.Modified {
		t.Fatal("Modified = true with no vcs.modified setting")
	}
	if got.Stamped() {
		t.Fatal("Stamped() = true with no revision")
	}
	if got.Version != "(devel)" {
		t.Fatalf("Version = %q, want (devel)", got.Version)
	}
}

func TestReadSurvivesBuildInfoBeingUnavailable(t *testing.T) {
	got := read(nil, false)
	if got.Version != "" || got.Revision != "" || got.Modified || got.Time != "" {
		t.Fatalf("read(nil, false) = %+v, want the zero Info", got)
	}
}

func TestModifiedIsTrueOnlyForTheLiteralTrue(t *testing.T) {
	tests := []struct {
		value string
		want  bool
	}{
		{value: "true", want: true},
		{value: "false", want: false},
		{value: "", want: false},
		{value: "TRUE", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			bi := &debug.BuildInfo{Settings: []debug.BuildSetting{
				{Key: "vcs.revision", Value: stampedRevision},
				{Key: "vcs.modified", Value: tt.value},
			}}
			if got := read(bi, true).Modified; got != tt.want {
				t.Fatalf("Modified = %t for vcs.modified %q, want %t", got, tt.value, tt.want)
			}
		})
	}
}

func TestPinReturnsTheStampedRevision(t *testing.T) {
	got, err := pin(Info{Revision: stampedRevision})
	if err != nil {
		t.Fatalf("pin: %v", err)
	}
	if got != stampedRevision {
		t.Fatalf("pin = %q, want the stamped revision", got)
	}
}

func TestPinRefusesAnAbsentRevision(t *testing.T) {
	got, err := pin(Info{Version: "(devel)"})
	if !errors.Is(err, ErrRevisionAbsent) {
		t.Fatalf("pin error = %v, want ErrRevisionAbsent", err)
	}
	if got != "" {
		t.Fatalf("pin = %q, want empty on refusal", got)
	}
}

// A dirty tree pins a revision the checkout does not actually hold, so the
// refusal has to be distinguishable from the absent one: only one of the two
// is fixable by committing.
func TestPinRefusesAModifiedBuildDistinguishably(t *testing.T) {
	_, err := pin(Info{Revision: stampedRevision, Modified: true})
	if !errors.Is(err, ErrRevisionModified) {
		t.Fatalf("pin error = %v, want ErrRevisionModified", err)
	}
	if errors.Is(err, ErrRevisionAbsent) {
		t.Fatal("a modified build reports the absent-revision refusal")
	}
}

func TestPinRefusesARevisionThatIsNotFortyLowercaseHex(t *testing.T) {
	for _, rev := range []string{"abc", "0123456789ABCDEF0123456789abcdef01234567"} {
		if _, err := pin(Info{Revision: rev}); err == nil {
			t.Fatalf("pin(%q) = nil error, want a refusal", rev)
		}
	}
}

// Ordinary commands still run from a development build, so the exported
// readers must never panic on the test binary, which carries no vcs settings.
func TestReadAndPinRunFromADevelopmentBuild(t *testing.T) {
	info := Read()
	if _, err := Pin(); err == nil && !info.Stamped() {
		t.Fatal("Pin() succeeded on a build with no revision")
	}
}
