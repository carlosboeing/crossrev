package harness_test

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/exec"
	"github.com/carlosboeing/crossrev/internal/harness"
)

// Sanitized OpenCode 2.3.0 counts from the three-file live probe.
func TestModernOpencodeExportCountsReasoningAndRecoversIdentityAndCost(t *testing.T) {
	raw := []byte(`{"info":{"tokens":{"input":16315,"output":304,"reasoning":616,"cache":{"read":19382,"write":0}},"cost":0.03192035},"messages":[{"type":"user"},{"type":"assistant","model":{"id":"gemini-4-hydrogen","providerID":"opencode"},"tokens":{"input":15708,"output":296,"reasoning":616,"cache":{"read":19382,"write":0}}}]}`)
	u := harness.ParseOpencodeExport(raw)
	if u == nil {
		t.Fatal("missing usage")
	}
	if u.Total == nil || *u.Total != 36617 || u.Output != 920 || u.Cached() != 19382 {
		t.Errorf("normalized usage = %+v, want total 36617, output 920, cached 19382", u)
	}
	if u.CostUSD == nil || *u.CostUSD != 0.03192035 {
		t.Errorf("export cost lost: %+v", u)
	}
	if model := harness.OpencodeModelID(raw); model != "gemini-4-hydrogen" {
		t.Errorf("model = %q", model)
	}
}

