package readserve

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/exec"
)

// mustGit runs one git call and fails the test unless it exits zero.
func mustGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	res := exec.NewOSRunner().Run(context.Background(), exec.Spec{
		Path: "git",
		Args: args,
		Dir:  dir,
	})
	if res.Err != nil || res.ExitCode != 0 {
		t.Fatalf("git %s exited %d: %s %s", strings.Join(args, " "), res.ExitCode, res.Stderr, res.Err)
	}
}

// createRepo builds a git fixture with a base and head commit.
func createRepo(t *testing.T) (dir, base, head string) {
	t.Helper()
	dir = t.TempDir()
	mustGit(t, dir, "init", "-q", "-b", "main")
	mustGit(t, dir, "config", "user.name", "Test")
	mustGit(t, dir, "config", "user.email", "test@example.com")

	content := ""
	for i := 1; i <= 500; i++ {
		content += fmt.Sprintf("line %d\n", i)
	}
	_ = os.WriteFile(filepath.Join(dir, "large.txt"), []byte(content), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "small.txt"), []byte("line 1\nline 2\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "empty.txt"), []byte(""), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "noeol.txt"), []byte("alpha\nomega"), 0o644)
	_ = os.Mkdir(filepath.Join(dir, "nested"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "nested", "file.txt"), []byte("nested\n"), 0o644)
	_ = os.Symlink("small.txt", filepath.Join(dir, "symlink.txt"))
	_ = os.WriteFile(filepath.Join(dir, "blob.bin"), []byte("GIF89a\x00\x01\x02"), 0o644)

	mustGit(t, dir, "add", "-A")
	mustGit(t, dir, "commit", "-m", "base")
	
	res := exec.NewOSRunner().Run(context.Background(), exec.Spec{
		Path: "git", Args: []string{"rev-parse", "HEAD"}, Dir: dir,
	})
	base = strings.TrimSpace(string(res.Stdout))

	_ = os.WriteFile(filepath.Join(dir, "small.txt"), []byte("line 1 edited\nline 2\n"), 0o644)
	mustGit(t, dir, "add", "small.txt")
	mustGit(t, dir, "commit", "-m", "head")

	// A submodule the server must refuse with the `submodule` reason. The
	// source is a local scratch repository, so the add stays offline; the
	// file protocol is allowed for this invocation only.
	subSrc := t.TempDir()
	mustGit(t, subSrc, "init", "-q", "-b", "main")
	mustGit(t, subSrc, "config", "user.name", "Test")
	mustGit(t, subSrc, "config", "user.email", "test@example.com")
	_ = os.WriteFile(filepath.Join(subSrc, "inner.txt"), []byte("inner\n"), 0o644)
	mustGit(t, subSrc, "add", "inner.txt")
	mustGit(t, subSrc, "commit", "-m", "inner")
	mustGit(t, dir, "-c", "protocol.file.allow=always", "submodule", "add", subSrc, "submod")
	mustGit(t, dir, "add", ".gitmodules", "submod")
	mustGit(t, dir, "commit", "-m", "submodule")

	res = exec.NewOSRunner().Run(context.Background(), exec.Spec{
		Path: "git", Args: []string{"rev-parse", "HEAD"}, Dir: dir,
	})
	head = strings.TrimSpace(string(res.Stdout))

	return dir, base, head
}

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int    `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params"`
}

type rpcResponse struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int    `json:"id"`
	Result  struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	} `json:"result,omitempty"`
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

func runRPC(t *testing.T, args []string, reqs []rpcRequest) ([]rpcResponse, string) {
	t.Helper()
	var inBuf, outBuf, errBuf bytes.Buffer
	for _, req := range reqs {
		b, _ := json.Marshal(req)
		inBuf.Write(b)
		inBuf.WriteString("\n")
	}
	code := Run(context.Background(), args, &inBuf, &outBuf, &errBuf)
	if code != 0 {
		t.Fatalf("Run exited %d: %s", code, errBuf.String())
	}

	var resps []rpcResponse
	lines := strings.Split(strings.TrimSpace(outBuf.String()), "\n")
	for _, line := range lines {
		if line == "" {
			continue
		}
		var resp rpcResponse
		if err := json.Unmarshal([]byte(line), &resp); err != nil {
			t.Fatalf("failed to decode response: %s", line)
		}
		resps = append(resps, resp)
	}
	return resps, errBuf.String()
}

