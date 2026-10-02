package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/coilyco/umbra/http/opcore"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// rejectEmptyConfig is the set of projected tool names whose empty answers are
// refused rather than returned.
type rejectEmptyConfig map[string]struct{}

// parseRejectEmpty reads top-level `reject-empty` nodes, siblings of `wrap`:
//
//	reject-empty "get_thing"
//
// The argument is the PROJECTED TOOL NAME, matching `cache`, `confirm`, and
// `withhold`, because that is the name a client dispatches on.
//
// A tool that answers with nothing hands the model a blank it cannot tell from
// a real answer, and the model then writes as though it had one. Refusing says
// so, and a tool error is something the model can act on.
//
// Opt-in per grant and off by default, because emptiness is an answer for some
// upstreams: a search with no hits legitimately returns an empty list, and
// refusing that would turn a correct result into an error.
//
// Stated beside `wrap` rather than inside it, like `cache` and `rate-limit`,
// because the wrap body is opcore's frozen grammar.
func parseRejectEmpty(sources []guardSource) (rejectEmptyConfig, error) {
	nodes, err := parseInlineNodes(sources, "reject-empty")
	if err != nil {
		return nil, err
	}
	out := rejectEmptyConfig{}
	for _, sn := range nodes {
		n := sn.node
		if n.Name() != "reject-empty" {
			continue
		}
		tool, err := oneStringArg(n, "reject-empty")
		if err != nil {
			return nil, err
		}
		if _, dup := out[tool]; dup {
			return nil, fmt.Errorf("mcp-beaver: duplicate `reject-empty` for tool %q", tool)
		}
		for key := range n.Properties() {
			return nil, fmt.Errorf(
				"mcp-beaver: unknown `reject-empty` property %q (it takes none; fail-closed)", key)
		}
		out[tool] = struct{}{}
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// validateRejectEmpty rejects a control aimed at a name this spec does not
// serve. Build-time rather than call-time, matching `cache`: an author who
// believes a tool is guarded and one who believes it is not should not both be
// able to run.
func validateRejectEmpty(cfg rejectEmptyConfig, descs []opcore.Descriptor) error {
	if cfg == nil {
		return nil
	}
	served := make(map[string]struct{}, len(descs))
	for _, d := range descs {
		served[toolName(d)] = struct{}{}
	}
	for tool := range cfg {
		if _, ok := served[tool]; !ok {
			return fmt.Errorf(
				"mcp-beaver: `reject-empty` names %q, which is not a grant-backed tool this spec serves", tool)
		}
	}
	return nil
}

// emptyResult reports whether an answer carries nothing a reader could use.
//
// WHAT COUNTS IS DECIDED HERE RATHER THAN INHERITED. Go, JSON, and JavaScript
// each draw truthiness differently, and a control that borrowed one of them
// would refuse a different set than its name suggests. Empty is: no content at
// all, text that is empty or whitespace, and a JSON body of null, "", [], or
// {}.
//
// `false` and `0` are deliberately NOT empty. A tool answering "no" or "none"
// has answered, and refusing that would turn a correct result into an error,
// which is the failure this control exists to prevent rather than to cause.
// See docs/guardfile-controls.md.
func emptyResult(result *mcp.CallToolResult) bool {
	if result == nil {
		return true
	}
	if result.StructuredContent != nil {
		// Round-tripped rather than type-asserted: this arrives as whatever
		// the handler built, a struct for a wrapped grant and a decoded map
		// for the passthrough, and only its JSON shape is the contract.
		return emptyJSONValue(unwrapCoverage(asJSONValue(result.StructuredContent)))
	}
	for _, content := range result.Content {
		text, ok := content.(*mcp.TextContent)
		if !ok {
			// An image or a resource is a payload this cannot read, so it
			// counts as an answer rather than an absence.
			return false
		}
		if strings.TrimSpace(text.Text) == "" {
			continue
		}
		var decoded any
		if err := json.Unmarshal([]byte(text.Text), &decoded); err != nil {
			// Not JSON, and not whitespace, so it is prose with something in it.
			return false
		}
		if !emptyJSONValue(unwrapCoverage(decoded)) {
			return false
		}
	}
	return true
}

// asJSONValue renders any value as the decoded JSON it serialises to. A value
// that cannot round-trip is returned as it stands, which reads as an answer.
func asJSONValue(value any) any {
	encoded, err := json.Marshal(value)
	if err != nil {
		return value
	}
	var decoded any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		return value
	}
	return decoded
}

// unwrapCoverage reads past the wrapped-tool envelope. A `wrap` grant answers
// `{"coverage":{...},"result":...}`, both required, so an envelope is never
// empty and judging it would make this control fire on nothing at all. The
// upstream passthrough returns the upstream's own answer with no envelope, so
// a value that is not one is judged as it stands. See coverage.go.
func unwrapCoverage(value any) any {
	object, ok := value.(map[string]any)
	if !ok || len(object) != 2 {
		return value
	}
	if _, hasCoverage := object["coverage"]; !hasCoverage {
		return value
	}
	inner, hasResult := object["result"]
	if !hasResult {
		return value
	}
	return inner
}

// emptyJSONValue reads one decoded JSON value. Only the container and string
// cases are empty, keeping `false` and `0` real answers.
func emptyJSONValue(value any) bool {
	switch typed := value.(type) {
	case nil:
		return true
	case string:
		return strings.TrimSpace(typed) == ""
	case []any:
		return len(typed) == 0
	case map[string]any:
		return len(typed) == 0
	default:
		return false
	}
}

// withRejectEmpty turns an empty answer into a tool error naming the reason.
//
// A TOOL ERROR RATHER THAN A PROTOCOL ERROR, so the calling model reads it as a
// result it can act on and correct itself, rather than as a transport failure
// it can only retry. An upstream that already failed is passed through
// untouched: it has its own reason and this one would replace it with a worse
// one.
func withRejectEmpty(name string, next mcp.ToolHandler) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		result, err := next(ctx, req)
		if err != nil || (result != nil && result.IsError) {
			return result, err
		}
		if emptyResult(result) {
			return toolError(fmt.Errorf(
				"mcp-beaver: %q answered with nothing, and this tool is declared `reject-empty`. "+
					"Treat this as no result rather than as an empty one", name)), nil
		}
		return result, nil
	}
}
