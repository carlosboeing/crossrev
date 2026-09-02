// config.go — the policy file `init` commits (_init_write_config,
// lib/init.sh:855-893, and _init_policy_pairing, :894-907).
//
// The generated config states plainly where deferred work goes and which
// pairing was provisioned for, because `auto` is a bootstrap convenience rather
// than a runtime mode and because a policy file naming a different reviewer
// leaves the repository provisioned for a leg that never runs.
//
// # Why the document is edited rather than re-encoded
//
// The Bash pipes the template through `yq`, which parses it, applies the
// assignments and prints it again. Two things about that printing are
// observable in the committed file: every blank line goes, and a line comment
// is normalised to one space before the `#`. A port that re-encoded the
// document from a decoded value would have to reproduce that emitter exactly.
//
// It does reproduce it, by editing the lines. The input is never an arbitrary
// document: lib/init.sh:874 always reads `$ROOT/templates/crossrev.yml`, so the
// one document this has to be right about is the template beside it, and
// testdata/config holds five whole files the shell wrote to prove it is. A
// template edit that changes the shape here fails those, which is the point of
// their being whole files.
//
// The alternative was `go.yaml.in/yaml/v3`, which reproduces yq's output byte
// for byte at indent 2 — measured. internal/archtest allows that module to
// internal/config alone, and this package may not edit that rule.

package initcmd

import (
	"regexp"
	"strconv"
	"strings"
)