func TestRangesFromBaseAndHeadByteMatchGit(t *testing.T) {
	dir, base, head := createRepo(t)
	logPath := filepath.Join(t.TempDir(), "log.jsonl")

	reqs := []rpcRequest{
		{JSONRPC: "2.0", ID: 1, Method: "initialize", Params: map[string]any{"protocolVersion": "2024-11-05"}},
		{JSONRPC: "2.0", ID: 2, Method: "tools/call", Params: map[string]any{
			"name": "read_file",
			"arguments": map[string]any{
				"path": "small.txt", "revision": "base",
			},
		}},
		{JSONRPC: "2.0", ID: 3, Method: "tools/call", Params: map[string]any{
			"name": "read_file",
			"arguments": map[string]any{
				"path": "small.txt", "revision": "head",
			},
		}},
	}
	
	args := []string{"--repo", dir, "--base", base, "--head", head, "--log", logPath, "--call", "1", "--max-result-bytes", "12288", "--max-result-lines", "400", "--per-call-bytes", "262144"}
	resps, _ := runRPC(t, args, reqs)
	
	if len(resps) != 3 {
		t.Fatalf("expected 3 responses, got %d", len(resps))
	}
	if resps[1].Result.IsError {
		t.Fatalf("expected success for base read, got error: %v", resps[1].Result)
	}
	if !strings.Contains(resps[1].Result.Content[0].Text, "line 1\n") {
		t.Errorf("base read missing line 1: %s", resps[1].Result.Content[0].Text)
	}
	if !strings.Contains(resps[2].Result.Content[0].Text, "line 1 edited") {
		t.Errorf("head read missing edit: %s", resps[2].Result.Content[0].Text)
	}
}

func TestEveryRefusalReason(t *testing.T) {
	dir, base, head := createRepo(t)
	logPath := filepath.Join(t.TempDir(), "log.jsonl")
	
	reqs := []rpcRequest{
		{JSONRPC: "2.0", ID: 1, Method: "tools/call", Params: map[string]any{
			"name": "read_file", "arguments": map[string]any{"path": "small.txt", "revision": "unknown_rev"},
		}},
		{JSONRPC: "2.0", ID: 2, Method: "tools/call", Params: map[string]any{
			"name": "read_file", "arguments": map[string]any{"path": "../outside.txt", "revision": "head"},
		}},
		{JSONRPC: "2.0", ID: 3, Method: "tools/call", Params: map[string]any{
			"name": "read_file", "arguments": map[string]any{"path": "missing.txt", "revision": "head"},
		}},
		{JSONRPC: "2.0", ID: 4, Method: "tools/call", Params: map[string]any{
			"name": "read_file", "arguments": map[string]any{"path": "blob.bin", "revision": "head"},
		}},
		{JSONRPC: "2.0", ID: 5, Method: "tools/call", Params: map[string]any{
			"name": "read_file", "arguments": map[string]any{"path": "symlink.txt", "revision": "head"},
		}},
		{JSONRPC: "2.0", ID: 6, Method: "tools/call", Params: map[string]any{
			"name": "read_file", "arguments": map[string]any{"path": "small.txt", "revision": "head", "start_line": 5},
		}},
		{JSONRPC: "2.0", ID: 7, Method: "tools/call", Params: map[string]any{
			"name": "read_file", "arguments": map[string]any{"path": "nested", "revision": "head"},
		}},
		{JSONRPC: "2.0", ID: 8, Method: "tools/call", Params: map[string]any{
			"name": "read_file", "arguments": map[string]any{"path": "submod", "revision": "head"},
		}},
	}
	args := []string{"--repo", dir, "--base", base, "--head", head, "--log", logPath, "--per-call-bytes", "262144"}
	resps, _ := runRPC(t, args, reqs)

	wants := []string{"outside_revisions", "path_invalid", "not_found", "binary", "symlink", "bad_range", "not_found", "submodule"}
	for i, resp := range resps {
		if !resp.Result.IsError {
			t.Errorf("expected error for case %d, got success", i)
			continue
		}
		if resp.Result.Content[0].Text != wants[i] {
			t.Errorf("expected reason %q for case %d, got %q", wants[i], i, resp.Result.Content[0].Text)
		}
	}
}

func TestResultCarriesRangeHeader(t *testing.T) {
	dir, base, head := createRepo(t)
	logPath := filepath.Join(t.TempDir(), "log.jsonl")

	reqs := []rpcRequest{
		{JSONRPC: "2.0", ID: 1, Method: "tools/call", Params: map[string]any{
			"name": "read_file", "arguments": map[string]any{
				"path": "small.txt", "revision": "head", "start_line": 1, "end_line": 2,
			},
		}},
	}
	args := []string{"--repo", dir, "--base", base, "--head", head, "--log", logPath, "--per-call-bytes", "262144"}
	resps, _ := runRPC(t, args, reqs)

	if resps[0].Result.IsError {
		t.Fatalf("expected success, got error: %s", resps[0].Result.Content[0].Text)
	}
	want := "small.txt@head lines 1-2\n1: line 1 edited\n2: line 2\n"
	if resps[0].Result.Content[0].Text != want {
		t.Errorf("got %q, want %q", resps[0].Result.Content[0].Text, want)
	}
}

