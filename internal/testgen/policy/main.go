package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"go/format"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

type StateTestCase struct {
	Desc     string
	Input    string
	Expected int
}

type ShouldContinueTestCase struct {
	Desc           string
	Verdict        string
	Pass           int
	MaxPasses      int
	Stop           bool
	Blocked        bool
	OtherPRsToday  int
	MaxPRsPerDay   int
	Files          int
	MaxFiles       int
	ExpectedAction string
	ExpectedFull   string
}

type RedrivableTestCase struct {
	Desc     string
	Input    string
	Expected bool
}

func findRepoRoot() string {
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	out, err := cmd.Output()
	if err == nil {
		return strings.TrimSpace(string(out))
	}
	// Fallback: look upward for go.mod
	dir, err := os.Getwd()
	if err != nil {
		return "."
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "."
}

func extractSection(filename, startMarker, endMarker string) ([]string, error) {
	content, err := os.ReadFile(filename)
	if err != nil {
		return nil, err
	}

	var lines []string
	scanner := bufio.NewScanner(bytes.NewReader(content))
	foundStart := false
	foundEnd := false

	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)
		if strings.Contains(trimmed, startMarker) {
			foundStart = true
			continue
		}
		if foundStart && strings.Contains(trimmed, endMarker) {
			foundEnd = true
			break
		}
		if foundStart {
			lines = append(lines, line)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if !foundStart {
		return nil, fmt.Errorf("start marker %q not found in %s", startMarker, filename)
	}
	if !foundEnd {
		return nil, fmt.Errorf("end marker %q not found in %s", endMarker, filename)
	}
	if len(lines) == 0 {
		return nil, fmt.Errorf("section %q in %s is empty", startMarker, filename)
	}
	return lines, nil
}

func parseStateCases(lines []string, fnName string) ([]StateTestCase, error) {
	var cases []StateTestCase
	joined := strings.Join(lines, "\n")
	scanner := bufio.NewScanner(strings.NewReader(joined))

	var currentStatement string
	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") || trimmed == "" {
			continue
		}
		if currentStatement != "" {
			currentStatement += " " + trimmed
		} else {
			currentStatement = trimmed
		}
		if strings.HasSuffix(currentStatement, "\\") {
			currentStatement = strings.TrimSuffix(currentStatement, "\\")
			currentStatement = strings.TrimSpace(currentStatement)
			continue
		}

		stmt := currentStatement
		currentStatement = ""

		if !strings.HasPrefix(stmt, "is ") {
			return nil, fmt.Errorf("unexpected statement format in state cases (expected 'is ...'): %q", stmt)
		}

		rest := strings.TrimPrefix(stmt, "is ")
		desc, afterDesc, ok := extractQuoted(rest)
		if !ok {
			return nil, fmt.Errorf("failed to extract description in state case: %q", stmt)
		}
		afterDesc = strings.TrimSpace(afterDesc)

		prefix := fmt.Sprintf("\"$(%s '", fnName)
		if !strings.HasPrefix(afterDesc, prefix) {
			return nil, fmt.Errorf("failed to find function call %q in state case: %q", prefix, stmt)
		}
		afterPrefix := afterDesc[len(prefix):]

		quoteEnd := strings.Index(afterPrefix, "')\"")
		if quoteEnd == -1 {
			return nil, fmt.Errorf("failed to find end of argument in state case: %q", stmt)
		}
		inputJSON := afterPrefix[:quoteEnd]
		if !json.Valid([]byte(inputJSON)) {
			return nil, fmt.Errorf("invalid marker JSON in state case: %q", stmt)
		}
		afterCall := strings.TrimSpace(afterPrefix[quoteEnd+len("')\""):])
		expStr, trailing, ok := extractQuoted(afterCall)
		if !ok {
			return nil, fmt.Errorf("failed to extract expected value in state case: %q", stmt)
		}
		if strings.TrimSpace(trailing) != "" {
			return nil, fmt.Errorf("unexpected trailing content in state case: %q", stmt)
		}
		expVal, err := strconv.Atoi(expStr)
		if err != nil {
			return nil, fmt.Errorf("invalid expected int %q in state case: %q", expStr, stmt)
		}
		cases = append(cases, StateTestCase{
			Desc:     desc,
			Input:    inputJSON,
			Expected: expVal,
		})
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan state cases: %w", err)
	}
	if currentStatement != "" {
		return nil, fmt.Errorf("dangling continuation in state cases: %q", currentStatement)
	}
	if len(cases) == 0 {
		return nil, fmt.Errorf("zero test cases parsed for function %s", fnName)
	}
	return cases, nil
}

