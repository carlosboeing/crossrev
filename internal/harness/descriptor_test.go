package harness_test

import (
	"encoding/json"
	"os/exec"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/harness"
)

// The shipped descriptor loads, and says what tests/test-harnesses.sh:20-103
// asserts it says.
func TestShippedDescriptorReadsBack(t *testing.T) {
	doc := descriptors(t)

	if got := doc.Names(); !reflect.DeepEqual(got, []string{"claude", "codex", "agy", "grok", "opencode"}) {
		t.Errorf("Names() = %v, want the five in descriptor order", got)
	}
	if got := doc.Version(); got != harness.Version {
		t.Errorf("Version() = %d, want %d", got, harness.Version)
	}
	if got := doc.EndpointHost(); got != "claude" {
		t.Errorf("EndpointHost() = %q, want claude", got)
	}
	if got := doc.DefaultPairing(); got.Reviewer != "codex" || got.Resolver != "claude" {
		t.Errorf("DefaultPairing() = %+v, want codex reviewing and claude resolving", got)
	}
	if got := doc.NamesHuman(); got != "claude, codex, agy, grok and opencode" {
		t.Errorf("NamesHuman() = %q", got)
	}
}

func TestDescriptorFieldsMatchTheShellReads(t *testing.T) {
	doc := descriptors(t)

	tests := []struct {
		name string
		read func(harness.Document) string
		want string
	}{
		{name: "codex secret", read: func(d harness.Document) string { return credential(d, "codex").Secret }, want: "CROSSREV_CODEX_AUTH"},
		{name: "agy secret is empty", read: func(d harness.Document) string { return credential(d, "agy").Secret }, want: ""},
		{name: "agy archetype", read: func(d harness.Document) string { return credential(d, "agy").Archetype }, want: "C"},
		{name: "agy provenance", read: func(d harness.Document) string { return credential(d, "agy").Provenance }, want: "measured"},
		{name: "grok archetype", read: func(d harness.Document) string { return credential(d, "grok").Archetype }, want: "C"},
		{name: "grok secret", read: func(d harness.Document) string { return credential(d, "grok").Secret }, want: "CROSSREV_GROK_AUTH"},
		{name: "grok staging env", read: func(d harness.Document) string { return credential(d, "grok").Staging.Env }, want: "GROK_HOME"},
		{name: "opencode product name stays lowercase", read: func(d harness.Document) string { return entry(d, "opencode").ProductName }, want: "opencode"},
		{name: "opencode schema style", read: func(d harness.Document) string { return entry(d, "opencode").SchemaStyle }, want: "prompt"},
		{name: "opencode secret", read: func(d harness.Document) string { return credential(d, "opencode").Secret }, want: "CROSSREV_OPENCODE_AUTH"},
		{name: "opencode archetype", read: func(d harness.Document) string { return credential(d, "opencode").Archetype }, want: "A"},
		{name: "opencode staging env", read: func(d harness.Document) string { return credential(d, "opencode").Staging.Env }, want: "XDG_DATA_HOME"},
		{name: "opencode staging path", read: func(d harness.Document) string { return credential(d, "opencode").Staging.Path }, want: "opencode/auth.json"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.read(doc); got != tt.want {
				t.Errorf("= %q, want %q", got, tt.want)
			}
		})
	}

	if credential(doc, "agy").Store == "" || !strings.Contains(credential(doc, "agy").Store, "antigravity-oauth-token") {
		t.Errorf("the agy store does not name the token file: %q", credential(doc, "agy").Store)
	}
	if entry(doc, "opencode").SchemaNative {
		t.Error("opencode is the one harness that does not constrain its own output")
	}
	for _, name := range []string{"claude", "codex", "agy", "grok"} {
		if !entry(doc, name).SchemaNative {
			t.Errorf("%s is schema-native in the descriptor", name)
		}
	}
}

