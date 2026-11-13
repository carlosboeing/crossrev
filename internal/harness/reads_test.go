package harness_test

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/exec"
	"github.com/carlosboeing/crossrev/internal/harness"
)

// The descriptor gains read_mode: served (CrossRev's tool is the only read
// path), file_tool (unused until slice 9), supplied (no read tool).
func TestParseReadMode(t *testing.T) {
	for _, tt := range []struct {
		text string
		want harness.ReadMode
		ok   bool
	}{
		{text: "served", want: harness.ReadModeServed, ok: true},
		{text: "file_tool", want: harness.ReadModeFileTool, ok: true},
		{text: "supplied", want: harness.ReadModeSupplied, ok: true},
		{text: "", ok: false},
		{text: "direct", ok: false},
	} {
		got, ok := harness.ParseReadMode(tt.text)
		if ok != tt.ok || (ok && got != tt.want) {
			t.Errorf("ParseReadMode(%q) = (%q, %t), want (%q, %t)", tt.text, got, ok, tt.want, tt.ok)
		}
	}
}

// file_tool runs supplied until it is wired; served and supplied run as
// declared. Resolve and status read this to compute the same review-contract
// engine identity the review leg publishes under.
func TestEffectiveReadMode(t *testing.T) {
	for _, tt := range []struct {
		declared, want harness.ReadMode
	}{
		{declared: harness.ReadModeServed, want: harness.ReadModeServed},
		{declared: harness.ReadModeFileTool, want: harness.ReadModeSupplied},
		{declared: harness.ReadModeSupplied, want: harness.ReadModeSupplied},
	} {
		if got := harness.EffectiveReadMode(tt.declared); got != tt.want {
			t.Errorf("EffectiveReadMode(%q) = %q, want %q", tt.declared, got, tt.want)
		}
	}
}

// The shipped mapping: codex and claude read only through the served tool,
// grok and opencode run supplied, agy stays supplied with its no-commands
// directive.
func TestShippedReadModes(t *testing.T) {
	doc := descriptors(t)
	for _, tt := range []struct {
		name string
		want harness.ReadMode
	}{
		{name: "codex", want: harness.ReadModeServed},
		{name: "claude", want: harness.ReadModeServed},
		{name: "grok", want: harness.ReadModeSupplied},
		{name: "opencode", want: harness.ReadModeSupplied},
		{name: "agy", want: harness.ReadModeSupplied},
	} {
		entry, found := doc.For(tt.name)
		if !found {
			t.Fatalf("the descriptor carries no %s entry", tt.name)
		}
		if got := entry.ReadMode(); got != tt.want {
			t.Errorf("%s ReadMode() = %q, want %q", tt.name, got, tt.want)
		}
	}
}

// An absent read_mode stays supplied: fail closed. There is no shell saying
// otherwise, so absence means no served tool.
func TestAbsentReadModeIsSupplied(t *testing.T) {
	raw := `{"version":1,"endpoint_host":"claude","default_pairing":{"reviewer":"codex","resolver":"claude"},"quarantine_shared":[],"not_driven":[],"harnesses":[{"name":"codex","binary":"codex","product_name":"Codex","install":{"kind":"script","url":"https://example.invalid","command":"install 0.148.0","pinned_version":"0.148.0","needs_credential":false,"hint":"https://example.invalid"},"schema_style":"path","schema_native":true,"prompt_transport":"stdin","window_tokens":258400,"sandbox_args":["--ignore-user-config"],"quarantine":[],"credential":{"archetype":"B","provenance":"measured","access_token_seconds":1,"store":"s","billing":"subscription","secret":"X","env_names":["X"],"env_keep":[],"seed_command":null,"seed_hint":null,"staging":{"kind":"none","env":null,"path":null},"access_token_jq":null,"assert_fresh":false,"refresher":false}}]}`
	doc, err := harness.Load([]byte(raw))
	if err != nil {
		t.Fatalf("loading a descriptor with no read_mode: %v", err)
	}
	entry, _ := doc.For("codex")
	if got := entry.ReadMode(); got != harness.ReadModeSupplied {
		t.Errorf("absent read_mode = %q, want supplied", got)
	}
}