func TestCutResultsNamingNextStart(t *testing.T) {
	dir, base, head := createRepo(t)
	logPath := filepath.Join(t.TempDir(), "log.jsonl")
	
	reqs := []rpcRequest{
		{JSONRPC: "2.0", ID: 1, Method: "tools/call", Params: map[string]any{
			"name": "read_file", "arguments": map[string]any{"path": "large.txt", "revision": "head"},
		}},
	}
	// Max 10 lines
	args := []string{"--repo", dir, "--base", base, "--head", head, "--log", logPath, "--max-result-lines", "10", "--per-call-bytes", "262144"}
	resps, _ := runRPC(t, args, reqs)
	
	if resps[0].Result.IsError {
		t.Fatalf("expected success, got error: %s", resps[0].Result.Content[0].Text)
	}
	text := resps[0].Result.Content[0].Text
	if !strings.Contains(text, "start_line=11") {
		t.Errorf("expected cut message naming next start line 11, got: %s", text)
	}
	if first := strings.SplitN(text, "\n", 2)[0]; first != "large.txt@head lines 1-10" {
		t.Errorf("expected header naming the returned range, got: %q", first)
	}
}

func TestLineTooLongMovingForward(t *testing.T) {
	dir, base, head := createRepo(t)
	logPath := filepath.Join(t.TempDir(), "log.jsonl")
	
	// Create a file with a very long line
	_ = os.WriteFile(filepath.Join(dir, "long.txt"), []byte("short line\n"+strings.Repeat("a", 20000)+"\nshort line 2\n"), 0o644)
	mustGit(t, dir, "add", "long.txt")
	mustGit(t, dir, "commit", "-m", "long line")
	
	res := exec.NewOSRunner().Run(context.Background(), exec.Spec{
		Path: "git", Args: []string{"rev-parse", "HEAD"}, Dir: dir,
	})
	head = strings.TrimSpace(string(res.Stdout))
	
	reqs := []rpcRequest{
		{JSONRPC: "2.0", ID: 1, Method: "tools/call", Params: map[string]any{
			"name": "read_file", "arguments": map[string]any{"path": "long.txt", "revision": "head", "start_line": 2},
		}},
	}
	args := []string{"--repo", dir, "--base", base, "--head", head, "--log", logPath, "--max-result-bytes", "12288", "--per-call-bytes", "262144"}
	resps, _ := runRPC(t, args, reqs)
	
	if !resps[0].Result.IsError {
		t.Fatalf("expected error for line_too_long, got success")
	}
	text := resps[0].Result.Content[0].Text
	if !strings.Contains(text, "line_too_long") {
		t.Errorf("expected line_too_long, got: %s", text)
	}
	if !strings.Contains(text, "start_line=3") {
		t.Errorf("expected start_line=3 in error message, got: %s", text)
	}
}

func TestBudgetExhaustionAsRefusal(t *testing.T) {
	dir, base, head := createRepo(t)
	logPath := filepath.Join(t.TempDir(), "log.jsonl")
	
	reqs := []rpcRequest{
		{JSONRPC: "2.0", ID: 1, Method: "tools/call", Params: map[string]any{
			"name": "read_file", "arguments": map[string]any{"path": "small.txt", "revision": "head"},
		}},
		{JSONRPC: "2.0", ID: 2, Method: "tools/call", Params: map[string]any{
			"name": "read_file", "arguments": map[string]any{"path": "large.txt", "revision": "head"},
		}},
	}
	// Per call budget 1000 bytes
	args := []string{"--repo", dir, "--base", base, "--head", head, "--log", logPath, "--per-call-bytes", "1000", "--max-result-bytes", "12288", "--max-result-lines", "400"}
	resps, _ := runRPC(t, args, reqs)
	
	if resps[0].Result.IsError {
		t.Fatalf("first read failed: %s", resps[0].Result.Content[0].Text)
	}
	if !resps[1].Result.IsError {
		t.Fatalf("second read should fail with budget_exhausted")
	}
	if !strings.Contains(resps[1].Result.Content[0].Text, "budget_exhausted") {
		t.Errorf("expected budget_exhausted, got: %s", resps[1].Result.Content[0].Text)
	}
}