func parseShouldContinueCases(lines []string) ([]ShouldContinueTestCase, error) {
	var cases []ShouldContinueTestCase
	joined := strings.Join(lines, "\n")
	scanner := bufio.NewScanner(strings.NewReader(joined))

	var currentStatement string
	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") || trimmed == "" {
			continue
		}
		if currentStatement != "" {
			currentStatement += " " + trimmed
		} else {
			currentStatement = trimmed
		}
		if strings.HasSuffix(currentStatement, "\\") {
			currentStatement = strings.TrimSuffix(currentStatement, "\\")
			currentStatement = strings.TrimSpace(currentStatement)
			continue
		}

		stmt := currentStatement
		currentStatement = ""

		if strings.HasPrefix(stmt, "decides ") {
			rest := strings.TrimPrefix(stmt, "decides ")
			parts := strings.Fields(rest)
			if len(parts) == 0 {
				return nil, fmt.Errorf("empty decides statement: %q", stmt)
			}
			action := parts[0]
			afterAction := strings.TrimSpace(strings.TrimPrefix(rest, action))
			desc, afterDesc, ok := extractQuoted(afterAction)
			if !ok {
				return nil, fmt.Errorf("failed to extract description in decides statement: %q", stmt)
			}
			args := strings.Fields(afterDesc)
			if action != "continue" && action != "converged" && action != "halt" {
				return nil, fmt.Errorf("invalid action %q in decides statement: %q", action, stmt)
			}
			parsed, err := parseShouldContinueArgs(args, stmt)
			if err != nil {
				return nil, err
			}
			parsed.Desc = desc
			parsed.ExpectedAction = action
			cases = append(cases, parsed)
		} else if strings.HasPrefix(stmt, "is ") {
			rest := strings.TrimPrefix(stmt, "is ")
			desc, afterDesc, ok := extractQuoted(rest)
			if !ok {
				return nil, fmt.Errorf("failed to extract description in is statement: %q", stmt)
			}
			afterDesc = strings.TrimSpace(afterDesc)
			prefix := "\"$(legs_should_continue "
			if !strings.HasPrefix(afterDesc, prefix) {
				return nil, fmt.Errorf("missing legs_should_continue call in statement: %q", stmt)
			}
			afterPrefix := afterDesc[len(prefix):]
			endIdx := strings.Index(afterPrefix, ")\"")
			if endIdx == -1 {
				return nil, fmt.Errorf("unclosed legs_should_continue call in statement: %q", stmt)
			}
			argsStr := afterPrefix[:endIdx]
			afterCall := strings.TrimSpace(afterPrefix[endIdx+2:])
			expFull, trailing, ok := extractQuoted(afterCall)
			if !ok {
				return nil, fmt.Errorf("failed to extract expected string in is statement: %q", stmt)
			}
			if strings.TrimSpace(trailing) != "" {
				return nil, fmt.Errorf("unexpected trailing content in is statement: %q", stmt)
			}
			args := strings.Fields(argsStr)
			parsed, err := parseShouldContinueArgs(args, stmt)
			if err != nil {
				return nil, err
			}
			// Read the verdict from the expected string rather than assuming it.
			// legs_should_continue answers "continue", "converged" or "halt", and
			// every `is` row in the table today happens to be a halt. Hardcoding
			// that would make the generator write the opposite of the oracle the
			// first time somebody adds a continue row, silently.
			expectedFields := strings.Fields(expFull)
			if len(expectedFields) == 0 {
				return nil, fmt.Errorf("empty expected string in is statement: %q", stmt)
			}
			action := expectedFields[0]
			if action != "continue" && action != "converged" && action != "halt" {
				return nil, fmt.Errorf("invalid action %q in expected string of is statement: %q", action, stmt)
			}

			parsed.Desc = desc
			parsed.ExpectedAction = action
			parsed.ExpectedFull = expFull
			cases = append(cases, parsed)
		} else {
			return nil, fmt.Errorf("unsupported statement in should_continue cases: %q", stmt)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan should_continue cases: %w", err)
	}
	if currentStatement != "" {
		return nil, fmt.Errorf("dangling continuation in should_continue cases: %q", currentStatement)
	}
	if len(cases) == 0 {
		return nil, fmt.Errorf("zero test cases parsed for should_continue")
	}
	return cases, nil
}