func TestDescriptorRejectsAnUnknownReadMode(t *testing.T) {
	raw := harness.DescriptorJSON()
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("decoding the shipped descriptor: %v", err)
	}
	harnesses := document["harnesses"].([]any)
	harnesses[0].(map[string]any)["read_mode"] = "direct"
	mutated, _ := json.Marshal(document)
	if problem := harness.Validate(mutated); !strings.Contains(problem, "read_mode") {
		t.Errorf("an unknown read_mode validates; problem = %q", problem)
	}
}

// A codex review leg reads only through the served tool: the shell is
// disabled twice over, the sandbox stays read-only, and the served command
// travels as config beside --ignore-user-config.
func TestCodexReviewServedDisablesTheShellAndServesMCP(t *testing.T) {
	adapter := codexAdapter(t)
	inv := invocation(t, "codex", false)
	inv.ReadMode = harness.ReadModeServed
	inv.Serve = &harness.ServeConfig{Command: "/bin/crossrev", Args: []string{"__read-server", "--repo", "r"}}

	spec, err := adapter.Spec(inv)
	if err != nil {
		t.Fatalf("building the spec: %v", err)
	}
	if !hasFlagPair(spec.Args, "--disable", "shell_tool") || !hasFlagPair(spec.Args, "--disable", "unified_exec") {
		t.Errorf("a served codex review keeps the shell disabled; got %v", spec.Args)
	}
	if !hasFlagPair(spec.Args, "--sandbox", "read-only") {
		t.Errorf("a served codex review stays read-only; got %v", spec.Args)
	}
	if !hasConfigPair(spec.Args, "mcp_servers.crossrev.command", "/bin/crossrev") {
		t.Errorf("a served codex review names the served command; got %v", spec.Args)
	}
	if !hasConfigPair(spec.Args, "mcp_servers.crossrev.default_tools_approval_mode", "approve") {
		t.Errorf("a served codex review approves the served tool; got %v", spec.Args)
	}
	found := false
	for _, arg := range spec.Args {
		if strings.Contains(arg, "mcp_servers.crossrev.args") && strings.Contains(arg, "__read-server") {
			found = true
		}
	}
	if !found {
		t.Errorf("a served codex review passes the served args as an array; got %v", spec.Args)
	}
}

// Without a served tool a codex review keeps its old shape: no MCP config.
func TestCodexReviewWithoutServeKeepsItsOldShape(t *testing.T) {
	adapter := codexAdapter(t)
	inv := invocation(t, "codex", false)

	spec, err := adapter.Spec(inv)
	if err != nil {
		t.Fatalf("building the spec: %v", err)
	}
	for _, arg := range spec.Args {
		if strings.Contains(arg, "mcp_servers") {
			t.Errorf("an unserved codex review names no MCP server; got %v", spec.Args)
		}
	}
}

// The served read tool reaches the codex resolve leg too.
func TestCodexResolveServedCarriesMCP(t *testing.T) {
	adapter := codexAdapter(t)
	inv := invocation(t, "codex", true)
	inv.ReadMode = harness.ReadModeServed
	inv.Serve = &harness.ServeConfig{Command: "/bin/crossrev", Args: []string{"__read-server"}}

	spec, err := adapter.Spec(inv)
	if err != nil {
		t.Fatalf("building the spec: %v", err)
	}
	if !hasConfigPair(spec.Args, "mcp_servers.crossrev.command", "/bin/crossrev") {
		t.Errorf("a served codex resolve names the served command; got %v", spec.Args)
	}
	if !hasConfigPair(spec.Args, "mcp_servers.crossrev.default_tools_approval_mode", "approve") {
		t.Errorf("a served codex resolve approves the served tool; got %v", spec.Args)
	}
	if !hasFlagPair(spec.Args, "--sandbox", "workspace-write") {
		t.Errorf("a served codex resolve stays workspace-writable; got %v", spec.Args)
	}
}

