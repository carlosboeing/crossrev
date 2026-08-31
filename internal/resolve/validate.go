package resolve

import (
	"encoding/json"
	"fmt"
	"strconv"
)

// mapNumbers replaces each resolution's finding_number with the finding id it
// was numbered from (lib/run.sh:2089-2092).
func mapNumbers(payload json.RawMessage, findings []map[string]json.RawMessage) (json.RawMessage, error) {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(payload, &doc); err != nil {
		return nil, err
	}
	raw, ok := doc["resolutions"]
	if !ok {
		return json.RawMessage("[]"), nil
	}
	var items []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, err
	}
	byNumber := map[int]string{}
	for _, f := range findings {
		n, _ := strconv.Atoi(jsonString(f["number"]))
		if n > 0 {
			byNumber[n] = jsonString(f["id"])
		}
	}
	for _, item := range items {
		n, _ := strconv.Atoi(string(item["finding_number"]))
		id, ok := byNumber[n]
		if !ok {
			return nil, fmt.Errorf("finding number %d has no id", n)
		}
		b, err := json.Marshal(id)
		if err != nil {
			return nil, err
		}
		item["finding_id"] = b
		delete(item, "finding_number")
	}
	out, err := json.Marshal(items)
	if err != nil {
		return nil, err
	}
	return out, nil
}
