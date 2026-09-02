// config.go — the policy file `init` commits (_init_write_config,
// lib/init.sh:855-893, and _init_policy_pairing, :894-907).
//
// The generated config states plainly where deferred work goes and which
// pairing was provisioned for, because `auto` is a bootstrap convenience rather
// than a runtime mode and because a policy file naming a different reviewer
// leaves the repository provisioned for a leg that never runs.
//
// # Why the document is decoded and re-encoded
//
// The Bash pipes the template through `yq`, which parses it into go-yaml's node
// tree, applies the assignments and prints it again through go-yaml's emitter.
// Two things about that printing are observable in the committed file: every
// blank line goes, and a line comment is normalised to one space before the `#`.
//
// This does the same thing with the same library. Decoding into a yaml.Node,
// editing the tree and encoding at indent 2 reproduces `yq .` over
// templates/crossrev.yml byte for byte — measured — so the emitter is not
// reimplemented and neither is its scalar quoting: a value is written as a
// `!!str` with no style, and go-yaml decides which of the three forms it needs.
//
// testdata/config holds five whole files the shell wrote, and config_test.go
// carries 62 quoting rows measured against yq. Both pin this against the shell
// rather than against the library.

package initcmd

import (
	"bytes"
	"strings"

	"go.yaml.in/yaml/v3"
)

// WriteConfig renders the policy file from the template
// (_init_write_config, lib/init.sh:855-875).
//
// The template bytes are not written through: the caller's copy is the
// binary's own, and every later run renders from it.
//
// The one document this is ever handed is templates/crossrev.yml, compiled in
// (lib/init.sh:874). A document that will not parse or will not print is a
// mistake in this package fixed by a recompile, not something a caller can
// cause, so it panics rather than growing an error return, the way
// assets.go's template() does.
func (p Plan) WriteConfig(template []byte) []byte {
	var document yaml.Node
	if err := yaml.Unmarshal(template, &document); err != nil {
		panic("initcmd: policy template: " + err.Error())
	}
	root := &document
	if root.Kind == yaml.DocumentNode && len(root.Content) > 0 {
		root = root.Content[0]
	}
	for _, edit := range p.policyEdits() {
		if edit.remove {
			removeAt(root, edit.path)
			continue
		}
		setAt(root, edit.path, edit.value)
	}

	var out bytes.Buffer
	encoder := yaml.NewEncoder(&out)
	encoder.SetIndent(2)
	if err := encoder.Encode(&document); err != nil {
		panic("initcmd: policy document: " + err.Error())
	}
	if err := encoder.Close(); err != nil {
		panic("initcmd: policy document: " + err.Error())
	}
	return out.Bytes()
}

// yamlEdit is one clause of the yq expression the shell builds.
type yamlEdit struct {
	path   []string
	value  string
	remove bool
}

// policyEdits is the expression at lib/init.sh:865-874, in its order.
//
// The order is observable: a key the template does not carry is appended to the
// end of its mapping, so two edits that both create a key land in the order
// they were applied.
func (p Plan) policyEdits() []yamlEdit {
	// `read -r _ layout path <<<"$resolved"` after a `repository *` match.
	// `repository` with nothing after it matches neither arm and falls
	// through to none, which is what the shell's `case` does with it.
	destination := "none"
	layout, path := "", ""
	fields := strings.Fields(p.BacklogResolved)
	switch {
	case p.BacklogResolved == "github_issues":
		destination = "github_issues"
	case strings.HasPrefix(p.BacklogResolved, "repository ") && len(fields) >= 3:
		destination, layout, path = "repository", fields[1], fields[2]
	}

	var edits []yamlEdit
	if path != "" {
		edits = append(edits,
			yamlEdit{path: []string{"backlog", "destination"}, value: "repository"},
			yamlEdit{path: []string{"backlog", "repository", "layout"}, value: layout},
			yamlEdit{path: []string{"backlog", "repository", "path"}, value: path},
		)
	} else {
		edits = append(edits, yamlEdit{path: []string{"backlog", "destination"}, value: destination})
	}

	// The pairing init actually provisioned for, written down. A field
	// resolving to nothing is deleted rather than left at the template's
	// value, so a leg cannot inherit `model: claude-fable-5` under a harness
	// that never had it (lib/init.sh:894-907).
	for _, leg := range []string{"reviewer", "resolver"} {
		for _, field := range []string{"harness", "model", "effort", "endpoint"} {
			value := p.Config.Get("." + leg + "." + field)
			if value == "" || value == "null" {
				edits = append(edits, yamlEdit{path: []string{leg, field}, remove: true})
				continue
			}
			edits = append(edits, yamlEdit{path: []string{leg, field}, value: value})
		}
	}
	return edits
}

// entryAt is the index of a mapping's key node for key, or not found. A
// mapping's Content alternates key, value, so the value sits one after it.
func entryAt(node *yaml.Node, key string) (int, bool) {
	if node.Kind != yaml.MappingNode {
		return 0, false
	}
	for at := 0; at+1 < len(node.Content); at += 2 {
		if node.Content[at].Value == key {
			return at, true
		}
	}
	return 0, false
}

// setAt writes a string at a dotted path, creating any mapping on the way down
// that the document does not carry, which is what yq's `=` does.
func setAt(node *yaml.Node, path []string, value string) {
	for depth, key := range path {
		at, found := entryAt(node, key)
		if !found {
			if node.Kind != yaml.MappingNode {
				return
			}
			node.Content = append(node.Content,
				&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
				newBranch(path[depth+1:], value))
			return
		}
		if depth == len(path)-1 {
			writeScalar(node.Content[at+1], value)
			return
		}
		node = node.Content[at+1]
	}
}

// removeAt deletes the entry at a dotted path, and anything nested under it. A
// path the document does not carry is left alone, which is what `del` does with
// one.
func removeAt(node *yaml.Node, path []string) {
	for depth, key := range path {
		at, found := entryAt(node, key)
		if !found {
			return
		}
		if depth == len(path)-1 {
			node.Content = append(node.Content[:at:at], node.Content[at+2:]...)
			return
		}
		node = node.Content[at+1]
	}
}

// newBranch is the nested mapping a path that does not exist yet becomes.
func newBranch(path []string, value string) *yaml.Node {
	if len(path) == 0 {
		node := &yaml.Node{}
		writeScalar(node, value)
		return node
	}
	return &yaml.Node{
		Kind: yaml.MappingNode,
		Tag:  "!!map",
		Content: []*yaml.Node{
			{Kind: yaml.ScalarNode, Tag: "!!str", Value: path[0]},
			newBranch(path[1:], value),
		},
	}
}

// writeScalar turns a node into the string value, leaving its comments alone —
// the comment belongs to the node, not to the value, which is why yq keeps a
// line comment across an assignment.
//
// The style is cleared so the emitter picks the form: plain where the text
// carries no meaning of its own, double quotes where the plain form would
// resolve to a null, a boolean, a number or a timestamp, single quotes where the
// plain form is not allowed at all.
func writeScalar(node *yaml.Node, value string) {
	node.Kind = yaml.ScalarNode
	node.Tag = "!!str"
	node.Style = 0
	node.Value = value
	node.Anchor = ""
	node.Alias = nil
	node.Content = nil
}