func parseShouldContinueArgs(args []string, stmt string) (ShouldContinueTestCase, error) {
	if len(args) != 9 {
		return ShouldContinueTestCase{}, fmt.Errorf("expected exactly 9 arguments, got %d: %q", len(args), stmt)
	}

	pass, err := parseIntArg("pass", args[1], stmt)
	if err != nil {
		return ShouldContinueTestCase{}, err
	}
	maxPasses, err := parseIntArg("max passes", args[2], stmt)
	if err != nil {
		return ShouldContinueTestCase{}, err
	}
	stop, err := parseBoolArg("stop", args[3], stmt)
	if err != nil {
		return ShouldContinueTestCase{}, err
	}
	blocked, err := parseBoolArg("blocked", args[4], stmt)
	if err != nil {
		return ShouldContinueTestCase{}, err
	}
	otherPRs, err := parseIntArg("other pull requests", args[5], stmt)
	if err != nil {
		return ShouldContinueTestCase{}, err
	}
	maxPRs, err := parseIntArg("maximum pull requests", args[6], stmt)
	if err != nil {
		return ShouldContinueTestCase{}, err
	}
	files, err := parseIntArg("files", args[7], stmt)
	if err != nil {
		return ShouldContinueTestCase{}, err
	}
	maxFiles, err := parseIntArg("maximum files", args[8], stmt)
	if err != nil {
		return ShouldContinueTestCase{}, err
	}

	return ShouldContinueTestCase{
		Verdict:       args[0],
		Pass:          pass,
		MaxPasses:     maxPasses,
		Stop:          stop,
		Blocked:       blocked,
		OtherPRsToday: otherPRs,
		MaxPRsPerDay:  maxPRs,
		Files:         files,
		MaxFiles:      maxFiles,
	}, nil
}

func parseIntArg(name, value, stmt string) (int, error) {
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("invalid %s integer %q in statement: %q", name, value, stmt)
	}
	return parsed, nil
}

func parseBoolArg(name, value, stmt string) (bool, error) {
	if value != "true" && value != "false" {
		return false, fmt.Errorf("invalid %s boolean %q in statement: %q", name, value, stmt)
	}
	return value == "true", nil
}

func parseRedrivableCases(lines []string, prefix string) ([]RedrivableTestCase, error) {
	var cases []RedrivableTestCase
	joined := strings.Join(lines, "\n")
	scanner := bufio.NewScanner(strings.NewReader(joined))

	var currentStatement string
	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") || trimmed == "" {
			continue
		}
		if currentStatement != "" {
			currentStatement += " " + trimmed
		} else {
			currentStatement = trimmed
		}
		if strings.HasSuffix(currentStatement, "\\") {
			currentStatement = strings.TrimSuffix(currentStatement, "\\")
			currentStatement = strings.TrimSpace(currentStatement)
			continue
		}

		stmt := currentStatement
		currentStatement = ""

		if !strings.HasPrefix(stmt, prefix+" ") {
			return nil, fmt.Errorf("unexpected statement format (expected %q prefix): %q", prefix, stmt)
		}

		rest := strings.TrimPrefix(stmt, prefix+" ")
		parts := strings.Fields(rest)
		if len(parts) == 0 {
			return nil, fmt.Errorf("empty redrivable statement: %q", stmt)
		}
		wantStr := parts[0]
		if wantStr != "yes" && wantStr != "no" {
			return nil, fmt.Errorf("invalid redrivable expectation %q in statement: %q", wantStr, stmt)
		}
		expected := wantStr == "yes"
		afterWant := strings.TrimSpace(strings.TrimPrefix(rest, wantStr))
		desc, afterDesc, ok := extractQuoted(afterWant)
		if !ok {
			return nil, fmt.Errorf("failed to extract description in redrivable statement: %q", stmt)
		}
		afterDesc = strings.TrimSpace(afterDesc)
		inputJSON, trailing, ok := extractQuoted(afterDesc)
		if !ok || strings.TrimSpace(trailing) != "" {
			return nil, fmt.Errorf("failed to parse input JSON in redrivable statement: %q", stmt)
		}
		if inputJSON != "" && !json.Valid([]byte(inputJSON)) {
			return nil, fmt.Errorf("invalid marker JSON in redrivable statement: %q", stmt)
		}
		cases = append(cases, RedrivableTestCase{
			Desc:     desc,
			Input:    inputJSON,
			Expected: expected,
		})
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan redrivable cases: %w", err)
	}
	if currentStatement != "" {
		return nil, fmt.Errorf("dangling continuation in redrivable cases: %q", currentStatement)
	}
	if len(cases) == 0 {
		return nil, fmt.Errorf("zero test cases parsed for redrivable prefix %s", prefix)
	}
	return cases, nil
}

