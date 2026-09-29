package readserve

import (
	"bytes"
	"context"
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

	res = exec.NewOSRunner().Run(context.Background(), exec.Spec{
		Path: "git", Args: []string{"rev-parse", "HEAD"}, Dir: dir,
	})
	head = strings.TrimSpace(string(res.Stdout))

	return dir, base, head
}

// Tests to add: ranges from base and head byte-match git; every refusal reason; cut results naming the next start; line_too_long moving forward; budget exhaustion as a refusal; the log replaying from (path, revision, returned) with matching digests; arch tests on the new package.
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
	}
	args := []string{"--repo", dir, "--base", base, "--head", head, "--log", logPath, "--per-call-bytes", "262144"}
	resps, _ := runRPC(t, args, reqs)

	wants := []string{"outside_revisions", "path_invalid", "not_found", "binary", "symlink", "bad_range", "not_found"}
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
