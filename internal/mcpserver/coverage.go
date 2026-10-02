package mcpserver

import (
	"encoding/json"

	"github.com/coilyco/umbra/http/opcore"
)

// consumerBudgetBytes is the smallest tool-result cap observed in a consuming
// harness (sirens-echo#449 measured 8192 on one profile and 16384 on another).
// It is not a limit this runtime enforces - nothing here truncates - it is the
// threshold past which a response STOPS being self-describing, because whether
// the model sees all of it becomes a property of which consumer read it.
const consumerBudgetBytes = 8192

// coverage states the bound of a result. It is the first field of every
// grant-backed response, and that position is the whole point: a consuming
// harness that bounds a tool result by head-slicing keeps the front and
// discards the tail, so a caveat serialized last is the first thing destroyed
// (mcp-beaver#68). A model then reads rows with no caveat and answers as
// though the view were complete.
type coverage struct {
	// Truncated is stated rather than omitted, so a consumer can attribute a
	// short view to its own slicing rather than to this runtime. It is false
	// for every upstream read, because nothing here cuts one. A `sql` grant's
	// `max-rows` is the one bound this runtime applies itself, and it sets
	// this true rather than leaving the caveat at the tail (mcp-beaver#89).
	Truncated bool `json:"truncated"`
	// Bytes is the size of the upstream payload this result carries.
	Bytes int `json:"bytes"`
	// OverBudget reports Bytes past consumerBudgetBytes: this response is
	// large enough that some consumer will cut it, and the model may be
	// reading a prefix.
	OverBudget bool `json:"over_budget"`
	// Items counts each array in the payload, keyed by its dotted field path,
	// or by "result" when the payload is itself an array. A count in meaning is what
	// changes an answer; a byte total is not. This is also where an array the
	// upstream failed to bound becomes visible to the author on the first call
	// rather than after a day of investigation.
	Items map[string]int `json:"items,omitempty"`
	// Pages is present only on an extracted document. It is one of the two
	// places this runtime can honestly state a shown-of-total, because the
	// bound is its own rather than the upstream's.
	Pages *pageCoverage `json:"pages,omitempty"`
	// Entries is present only on an extracted feed, and is the other. A feed
	// carries its whole item set in one document, so unlike an upstream's
	// paging the total here is measured rather than guessed.
	Entries *entryCoverage `json:"entries,omitempty"`
}

// pageCoverage is how much of a document the extraction read. Present so a
// bounded read cannot be mistaken for the whole document - a model told only
// that it received text has no way to know page 21 exists.
type pageCoverage struct {
	Shown int `json:"shown"`
	Total int `json:"total"`
}

// entryCoverage is how many of a feed's items the projection carried. Present
// so a bounded read cannot be mistaken for the whole feed - a model told only
// that it received entries has no way to know the 26th exists.
type entryCoverage struct {
	Shown int `json:"shown"`
	Total int `json:"total"`
}

// toolPayload is the response envelope. FIELD ORDER IS THE CONTRACT: Go
// serializes struct fields in declaration order, so coverage reaches the wire
// ahead of the bulk payload. A map would sort keys alphabetically and put this
// at the mercy of whatever the caveat field is named next.
type toolPayload struct {
	Coverage coverage `json:"coverage"`
	Result   any      `json:"result"`
}

var resultOutputSchema = json.RawMessage(`{
	"type":"object",
	"properties":{
		"coverage":{
			"type":"object",
			"description":"The bound of this result, stated before the payload so a truncating consumer cannot remove it.",
			"properties":{
				"truncated":{"type":"boolean"},
				"bytes":{"type":"integer"},
				"over_budget":{"type":"boolean"},
				"items":{"type":"object","description":"Array lengths in the payload, keyed by dotted field path.","additionalProperties":{"type":"integer"}},
				"pages":{
					"type":"object",
					"description":"Pages read of pages the document holds. Present only on an extracted document.",
					"properties":{"shown":{"type":"integer"},"total":{"type":"integer"}},
					"required":["shown","total"]
				},
				"entries":{
					"type":"object",
					"description":"Entries carried of entries the feed holds. Present only on an extracted feed.",
					"properties":{"shown":{"type":"integer"},"total":{"type":"integer"}},
					"required":["shown","total"]
				}
			},
			"required":["truncated","bytes","over_budget"]
		},
		"result":{}
	},
	"required":["coverage","result"],
	"additionalProperties":false
}`)

// newCoverage measures what this runtime can honestly measure: the payload
// size and the length of every array in it. It never reports a shown-of-total,
// because the total lives upstream and this runtime never asked for it -
// inventing one would be exactly the confident wrong answer #68 is about.
func newCoverage(resp opcore.Response, result any) coverage {
	size := len(resp.Raw)
	if size == 0 {
		if encoded, err := json.Marshal(result); err == nil {
			size = len(encoded)
		}
	}
	return coverage{
		Truncated:  false,
		Bytes:      size,
		OverBudget: size > consumerBudgetBytes,
		Items:      countArrays(result),
	}
}

// maxCountDepth bounds how far the count descends through nested objects, so a
// pathological payload cannot make the coverage block the expensive part of the
// response. Four covers a GraphQL `data.<Query>.<field>` and a layer past it.
const maxCountDepth = 4

// countArrays names every array in the payload and how long it is, keyed by its
// dotted path.
//
// It descends through OBJECTS and never into an array's elements. That is the
// original one-level rule applied where it was aimed: an array inside an array
// element is a per-row detail, and enumerating those would multiply the block by
// the row count. An array inside an object is a statement about the view, which
// is exactly what a count is for. Walking only one level meant a GraphQL
// response, whose arrays always sit under `data`, reported no count at all
// (mcp-beaver#88).
func countArrays(result any) map[string]int {
	switch typed := result.(type) {
	case []any:
		return map[string]int{"result": len(typed)}
	case map[string]any:
		counts := map[string]int{}
		collectArrayCounts(typed, "", 1, counts)
		if len(counts) == 0 {
			return nil
		}
		return counts
	default:
		return nil
	}
}

// collectArrayCounts walks one object level, recording arrays and recursing
// into child objects until maxCountDepth.
func collectArrayCounts(obj map[string]any, prefix string, depth int, counts map[string]int) {
	for key, value := range obj {
		path := key
		if prefix != "" {
			path = prefix + "." + key
		}
		switch child := value.(type) {
		case []any:
			counts[path] = len(child)
		case map[string]any:
			if depth < maxCountDepth {
				collectArrayCounts(child, path, depth+1, counts)
			}
		}
	}
}
