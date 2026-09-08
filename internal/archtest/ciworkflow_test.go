package archtest_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

// CrossRev CI runs on every pushed branch, at the pushed commit.
//
// The workflow keeps both triggers: an unfiltered `push`, which GitHub checks
// out at the event SHA — the branch commit itself — and `pull_request`, which
// keeps its merge-ref behaviour. A branch push with an open pull request
// therefore starts two runs by design: the push run tests the branch commit
// and the pull-request run tests the synthetic merge revision. Their
// `github.ref` values differ, so the `ci-${{ github.ref }}` concurrency group
// does not combine them; the extra run is the cost of exact-head evidence.
//
// No checkout step may carry `ref:`. The default checkout is what puts a push
// run on the event SHA; a pinned ref would silently repoint it, and the run
// would then vouch for a revision it never tested.
func TestCIRunsForEveryPushedBranchAtTheEventSHA(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(findRepoRoot(t), ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatalf("reading ci.yml: %v", err)
	}

	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parsing ci.yml: %v", err)
	}
	top := ciDocRoot(&doc)

	on := mappingValue(mappingContent(top), "on")
	if on == nil {
		t.Fatal("ci.yml has no `on:` block, so nothing triggers CI")
	}
	for _, violation := range auditCITriggers(mappingContent(on)) {
		t.Error(violation)
	}

	steps := findCheckoutSteps(&doc)
	if len(steps) != 3 {
		t.Fatalf("found %d actions/checkout steps, want 3; this rule cannot tell what an added or removed checkout tests", len(steps))
	}
	for _, s := range steps {
		if s.ref != "" {
			t.Errorf("actions/checkout at ci.yml:%d pins `ref: %s`, which moves the run off the event SHA; checkout steps must not name a ref", s.line, s.ref)
		}
	}
}

// ciDocRoot unwraps the document node yaml.Unmarshal produces, so the caller
// always holds the workflow's top-level mapping.
func ciDocRoot(doc *yaml.Node) *yaml.Node {
	if doc.Kind == yaml.DocumentNode && len(doc.Content) > 0 {
		return doc.Content[0]
	}
	return doc
}

// mappingContent returns a mapping node's key/value pairs, or nil when the
// node is not a mapping. A null trigger such as `push:` with no body is not
// one either — and that null is exactly the unfiltered form this rule
// requires, so it must read as "no filter" rather than as an error.
func mappingContent(n *yaml.Node) []*yaml.Node {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	return n.Content
}

// mappingValue returns the value node held under key, or nil when absent.
func mappingValue(content []*yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(content); i += 2 {
		if content[i].Value == key {
			return content[i+1]
		}
	}
	return nil
}

// auditCITriggers reports every violation of the trigger contract in an `on:`
// mapping: a missing `push`, a `push` narrowed by `branches:`, a missing
// `pull_request`.
func auditCITriggers(on []*yaml.Node) []string {
	var violations []string
	push := mappingValue(on, "push")
	switch {
	case push == nil:
		violations = append(violations, "ci.yml has no `push` trigger, so only pull requests run CI")
	case mappingValue(mappingContent(push), "branches") != nil:
		violations = append(violations, "ci.yml restricts `push` with `branches:`, so branches outside it get no run at the event SHA; `push` must stay unfiltered")
	}
	if mappingValue(on, "pull_request") == nil {
		violations = append(violations, "ci.yml lost its `pull_request` trigger; both triggers must remain")
	}
	return violations
}

type checkoutStep struct {
	line int
	ref  string
}

// findCheckoutSteps walks the whole workflow and records every
// actions/checkout step with the `ref:` it pins, if any. A `with:` block
// holding other keys, such as the changelog job's `fetch-depth: 0`, records
// no ref.
func findCheckoutSteps(n *yaml.Node) []checkoutStep {
	var found []checkoutStep
	var walk func(n *yaml.Node)
	walk = func(n *yaml.Node) {
		if n == nil {
			return
		}
		if n.Kind == yaml.MappingNode {
			var uses, with *yaml.Node
			for i := 0; i+1 < len(n.Content); i += 2 {
				switch n.Content[i].Value {
				case "uses":
					uses = n.Content[i+1]
				case "with":
					with = n.Content[i+1]
				}
			}
			if uses != nil && strings.HasPrefix(uses.Value, "actions/checkout@") {
				step := checkoutStep{line: uses.Line}
				if ref := mappingValue(mappingContent(with), "ref"); ref != nil {
					step.ref = ref.Value
				}
				found = append(found, step)
			}
		}
		for _, c := range n.Content {
			walk(c)
		}
	}
	walk(n)
	return found
}