func TestCodexRecoversIdentityFromTheChildEnvironment(t *testing.T) {
	for _, staged := range []bool{false, true} {
		t.Run(map[bool]string{false: "local", true: "staged"}[staged], func(t *testing.T) {
			inv := invocation(t, "codex", false)
			inv.CodexHome = ""
			home := t.TempDir()
			inv.Env = []string{"HOME=" + home}
			rolloutHome := filepath.Join(home, ".codex")
			if staged {
				rolloutHome = t.TempDir()
				inv.Env = append(inv.Env, "CODEX_HOME="+rolloutHome)
			}
			sessions := filepath.Join(rolloutHome, "sessions")
			if err := os.MkdirAll(sessions, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(sessions, "rollout-probe-session.jsonl"), []byte(`{"type":"turn_context","payload":{"model":"gpt-6.1-sol","effort":"low"}}`+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(inv.PayloadPath, []byte(`{"findings":[]}`), 0600); err != nil {
				t.Fatal(err)
			}
			env := codexAdapter(t).Envelope(inv, exec.Result{Stdout: []byte(`{"type":"thread.started","thread_id":"probe-session"}` + "\n" + `{"type":"turn.completed","usage":{"input_tokens":120481,"cached_input_tokens":89856,"output_tokens":94}}`)})
			if env.ModelReported == nil || *env.ModelReported != "gpt-6.1-sol" || env.EffortReported == nil || *env.EffortReported != "low" {
				t.Errorf("identity not recovered: %+v", env)
			}
			if env.Tokens == nil || *env.Tokens != 120575 {
				t.Errorf("tokens = %v", env.Tokens)
			}
		})
	}
}

func TestUsageMergeSumsCostsModelsAndReasoningWithoutMutatingInputs(t *testing.T) {
	first := harness.ParseOpencodeExport([]byte(`{"info":{"tokens":{"input":10,"output":2,"reasoning":3,"cache":{"read":40,"write":0}},"cost":0.1},"messages":[{"type":"assistant","model":{"id":"a"},"tokens":{"input":10,"output":2,"reasoning":3,"cache":{"read":40,"write":0}}}]}`))
	second := harness.ParseOpencodeExport([]byte(`{"info":{"tokens":{"input":20,"output":4,"reasoning":6,"cache":{"read":80,"write":0}},"cost":0.2},"messages":[{"type":"assistant","model":{"id":"b"},"tokens":{"input":20,"output":4,"reasoning":6,"cache":{"read":80,"write":0}}}]}`))
	got := harness.MergeUsage(*first, *second)
	if got.Total == nil || *got.Total != 165 || got.Reasoning == nil || *got.Reasoning != 9 || got.CostUSD == nil || *got.CostUSD < 0.29999 || len(got.Models) != 2 {
		t.Errorf("merge = %+v", got)
	}
	if *first.Total != 55 || *first.CostUSD != 0.1 || len(first.Models) != 1 || *first.Reasoning != 3 {
		t.Fatal("merge mutated source")
	}
	missing := *second
	missing.CostUSD = nil
	got = harness.MergeUsage(*first, missing)
	if got.CostUSD != nil {
		t.Fatal("partial cost presented as complete")
	}
}

func TestMissingInvocationPreventsACompleteCostEstimate(t *testing.T) {
	cost := 0.1
	source := "harness"
	known := harness.Usage{InputFresh: 10, CostUSD: &cost, CostSource: &source}
	for _, unknownFirst := range []bool{false, true} {
		var sum *harness.Usage
		if unknownFirst {
			sum = harness.FoldUsage(sum, nil)
		}
		sum = harness.FoldUsage(sum, &known)
		if !unknownFirst {
			sum = harness.FoldUsage(sum, nil)
		}
		table, err := harness.PriceTable()
		if err != nil {
			t.Fatal(err)
		}
		got := table.Attach(*sum, harness.Document{}, "codex", "", "gpt-5.6", false)
		if got.Total == nil || *got.Total != 10 || got.CostUSD != nil || got.CostUnavailable != "partial_usage" {
			t.Fatalf("partial usage = %+v", got)
		}
		if note := harness.TelemetryNote(&got); !strings.Contains(note, "Usage is partial") || !strings.Contains(note, "Cost unavailable") {
			t.Fatal(note)
		}
	}
}

func TestTelemetryLogExcludesTheModelPayload(t *testing.T) {
	env := harness.Envelope{Harness: "grok", OK: true, Payload: []byte(`{"secret":"private prompt"}`), Usage: &harness.Usage{InputFresh: 10}}
	raw := harness.TelemetryJSON(&env, 1)
	if strings.Contains(raw, "secret") || strings.Contains(raw, "private prompt") || !strings.Contains(raw, `"normalization_version":1`) || !strings.Contains(raw, "side_model_calls") {
		t.Fatal(raw)
	}
}

// Standard vendor rates verified against https://developers.openai.com/api/docs/pricing.
func TestCurrentCodexModelHasAReproducibleEstimate(t *testing.T) {
	table, err := harness.PriceTable()
	if err != nil {
		t.Fatal(err)
	}
	u := harness.Usage{InputFresh: 30625, CacheRead: 89856, Output: 94}
	got := table.Price(u, "gpt-6.1-sol")
	if got.CostUSD == nil || math.Abs(*got.CostUSD-0.0711756) > 1e-9 || got.PriceTable == nil || *got.PriceTable != table.Version() {
		t.Fatalf("estimate = %+v", got)
	}
	u.CacheWriteUnsplit = 1
	if got := table.Price(u, "gpt-6.1-sol"); got.CostUSD != nil {
		t.Fatal("priced cache writes with unknown retention")
	}
	u.CacheWriteUnsplit = 0
	u.InputFresh = 272000
	if got := table.Price(u, "gpt-6.1-sol"); got.CostUSD != nil {
		t.Fatal("priced cumulative counts across a context tier")
	}
}

func TestCompletelyMissingCountsStayUnavailable(t *testing.T) {
	missing := harness.FoldUsage(nil, nil)
	total := missing.WithTotal()
	if total.Total != nil {
		t.Fatal("missing counts became a measured zero")
	}
	combined := harness.FoldUsage(missing, nil)
	if combined.Total != nil {
		t.Fatal("two missing records became a measured zero")
	}
	zero := harness.Zero().WithTotal()
	combined = harness.FoldUsage(missing, &zero)
	if combined.Total == nil || *combined.Total != 0 {
		t.Fatal("measured zero was lost")
	}
}
