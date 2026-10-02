package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/coilyco/umbra/http/opcore"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// emptyArgConfig maps a projected tool name to the argument names that may not
// arrive empty.
type emptyArgConfig map[string][]string

// parseRejectEmptyArguments reads top-level `reject-empty-argument` nodes,
// siblings of `wrap`:
//
//	reject-empty-argument "create_message" field="content"
//
// The mirror of `reject-empty`, and a separate control on purpose: one refuses
// an answer that carries nothing, this refuses a WRITE that would carry
// nothing. Folding both into one node would put two behaviours under one name
// and the guardfile would not say which was meant.
//
// It exists because a blank write succeeds. Dowel posted four fully empty
// messages into a shared channel, roughly 9% of its visible posts, and every
// layer reported success because an empty `content` is a valid request
// (sirens-echo#1035).
//
// Named per field rather than "any required argument", because emptiness is
// legitimate for some inputs and a control that refuses a write must say
// exactly what it is refusing.
func parseRejectEmptyArguments(sources []guardSource) (emptyArgConfig, error) {
	nodes, err := parseInlineNodes(sources, "reject-empty-argument")
	if err != nil {
		return nil, err
	}
	out := emptyArgConfig{}
	for _, sn := range nodes {
		n := sn.node
		if n.Name() != "reject-empty-argument" {
			continue
		}
		tool, err := oneStringArg(n, "reject-empty-argument")
		if err != nil {
			return nil, err
		}
		field := ""
		for key, value := range n.Properties() {
			if key != "field" {
				return nil, fmt.Errorf(
					"mcp-beaver: unknown `reject-empty-argument` property %q (want field; fail-closed)", key)
			}
			field = strings.TrimSpace(value.String())
		}
		if field == "" {
			return nil, fmt.Errorf(
				"mcp-beaver: `reject-empty-argument` %q needs a field, e.g. `reject-empty-argument %q field=\"content\"`",
				tool, tool)
		}
		for _, existing := range out[tool] {
			if existing == field {
				return nil, fmt.Errorf(
					"mcp-beaver: duplicate `reject-empty-argument` for %q field %q", tool, field)
			}
		}
		out[tool] = append(out[tool], field)
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// declaredInputs names every caller-supplied input on one grant, across the
// body, query, form, and path. A control aimed at a name none of them declares
// is a typo that would otherwise guard nothing.
func declaredInputs(desc opcore.Descriptor) map[string]bool {
	names := map[string]bool{}
	for _, group := range [][]opcore.Field{desc.BodyFlags, desc.QueryFlags, desc.FormFlags} {
		for _, f := range group {
			names[f.Name] = true
		}
	}
	for _, p := range desc.PathParams {
		names[p] = true
	}
	for _, m := range desc.BodyMappings {
		names[m.Target] = true
		if len(m.SourcePath) > 0 {
			names[m.SourcePath[0]] = true
		}
	}
	return names
}

// validateRejectEmptyArguments fails the build on a tool this spec does not
// serve or a field the grant does not declare, matching `cache` and `pin`.
func validateRejectEmptyArguments(cfg emptyArgConfig, descs []opcore.Descriptor) error {
	if cfg == nil {
		return nil
	}
	byTool := make(map[string]opcore.Descriptor, len(descs))
	for _, d := range descs {
		byTool[toolName(d)] = d
	}
	for tool, fields := range cfg {
		desc, served := byTool[tool]
		if !served {
			return fmt.Errorf(
				"mcp-beaver: `reject-empty-argument` names %q, which is not a grant-backed tool this spec serves", tool)
		}
		declared := declaredInputs(desc)
		for _, field := range fields {
			if !declared[field] {
				known := make([]string, 0, len(declared))
				for name := range declared {
					known = append(known, name)
				}
				sort.Strings(known)
				return fmt.Errorf(
					"mcp-beaver: `reject-empty-argument` %q names field %q, which the grant does not declare (it has %s)",
					tool, field, strings.Join(known, ", "))
			}
		}
	}
	return nil
}

// withRejectEmptyArguments refuses a call that would write nothing.
//
// A FIELD THAT IS ABSENT IS NOT REFUSED HERE. Absence of a required input is
// the grant's own error and it says so better, and absence of an optional one
// is the caller declining to set it. What this catches is the field arriving
// present and blank, which every layer otherwise reports as a success.
//
// Outermost, so a refused write spends no rate-limit slot and never asks a
// human to confirm something that was going to be refused anyway.
func withRejectEmptyArguments(name string, fields []string, next mcp.ToolHandler) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if len(req.Params.Arguments) > 0 {
			var supplied map[string]any
			if err := json.Unmarshal(req.Params.Arguments, &supplied); err == nil {
				for _, field := range fields {
					value, present := supplied[field]
					if !present {
						continue
					}
					if emptyJSONValue(value) {
						return toolError(fmt.Errorf(
							"mcp-beaver: %q was called with an empty %q, and this tool is declared "+
								"`reject-empty-argument`. Supply content or do not call it",
							name, field)), nil
					}
				}
			}
		}
		return next(ctx, req)
	}
}