// Each clause of the verdict has a fixture, because each was removable with
// the suite still green: dropping the branches check keeps the workflow's own
// text failing only while it still says `branches: [main]`, and dropping the
// ref walk keeps it failing only until a checkout pins one.
func TestCIWorkflowAuditVerdict(t *testing.T) {
	t.Run("triggers", func(t *testing.T) {
		tests := []struct {
			name  string
			doc   string
			wants []string
		}{
			{
				name:  "unfiltered push beside pull request",
				doc:   "on:\n  push:\n  pull_request:\n",
				wants: nil,
			},
			{
				name:  "push restricted to main",
				doc:   "on:\n  push:\n    branches: [main]\n  pull_request:\n",
				wants: []string{"ci.yml restricts `push` with `branches:`"},
			},
			{
				name:  "push missing",
				doc:   "on:\n  pull_request:\n",
				wants: []string{"ci.yml has no `push` trigger"},
			},
			{
				name:  "pull request missing",
				doc:   "on:\n  push:\n",
				wants: []string{"ci.yml lost its `pull_request` trigger"},
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				var doc yaml.Node
				if err := yaml.Unmarshal([]byte(tt.doc), &doc); err != nil {
					t.Fatalf("parsing fixture: %v", err)
				}
				on := mappingValue(mappingContent(ciDocRoot(&doc)), "on")
				got := auditCITriggers(mappingContent(on))
				if len(got) != len(tt.wants) {
					t.Fatalf("violations = %q, want %d violation(s) %q", got, len(tt.wants), tt.wants)
				}
				for i, want := range tt.wants {
					if !strings.Contains(got[i], want) {
						t.Errorf("violation %d = %q, want it to contain %q", i, got[i], want)
					}
				}
			})
		}
	})

	t.Run("checkout steps", func(t *testing.T) {
		raw := "jobs:\n" +
			"  test:\n" +
			"    steps:\n" +
			"      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1\n" +
			"  changelog:\n" +
			"    steps:\n" +
			"      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1\n" +
			"        with:\n" +
			"          fetch-depth: 0\n" +
			"      - name: Set up Go\n" +
			"        uses: actions/setup-go@40f1582b2485089dde7abd97c1529aa768e1baff # v5.6.0\n" +
			"        with:\n" +
			"          go-version: '1.27.0'\n"
		var doc yaml.Node
		if err := yaml.Unmarshal([]byte(raw), &doc); err != nil {
			t.Fatalf("parsing fixture: %v", err)
		}
		steps := findCheckoutSteps(&doc)
		if len(steps) != 2 {
			t.Fatalf("found %d checkout steps, want 2", len(steps))
		}
		for _, s := range steps {
			if s.ref != "" {
				t.Errorf("checkout at line %d records ref %q, want none", s.line, s.ref)
			}
		}

		pinned := strings.ReplaceAll(raw,
			"with:\n          fetch-depth: 0",
			"with:\n          ref: main")
		var pinnedDoc yaml.Node
		if err := yaml.Unmarshal([]byte(pinned), &pinnedDoc); err != nil {
			t.Fatalf("parsing fixture: %v", err)
		}
		pinnedSteps := findCheckoutSteps(&pinnedDoc)
		refs := 0
		for _, s := range pinnedSteps {
			if s.ref != "" {
				refs++
				if s.ref != "main" {
					t.Errorf("recorded ref = %q, want %q", s.ref, "main")
				}
			}
		}
		if refs != 1 {
			t.Fatalf("recorded %d pinned checkout step(s), want 1", refs)
		}
	})
}
