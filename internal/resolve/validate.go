package resolve

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/carlosboeing/crossrev/internal/harness"
)

// mapNumbers replaces each resolution's finding_number with the finding id it
// was numbered from (lib/run.sh:2089-2092).
func mapNumbers(payload json.RawMessage, findings []harness.Node) (json.RawMessage, error) {
	doc, err := harness.DecodeOrdered(payload)
	if err != nil {
		return nil, err
	}
	rawNode := doc.Member("resolutions")
	if !rawNode.Present() || rawNode.IsNull() {
		return json.RawMessage("[]"), nil
	}
	rawBytes, err := rawNode.MarshalJSON()
	if err != nil {
		return nil, err
	}
	var items []harness.Node
	if err := json.Unmarshal(rawBytes, &items); err != nil {
		return nil, err
	}
	byNumber := map[int]string{}
	for _, f := range findings {
		n, _ := strconv.Atoi(f.Member("number").StringVal())
		if n > 0 {
			byNumber[n] = f.Member("id").StringVal()
		}
	}
	for i := range items {
		n, _ := strconv.Atoi(items[i].Member("finding_number").StringVal())
		id, ok := byNumber[n]
		if !ok {
			return nil, fmt.Errorf("finding number %d has no id", n)
		}
		items[i].Set("finding_id", harness.FromString(id))
		items[i].Delete("finding_number")
	}
	out, err := json.Marshal(items)
	if err != nil {
		return nil, err
	}
	return out, nil
}
