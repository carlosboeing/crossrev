package main

import (
	"bufio"
	"bytes"
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
	inSection := false

	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)
		if strings.Contains(trimmed, startMarker) {
			inSection = true
			continue
		}
		if inSection && strings.Contains(trimmed, endMarker) {
			inSection = false
			break
		}
		if inSection {
			lines = append(lines, line)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return lines, nil
}

func parseStateCases(lines []string, fnName string) []StateTestCase {
	// Lines look like:
	// is "desc" "$(state_pass '[]')" "1"
	// or multiline:
	// is "desc" \
	//   "$(state_pass '[{"leg":"review","pass":1,"state":"complete"}]')" "2"
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

		// Process statement
		stmt := currentStatement
		currentStatement = ""

		// Format: is "desc" "$(fn 'input')" "expected"
		if strings.HasPrefix(stmt, "is ") {
			rest := strings.TrimPrefix(stmt, "is ")
			// extract first quoted string: desc
			desc, afterDesc, ok := extractQuoted(rest)
			if !ok {
				continue
			}
			afterDesc = strings.TrimSpace(afterDesc)
			// Now afterDesc should start with "$(" + fnName
			prefix := fmt.Sprintf("\"$(%s '", fnName)
			idx := strings.Index(afterDesc, prefix)
			if idx == -1 {
				continue
			}
			afterPrefix := afterDesc[idx+len(prefix):]
			// find end of single quote
			quoteEnd := strings.Index(afterPrefix, "')\"")
			if quoteEnd == -1 {
				continue
			}
			inputJSON := afterPrefix[:quoteEnd]
			afterCall := strings.TrimSpace(afterPrefix[quoteEnd+len("')\""):])
			expStr, _, ok := extractQuoted(afterCall)
			if !ok {
				continue
			}
			expVal, _ := strconv.Atoi(expStr)
			cases = append(cases, StateTestCase{
				Desc:     desc,
				Input:    inputJSON,
				Expected: expVal,
			})
		}
	}
	return cases
}

func parseShouldContinueCases(lines []string) []ShouldContinueTestCase {
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
				continue
			}
			action := parts[0]
			afterAction := strings.TrimSpace(strings.TrimPrefix(rest, action))
			desc, afterDesc, ok := extractQuoted(afterAction)
			if !ok {
				continue
			}
			args := strings.Fields(afterDesc)
			if len(args) < 9 {
				continue
			}
			verdict := args[0]
			pass, _ := strconv.Atoi(args[1])
			maxPasses, _ := strconv.Atoi(args[2])
			stop := args[3] == "true"
			blocked := args[4] == "true"
			otherPRs, _ := strconv.Atoi(args[5])
			maxPRs, _ := strconv.Atoi(args[6])
			files, _ := strconv.Atoi(args[7])
			maxFiles, _ := strconv.Atoi(args[8])

			cases = append(cases, ShouldContinueTestCase{
				Desc:           desc,
				Verdict:        verdict,
				Pass:           pass,
				MaxPasses:      maxPasses,
				Stop:           stop,
				Blocked:        blocked,
				OtherPRsToday:  otherPRs,
				MaxPRsPerDay:   maxPRs,
				Files:          files,
				MaxFiles:       maxFiles,
				ExpectedAction: action,
			})
		} else if strings.HasPrefix(stmt, "is ") {
			rest := strings.TrimPrefix(stmt, "is ")
			desc, afterDesc, ok := extractQuoted(rest)
			if !ok {
				continue
			}
			afterDesc = strings.TrimSpace(afterDesc)
			prefix := "\"$(legs_should_continue "
			if !strings.HasPrefix(afterDesc, prefix) {
				continue
			}
			afterPrefix := afterDesc[len(prefix):]
			endIdx := strings.Index(afterPrefix, ")\"")
			if endIdx == -1 {
				continue
			}
			argsStr := afterPrefix[:endIdx]
			afterCall := strings.TrimSpace(afterPrefix[endIdx+2:])
			expFull, _, ok := extractQuoted(afterCall)
			if !ok {
				continue
			}
			args := strings.Fields(argsStr)
			if len(args) < 9 {
				continue
			}
			verdict := args[0]
			pass, _ := strconv.Atoi(args[1])
			maxPasses, _ := strconv.Atoi(args[2])
			stop := args[3] == "true"
			blocked := args[4] == "true"
			otherPRs, _ := strconv.Atoi(args[5])
			maxPRs, _ := strconv.Atoi(args[6])
			files, _ := strconv.Atoi(args[7])
			maxFiles, _ := strconv.Atoi(args[8])

			cases = append(cases, ShouldContinueTestCase{
				Desc:           desc,
				Verdict:        verdict,
				Pass:           pass,
				MaxPasses:      maxPasses,
				Stop:           stop,
				Blocked:        blocked,
				OtherPRsToday:  otherPRs,
				MaxPRsPerDay:   maxPRs,
				Files:          files,
				MaxFiles:       maxFiles,
				ExpectedAction: "halt",
				ExpectedFull:   expFull,
			})
		}
	}
	return cases
}