func extractQuoted(s string) (string, string, bool) {
	s = strings.TrimSpace(s)
	if len(s) < 2 {
		return "", "", false
	}
	quoteChar := s[0]
	if quoteChar != '"' && quoteChar != '\'' {
		return "", "", false
	}
	escaped := false
	for i := 1; i < len(s); i++ {
		if escaped {
			escaped = false
			continue
		}
		if s[i] == '\\' {
			escaped = true
			continue
		}
		if s[i] == quoteChar {
			return s[1:i], s[i+1:], true
		}
	}
	return "", "", false
}

func renderPrstateFixtures(passCases, maxPassCases, currentReviewPassCases []StateTestCase) []byte {
	var buf bytes.Buffer
	buf.WriteString("// Code generated by internal/testgen/policy; DO NOT EDIT.\n\n")
	buf.WriteString("package prstate_test\n\n")
	buf.WriteString("type stateParityCase struct {\n\tdesc string\n\tmarkers string\n\twant int\n}\n\n")

	writeStateCases := func(name string, cases []StateTestCase) {
		buf.WriteString("var " + name + " = []stateParityCase{\n")
		for _, tc := range cases {
			buf.WriteString(fmt.Sprintf("\t{desc: %q, markers: %q, want: %d},\n", tc.Desc, tc.Input, tc.Expected))
		}
		buf.WriteString("}\n\n")
	}

	writeStateCases("parityStatePassCases", passCases)
	writeStateCases("parityStateMaxPassCases", maxPassCases)
	writeStateCases("parityStateCurrentReviewPassCases", currentReviewPassCases)
	return buf.Bytes()
}

func renderPolicyFixtures(shouldContinueCases []ShouldContinueTestCase, resolveRedrivableCases, reviewRedrivableCases []RedrivableTestCase) []byte {
	var buf bytes.Buffer
	buf.WriteString("// Code generated by internal/testgen/policy; DO NOT EDIT.\n\n")
	buf.WriteString("package policy_test\n\n")
	buf.WriteString("type shouldContinueParityCase struct {\n")
	buf.WriteString("\tdesc string\n\tverdict string\n\tpass int\n\tmaxPasses int\n")
	buf.WriteString("\tstop bool\n\tblocked bool\n\totherPRsToday int\n\tmaxPRsPerDay int\n")
	buf.WriteString("\tfiles int\n\tmaxFilesChangedPerPR int\n\twantAction string\n\twantFull string\n}\n\n")
	buf.WriteString("var parityShouldContinueCases = []shouldContinueParityCase{\n")
	for _, tc := range shouldContinueCases {
		buf.WriteString(fmt.Sprintf("\t{desc: %q, verdict: %q, pass: %d, maxPasses: %d, stop: %t, blocked: %t, otherPRsToday: %d, maxPRsPerDay: %d, files: %d, maxFilesChangedPerPR: %d, wantAction: %q, wantFull: %q},\n",
			tc.Desc, tc.Verdict, tc.Pass, tc.MaxPasses, tc.Stop, tc.Blocked, tc.OtherPRsToday, tc.MaxPRsPerDay, tc.Files, tc.MaxFiles, tc.ExpectedAction, tc.ExpectedFull))
	}
	buf.WriteString("}\n\n")

	buf.WriteString("type redrivableParityCase struct {\n\tdesc string\n\tmarker string\n\twant bool\n}\n\n")
	writeRedrivableCases := func(name string, cases []RedrivableTestCase) {
		buf.WriteString("var " + name + " = []redrivableParityCase{\n")
		for _, tc := range cases {
			buf.WriteString(fmt.Sprintf("\t{desc: %q, marker: %q, want: %t},\n", tc.Desc, tc.Input, tc.Expected))
		}
		buf.WriteString("}\n\n")
	}

	writeRedrivableCases("parityResolveRedrivableCases", resolveRedrivableCases)
	writeRedrivableCases("parityReviewRedrivableCases", reviewRedrivableCases)
	return buf.Bytes()
}

