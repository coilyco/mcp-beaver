package mcpserver

import (
	"encoding/json"
	"errors"
	"fmt"

	"forgejo.coilysiren.me/coilyco-flight-deck/umbra/http/opcore"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// splitArgs routes each supplied argument onto the opcore Args location its
// schema property names. Path and query values reach the URL, body values do
// not. An argument with no matching property is dropped - the tool surface is
// exactly the schema.
func splitArgs(schema opcore.Schema, in map[string]any) opcore.Args {
	a := opcore.Args{
		Path:        map[string]string{},
		Query:       map[string]string{},
		QueryValues: map[string]any{},
		Body:        map[string]any{},
	}
	for name, val := range in {
		prop, ok := schema.Properties[name]
		if !ok {
			continue
		}
		switch prop.Location {
		case opcore.LocationPath:
			a.Path[name] = scalarString(val)
		case opcore.LocationQuery:
			a.QueryValues[name] = val
		case opcore.LocationBody, opcore.LocationForm:
			a.Body[name] = val
		}
	}
	return a
}

// scalarString renders a path scalar as the string opcore fills into the URL.
// Query values retain their JSON types in Args.QueryValues so opcore can enforce
// typed scalar and repeated-array contracts before assembling the request.
func scalarString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", v)
}

// toolSuccess wraps the upstream payload in the coverage-first envelope. The
// text content carries the SAME envelope rather than the bare upstream body:
// the two used to disagree, and the text half is what a head-slicing consumer
// cuts, so leading the structured half alone would have left the caveat where
// it always got destroyed (mcp-beaver#68).
func toolSuccess(resp opcore.Response, desc opcore.Descriptor) *mcp.CallToolResult {
	result := any(resp.Decoded)
	if result == nil {
		text := string(resp.Raw)
		if text == "" {
			text = resp.Status
		}
		result = text
	}
	cov := newCoverage(resp, result)
	// A `max-rows` bound means this runtime really did cut the result, and
	// umbra states that at the END of the payload - the position #68 exists
	// because a slicing consumer destroys it. Lift it to the front.
	if desc.SQL != nil {
		cov.Truncated = sqlResultTruncated(result)
	}
	payload := toolPayload{Coverage: cov, Result: result}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return toolError(fmt.Errorf("serialize tool result: %w", err))
	}
	return &mcp.CallToolResult{
		Content:           []mcp.Content{&mcp.TextContent{Text: string(encoded)}},
		StructuredContent: payload,
	}
}

// sqlResultTruncated reads umbra's own truncation statement off a sql result.
// Keyed on the declared grant kind by the caller, never on sniffing a field
// name out of an arbitrary upstream payload.
func sqlResultTruncated(result any) bool {
	obj, ok := result.(map[string]any)
	if !ok {
		return false
	}
	cut, _ := obj["truncated"].(bool)
	return cut
}

// toolError redacts before the caller sees it. The log line was redacted and
// the caller's copy of the same string was not. See docs/refusals.md.
func toolError(err error) *mcp.CallToolResult {
	out := &mcp.CallToolResult{}
	if redacted := redactSecretPaths(err.Error()); redacted != err.Error() {
		err = errors.New(redacted)
	}
	out.SetError(err)
	return out
}

// schemaNames is the declared argument surface of a generated tool. The schema
// is umbra's own projection of the operation, so it is closed by construction:
// a name it does not carry is a name the tool does not have.
func schemaNames(schema opcore.Schema) map[string]bool {
	out := make(map[string]bool, len(schema.Properties))
	for name := range schema.Properties {
		out[name] = true
	}
	return out
}
