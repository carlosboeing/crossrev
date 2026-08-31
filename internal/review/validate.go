package review

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/diff"
	"github.com/carlosboeing/crossrev/internal/prstate"
	"github.com/carlosboeing/crossrev/internal/validate"
)

func (l *Leg) checkPayload(payload []byte) error {
	if l != nil && l.Validate != nil {
		return l.Validate(payload)
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
		var f map[string]json.RawMessage
		if err := json.Unmarshal(raw, &f); err != nil {
			out = append(out, raw)
			continue
		}
		path, _ := jsonString(f["path"])
		title, _ := jsonString(f["title"])
		side, _ := jsonString(f["side"])
		if side == "" {
			side = string(core.SideRight)
		}
		line := jsonInt(f["line"])
		if moved, ok := parsed.Anchor(path, core.Side(side), line, diff.DefaultSnap); ok && moved != line {
			snaps = append(snaps, fmt.Sprintf("%s:%d (%s) is not a line the diff shows; anchoring the finding to line %d instead.", path, line, side, moved))
			f["line"] = json.RawMessage(strconv.Itoa(moved))
			line = moved
		}
		content, _ := os.ReadFile(filepath.Join(workdir, path))
		anchor := prstate.AnchorAt(content, line)
		id := prstate.NewFindingID(path, title, anchor)
		f["id"] = marshalString(string(id))
		f["anchor"] = marshalString(anchor.String())
		f["thread_id"] = json.RawMessage("null")
		f["root_comment_id"] = json.RawMessage("null")
		f["resolution"] = json.RawMessage("null")
		f["tracked_as"] = json.RawMessage("null")
		enriched, err := json.Marshal(f)
		if err != nil {
			return nil, snaps, err
		}
		out = append(out, enriched)
	}
	body, err := json.Marshal(out)
	return body, snaps, err
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