func TestLogRecordsDigests(t *testing.T) {
	dir, base, head := createRepo(t)
	logPath := filepath.Join(t.TempDir(), "log.jsonl")
	
	reqs := []rpcRequest{
		{JSONRPC: "2.0", ID: 1, Method: "tools/call", Params: map[string]any{
			"name": "read_file", "arguments": map[string]any{"path": "small.txt", "revision": "head"},
		}},
	}
	args := []string{"--repo", dir, "--base", base, "--head", head, "--log", logPath, "--per-call-bytes", "262144"}
	_, _ = runRPC(t, args, reqs)
	
	b, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("failed to read log: %v", err)
	}
	
	logContent := string(b)
	if !strings.Contains(logContent, `"event":"read"`) {
		t.Errorf("log missing read event: %s", logContent)
	}
	if !strings.Contains(logContent, `"digest":`) {
		t.Errorf("log missing digest: %s", logContent)
	}
	if !strings.Contains(logContent, `"path":"small.txt"`) {
		t.Errorf("log missing path: %s", logContent)
	}
	if strings.Contains(logContent, "line 1 edited") {
		t.Errorf("log should not contain file text: %s", logContent)
	}
}

// gitShow returns the exact bytes `git show <rev>:<path>` prints.
func gitShow(t *testing.T, dir, rev, path string) []byte {
	t.Helper()
	res := exec.NewOSRunner().Run(context.Background(), exec.Spec{
		Path: "git", Args: []string{"-C", dir, "show", rev + ":" + path}, Dir: dir,
	})
	if res.Err != nil || res.ExitCode != 0 {
		t.Fatalf("git show %s:%s exited %d: %s %s", rev, path, res.ExitCode, res.Stderr, res.Err)
	}
	return res.Stdout
}

// readEvent is the logged payload of one successful read.
type readEvent struct {
	Path           string `json:"path"`
	Revision       string `json:"revision"`
	StartLine      int    `json:"start_line"`
	EndLine        int    `json:"end_line"`
	RequestedStart int    `json:"requested_start"`
	RequestedEnd   int    `json:"requested_end"`
	NextStart      int    `json:"next_start"`
	Digest         string `json:"digest"`
}

func readEvents(t *testing.T, logPath string) []readEvent {
	t.Helper()
	b, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("failed to read log: %v", err)
	}
	var out []readEvent
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var envelope struct {
			Event   string          `json:"event"`
			Payload json.RawMessage `json:"payload"`
		}
		if err := json.Unmarshal([]byte(line), &envelope); err != nil {
			t.Fatalf("failed to decode log line: %s", line)
		}
		if envelope.Event != "read" {
			continue
		}
		var ev readEvent
		if err := json.Unmarshal(envelope.Payload, &ev); err != nil {
			t.Fatalf("failed to decode read payload: %s", envelope.Payload)
		}
		out = append(out, ev)
	}
	return out
}

func TestContractBudgetDefaults(t *testing.T) {
	cfg := parseArgs([]string{"--repo", "r", "--base", "b", "--head", "h"})
	if cfg.MaxResultBytes != 12288 {
		t.Errorf("MaxResultBytes = %d, want 12288", cfg.MaxResultBytes)
	}
	if cfg.MaxResultLines != 400 {
		t.Errorf("MaxResultLines = %d, want 400", cfg.MaxResultLines)
	}
	if cfg.PerCallBytes != 262144 {
		t.Errorf("PerCallBytes = %d, want 262144", cfg.PerCallBytes)
	}
	if cfg.PerLegReads != 200 {
		t.Errorf("PerLegReads = %d, want 200", cfg.PerLegReads)
	}
	if cfg.PerLegBytes != 1048576 {
		t.Errorf("PerLegBytes = %d, want 1048576", cfg.PerLegBytes)
	}
}

func TestPerCallBudgetDefaultServesReads(t *testing.T) {
	dir, base, head := createRepo(t)
	logPath := filepath.Join(t.TempDir(), "log.jsonl")

	reqs := []rpcRequest{
		{JSONRPC: "2.0", ID: 1, Method: "tools/call", Params: map[string]any{
			"name": "read_file",
			"arguments": map[string]any{
				"path": "small.txt", "revision": "head",
			},
		}},
	}
	// No --per-call-bytes flag: the contract default must serve the read.
	args := []string{"--repo", dir, "--base", base, "--head", head, "--log", logPath}
	resps, _ := runRPC(t, args, reqs)

	if resps[0].Result.IsError {
		t.Fatalf("expected success with default per-call budget, got: %s", resps[0].Result.Content[0].Text)
	}
}