// The image reader is a local-file read path outside the served server, and
// the shell denial leaves it enabled: an empty CODEX_HOME reports
// view_image=true beside shell_tool=false on 0.159.2. Every explicit served
// and supplied mode denies it on both legs; the zero mode keeps the legacy
// shape every stub test pins.
func TestCodexExplicitModesDisableViewImage(t *testing.T) {
	adapter := codexAdapter(t)
	serve := &harness.ServeConfig{Command: "/bin/crossrev", Args: []string{"__read-server"}}
	tests := []struct {
		name  string
		write bool
		mode  harness.ReadMode
	}{
		{name: "served review", write: false, mode: harness.ReadModeServed},
		{name: "served resolve", write: true, mode: harness.ReadModeServed},
		{name: "supplied review", write: false, mode: harness.ReadModeSupplied},
		{name: "supplied resolve", write: true, mode: harness.ReadModeSupplied},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inv := invocation(t, "codex", tt.write)
			inv.ReadMode = tt.mode
			inv.Serve = serve
			spec, err := adapter.Spec(inv)
			if err != nil {
				t.Fatalf("building the spec: %v", err)
			}
			if !hasFlagPair(spec.Args, "--disable", "view_image") {
				t.Errorf("an explicit %s keeps the image reader enabled; got %v", tt.name, spec.Args)
			}
		})
	}
}

// Connected-app tools bypass the served read path, and removing the shell
// does not remove the connectors: the served registry on 0.159.2 exposes
// them beside the served read. Served review and resolve deny them; the
// zero mode keeps the legacy argv every stub test pins.
func TestCodexServedModesDisableApps(t *testing.T) {
	adapter := codexAdapter(t)
	serve := &harness.ServeConfig{Command: "/bin/crossrev", Args: []string{"__read-server"}}
	for _, write := range []bool{false, true} {
		inv := invocation(t, "codex", write)
		inv.ReadMode = harness.ReadModeServed
		inv.Serve = serve
		spec, err := adapter.Spec(inv)
		if err != nil {
			t.Fatalf("building the spec: %v", err)
		}
		if !hasFlagPair(spec.Args, "--disable", "apps") {
			t.Errorf("a served codex leg keeps the app connectors enabled; got %v", spec.Args)
		}
	}
	inv := invocation(t, "codex", false)
	spec, err := adapter.Spec(inv)
	if err != nil {
		t.Fatalf("building the spec: %v", err)
	}
	if hasFlagPair(spec.Args, "--disable", "apps") {
		t.Errorf("a zero-mode leg denies the app connectors; got %v", spec.Args)
	}
}

// Web search and image generation read outside the served server through
// their own tools, independently of Apps and the local image viewer. Served
// review and resolve deny both; the zero mode keeps the legacy argv every
// stub test pins.
func TestCodexServedModesDisableWebAndImagegen(t *testing.T) {
	adapter := codexAdapter(t)
	serve := &harness.ServeConfig{Command: "/bin/crossrev", Args: []string{"__read-server"}}
	for _, write := range []bool{false, true} {
		inv := invocation(t, "codex", write)
		inv.ReadMode = harness.ReadModeServed
		inv.Serve = serve
		spec, err := adapter.Spec(inv)
		if err != nil {
			t.Fatalf("building the spec: %v", err)
		}
		if !hasFlagPair(spec.Args, "--disable", "image_generation") {
			t.Errorf("a served codex leg keeps image generation enabled; got %v", spec.Args)
		}
		if !hasConfigPair(spec.Args, "web_search", "disabled") {
			t.Errorf("a served codex leg keeps web search enabled; got %v", spec.Args)
		}
	}
	inv := invocation(t, "codex", false)
	spec, err := adapter.Spec(inv)
	if err != nil {
		t.Fatalf("building the spec: %v", err)
	}
	if hasFlagPair(spec.Args, "--disable", "image_generation") {
		t.Errorf("a zero-mode leg denies image generation; got %v", spec.Args)
	}
	for _, arg := range spec.Args {
		if strings.HasPrefix(arg, "web_search=") {
			t.Errorf("a zero-mode leg sets a web search mode; got %v", spec.Args)
		}
	}
}

