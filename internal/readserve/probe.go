// probe.go — the leg-start self-test against the served read tool.
//
// Before any harness child starts, the leg speaks the handshake to a fresh
// server itself: initialize, tools/list naming read_file, one read
// byte-checked line by line against bytes the leg read itself through the
// VCS layer, and one probe call outside read_file that must be refused. A
// mismatch fails the self-test rather than trusting the tool, and the leg
// degrades or halts per policy.
//
// The server runs one-shot through the runner with the scripted session on
// stdin: it answers each request and exits at EOF, so no interactive piping
// is needed. This stays inside the package's isolation: no environment is
// read and no file is written — the runner owns the child.

package readserve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/carlosboeing/crossrev/internal/exec"
)

// Session is one server invocation: the repository and revisions it may
// read, the call log it appends to, and the call number naming the log.
type Session struct {
	Repo    string
	Base    string
	Head    string
	LogPath string
	Call    string
}

// Args answers the server argv after the program name.
func (s Session) Args() []string {
	return []string{
		"__read-server",
		"--repo", s.Repo,
		"--base", s.Base,
		"--head", s.Head,
		"--log", s.LogPath,
		"--call", s.Call,
	}
}

// Probe is the one read the self-test byte-checks against git: the path,
// the revision the server must read it at, and the bytes the caller read
// itself through the VCS layer.
type Probe struct {
	Path     string
	Revision string
	Want     []byte
}

// SelfTest speaks the served handshake to a fresh server: initialize,
// tools/list, one byte-checked read, and one refused probe call.
func SelfTest(ctx context.Context, runner exec.Runner, command string, args, env []string, probe Probe) error {
	if command == "" {
		return errors.New("self-test: no serve command")
	}
	requests := []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"read_file","arguments":` + probeArguments(probe) + `}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"probe","arguments":{}}}`,
	}
	res := runner.Run(ctx, exec.Spec{
		Path:  command,
		Args:  args,
		Env:   env,
		Stdin: []byte(strings.Join(requests, "\n") + "\n"),
	})
	if res.Err != nil {
		return fmt.Errorf("self-test: the read server did not start: %v", res.Err)
	}
	lines := strings.Split(strings.TrimRight(string(res.Stdout), "\n"), "\n")
	if len(lines) != len(requests) {
		return fmt.Errorf("self-test: the read server answered %d of %d requests", len(lines), len(requests))
	}
	answers := make(map[string]json.RawMessage, len(lines))
	for _, line := range lines {
		var envelope struct {
			ID     json.RawMessage `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  json.RawMessage `json:"error"`
		}
		if err := json.Unmarshal([]byte(line), &envelope); err != nil {
			return fmt.Errorf("self-test: the read server answered non-JSON: %v", err)
		}
		var id int
		if err := json.Unmarshal(envelope.ID, &id); err != nil {
			return fmt.Errorf("self-test: the read server answered without an id")
		}
		if len(envelope.Error) != 0 {
			answers[strconv.Itoa(id)] = json.RawMessage(`{"transport_error":true}`)
			continue
		}
		answers[strconv.Itoa(id)] = envelope.Result
	}
	if _, ok := answers["1"]; !ok {
		return errors.New("self-test: initialize was not answered")
	}
	var listing struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(answers["2"], &listing); err != nil {
		return fmt.Errorf("self-test: tools/list answered no tool list: %v", err)
	}
	names := false
	for _, tool := range listing.Tools {
		if tool.Name == "read_file" {
			names = true
		}
	}
	if !names {
		return errors.New("self-test: tools/list names no read_file tool")
	}
	var read struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(answers["3"], &read); err != nil || read.IsError || len(read.Content) == 0 {
		return fmt.Errorf("self-test: the byte-check read was refused")
	}
	if err := checkProbeBytes(probe, read.Content[0].Text); err != nil {
		return err
	}
	var refused struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(answers["4"], &refused); err != nil || (!refused.IsError && string(answers["4"]) != `{"transport_error":true}`) {
		return errors.New("self-test: a probe call outside read_file served instead of refused")
	}
	return nil
}

func probeArguments(probe Probe) string {
	revision := probe.Revision
	if revision == "" {
		revision = "base"
	}
	raw, _ := json.Marshal(map[string]any{"path": probe.Path, "revision": revision})
	return string(raw)
}

// checkProbeBytes byte-checks the served text against git, line by line in
// gutter form: every content line of Want must arrive as its numbered line,
// under a header naming the path, revision and returned range.
func checkProbeBytes(probe Probe, text string) error {
	revision := probe.Revision
	if revision == "" {
		revision = "base"
	}
	header := probe.Path + "@" + revision + " lines"
	if !strings.Contains(text, header) {
		return fmt.Errorf("self-test: the read names no %q header", header)
	}
	want := strings.TrimSuffix(string(probe.Want), "\n")
	if want == "" {
		return nil
	}
	for at, line := range strings.Split(want, "\n") {
		gutter := strconv.Itoa(at+1) + ": " + line
		if !strings.Contains(text, gutter) {
			return fmt.Errorf("self-test: line %d of %s differs from git", at+1, probe.Path)
		}
	}
	return nil
}
