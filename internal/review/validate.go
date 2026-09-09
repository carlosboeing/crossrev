package review

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/diff"
	"github.com/carlosboeing/crossrev/internal/harness"
	"github.com/carlosboeing/crossrev/internal/prstate"
	"github.com/carlosboeing/crossrev/internal/validate"
)

func (l *Leg) checkPayload(payload []byte) error {
	if l != nil && l.Validate != nil {
		return l.Validate(payload, l.Expect)
	}
	if l != nil && len(l.Expect.Units) > 0 {
		return validate.Review(payload, l.Expect)
	}
	return validate.Findings(payload)
}

func validateCode(err error) int {
	if err == nil {
		return 0
	}
	var semantic *validate.SemanticError
	if errors.As(err, &semantic) {
		return 2
	}
	return 1
}

func enrichFindings(payload, diffBytes []byte, workdir string) (json.RawMessage, []string, error) {
	return enrichFindingsInScope(payload, diffBytes, workdir, nil)
}

// enrichFindingsInScope enriches findings the way enrichFindings does, and
// records how each finding anchors: line for a valid hunk line in the diff,
// file for a required path with no valid hunk line, outside_diff for any
// other path. Required holds the current required paths; nil means the
// frozen path, where every finding anchors by the diff alone.
func enrichFindingsInScope(payload, diffBytes []byte, workdir string, required map[string]bool) (json.RawMessage, []string, error) {
	var doc struct {
		Findings []json.RawMessage `json:"findings"`
	}
	if err := json.Unmarshal(payload, &doc); err != nil {
		return json.RawMessage("[]"), nil, nil
	}
	parsed := diff.Parse(diffBytes, core.RevisionPair{})
	out := make([]json.RawMessage, 0, len(doc.Findings))
	var snaps []string
	for _, raw := range doc.Findings {
		// harness.Node rather than a map, because encoding/json sorts a map's
		// keys and jq does not. Bash writes `$f + {id:…, anchor:…, …}`
		// (lib/run.sh:1184-1186), and jq's + keeps the model's own key order
		// then appends the six in the order written. These bytes end up in the
		// marker every pull request carries, so the order is not cosmetic.
		var f harness.Node
		if err := json.Unmarshal(raw, &f); err != nil {
			out = append(out, raw)
			continue
		}
		path, _ := f.Member("path").AsString()
		title, _ := f.Member("title").AsString()
		side, _ := f.Member("side").AsString()
		if side == "" {
			side = string(core.SideRight)
		}
		line64, _ := f.Member("line").AsInt()
		line := int(line64)
		if moved, ok := parsed.Anchor(path, core.Side(side), line, diff.DefaultSnap); ok && moved != line {
			snaps = append(snaps, fmt.Sprintf("%s:%d (%s) is not a line the diff shows; anchoring the finding to line %d instead.", path, line, side, moved))
			f.Set("line", harness.FromInt(int64(moved)))
			line = moved
		}
		anchorSide := core.SideRight
		if parsedSide, err := core.ParseSide(side); err == nil {
			anchorSide = parsedSide
		}
		kind, reason := anchorKind(parsed, required, path, anchorSide, line)
		content, _ := os.ReadFile(filepath.Join(workdir, path))
		anchor := prstate.AnchorAt(content, line)
		id := prstate.NewFindingID(path, title, anchor)
		f.Set("id", harness.FromString(string(id)))
		f.Set("anchor", harness.FromString(anchor.String()))
		f.Set("anchor_kind", harness.FromString(string(kind)))
		if reason == "" {
			f.Set("anchor_reason", harness.FromNull())
		} else {
			f.Set("anchor_reason", harness.FromString(reason))
		}
		f.Set("thread_id", harness.FromNull())
		f.Set("root_comment_id", harness.FromNull())
		f.Set("resolution", harness.FromNull())
		f.Set("tracked_as", harness.FromNull())
		enriched, err := json.Marshal(f)
		if err != nil {
			return nil, snaps, err
		}
		out = append(out, enriched)
	}
	body, err := json.Marshal(out)
	return body, snaps, err
}

// anchorKind decides how one finding anchors: a valid hunk line in the diff
// stays line; a required path with no valid hunk line is file-level; any
// other path is outside the diff. The kind travels on the persisted finding
// so the resolve leg keeps resolution identity without requiring a thread.
// AnchorKind is how one persisted finding anchors: a valid hunk line in
// the diff, a required file with no valid hunk line, or a path outside the
// changed files.
type AnchorKind string

const (
	// AnchorLine is a valid hunk line: a GitHub inline comment anchors there.
	AnchorLine AnchorKind = "line"
	// AnchorFile is a changed path with no valid hunk line: the comment
	// lands on the file rather than a line.
	AnchorFile AnchorKind = "file"
	// AnchorOutsideDiff is any other path, typically advisory context: the
	// comment lands at the top level and keeps its finding id without a
	// thread.
	AnchorOutsideDiff AnchorKind = "outside_diff"
)

func anchorKind(parsed *diff.Diff, required map[string]bool, path string, side core.Side, line int) (AnchorKind, string) {
	if _, ok := parsed.Anchor(path, side, line, diff.DefaultSnap); ok {
		return AnchorLine, ""
	}
	// A nil required set means the frozen path, where the prompt was the
	// diff itself: every finding is presumed on-diff, so a hunkless diff
	// still lands a file-level comment rather than a top-level one. Only
	// a known required set that excludes the path proves outside_diff.
	if required == nil || required[path] {
		return AnchorFile, "no hunk line covers this file, so the comment lands on the file rather than a line"
	}
	return AnchorOutsideDiff, "outside the changed files, so the comment lands at the top level and keeps its finding id"
}

func jsonString(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 {
		return "", false
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", false
	}
	return s, true
}

func jsonInt(raw json.RawMessage) int {
	var n int
	_ = json.Unmarshal(raw, &n)
	return n
}

func marshalString(s string) json.RawMessage {
	b, err := json.Marshal(s)
	if err != nil {
		return json.RawMessage(`""`)
	}
	return b
}
