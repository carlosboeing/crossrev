package prstate

import (
	"encoding/json"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/carlosboeing/crossrev/internal/core"
)

// Comment is one comment as the trusted-author read emits it: a compact object
// per line carrying the id, the body and the creation time
// (lib/state.sh:59-63).
type Comment struct {
	ID        int64  `json:"id"`
	Body      string `json:"body"`
	CreatedAt string `json:"created_at"`
}

// Markers decodes every marker on the pull request out of that stream, in the
// order the comments arrive, which is chronological (lib/state.sh:141-155).
//
// The stream is read line by line rather than as one JSON document, so a line
// that cannot be read is skipped the way the per-comment loop skipped it rather
// than failing the whole read. A comment carrying no marker, two markers, or a
// payload that is not one object contributes nothing, and the markers around it
// survive.
//
// Nothing here reports an error, because nothing in Bash does: a marker that
// cannot be read is a comment somebody else wrote. An empty read is an empty
// slice rather than nil, because lib/state.sh:153 forces `[]` for the same
// reason: a caller that marshals the result writes an empty array, not a null.
func Markers(stream []byte) []Marker {
	markers := []Marker{}
	for line := range strings.SplitSeq(string(stream), "\n") {
		if line == "" {
			continue
		}
		// Reading the line into Comment refuses an array, a string, a number
		// and a boolean, which is what `select(type == "object")` refuses
		// (lib/state.sh:148).
		var comment Comment
		if err := json.Unmarshal([]byte(line), &comment); err != nil {
			continue
		}
		raw, ok := DecodeMarker(comment.Body)
		if !ok {
			continue
		}
		marker, err := ParseMarker(raw)
		if err != nil {
			continue
		}
		marker.commentID = comment.ID
		markers = append(markers, marker)
	}
	return markers
}

// EncodeFindingMarker writes the per-write marker that makes recovery exact
// (lib/state.sh:168-172).
//
// A ledger written after a successful post has a window in it: GitHub accepts
// the comment, the process dies, and the mapping from comment to finding is
// gone. Recovery then cannot tell an already-posted finding from a missing one
// and posts it twice. Carrying the id in the body closes the window by
// construction, because the record and the thing it records are one HTTP call.
//
// The three keys are written in this order and the prefix has no trailing
// space, which is the only thing keeping this marker out of the state decoder.
func EncodeFindingMarker(id core.FindingID, pass int, leg core.Leg) string {
	var out strings.Builder
	out.WriteString("\n\n")
	out.WriteString(findingMarkerOpen)
	out.WriteString(`{"id":`)
	out.Write(appendJSONString(nil, string(id)))
	out.WriteString(`,"pass":`)
	out.WriteString(strconv.Itoa(pass))
	out.WriteString(`,"leg":`)
	out.Write(appendJSONString(nil, string(leg)))
	out.WriteString("}")
	out.WriteString(markerClose)
	return out.String()
}

// FindingIDs reads the finding ids back out of a stream of comment bodies,
// narrowed to one leg and optionally to one pass, sorted and deduplicated
// (lib/state.sh:216-222).
//
// The leg is not optional, and conflating the two would be silent. The review
// leg stamps a marker on every inline comment it posts, so a resolve leg
// reading ids without the leg would find every id already present, skip every
// reply as a duplicate, and resolve the threads anyway. That leaves a
// collaborator with a resolved thread and no explanation.
//
// A pass of zero reads every pass, which is the empty `$pass` argument Bash
// passes: pass numbers start at 1, so no real pass is excluded by the sentinel.
//
// One divergence, deliberately left: on a line where a second ` -->` follows
// the marker, the greedy extraction hands both the payload and the trailing
// text to the reader. jq reads that as a stream, emits `.id` from the object
// and only then errors, and `2>/dev/null` keeps the id; Go refuses the whole
// value and drops it. CrossRev appends the marker last on every line it writes,
// so only a hand-written body reaches it, and reproducing jq's stream parser to
// recover a forged id is not worth the code.
func FindingIDs(bodies []string, leg core.Leg, pass int) []core.FindingID {
	seen := map[string]bool{}
	for _, body := range bodies {
		for line := range strings.SplitSeq(body, "\n") {
			payload, ok := extractFindingPayload(line)
			if !ok {
				continue
			}
			var mark struct {
				ID   string   `json:"id"`
				Pass int      `json:"pass"`
				Leg  core.Leg `json:"leg"`
			}
			if err := json.Unmarshal([]byte(payload), &mark); err != nil {
				continue
			}
			if mark.Leg != leg || (pass != 0 && mark.Pass != pass) {
				continue
			}
			seen[mark.ID] = true
		}
	}

	sorted := slices.Sorted(maps.Keys(seen))

	ids := make([]core.FindingID, 0, len(sorted))
	for _, id := range sorted {
		// Reading an id off a marker is not minting one, so a value the hash
		// could not have produced is dropped rather than carried.
		parsed, err := ParseFindingID(id)
		if err != nil {
			continue
		}
		ids = append(ids, parsed)
	}
	return ids
}

// extractFindingPayload applies the rule the sed at lib/state.sh:218 applies:
// per line, the text after the LAST `<!-- crossrev:f ` and before the LAST
// ` -->` after it. Both `.*` in that expression are greedy, which is what makes
// each delimiter the last one rather than the first.
func extractFindingPayload(line string) (string, bool) {
	i := strings.LastIndex(line, findingMarkerOpen)
	if i < 0 {
		return "", false
	}
	rest := line[i+len(findingMarkerOpen):]
	j := strings.LastIndex(rest, markerClose)
	if j < 0 {
		return "", false
	}
	return rest[:j], true
}