func parseRedrivableCases(lines []string, prefix string) []RedrivableTestCase {
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

		if strings.HasPrefix(stmt, prefix+" ") {
			rest := strings.TrimPrefix(stmt, prefix+" ")
			parts := strings.Fields(rest)
			if len(parts) == 0 {
				continue
			}
			wantStr := parts[0]
			expected := wantStr == "yes"
			afterWant := strings.TrimSpace(strings.TrimPrefix(rest, wantStr))
			desc, afterDesc, ok := extractQuoted(afterWant)
			if !ok {
				continue
			}
			afterDesc = strings.TrimSpace(afterDesc)
			var inputJSON string
			if strings.HasPrefix(afterDesc, "'") && strings.HasSuffix(afterDesc, "'") {
				inputJSON = strings.Trim(afterDesc, "'")
			} else if strings.HasPrefix(afterDesc, "\"") && strings.HasSuffix(afterDesc, "\"") {
				inputJSON = strings.Trim(afterDesc, "\"")
			}
			cases = append(cases, RedrivableTestCase{
				Desc:     desc,
				Input:    inputJSON,
				Expected: expected,
			})
		}
	}
	return cases
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

func main() {
	root := findRepoRoot()
	stateFile := filepath.Join(root, "tests", "test-state.sh")
	legsFile := filepath.Join(root, "tests", "test-legs.sh")
	outFile := filepath.Join(root, "internal", "policy", "parity_generated_test.go")

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

	passCases := parseStateCases(passLines, "state_pass")
	maxPassCases := parseStateCases(maxPassLines, "state_max_pass")
	currentReviewPassCases := parseStateCases(currentReviewPassLines, "state_current_review_pass")
	shouldContinueCases := parseShouldContinueCases(shouldContinueLines)
	resolveRedrivableCases := parseRedrivableCases(resolveRedrivableLines, "redrivable")
	reviewRedrivableCases := parseRedrivableCases(reviewRedrivableLines, "review_redrivable")

	var buf bytes.Buffer
	buf.WriteString("// Code generated by internal/testgen/policy; DO NOT EDIT.\n\n")
	buf.WriteString("package policy_test\n\n")
	buf.WriteString("import (\n")
	buf.WriteString("\t\"testing\"\n\n")
	buf.WriteString("\t\"github.com/carlosboeing/crossrev/internal/policy\"\n")
	buf.WriteString(")\n\n")

	// TestParityStatePass
	buf.WriteString("func TestParityStatePass(t *testing.T) {\n")
	buf.WriteString("\ttests := []struct {\n\t\tdesc string\n\t\tmarkers string\n\t\twant int\n\t}{\n")
	for _, tc := range passCases {
		buf.WriteString(fmt.Sprintf("\t\t{desc: %q, markers: %q, want: %d},\n", tc.Desc, tc.Input, tc.Expected))
	}
	buf.WriteString("\t}\n\tfor _, tt := range tests {\n")
	buf.WriteString("\t\tt.Run(tt.desc, func(t *testing.T) {\n")
	buf.WriteString("\t\t\tgot := policy.Pass(tt.markers)\n")
	buf.WriteString("\t\t\tif got != tt.want {\n")
	buf.WriteString("\t\t\t\tt.Errorf(\"policy.Pass() = %d, want %d\", got, tt.want)\n")
	buf.WriteString("\t\t\t}\n\t\t})\n\t}\n}\n\n")

	// TestParityStateMaxPass
	buf.WriteString("func TestParityStateMaxPass(t *testing.T) {\n")
	buf.WriteString("\ttests := []struct {\n\t\tdesc string\n\t\tmarkers string\n\t\twant int\n\t}{\n")
	for _, tc := range maxPassCases {
		buf.WriteString(fmt.Sprintf("\t\t{desc: %q, markers: %q, want: %d},\n", tc.Desc, tc.Input, tc.Expected))
	}
	buf.WriteString("\t}\n\tfor _, tt := range tests {\n")
	buf.WriteString("\t\tt.Run(tt.desc, func(t *testing.T) {\n")
	buf.WriteString("\t\t\tgot := policy.MaxPass(tt.markers)\n")
	buf.WriteString("\t\t\tif got != tt.want {\n")
	buf.WriteString("\t\t\t\tt.Errorf(\"policy.MaxPass() = %d, want %d\", got, tt.want)\n")
	buf.WriteString("\t\t\t}\n\t\t})\n\t}\n}\n\n")

	// TestParityStateCurrentReviewPass
	buf.WriteString("func TestParityStateCurrentReviewPass(t *testing.T) {\n")
	buf.WriteString("\ttests := []struct {\n\t\tdesc string\n\t\tmarkers string\n\t\twant int\n\t}{\n")
	for _, tc := range currentReviewPassCases {
		buf.WriteString(fmt.Sprintf("\t\t{desc: %q, markers: %q, want: %d},\n", tc.Desc, tc.Input, tc.Expected))
	}
	buf.WriteString("\t}\n\tfor _, tt := range tests {\n")
	buf.WriteString("\t\tt.Run(tt.desc, func(t *testing.T) {\n")
	buf.WriteString("\t\t\tgot := policy.CurrentReviewPass(tt.markers)\n")
	buf.WriteString("\t\t\tif got != tt.want {\n")
	buf.WriteString("\t\t\t\tt.Errorf(\"policy.CurrentReviewPass() = %d, want %d\", got, tt.want)\n")
	buf.WriteString("\t\t\t}\n\t\t})\n\t}\n}\n\n")

	// TestParityShouldContinue
	buf.WriteString("func TestParityShouldContinue(t *testing.T) {\n")
	buf.WriteString("\ttests := []struct {\n")
	buf.WriteString("\t\tdesc string\n")
	buf.WriteString("\t\tverdict string\n")
	buf.WriteString("\t\tpass int\n")
	buf.WriteString("\t\tmaxPasses int\n")
	buf.WriteString("\t\tstop bool\n")
	buf.WriteString("\t\tblocked bool\n")
	buf.WriteString("\t\totherPRsToday int\n")
	buf.WriteString("\t\tmaxPRsPerDay int\n")
	buf.WriteString("\t\tfiles int\n")
	buf.WriteString("\t\tmaxFilesChangedPerPR int\n")
	buf.WriteString("\t\twantAction string\n")
	buf.WriteString("\t\twantFull string\n")
	buf.WriteString("\t}{\n")
	for _, tc := range shouldContinueCases {
		buf.WriteString(fmt.Sprintf("\t\t{desc: %q, verdict: %q, pass: %d, maxPasses: %d, stop: %t, blocked: %t, otherPRsToday: %d, maxPRsPerDay: %d, files: %d, maxFilesChangedPerPR: %d, wantAction: %q, wantFull: %q},\n",
			tc.Desc, tc.Verdict, tc.Pass, tc.MaxPasses, tc.Stop, tc.Blocked, tc.OtherPRsToday, tc.MaxPRsPerDay, tc.Files, tc.MaxFiles, tc.ExpectedAction, tc.ExpectedFull))
	}
	buf.WriteString("\t}\n\tfor _, tt := range tests {\n")
	buf.WriteString("\t\tt.Run(tt.desc, func(t *testing.T) {\n")
	buf.WriteString("\t\t\tgot := policy.ShouldContinue(tt.verdict, tt.pass, tt.maxPasses, tt.stop, tt.blocked, tt.otherPRsToday, tt.maxPRsPerDay, tt.files, tt.maxFilesChangedPerPR)\n")
	buf.WriteString("\t\t\tif got.Action != tt.wantAction {\n")
	buf.WriteString("\t\t\t\tt.Errorf(\"ShouldContinue().Action = %q, want %q\", got.Action, tt.wantAction)\n")
	buf.WriteString("\t\t\t}\n")
	buf.WriteString("\t\t\tif tt.wantFull != \"\" && got.String() != tt.wantFull {\n")
	buf.WriteString("\t\t\t\tt.Errorf(\"ShouldContinue().String() = %q, want %q\", got.String(), tt.wantFull)\n")
	buf.WriteString("\t\t\t}\n")
	buf.WriteString("\t\t})\n\t}\n}\n\n")

	// TestParityResolveRedrivable
	buf.WriteString("func TestParityResolveRedrivable(t *testing.T) {\n")
	buf.WriteString("\ttests := []struct {\n\t\tdesc string\n\t\tmarker string\n\t\twant bool\n\t}{\n")
	for _, tc := range resolveRedrivableCases {
		buf.WriteString(fmt.Sprintf("\t\t{desc: %q, marker: %q, want: %t},\n", tc.Desc, tc.Input, tc.Expected))
	}
	buf.WriteString("\t}\n\tfor _, tt := range tests {\n")
	buf.WriteString("\t\tt.Run(tt.desc, func(t *testing.T) {\n")
	buf.WriteString("\t\t\tgot := policy.ResolveRedrivable(tt.marker)\n")
	buf.WriteString("\t\t\tif got != tt.want {\n")
	buf.WriteString("\t\t\t\tt.Errorf(\"ResolveRedrivable() = %t, want %t\", got, tt.want)\n")
	buf.WriteString("\t\t\t}\n\t\t})\n\t}\n}\n\n")

	// TestParityReviewRedrivable
	buf.WriteString("func TestParityReviewRedrivable(t *testing.T) {\n")
	buf.WriteString("\ttests := []struct {\n\t\tdesc string\n\t\tmarker string\n\t\twant bool\n\t}{\n")
	for _, tc := range reviewRedrivableCases {
		buf.WriteString(fmt.Sprintf("\t\t{desc: %q, marker: %q, want: %t},\n", tc.Desc, tc.Input, tc.Expected))
	}
	buf.WriteString("\t}\n\tfor _, tt := range tests {\n")
	buf.WriteString("\t\tt.Run(tt.desc, func(t *testing.T) {\n")
	buf.WriteString("\t\t\tgot := policy.ReviewRedrivable(tt.marker)\n")
	buf.WriteString("\t\t\tif got != tt.want {\n")
	buf.WriteString("\t\t\t\tt.Errorf(\"ReviewRedrivable() = %t, want %t\", got, tt.want)\n")
	buf.WriteString("\t\t\t}\n\t\t})\n\t}\n}\n")

	formatted, err := format.Source(buf.Bytes())
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to format generated code: %v\n", err)
		os.WriteFile(outFile, buf.Bytes(), 0644)
		os.Exit(1)
	}

	if err := os.WriteFile(outFile, formatted, 0644); err != nil {
		fmt.Fprintf(os.Stderr, "failed to write %s: %v\n", outFile, err)
		os.Exit(1)
	}
	fmt.Printf("Generated %s successfully (%d pass, %d max_pass, %d current_review_pass, %d should_continue, %d resolve_redrive, %d review_redrive cases)\n",
		outFile, len(passCases), len(maxPassCases), len(currentReviewPassCases), len(shouldContinueCases), len(resolveRedrivableCases), len(reviewRedrivableCases))
}