func TestPerLegReadLimitRefuses(t *testing.T) {
	dir, base, head := createRepo(t)
	logPath := filepath.Join(t.TempDir(), "log.jsonl")

	read := func(id int) rpcRequest {
		return rpcRequest{JSONRPC: "2.0", ID: id, Method: "tools/call", Params: map[string]any{
			"name": "read_file",
			"arguments": map[string]any{
				"path": "small.txt", "revision": "head",
			},
		}}
	}
	reqs := []rpcRequest{read(1), read(2), read(3)}
	args := []string{"--repo", dir, "--base", base, "--head", head, "--log", logPath,
		"--per-leg-reads", "2", "--per-call-bytes", "262144"}
	resps, _ := runRPC(t, args, reqs)

	if resps[0].Result.IsError || resps[1].Result.IsError {
		t.Fatalf("first two reads should succeed, got %q and %q",
			resps[0].Result.Content[0].Text, resps[1].Result.Content[0].Text)
	}
	if !resps[2].Result.IsError || resps[2].Result.Content[0].Text != "budget_exhausted" {
		t.Errorf("third read should refuse budget_exhausted, got %+v", resps[2].Result)
	}
}

func TestPerLegByteLimitRefuses(t *testing.T) {
	dir, base, head := createRepo(t)
	logPath := filepath.Join(t.TempDir(), "log.jsonl")

	read := func(id int) rpcRequest {
		return rpcRequest{JSONRPC: "2.0", ID: id, Method: "tools/call", Params: map[string]any{
			"name": "read_file",
			"arguments": map[string]any{
				"path": "small.txt", "revision": "head",
			},
		}}
	}
	reqs := []rpcRequest{read(1), read(2)}
	// One small read is 52 bytes of header and gutter lines; the second
	// must not fit in the remaining 8.
	args := []string{"--repo", dir, "--base", base, "--head", head, "--log", logPath,
		"--per-leg-bytes", "60", "--per-call-bytes", "262144"}
	resps, _ := runRPC(t, args, reqs)

	if resps[0].Result.IsError {
		t.Fatalf("first read should succeed, got: %s", resps[0].Result.Content[0].Text)
	}
	if !resps[1].Result.IsError || resps[1].Result.Content[0].Text != "budget_exhausted" {
		t.Errorf("second read should refuse budget_exhausted, got %+v", resps[1].Result)
	}
}

// An explicit zero leg allowance is exhaustion, not an absent flag: the
// leg passes its remainder down to every server session, and a spent leg
// refuses every further read instead of resetting to the contract
// defaults. Absent flags still default; only an explicit zero exhausts.
func TestExplicitZeroLegReadsRefuses(t *testing.T) {
	dir, base, head := createRepo(t)
	logPath := filepath.Join(t.TempDir(), "log.jsonl")

	reqs := []rpcRequest{
		{JSONRPC: "2.0", ID: 1, Method: "tools/call", Params: map[string]any{
			"name": "read_file", "arguments": map[string]any{"path": "small.txt", "revision": "head"},
		}},
		{JSONRPC: "2.0", ID: 2, Method: "ping"},
	}
	args := []string{"--repo", dir, "--base", base, "--head", head, "--log", logPath,
		"--per-leg-reads", "0"}
	resps, _ := runRPC(t, args, reqs)

	if !resps[0].Result.IsError || resps[0].Result.Content[0].Text != "budget_exhausted" {
		t.Errorf("a read with no remaining leg allowance should refuse budget_exhausted, got %+v", resps[0].Result)
	}
	if resps[1].Error != nil {
		t.Errorf("the server halted after the refusal: ping answered %+v", resps[1].Error)
	}
}

// An explicit zero leg byte allowance exhausts the same way: any positive
// read refuses, while the handshake the leg checks still answers.
func TestExplicitZeroLegBytesRefuses(t *testing.T) {
	dir, base, head := createRepo(t)
	logPath := filepath.Join(t.TempDir(), "log.jsonl")

	reqs := []rpcRequest{
		{JSONRPC: "2.0", ID: 1, Method: "initialize"},
		{JSONRPC: "2.0", ID: 2, Method: "tools/list"},
		{JSONRPC: "2.0", ID: 3, Method: "tools/call", Params: map[string]any{
			"name": "read_file", "arguments": map[string]any{"path": "small.txt", "revision": "head"},
		}},
	}
	args := []string{"--repo", dir, "--base", base, "--head", head, "--log", logPath,
		"--per-leg-bytes", "0"}
	resps, _ := runRPC(t, args, reqs)

	if resps[0].Error != nil || resps[1].Error != nil {
		t.Fatalf("the handshake should answer with no remaining byte allowance: %+v %+v", resps[0].Error, resps[1].Error)
	}
	if !resps[2].Result.IsError || resps[2].Result.Content[0].Text != "budget_exhausted" {
		t.Errorf("a read with no remaining leg bytes should refuse budget_exhausted, got %+v", resps[2].Result)
	}
}

