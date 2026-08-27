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

func TestParseStateRejectsMalformedMarkerJSON(t *testing.T) {
	line := `is "malformed marker" "$(state_pass '{')" "1"`
	if _, err := parseStateCases([]string{line}, "state_pass"); err == nil {
		t.Fatal("expected malformed marker JSON to be rejected")
	}
}

func TestParseRedrivableValidatesNonemptyMarkerJSON(t *testing.T) {
	if _, err := parseRedrivableCases([]string{`redrivable no "malformed marker" '{'`}, "redrivable"); err == nil {
		t.Fatal("expected malformed nonempty marker JSON to be rejected")
	}

	cases, err := parseRedrivableCases([]string{`redrivable no "empty marker" ''`}, "redrivable")
	if err != nil {
		t.Fatalf("empty marker is a parity case and must remain accepted: %v", err)
	}
	if len(cases) != 1 || cases[0].Input != "" {
		t.Fatalf("empty marker cases = %#v, want one empty input", cases)
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

// The `is` branch used to hardcode "halt". A continue or converged row would
// have been written into the Go vectors as its opposite, with every check green.
func TestParseShouldContinueReadsTheIsRowActionFromItsExpectedString(t *testing.T) {
	tests := []struct {
		name       string
		line       string
		wantAction string
	}{
		{
			name:       "halt",
			line:       `is "the daily halt names the others" "$(legs_should_continue issues-remain 1 3 false false 12 12 10 200)" "halt reached max_prs_per_day (12)"`,
			wantAction: "halt",
		},
		{
			name:       "continue",
			line:       `is "a healthy verdict continues" "$(legs_should_continue issues-remain 1 3 false false 0 12 10 200)" "continue issues remain"`,
			wantAction: "continue",
		},
		{
			name:       "converged",
			line:       `is "convergence stops the loop" "$(legs_should_continue converged 1 3 false false 0 12 10 200)" "converged nothing left to fix"`,
			wantAction: "converged",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cases, err := parseShouldContinueCases([]string{tt.line})
			if err != nil {
				t.Fatalf("parse failed: %v", err)
			}
			if len(cases) != 1 {
				t.Fatalf("expected 1 case, got %d", len(cases))
			}
			if cases[0].ExpectedAction != tt.wantAction {
				t.Errorf("expected action %q, got %q", tt.wantAction, cases[0].ExpectedAction)
			}
		})
	}
}

func TestParseShouldContinueRejectsAnUnknownIsRowAction(t *testing.T) {
	line := `is "an invented verdict" "$(legs_should_continue issues-remain 1 3 false false 0 12 10 200)" "perhaps something happened"`
	if _, err := parseShouldContinueCases([]string{line}); err == nil {
		t.Fatal("expected an unknown action in the expected string to be rejected")
	}
}
