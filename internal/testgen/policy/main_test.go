package main

import "testing"

func TestParseShouldContinueRejectsMalformedArguments(t *testing.T) {
	tests := []struct {
		name string
		line string
	}{
		{
			name: "invalid integer",
			line: `decides halt "invalid pass" issues-remain nope 3 false false 0 12 1 200`,
		},
		{
			name: "invalid boolean",
			line: `decides halt "invalid stop" issues-remain 1 3 maybe false 0 12 1 200`,
		},
		{
			name: "excess argument",
			line: `decides halt "extra value" issues-remain 1 3 false false 0 12 1 200 ignored`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := parseShouldContinueCases([]string{tt.line}); err == nil {
				t.Fatal("expected malformed arguments to be rejected")
			}
		})
	}
}

func TestParseRedrivableRejectsUnknownExpectedValue(t *testing.T) {
	line := `redrivable perhaps "unknown expectation" '{"state":"complete"}'`
	if _, err := parseRedrivableCases([]string{line}, "redrivable"); err == nil {
		t.Fatal("expected unknown redrivable value to be rejected")
	}
}

func TestParsersRejectDanglingContinuation(t *testing.T) {
	tests := []struct {
		name  string
		parse func() error
	}{
		{
			name: "state",
			parse: func() error {
				_, err := parseStateCases([]string{
					`is "empty state" "$(state_pass '[]')" "1"`,
					`is "unfinished" \`,
				}, "state_pass")
				return err
			},
		},
		{
			name: "should continue",
			parse: func() error {
				_, err := parseShouldContinueCases([]string{
					`decides continue "valid" issues-remain 1 3 false false 0 12 1 200`,
					`decides halt "unfinished" \`,
				})
				return err
			},
		},
		{
			name: "redrivable",
			parse: func() error {
				_, err := parseRedrivableCases([]string{
					`redrivable yes "valid" '{"state":"complete"}'`,
					`redrivable no "unfinished" \`,
				}, "redrivable")
				return err
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.parse(); err == nil {
				t.Fatal("expected dangling continuation to be rejected")
			}
		})
	}
}