func TestBudgetExhaustionNeverHalts(t *testing.T) {
	dir, base, head := createRepo(t)
	logPath := filepath.Join(t.TempDir(), "log.jsonl")

	reqs := []rpcRequest{
		{JSONRPC: "2.0", ID: 1, Method: "tools/call", Params: map[string]any{
			"name": "read_file",
			"arguments": map[string]any{
				"path": "small.txt", "revision": "head",
			},
		}},
		{JSONRPC: "2.0", ID: 2, Method: "tools/call", Params: map[string]any{
			"name": "read_file",
			"arguments": map[string]any{
				"path": "large.txt", "revision": "head",
			},
		}},
		{JSONRPC: "2.0", ID: 3, Method: "ping"},
	}
	args := []string{"--repo", dir, "--base", base, "--head", head, "--log", logPath,
		"--per-call-bytes", "1000", "--max-result-bytes", "12288", "--max-result-lines", "400"}
	resps, _ := runRPC(t, args, reqs)

	if len(resps) != 3 {
		t.Fatalf("expected 3 responses, got %d", len(resps))
	}
	if !resps[1].Result.IsError || !strings.Contains(resps[1].Result.Content[0].Text, "budget_exhausted") {
		t.Fatalf("second read should refuse budget_exhausted, got %+v", resps[1].Result)
	}
	if resps[2].Error != nil {
		t.Errorf("server halted after exhaustion: ping answered %+v", resps[2].Error)
	}
}

func TestTrailingNewlineHasNoPhantomLine(t *testing.T) {
	dir, base, head := createRepo(t)
	logPath := filepath.Join(t.TempDir(), "log.jsonl")

	if got := string(gitShow(t, dir, head, "small.txt")); got != "line 1 edited\nline 2\n" {
		t.Fatalf("fixture drifted: git show = %q", got)
	}

	reqs := []rpcRequest{
		{JSONRPC: "2.0", ID: 1, Method: "tools/call", Params: map[string]any{
			"name": "read_file",
			"arguments": map[string]any{
				"path": "small.txt", "revision": "head",
			},
		}},
	}
	args := []string{"--repo", dir, "--base", base, "--head", head, "--log", logPath, "--per-call-bytes", "262144"}
	resps, _ := runRPC(t, args, reqs)

	want := "small.txt@head lines 1-2\n1: line 1 edited\n2: line 2\n"
	if resps[0].Result.IsError {
		t.Fatalf("expected success, got: %s", resps[0].Result.Content[0].Text)
	}
	if resps[0].Result.Content[0].Text != want {
		t.Errorf("got %q, want %q", resps[0].Result.Content[0].Text, want)
	}
}

func TestEmptyFileReadsAsZeroLines(t *testing.T) {
	dir, base, head := createRepo(t)
	logPath := filepath.Join(t.TempDir(), "log.jsonl")

	if got := gitShow(t, dir, head, "empty.txt"); len(got) != 0 {
		t.Fatalf("fixture drifted: git show empty.txt = %q", got)
	}

	reqs := []rpcRequest{
		{JSONRPC: "2.0", ID: 1, Method: "tools/call", Params: map[string]any{
			"name": "read_file", "arguments": map[string]any{"path": "empty.txt", "revision": "head"},
		}},
		{JSONRPC: "2.0", ID: 2, Method: "tools/call", Params: map[string]any{
			"name": "read_file", "arguments": map[string]any{"path": "empty.txt", "revision": "head", "start_line": 2},
		}},
	}
	args := []string{"--repo", dir, "--base", base, "--head", head, "--log", logPath, "--per-call-bytes", "262144"}
	resps, _ := runRPC(t, args, reqs)

	// `git show` succeeds with zero bytes, so the default read succeeds
	// with an empty body and a header naming the empty range.
	if resps[0].Result.IsError {
		t.Fatalf("expected success for empty file, got: %s", resps[0].Result.Content[0].Text)
	}
	if want := "empty.txt@head lines 1-0\n"; resps[0].Result.Content[0].Text != want {
		t.Errorf("got %q, want %q", resps[0].Result.Content[0].Text, want)
	}
	if !resps[1].Result.IsError || resps[1].Result.Content[0].Text != "bad_range" {
		t.Errorf("start past the only line should refuse bad_range, got %+v", resps[1].Result)
	}
}