// An absent `legs` key means both legs, and an entry that declares them means
// exactly what it says. The two are different facts, so the type keeps them
// apart (lib/harnesses.sh:150-153).
func TestLegsDefaultToBothWhenTheKeyIsAbsent(t *testing.T) {
	doc := descriptors(t)

	if entry(doc, "claude").DeclaresLegs() {
		t.Error("claude declares no legs field, so the absence is the fact")
	}
	if got := entry(doc, "claude").Legs(); !reflect.DeepEqual(got, []string{"review", "resolve"}) {
		t.Errorf("claude Legs() = %v, want both", got)
	}
	if !entry(doc, "opencode").DeclaresLegs() {
		t.Error("opencode declares a legs field")
	}
	if got := entry(doc, "opencode").Legs(); !reflect.DeepEqual(got, []string{"review", "resolve"}) {
		t.Errorf("opencode Legs() = %v", got)
	}

	for _, leg := range []string{"review", "resolve"} {
		if !doc.ServesLeg("opencode", leg) {
			t.Errorf("opencode serves the %s leg", leg)
		}
		if got := doc.NamesForLeg(leg); !reflect.DeepEqual(got, doc.Names()) {
			t.Errorf("NamesForLeg(%q) = %v, want every harness", leg, got)
		}
	}
	if doc.ServesLeg("not-a-harness", "review") {
		t.Error("a harness the descriptor does not carry serves nothing")
	}
}

func TestNotDrivenReadsBack(t *testing.T) {
	doc := descriptors(t)

	reason, notDriven := doc.NotDrivenReason("kimi")
	if !notDriven {
		t.Fatal("kimi is not driven")
	}
	if !strings.Contains(reason, "reached through the claude adapter") {
		t.Errorf("the reason does not say why: %q", reason)
	}
	if _, notDriven := doc.NotDrivenReason("claude"); notDriven {
		t.Error("claude is driven, so it has no not-driven reason")
	}
	if doc.Known("kimi") {
		t.Error("a not-driven name is not a driven harness")
	}
	for _, name := range doc.Names() {
		if !doc.Known(name) {
			t.Errorf("Known(%q) is false for a harness the document carries", name)
		}
	}
}

func TestSandboxArgsAndQuarantinePaths(t *testing.T) {
	doc := descriptors(t)

	if got := doc.SandboxArgs("codex"); !reflect.DeepEqual(got, []string{"--ignore-user-config"}) {
		t.Errorf("SandboxArgs(codex) = %v", got)
	}
	if got := doc.SandboxArgs("claude"); len(got) != 0 {
		t.Errorf("SandboxArgs(claude) = %v, want none", got)
	}

	paths := doc.QuarantinePaths()
	if !slices.IsSorted(paths) {
		t.Errorf("QuarantinePaths() is not sorted: %v", paths)
	}
	for _, want := range []string{".claude", ".codex", ".gemini", ".grok", ".opencode", "opencode.json", "AGENTS.md"} {
		if !slices.Contains(paths, want) {
			t.Errorf("QuarantinePaths() does not carry %q", want)
		}
	}
	// The union deduplicates, which is jq's `unique`.
	for at := 1; at < len(paths); at++ {
		if paths[at] == paths[at-1] {
			t.Errorf("QuarantinePaths() repeats %q", paths[at])
		}
	}
}

// The accessors hand back copies, so a caller cannot edit the document by
// writing to what it got.
func TestDescriptorAccessorsHandBackCopies(t *testing.T) {
	doc := descriptors(t)

	got, _ := doc.For("codex")
	got.SandboxArgs[0] = "--danger"
	got.Quarantine[0] = "/etc"
	got.Credential.EnvNames[0] = "EDITED"

	again, _ := doc.For("codex")
	if again.SandboxArgs[0] != "--ignore-user-config" {
		t.Error("SandboxArgs is shared with the document")
	}
	if again.Quarantine[0] != ".codex" {
		t.Error("Quarantine is shared with the document")
	}
	if again.Credential.EnvNames[0] != "CROSSREV_CODEX_AUTH" {
		t.Error("EnvNames is shared with the document")
	}
}