// WriteConfig renders the policy file from the template
// (_init_write_config, lib/init.sh:855-875).
//
// The template bytes are not written through: the caller's copy is the
// binary's own, and every later run renders from it.
func (p Plan) WriteConfig(template []byte) []byte {
	document := newYAMLDocument(string(template))
	for _, edit := range p.policyEdits() {
		if edit.remove {
			document.remove(edit.path)
			continue
		}
		document.set(edit.path, yamlScalar(edit.value))
	}
	return []byte(document.render())
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

// yamlDocument is a block-mapping document held as the lines it will be
// written back as.
type yamlDocument struct{ lines []string }

// newYAMLDocument reads the template and applies the two things yq's emitter
// does to a document it re-prints: it drops every blank line, and it puts
// exactly one space before a line comment.
func newYAMLDocument(text string) *yamlDocument {
	document := &yamlDocument{}
	for _, line := range strings.Split(strings.TrimSuffix(text, "\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		document.lines = append(document.lines, normaliseComment(line))
	}
	return document
}

// render writes the lines back out, always with a closing newline. An empty
// document renders as nothing, which is what yq prints for one.
func (d *yamlDocument) render() string {
	if len(d.lines) == 0 {
		return ""
	}
	return strings.Join(d.lines, "\n") + "\n"
}

// keyLine matches a mapping entry: its indent, its key, and everything after
// the colon. A comment line and a sequence entry match neither.
var keyLine = regexp.MustCompile(`^( *)([^ #:][^:]*):( .*)?$`)

// entry reads one line as a mapping entry.
func entry(line string) (indent int, key, rest string, ok bool) {
	match := keyLine.FindStringSubmatch(line)
	if match == nil {
		return 0, "", "", false
	}
	return len(match[1]), match[2], strings.TrimPrefix(match[3], " "), true
}

// set writes a value at a dotted path, creating any mapping on the way down
// that the document does not carry.
func (d *yamlDocument) set(path []string, value string) {
	start, end, indent := 0, len(d.lines), 0
	for depth, key := range path {
		at, found := d.find(start, end, indent, key)
		if !found {
			d.insert(d.blockEnd(start, end), buildBlock(indent, path[depth:], value))
			return
		}
		if depth == len(path)-1 {
			d.lines[at] = writeValue(d.lines[at], value)
			return
		}
		start, end = at+1, d.childEnd(at, indent)
		indent = d.childIndent(start, end, indent)
	}
}

// remove deletes the entry at a dotted path, and anything nested under it. A
// path the document does not carry is left alone, which is what `del` does with
// one.
func (d *yamlDocument) remove(path []string) {
	start, end, indent := 0, len(d.lines), 0
	for depth, key := range path {
		at, found := d.find(start, end, indent, key)
		if !found {
			return
		}
		if depth == len(path)-1 {
			d.lines = append(d.lines[:at], d.lines[d.childEnd(at, indent):]...)
			return
		}
		start, end = at+1, d.childEnd(at, indent)
		indent = d.childIndent(start, end, indent)
	}
}

// find is the line declaring key at indent inside [start, end).
func (d *yamlDocument) find(start, end, indent int, key string) (int, bool) {
	for at := start; at < end; at++ {
		lineIndent, lineKey, _, ok := entry(d.lines[at])
		if ok && lineIndent == indent && lineKey == key {
			return at, true
		}
	}
	return 0, false
}

// childEnd is the line after the last one nested under the entry at `at`.
func (d *yamlDocument) childEnd(at, indent int) int {
	for next := at + 1; next < len(d.lines); next++ {
		if lineIndent := leadingSpaces(d.lines[next]); lineIndent <= indent {
			return next
		}
	}
	return len(d.lines)
}

// childIndent is the indent the entries of a mapping sit at, taken from the
// first of them rather than assumed. Two is what yq writes, and what a mapping
// with no entries yet gets.
func (d *yamlDocument) childIndent(start, end, parent int) int {
	for at := start; at < end; at++ {
		if indent, _, _, ok := entry(d.lines[at]); ok {
			return indent
		}
	}
	return parent + 2
}

// blockEnd is where a new entry goes: after the last entry of the mapping, and
// before any comment block trailing it.
//
// A comment run at the end of a mapping is the head comment of whatever comes
// next, which is where yaml's own reader attaches it and where yq leaves it.
func (d *yamlDocument) blockEnd(start, end int) int {
	last := start
	for at := start; at < end; at++ {
		if !strings.HasPrefix(strings.TrimSpace(d.lines[at]), "#") {
			last = at + 1
		}
	}
	return last
}

// insert puts lines at an index.
func (d *yamlDocument) insert(at int, lines []string) {
	tail := append([]string(nil), d.lines[at:]...)
	d.lines = append(append(d.lines[:at], lines...), tail...)
}

// buildBlock is the nested mapping a path that does not exist yet becomes.
func buildBlock(indent int, path []string, value string) []string {
	block := make([]string, 0, len(path))
	for depth, key := range path {
		pad := strings.Repeat(" ", indent+2*depth)
		if depth == len(path)-1 {
			block = append(block, pad+key+": "+value)
			continue
		}
		block = append(block, pad+key+":")
	}
	return block
}

// writeValue replaces an entry's value and keeps its line comment, which is
// what yq does: the comment belongs to the node, not to the value.
func writeValue(line, value string) string {
	indent, key, rest, ok := entry(line)
	if !ok {
		return line
	}
	written := strings.Repeat(" ", indent) + key + ": " + value
	if comment := commentOf(rest); comment != "" {
		written += " " + comment
	}
	return written
}

// normaliseComment collapses the run of spaces before a line comment to one,
// which is what yq's emitter writes.
func normaliseComment(line string) string {
	indent, key, rest, ok := entry(line)
	if !ok {
		return line
	}
	comment := commentOf(rest)
	if comment == "" {
		return line
	}
	value := strings.TrimRight(rest[:len(rest)-len(comment)], " \t")
	written := strings.Repeat(" ", indent) + key + ":"
	if value != "" {
		written += " " + value
	}
	return written + " " + comment
}

// commentOf is the trailing comment of an entry's value, or empty.
//
// A `#` starts a comment only when whitespace comes before it and it is outside
// a quoted scalar, which is YAML's own rule — `path: x#y` has no comment in it.
func commentOf(rest string) string {
	var quote byte
	for at := 0; at < len(rest); at++ {
		switch character := rest[at]; {
		case quote != 0:
			if character == quote {
				quote = 0
			}
		case character == '\'' || character == '"':
			quote = character
		case character == '#' && at > 0 && (rest[at-1] == ' ' || rest[at-1] == '\t'):
			return rest[at:]
		}
	}
	return ""
}

// leadingSpaces is a line's indent.
func leadingSpaces(line string) int {
	return len(line) - len(strings.TrimLeft(line, " "))
}

// The plain forms yaml's core schema resolves to something other than a string.
// A scalar matching one of these has to be quoted, or reading the file back
// would give a boolean, a number or a null where a string was written.
var (
	yamlNull      = regexp.MustCompile(`^(~|null|Null|NULL)$`)
	yamlBool      = regexp.MustCompile(`^(true|True|TRUE|false|False|FALSE)$`)
	yamlInt       = regexp.MustCompile(`^([-+]?[0-9][0-9_]*|0[bB][01_]+|0[oO]?[0-7_]+|0[xX][0-9a-fA-F_]+)$`)
	yamlFloat     = regexp.MustCompile(`^([-+]?(\.[0-9_]+|[0-9][0-9_]*(\.[0-9_]*)?)([eE][-+]?[0-9]+)?|[-+]?\.(inf|Inf|INF)|\.(nan|NaN|NAN))$`)
	yamlTimestamp = regexp.MustCompile(`^[0-9]{4}-[0-9]{1,2}-[0-9]{1,2}([Tt ].*)?$`)
)

// yamlScalar writes a string the way yq's emitter writes one.
//
// Three forms, and which one is used is not cosmetic. Double quotes where the
// plain text would resolve to a null, a boolean, a number or a timestamp;
// single quotes where the plain form is not allowed at all; plain otherwise.
// Every case is measured against yq in config_test.go, because a model id, a
// backlog path or an endpoint name is whatever the operator wrote.
func yamlScalar(value string) string {
	switch {
	case value == "":
		return `""`
	case !plainAllowed(value):
		return "'" + strings.ReplaceAll(value, "'", "''") + "'"
	case yamlNull.MatchString(value), yamlBool.MatchString(value),
		yamlInt.MatchString(value), yamlFloat.MatchString(value),
		yamlTimestamp.MatchString(value):
		return strconv.Quote(value)
	default:
		return value
	}
}

// The characters a plain scalar may not start with. `-`, `?` and `:` are not
// among them: they only end the plain form when a space follows, which is what
// separates `-dash` from `- x`.
const plainForbiddenFirst = ",[]{}#&*!|>'\"%@`"

// plainAllowed reports whether a scalar can be written with no quotes at all.
func plainAllowed(value string) bool {
	if value == "" {
		return false
	}
	if strings.TrimSpace(value) != value {
		return false
	}
	if strings.ContainsAny(value[:1], plainForbiddenFirst) {
		return false
	}
	if len(value) > 1 && strings.ContainsAny(value[:1], "-?:") && value[1] == ' ' {
		return false
	}
	if strings.HasSuffix(value, ":") {
		return false
	}
	// A lone `-` is an indicator with nothing after it, and `---` and `...`
	// open and close a document. Each was measured: `--` and `x---` are
	// plain, so the rule is the marker at the start of the line rather than
	// the characters anywhere in it.
	if value == "-" || value == "---" || value == "..." ||
		strings.HasPrefix(value, "--- ") || strings.HasPrefix(value, "... ") {
		return false
	}
	for at := 0; at+1 < len(value); at++ {
		// `: ` ends a plain scalar and ` #` starts a comment, so a
		// scalar carrying either cannot be written plain.
		if value[at] == ':' && value[at+1] == ' ' {
			return false
		}
		if (value[at] == ' ' || value[at] == '\t') && value[at+1] == '#' {
			return false
		}
	}
	for at := 0; at < len(value); at++ {
		if value[at] < 0x20 || value[at] == 0x7f {
			return false
		}
	}
	return true
}