func TestLastLineMatchesGitShow(t *testing.T) {
	dir, base, head := createRepo(t)
	logPath := filepath.Join(t.TempDir(), "log.jsonl")

	// No trailing newline: the last line is bare bytes, like `git show` prints.
	if got := string(gitShow(t, dir, head, "noeol.txt")); got != "alpha\nomega" {
		t.Fatalf("fixture drifted: git show = %q", got)
	}

	reqs := []rpcRequest{
		{JSONRPC: "2.0", ID: 1, Method: "tools/call", Params: map[string]any{
			"name": "read_file",
			"arguments": map[string]any{
				"path": "noeol.txt", "revision": "head",
			},
		}},
		{JSONRPC: "2.0", ID: 2, Method: "tools/call", Params: map[string]any{
			"name": "read_file",
			"arguments": map[string]any{
				"path": "noeol.txt", "revision": "head", "start_line": 2, "end_line": 2,
			},
		}},
	}
	args := []string{"--repo", dir, "--base", base, "--head", head, "--log", logPath, "--per-call-bytes", "262144"}
	resps, _ := runRPC(t, args, reqs)

	if want := "noeol.txt@head lines 1-2\n1: alpha\n2: omega\n"; resps[0].Result.Content[0].Text != want {
		t.Errorf("full read got %q, want %q", resps[0].Result.Content[0].Text, want)
	}
	if want := "noeol.txt@head lines 2-2\n2: omega\n"; resps[1].Result.Content[0].Text != want {
		t.Errorf("last-line read got %q, want %q", resps[1].Result.Content[0].Text, want)
	}
}

func TestOutOfRangeStartRefuses(t *testing.T) {
	dir, base, head := createRepo(t)
	logPath := filepath.Join(t.TempDir(), "log.jsonl")

	reqs := []rpcRequest{
		{JSONRPC: "2.0", ID: 1, Method: "tools/call", Params: map[string]any{
			"name": "read_file",
			"arguments": map[string]any{
				"path": "small.txt", "revision": "head", "start_line": 99,
			},
		}},
	}
	args := []string{"--repo", dir, "--base", base, "--head", head, "--log", logPath, "--per-call-bytes", "262144"}
	resps, _ := runRPC(t, args, reqs)

	if !resps[0].Result.IsError || resps[0].Result.Content[0].Text != "bad_range" {
		t.Errorf("start past end of file should refuse bad_range, got %+v", resps[0].Result)
	}
}

func TestOutOfRangeEndClampsToEOF(t *testing.T) {
	dir, base, head := createRepo(t)
	logPath := filepath.Join(t.TempDir(), "log.jsonl")

	reqs := []rpcRequest{
		{JSONRPC: "2.0", ID: 1, Method: "tools/call", Params: map[string]any{
			"name": "read_file",
			"arguments": map[string]any{
				"path": "small.txt", "revision": "head", "start_line": 1, "end_line": 99,
			},
		}},
	}
	args := []string{"--repo", dir, "--base", base, "--head", head, "--log", logPath, "--per-call-bytes", "262144"}
	resps, _ := runRPC(t, args, reqs)

	// The end clamps to the last line git reports, and the header names
	// the clamped range rather than the requested one.
	want := "small.txt@head lines 1-2\n1: line 1 edited\n2: line 2\n"
	if resps[0].Result.IsError {
		t.Fatalf("expected clamped success, got: %s", resps[0].Result.Content[0].Text)
	}
	if resps[0].Result.Content[0].Text != want {
		t.Errorf("got %q, want %q", resps[0].Result.Content[0].Text, want)
	}
}

func TestInvertedRangeRefuses(t *testing.T) {
	dir, base, head := createRepo(t)
	logPath := filepath.Join(t.TempDir(), "log.jsonl")

	reqs := []rpcRequest{
		{JSONRPC: "2.0", ID: 1, Method: "tools/call", Params: map[string]any{
			"name": "read_file",
			"arguments": map[string]any{
				"path": "small.txt", "revision": "head", "start_line": 2, "end_line": 1,
			},
		}},
	}
	args := []string{"--repo", dir, "--base", base, "--head", head, "--log", logPath, "--per-call-bytes", "262144"}
	resps, _ := runRPC(t, args, reqs)

	if !resps[0].Result.IsError || resps[0].Result.Content[0].Text != "bad_range" {
		t.Errorf("inverted range should refuse bad_range, got %+v", resps[0].Result)
	}
}