// The credential view has to be the same file's, or a leg could strip one
// descriptor's variables while running another's harness.
func TestCredentialViewComesFromTheSameBytes(t *testing.T) {
	doc := descriptors(t)
	credentials := doc.Credentials()

	if got, want := credentials.Names(), doc.Names(); !reflect.DeepEqual(got, want) {
		t.Errorf("the credential view names %v, the descriptor names %v", got, want)
	}
	for _, name := range doc.Names() {
		mine := credential(doc, name)
		theirs := credentials.For(name).Credential
		if mine.Secret != theirs.Secret || mine.Archetype != theirs.Archetype ||
			mine.Staging.Kind != theirs.Staging.Kind || mine.Staging.Env != theirs.Staging.Env ||
			mine.Staging.Path != theirs.Staging.Path {
			t.Errorf("%s: the two views of the credential disagree", name)
		}
		if !reflect.DeepEqual(mine.EnvNames, theirs.EnvNames) {
			t.Errorf("%s: env_names disagree: %v and %v", name, mine.EnvNames, theirs.EnvNames)
		}
		if !reflect.DeepEqual(mine.EnvKeep, theirs.EnvKeep) {
			t.Errorf("%s: env_keep disagree: %v and %v", name, mine.EnvKeep, theirs.EnvKeep)
		}
	}
}

func TestNamesHumanReadsAsASentence(t *testing.T) {
	tests := []struct {
		names []string
		want  string
	}{
		{names: nil, want: ""},
		{names: []string{"a"}, want: "a"},
		{names: []string{"a", "b"}, want: "a and b"},
		{names: []string{"a", "b", "c"}, want: "a, b and c"},
	}
	for _, tt := range tests {
		if got := harness.NamesHuman(tt.names); got != tt.want {
			t.Errorf("NamesHuman(%v) = %q, want %q", tt.names, got, tt.want)
		}
	}
}

// --- the validator, against the shell that wrote it --------------------------

// mutation is one of the rejection cases tests/test-harnesses.sh:105-175 builds,
// applied to the shipped descriptor.
type mutation struct {
	name  string
	apply func(map[string]any)
	// snippet is what the Bash test asserts the message contains, so a Go
	// message that merely differed in wording would still be caught by the
	// full-string comparison against bash below AND by this.
	snippet string
}

func mutations() []mutation {
	harnessAt := func(document map[string]any, at int) map[string]any {
		return document["harnesses"].([]any)[at].(map[string]any)
	}
	credentialAt := func(document map[string]any, at int) map[string]any {
		return harnessAt(document, at)["credential"].(map[string]any)
	}
	return []mutation{
		{name: "wrong version", snippet: "reads version 1",
			apply: func(d map[string]any) { d["version"] = float64(2) }},
		{name: "harnesses as an object", snippet: "must be arrays",
			apply: func(d map[string]any) { d["harnesses"] = map[string]any{} }},
		{name: "empty harness array", snippet: "names no harnesses",
			apply: func(d map[string]any) { d["harnesses"] = []any{} }},
		{name: "duplicate harness name", snippet: "appears more than once",
			apply: func(d map[string]any) {
				list := d["harnesses"].([]any)
				d["harnesses"] = append(list, list[0])
			}},
		{name: "bad harness name pattern", snippet: "is not [a-z][a-z0-9-]*",
			apply: func(d map[string]any) { harnessAt(d, 0)["name"] = "Claude_Code" }},
		{name: "not_driven collides with a driven harness", snippet: "is also a driven harness",
			apply: func(d map[string]any) {
				d["not_driven"] = append(d["not_driven"].([]any),
					map[string]any{"name": "claude", "reason": "dup"})
			}},
		{name: "invalid environment variable name", snippet: "is not [A-Z_][A-Z0-9_]*",
			apply: func(d map[string]any) { credentialAt(d, 0)["secret"] = "bad-secret-name" }},
		{name: "out-of-range archetype", snippet: "out-of-range",
			apply: func(d map[string]any) { credentialAt(d, 0)["archetype"] = "Z" }},
		{name: "out-of-range credential billing", snippet: "not subscription, api or unknown",
			apply: func(d map[string]any) { credentialAt(d, 0)["billing"] = "free" }},
		{name: "quarantine path with a parent segment", snippet: "contains a .. segment",
			apply: func(d map[string]any) {
				entry := harnessAt(d, 0)
				entry["quarantine"] = append(entry["quarantine"].([]any), "../etc/passwd")
			}},
		{name: "installer command omitting its pinned version", snippet: "pinned version its command does not carry",
			apply: func(d map[string]any) {
				harnessAt(d, 0)["install"].(map[string]any)["pinned_version"] = "9.9.9"
			}},
		{name: "a quarantine entry that is not a string", snippet: "is absolute, empty, or contains a .. segment",
			apply: func(d map[string]any) {
				entry := harnessAt(d, 0)
				entry["quarantine"] = append(entry["quarantine"].([]any), float64(123))
			}},
		{name: "a harness name that is not a string", snippet: "is not [a-z][a-z0-9-]*",
			apply: func(d map[string]any) { harnessAt(d, 0)["name"] = float64(5) }},
		{name: "a legs element outside review and resolve", snippet: "drawn from review and resolve",
			apply: func(d map[string]any) { harnessAt(d, 0)["legs"] = []any{"review", "deploy"} }},
		{name: "an empty legs array", snippet: "drawn from review and resolve",
			apply: func(d map[string]any) { harnessAt(d, 0)["legs"] = []any{} }},
		{name: "legs as a bare string", snippet: "non-empty array drawn from review and resolve",
			apply: func(d map[string]any) { harnessAt(d, 0)["legs"] = "review" }},
	}
}