// writeGenerated writes the fixture, or in check mode compares it and reports a
// difference without touching the file. Check mode is what CI and the linter run,
// so an edit to a Bash policy table that nobody regenerated fails a check instead
// of leaving the Go vectors quietly disagreeing with the oracle they came from.
func writeGenerated(filename string, source []byte, checkOnly bool) error {
	formatted, err := format.Source(source)
	if err != nil {
		return fmt.Errorf("format generated code for %s: %w", filename, err)
	}

	if checkOnly {
		existing, err := os.ReadFile(filename)
		if err != nil {
			return fmt.Errorf("read %s for comparison: %w", filename, err)
		}
		if !bytes.Equal(existing, formatted) {
			return fmt.Errorf("%s is stale: regenerate it with `go run ./internal/testgen/policy`", filename)
		}
		return nil
	}

	if err := os.WriteFile(filename, formatted, 0644); err != nil {
		return fmt.Errorf("write %s: %w", filename, err)
	}
	return nil
}

func main() {
	checkOnly := flag.Bool("check", false, "compare the generated fixtures against the policy tables without writing them")
	flag.Parse()

	root := findRepoRoot()
	stateFile := filepath.Join(root, "tests", "fixtures", "policy", "state.txt")
	legsFile := filepath.Join(root, "tests", "fixtures", "policy", "legs.txt")
	policyOutFile := filepath.Join(root, "internal", "policy", "parity_generated_test.go")
	prstateOutFile := filepath.Join(root, "internal", "prstate", "parity_generated_test.go")

	passLines, err := extractSection(stateFile, "[policy-table: state_pass]", "[policy-table-end: state_pass]")
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to read state_pass: %v\n", err)
		os.Exit(1)
	}
	maxPassLines, err := extractSection(stateFile, "[policy-table: state_max_pass]", "[policy-table-end: state_max_pass]")
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to read state_max_pass: %v\n", err)
		os.Exit(1)
	}
	currentReviewPassLines, err := extractSection(stateFile, "[policy-table: state_current_review_pass]", "[policy-table-end: state_current_review_pass]")
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to read state_current_review_pass: %v\n", err)
		os.Exit(1)
	}
	shouldContinueLines, err := extractSection(legsFile, "[policy-table: legs_should_continue]", "[policy-table-end: legs_should_continue]")
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to read legs_should_continue: %v\n", err)
		os.Exit(1)
	}
	resolveRedrivableLines, err := extractSection(legsFile, "[policy-table: legs_resolve_redrivable]", "[policy-table-end: legs_resolve_redrivable]")
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to read legs_resolve_redrivable: %v\n", err)
		os.Exit(1)
	}
	reviewRedrivableLines, err := extractSection(legsFile, "[policy-table: legs_review_redrivable]", "[policy-table-end: legs_review_redrivable]")
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to read legs_review_redrivable: %v\n", err)
		os.Exit(1)
	}

	passCases, err := parseStateCases(passLines, "state_pass")
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to parse state_pass: %v\n", err)
		os.Exit(1)
	}
	maxPassCases, err := parseStateCases(maxPassLines, "state_max_pass")
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to parse state_max_pass: %v\n", err)
		os.Exit(1)
	}
	currentReviewPassCases, err := parseStateCases(currentReviewPassLines, "state_current_review_pass")
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to parse state_current_review_pass: %v\n", err)
		os.Exit(1)
	}
	shouldContinueCases, err := parseShouldContinueCases(shouldContinueLines)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to parse should_continue: %v\n", err)
		os.Exit(1)
	}
	resolveRedrivableCases, err := parseRedrivableCases(resolveRedrivableLines, "redrivable")
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to parse resolve_redrivable: %v\n", err)
		os.Exit(1)
	}
	reviewRedrivableCases, err := parseRedrivableCases(reviewRedrivableLines, "review_redrivable")
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to parse review_redrivable: %v\n", err)
		os.Exit(1)
	}

	if err := writeGenerated(prstateOutFile, renderPrstateFixtures(passCases, maxPassCases, currentReviewPassCases), *checkOnly); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := writeGenerated(policyOutFile, renderPolicyFixtures(shouldContinueCases, resolveRedrivableCases, reviewRedrivableCases), *checkOnly); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	verb := "Generated"
	if *checkOnly {
		verb = "Verified"
	}
	fmt.Printf("%s data-only parity fixtures successfully (%d pass, %d max_pass, %d current_review_pass, %d should_continue, %d resolve_redrive, %d review_redrive cases)\n",
		verb, len(passCases), len(maxPassCases), len(currentReviewPassCases), len(shouldContinueCases), len(resolveRedrivableCases), len(reviewRedrivableCases))
}
