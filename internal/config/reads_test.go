package config_test

import (
	"strings"
	"testing"
)

// `.policy.on_reads_unavailable` decides whether a leg with no served reads
// degrades visibly or stops: degrade by default, halt when asked.
func TestOnReadsUnavailableDefaultsToDegrade(t *testing.T) {
	cfg, err := load(t, "version: 2\n")
	if err != nil {
		t.Fatalf("loading a config with no policy: %v", err)
	}
	if got := cfg.OnReadsUnavailable(); got != "degrade" {
		t.Errorf("OnReadsUnavailable() = %q, want degrade", got)
	}
}

func TestOnReadsUnavailableReadsThePolicy(t *testing.T) {
	for _, tt := range []struct {
		yaml string
		want string
	}{
		{yaml: "version: 2\npolicy:\n  on_reads_unavailable: halt\n", want: "halt"},
		{yaml: "version: 2\npolicy:\n  on_reads_unavailable: degrade\n", want: "degrade"},
	} {
		cfg, err := load(t, tt.yaml)
		if err != nil {
			t.Fatalf("loading %q: %v", tt.yaml, err)
		}
		if got := cfg.OnReadsUnavailable(); got != tt.want {
			t.Errorf("OnReadsUnavailable() = %q, want %q", got, tt.want)
		}
	}
}

func TestOnReadsUnavailableRefusesAThirdValue(t *testing.T) {
	_, err := load(t, "version: 2\npolicy:\n  on_reads_unavailable: retry\n")
	if err == nil {
		t.Fatal("a third value for on_reads_unavailable loads")
	}
	if got := err.Error(); !strings.Contains(got, "on_reads_unavailable") {
		t.Errorf("err = %q, want it to name on_reads_unavailable", got)
	}
}