// mutate answers the shipped descriptor with one edit applied.
func mutate(t *testing.T, apply func(map[string]any)) []byte {
	t.Helper()
	var document map[string]any
	if err := json.Unmarshal(harness.DescriptorJSON(), &document); err != nil {
		t.Fatalf("decoding the descriptor: %v", err)
	}
	apply(document)
	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatalf("encoding the mutated descriptor: %v", err)
	}
	return encoded
}

// The shipped descriptor passes, so every rejection below is the mutation's
// doing rather than the file's.
func TestShippedDescriptorValidates(t *testing.T) {
	if problem := harness.Validate(harness.DescriptorJSON()); problem != "" {
		t.Errorf("the shipped descriptor is rejected: %s", problem)
	}
	if problem := harness.Validate([]byte("not json at all")); problem != harness.ErrNotJSON.Error() {
		t.Errorf("unparseable JSON = %q, want %q", problem, harness.ErrNotJSON.Error())
	}
}

func TestValidatorRejectsEveryCaseTheShellRejects(t *testing.T) {
	for _, tt := range mutations() {
		t.Run(tt.name, func(t *testing.T) {
			problem := harness.Validate(mutate(t, tt.apply))
			if problem == "" {
				t.Fatal("the mutated descriptor was accepted")
			}
			if !strings.Contains(problem, tt.snippet) {
				t.Errorf("message = %q, want it to contain %q", problem, tt.snippet)
			}
		})
	}
}

// The Go validator answers the same sentence the Bash one does, word for word.
//
// It runs the shell rather than reading a frozen vector, because
// harness_validate's messages interpolate the descriptor's own values and a
// frozen copy would go stale the moment lib/harnesses.json changed. The
// tests/fixtures/parity oracle covers what does not move; this covers what does.
func TestValidatorMessagesMatchTheShell(t *testing.T) {
	for _, tool := range []string{"bash", "jq"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not on PATH, so the shell side cannot be run", tool)
		}
	}

	if got := shellValidate(t, harness.DescriptorJSON()); got != "" {
		t.Fatalf("the shell rejects the shipped descriptor: %q; the cross-check is not comparing what it thinks it is", got)
	}

	for _, tt := range mutations() {
		t.Run(tt.name, func(t *testing.T) {
			descriptor := mutate(t, tt.apply)
			want := shellValidate(t, descriptor)
			if want == "" {
				t.Fatalf("the shell accepted the mutation, so there is nothing to compare")
			}
			if got := harness.Validate(descriptor); got != want {
				t.Errorf("Go  = %q\nBash = %q", got, want)
			}
		})
	}
}

func entry(doc harness.Document, name string) harness.Descriptor {
	found, _ := doc.For(name)
	return found
}

func credential(doc harness.Document, name string) harness.Credential {
	return entry(doc, name).Credential
}