func TestCutByBytesNamesNextStart(t *testing.T) {
	dir, base, head := createRepo(t)
	logPath := filepath.Join(t.TempDir(), "log.jsonl")

	reqs := []rpcRequest{
		{JSONRPC: "2.0", ID: 1, Method: "tools/call", Params: map[string]any{
			"name": "read_file", "arguments": map[string]any{"path": "large.txt", "revision": "head"},
		}},
	}
	args := []string{"--repo", dir, "--base", base, "--head", head, "--log", logPath,
		"--max-result-bytes", "100", "--per-call-bytes", "262144"}
	resps, _ := runRPC(t, args, reqs)

	if resps[0].Result.IsError {
		t.Fatalf("expected cut success, got: %s", resps[0].Result.Content[0].Text)
	}
	text := resps[0].Result.Content[0].Text
	// Guttered lines 1-9 cost 10 bytes each ("N: line N\n"), line 10 costs
	// 12; the 102 running bytes take line 11 over the 100-byte cap, so the
	// cut returns lines 1-10 and names line 11.
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	header := lines[0]
	if header != "large.txt@head lines 1-10" {
		t.Errorf("header = %q, want the returned range lines 1-10", header)
	}
	last := lines[len(lines)-1]
	if last != "(cut, next start_line=11)" {
		t.Errorf("cut marker = %q, want the next start named", last)
	}
	for i := 1; i <= 10; i++ {
		want := fmt.Sprintf("%d: line %d", i, i)
		if lines[i] != want {
			t.Errorf("body line %d = %q, want %q", i, lines[i], want)
		}
	}
}

func TestLogReplaysFromReturnedRange(t *testing.T) {
	dir, base, head := createRepo(t)
	logPath := filepath.Join(t.TempDir(), "log.jsonl")

	reqs := []rpcRequest{
		{JSONRPC: "2.0", ID: 1, Method: "tools/call", Params: map[string]any{
			"name": "read_file", "arguments": map[string]any{"path": "large.txt", "revision": "head"},
		}},
	}
	args := []string{"--repo", dir, "--base", base, "--head", head, "--log", logPath,
		"--max-result-lines", "10", "--per-call-bytes", "262144"}
	_, _ = runRPC(t, args, reqs)

	evs := readEvents(t, logPath)
	if len(evs) != 1 {
		t.Fatalf("expected 1 read event, got %d", len(evs))
	}
	ev := evs[0]
	// The log records the returned range, not the requested one: the
	// request named no range and lines 1-10 came back cut.
	if ev.StartLine != 1 || ev.EndLine != 10 {
		t.Errorf("logged range = %d-%d, want the returned 1-10", ev.StartLine, ev.EndLine)
	}
	if ev.NextStart != 11 {
		t.Errorf("logged next_start = %d, want 11 for the cut", ev.NextStart)
	}

	// Replay: re-fetch the blob from git, re-render the logged range over
	// the same bytes, and check the logged digest.
	raw := string(gitShow(t, dir, head, ev.Path))
	gitLines := strings.Split(strings.TrimSuffix(raw, "\n"), "\n")
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("%s@%s lines %d-%d\n", ev.Path, ev.Revision, ev.StartLine, ev.EndLine))
	for i := ev.StartLine; i <= ev.EndLine; i++ {
		fmt.Fprintf(&sb, "%d: %s\n", i, gitLines[i-1])
	}
	if ev.NextStart != 0 {
		fmt.Fprintf(&sb, "(cut, next start_line=%d)\n", ev.NextStart)
	}
	sum := sha256.Sum256([]byte(sb.String()))
	if got, want := ev.Digest, hex.EncodeToString(sum[:]); got != want {
		t.Errorf("logged digest = %s, replay over git bytes = %s", got, want)
	}
}

func TestServerWritesOnlyItsLog(t *testing.T) {
	dir, base, head := createRepo(t)
	logDir := t.TempDir()
	logPath := filepath.Join(logDir, "reads.jsonl")
	workDir := t.TempDir()

	prev, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(workDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer func() { _ = os.Chdir(prev) }()

	reqs := []rpcRequest{
		{JSONRPC: "2.0", ID: 1, Method: "tools/call", Params: map[string]any{
			"name": "read_file", "arguments": map[string]any{"path": "large.txt", "revision": "head"},
		}},
		{JSONRPC: "2.0", ID: 2, Method: "tools/call", Params: map[string]any{
			"name": "read_file", "arguments": map[string]any{"path": "missing.txt", "revision": "head"},
		}},
	}
	args := []string{"--repo", dir, "--base", base, "--head", head, "--log", logPath,
		"--max-result-lines", "10", "--per-call-bytes", "262144"}
	_, _ = runRPC(t, args, reqs)

	entries, err := os.ReadDir(workDir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("server wrote %d entries outside its log path: %v", len(entries), entries)
	}
	entries, err = os.ReadDir(logDir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "reads.jsonl" {
		t.Errorf("log dir holds %v, want only reads.jsonl", entries)
	}
}
