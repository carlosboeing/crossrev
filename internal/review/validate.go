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
		content, _ := os.ReadFile(filepath.Join(workdir, path))
		anchor := prstate.AnchorAt(content, line)
		id := prstate.NewFindingID(path, title, anchor)
		f.Set("id", harness.FromString(string(id)))
		f.Set("anchor", harness.FromString(anchor.String()))
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