// The zero mode keeps the legacy argv every stub test pins: no image-reader
// denial travels on a call that names no read mode.
func TestCodexZeroModeOmitsTheViewImageDisable(t *testing.T) {
	adapter := codexAdapter(t)
	for _, write := range []bool{false, true} {
		inv := invocation(t, "codex", write)
		spec, err := adapter.Spec(inv)
		if err != nil {
			t.Fatalf("building the spec: %v", err)
		}
		if hasFlagPair(spec.Args, "--disable", "view_image") {
			t.Errorf("a zero-mode leg denies the image reader; got %v", spec.Args)
		}
	}
}

// Codex resolves: the served tool gives the resolve leg its reads, so the
// working resolvers list it. TestWorkingResolversOnShippedDescriptor pins
// the whole list; this names codex because the refusal that once kept it
// off the resolve leg is what this change lifts.
func TestCodexResolvesThroughTheServedTool(t *testing.T) {
	found := false
	for _, name := range harness.WorkingResolvers(descriptors(t)) {
		if name == "codex" {
			found = true
		}
	}
	if !found {
		t.Errorf("codex is missing from the working resolvers: %v", harness.WorkingResolvers(descriptors(t)))
	}
}

// A claude review leg reads only through the served tool: the MCP config,
// an empty built-in tool list, the served allowlist, and the stream the
// tripwire and the envelope both read.
func TestClaudeReviewServedUsesMCPAllowlistAndStreams(t *testing.T) {
	adapter := claudeAdapter(t)
	inv := invocation(t, "claude", false)
	inv.ReadMode = harness.ReadModeServed
	inv.Serve = &harness.ServeConfig{Command: "/bin/crossrev", Args: []string{"__read-server"}}

	spec, err := adapter.Spec(inv)
	if err != nil {
		t.Fatalf("building the spec: %v", err)
	}
	if !containsFlag(spec.Args, "--strict-mcp-config") {
		t.Errorf("a served claude review keeps --strict-mcp-config; got %v", spec.Args)
	}
	if !hasFlagPair(spec.Args, "--tools", "") {
		t.Errorf("a served claude review empties the built-in tool list; got %v", spec.Args)
	}
	if !hasFlagPair(spec.Args, "--allowedTools", "mcp__crossrev__read_file") {
		t.Errorf("a served claude review allows only the served read; got %v", spec.Args)
	}
	if !hasFlagPair(spec.Args, "--output-format", "stream-json") {
		t.Errorf("a served claude review streams; got %v", spec.Args)
	}
	if !containsFlag(spec.Args, "--verbose") {
		t.Errorf("a served claude review passes --verbose; got %v", spec.Args)
	}
	mcp := ""
	for at := 0; at+1 < len(spec.Args); at++ {
		if spec.Args[at] == "--mcp-config" {
			mcp = spec.Args[at+1]
		}
	}
	if mcp == "" {
		t.Fatalf("a served claude review writes an MCP config; got %v", spec.Args)
	}
	raw, err := os.ReadFile(mcp) //nolint:gosec // the adapter wrote this path
	if err != nil {
		t.Fatalf("reading the MCP config: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("the MCP config is not JSON: %v", err)
	}
	servers, _ := decoded["mcpServers"].(map[string]any)
	server, _ := servers["crossrev"].(map[string]any)
	if server["command"] != "/bin/crossrev" {
		t.Errorf("the MCP config names the served command; got %s", raw)
	}
}

// Without a served tool a claude review keeps the PR 273 tool list.
func TestClaudeReviewWithoutServeKeepsTheToolList(t *testing.T) {
	adapter := claudeAdapter(t)
	inv := invocation(t, "claude", false)

	spec, err := adapter.Spec(inv)
	if err != nil {
		t.Fatalf("building the spec: %v", err)
	}
	if !hasFlagPair(spec.Args, "--tools", "Read,Grep,Glob") {
		t.Errorf("an unserved claude review keeps Read,Grep,Glob; got %v", spec.Args)
	}
	if !hasFlagPair(spec.Args, "--output-format", "json") {
		t.Errorf("an unserved claude review keeps json output; got %v", spec.Args)
	}
}

// The served claude review reads its answer and usage off the final result
// event of a recorded stream-json run.
func TestClaudeServedReviewEnvelopeReadsTheResultEvent(t *testing.T) {
	adapter := claudeAdapter(t)
	inv := invocation(t, "claude", false)
	inv.ReadMode = harness.ReadModeServed

	stdout := []byte("{\"type\":\"assistant\",\"message\":{\"content\":[{\"type\":\"tool_use\",\"name\":\"mcp__crossrev__read_file\",\"input\":{\"path\":\"a.go\"}}]}}\n" +
		"{\"type\":\"result\",\"result\":\"{\\\"verdict\\\":\\\"ok\\\"}\",\"is_error\":false,\"modelUsage\":{\"claude-test-model\":{\"inputTokens\":10,\"outputTokens\":5,\"cacheReadInputTokens\":0,\"cacheCreationInputTokens\":0,\"canonicalModel\":\"claude-test-model\"}},\"usage\":{\"input_tokens\":10,\"output_tokens\":5}}\n")
	envelope := adapter.Envelope(inv, exec.Result{Stdout: stdout})
	if !envelope.OK {
		t.Fatalf("the served review envelope failed: %v", deref(envelope.Error))
	}
	if got := string(envelope.Payload); !strings.Contains(got, "ok") {
		t.Errorf("payload = %s, want the result event's answer", got)
	}
	if envelope.Usage == nil {
		t.Fatal("the served review envelope carries no usage")
	}
}

// A command event on a review leg halts with review_leg_ran_command: the
// tripwire watches review legs, not just resolve legs.
func TestReviewCommandTripwire(t *testing.T) {
	codex := []byte("{\"type\":\"item.completed\",\"item\":{\"type\":\"command_execution\",\"command\":\"git status\"}}\n")
	if command, tripped := harness.ReviewCommand("codex", codex); !tripped || command != "git status" {
		t.Errorf("codex review command tripped = (%q, %t), want (git status, true)", command, tripped)
	}
	claude := []byte("{\"type\":\"assistant\",\"message\":{\"content\":[{\"type\":\"tool_use\",\"name\":\"Bash\",\"input\":{\"command\":\"ls\"}}]}}\n")
	if command, tripped := harness.ReviewCommand("claude", claude); !tripped || command != "ls" {
		t.Errorf("claude review command tripped = (%q, %t), want (ls, true)", command, tripped)
	}
	// A served read call is the review leg's own work and never trips.
	served := []byte("{\"type\":\"assistant\",\"message\":{\"content\":[{\"type\":\"tool_use\",\"name\":\"mcp__crossrev__read_file\",\"input\":{\"path\":\"a.go\"}}]}}\n")
	if command, tripped := harness.ReviewCommand("claude", served); tripped {
		t.Errorf("a served read tripped the review tripwire with %q", command)
	}
	// agy emits no tool events and opencode is denied its shell through its
	// isolation config: no record to watch, so no trip.
	if _, tripped := harness.ReviewCommand("agy", claude); tripped {
		t.Error("agy tripped with no tool record to watch")
	}
	if _, tripped := harness.ReviewCommand("opencode", claude); tripped {
		t.Error("opencode tripped with no command record to watch")
	}
}

// A served codex read stream passes the tripwire: mcp_tool_call items are
// the served path a served review wires, and only a command_execution item
// trips. Without this a change to the command match could halt every served
// codex review unseen.
func TestReviewCommandIgnoresServedCodexReads(t *testing.T) {
	reads := []byte(
		"{\"type\":\"item.completed\",\"item\":{\"type\":\"mcp_tool_call\",\"server\":\"crossrev\",\"tool\":\"read_file\",\"arguments\":\"{\\\"path\\\":\\\"a.go\\\"}\"}}\n" +
			"{\"type\":\"item.completed\",\"item\":{\"type\":\"mcp_tool_call\",\"server\":\"crossrev\",\"tool\":\"read_file\",\"arguments\":\"{\\\"path\\\":\\\"b.go\\\"}\"}}\n")
	if command, tripped := harness.ReviewCommand("codex", reads); tripped {
		t.Errorf("served codex reads tripped the review tripwire with %q", command)
	}
	// A command after the reads still trips, naming the command.
	tripped := append(reads,
		[]byte("{\"type\":\"item.completed\",\"item\":{\"type\":\"command_execution\",\"command\":\"git status\"}}\n")...)
	if command, ok := harness.ReviewCommand("codex", tripped); !ok || command != "git status" {
		t.Errorf("a command after served reads tripped = (%q, %t), want (git status, true)", command, ok)
	}
}

// The review tripwire refusal carries the review_leg_ran_command name.
func TestReviewCommandRefusalNamesTheFailure(t *testing.T) {
	refusal := harness.ReviewCommandRefusal("codex")
	if refusal == nil {
		t.Fatal("no refusal for a review-leg command")
	}
	if !strings.Contains(refusal.Reason, "review_leg_ran_command") {
		t.Errorf("reason = %q, want the review_leg_ran_command name", refusal.Reason)
	}
	if !errorIs(refusal, harness.ErrReviewCommand) {
		t.Error("the refusal does not match ErrReviewCommand")
	}
}

// Grok reviews as supplied with a tripwire: streaming-json output and an
// empty tools allowlist, so no read tool is granted. Grep returns file
// content and Glob enumerates paths, and neither trips the command
// tripwire, so even those two stay out.
func TestGrokReviewSuppliedStreamsWithTripwire(t *testing.T) {
	adapter := grokAdapter(t)
	inv := invocation(t, "grok", false)
	inv.ReadMode = harness.ReadModeSupplied

	spec, err := adapter.Spec(inv)
	if err != nil {
		t.Fatalf("building the spec: %v", err)
	}
	if !hasFlagPair(spec.Args, "--output-format", "streaming-json") {
		t.Errorf("a grok review streams for its tripwire; got %v", spec.Args)
	}
	seen := false
	tools := ""
	for at := 0; at+1 < len(spec.Args); at++ {
		if spec.Args[at] == "--tools" {
			seen = true
			tools = spec.Args[at+1]
		}
	}
	if !seen {
		t.Fatalf("a grok review passes a tools allowlist; got %v", spec.Args)
	}
	if tools != "" {
		t.Errorf("a grok supplied review grants no tools; got %q", tools)
	}
	for _, banned := range []string{"Bash", "Read", "Grep", "Glob", "Edit", "Write"} {
		if strings.Contains(tools, banned) {
			t.Errorf("a grok review allowlist holds %s; got %q", banned, tools)
		}
	}
}

// The grok tripwire reads tool_call and tool_call_update events.
func TestGrokReviewTripwireSeesToolCallUpdate(t *testing.T) {
	update := []byte("{\"type\":\"tool_call_update\",\"toolName\":\"bash\",\"rawInput\":{\"command\":\"id\"}}\n")
	if command, tripped := harness.ReviewCommand("grok", update); !tripped || command != "id" {
		t.Errorf("grok tool_call_update tripped = (%q, %t), want (id, true)", command, tripped)
	}
	call := []byte("{\"type\":\"tool_call\",\"toolName\":\"shell\",\"rawInput\":{\"command\":\"id\"}}\n")
	if _, tripped := harness.ReviewCommand("grok", call); !tripped {
		t.Error("a grok tool_call carrying a command does not trip")
	}
	read := []byte("{\"type\":\"tool_call\",\"toolName\":\"Read\",\"rawInput\":{\"path\":\"a.go\"}}\n")
	if command, tripped := harness.ReviewCommand("grok", read); tripped {
		t.Errorf("a grok read call tripped with %q", command)
	}
}

// A grok review leg is refused before any child starts, while its resolve
// leg is unaffected: the empty tools allowlist left the command tool
// callable on the pinned version, so the verified table carries no grok
// entry at any pin.
func TestGrokReviewUnverifiedIsRefused(t *testing.T) {
	if harness.IsolationVerified("grok", "1.0.5") {
		t.Error("the pinned grok tripwire block reads as verified")
	}
	if harness.IsolationVerified("grok", "9.9.9") {
		t.Error("a moved grok pin reads as verified")
	}
	if !harness.IsolationVerified("codex", "0.159.2") {
		t.Error("the pinned codex served block reads as unverified")
	}
	if !harness.IsolationVerified("claude", "2.1.237") {
		t.Error("the pinned claude served block reads as unverified")
	}
	if harness.IsolationVerified("agy", "1.2.13") {
		t.Error("agy reads as verified with no command block to verify")
	}
}

// An opencode review denies the five read tools; the resolve leg is
// untouched.
func TestOpencodeReviewDeniesTheFiveReadTools(t *testing.T) {
	adapter := opencodeAdapter(t)
	inv := invocation(t, "opencode", false)
	inv.ReadMode = harness.ReadModeSupplied
	spec, err := adapter.Spec(inv)
	if err != nil {
		t.Fatalf("building the spec: %v", err)
	}
	permission := isolation(t, spec)
	for _, tool := range []string{"read", "glob", "grep", "list", "lsp"} {
		if permission[tool] != "deny" {
			t.Errorf("an opencode review holds %s = %v, want deny", tool, permission[tool])
		}
	}
	resolve, err := adapter.Spec(invocation(t, "opencode", true))
	if err != nil {
		t.Fatalf("building the resolve spec: %v", err)
	}
	if isolation(t, resolve)["edit"] != "allow" {
		t.Error("an opencode resolve lost its write grant")
	}
}

// Served-mode flags are verified on each pinned version or the pin moves:
// a harness whose command block is unverified never reviews.
func TestUnverifiedServedBlockNeverReviews(t *testing.T) {
	if harness.IsolationVerified("codex", "0.0.0") {
		t.Error("an unknown codex pin reads as verified")
	}
	if harness.IsolationVerified("claude", "0.0.0") {
		t.Error("an unknown claude pin reads as verified")
	}
	if harness.IsolationVerified("opencode", "2.0.15") {
		t.Error("opencode reads as verified with no command block to verify")
	}
}

// A supplied codex leg keeps every independent reader denied: Apps, web
// search and image generation register independently of the shell flag, so
// a served-to-supplied fallback after a failed self-test must not regain
// the readers the served leg denied. Both legs deny them; the zero mode
// keeps the legacy argv every stub test pins.
func TestCodexSuppliedModesDenyIndependentReaders(t *testing.T) {
	adapter := codexAdapter(t)
	for _, write := range []bool{false, true} {
		inv := invocation(t, "codex", write)
		inv.ReadMode = harness.ReadModeSupplied
		spec, err := adapter.Spec(inv)
		if err != nil {
			t.Fatalf("building the spec: %v", err)
		}
		if !hasFlagPair(spec.Args, "--disable", "apps") {
			t.Errorf("a supplied codex leg keeps the app connectors enabled; got %v", spec.Args)
		}
		if !hasFlagPair(spec.Args, "--disable", "image_generation") {
			t.Errorf("a supplied codex leg keeps image generation enabled; got %v", spec.Args)
		}
		if !hasConfigPair(spec.Args, "web_search", "disabled") {
			t.Errorf("a supplied codex leg keeps web search enabled; got %v", spec.Args)
		}
	}
	inv := invocation(t, "codex", false)
	spec, err := adapter.Spec(inv)
	if err != nil {
		t.Fatalf("building the spec: %v", err)
	}
	if hasFlagPair(spec.Args, "--disable", "apps") {
		t.Errorf("a zero-mode leg denies the app connectors; got %v", spec.Args)
	}
	if hasFlagPair(spec.Args, "--disable", "image_generation") {
		t.Errorf("a zero-mode leg denies image generation; got %v", spec.Args)
	}
	for _, arg := range spec.Args {
		if strings.HasPrefix(arg, "web_search=") {
			t.Errorf("a zero-mode leg sets a web search mode; got %v", spec.Args)
		}
	}
}

// hasConfigPair reports a codex -c key=value override.
func hasConfigPair(args []string, key, value string) bool {
	for at := 0; at+1 < len(args); at++ {
		if args[at] == "-c" && strings.HasPrefix(args[at+1], key+"=") && strings.Contains(args[at+1], value) {
			return true
		}
	}
	return false
}

// containsFlag reports a bare flag.
func containsFlag(args []string, flag string) bool {
	for _, arg := range args {
		if arg == flag {
			return true
		}
	}
	return false
}
