package mcpserver

import (
	"fmt"

	"forgejo.coilysiren.me/coilyco-flight-deck/umbra/http/opcore"
)

// ignoreUndeclaredConfig is the set of projected tool names whose undeclared
// top-level arguments are dropped rather than refused.
type ignoreUndeclaredConfig map[string]struct{}

// parseIgnoreUndeclared reads top-level `ignore-undeclared-arguments` nodes,
// siblings of `wrap`:
//
//	ignore-undeclared-arguments "create_message"
//
// The one opening in the mcp-beaver#94 guard. A webhook sender posts its whole
// payload and cannot be told to leave keys out: an Alertmanager-shaped body
// carries `alerts`, `status`, `receiver` and more beside the one annotation a
// `map` reads, so the guard refused every delivery (teable:coilyco/deploy#8419).
func parseIgnoreUndeclared(sources []guardSource) (ignoreUndeclaredConfig, error) {
	nodes, err := parseInlineNodes(sources, "ignore-undeclared-arguments")
	if err != nil {
		return nil, err
	}
	out := ignoreUndeclaredConfig{}
	for _, sn := range nodes {
		n := sn.node
		if n.Name() != "ignore-undeclared-arguments" {
			continue
		}
		tool, err := oneStringArg(n, "ignore-undeclared-arguments")
		if err != nil {
			return nil, err
		}
		if _, dup := out[tool]; dup {
			return nil, fmt.Errorf("mcp-beaver: duplicate `ignore-undeclared-arguments` for tool %q", tool)
		}
		for key := range n.Properties() {
			return nil, fmt.Errorf(
				"mcp-beaver: unknown `ignore-undeclared-arguments` property %q (it takes none; fail-closed)", key)
		}
		out[tool] = struct{}{}
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// validateIgnoreUndeclared admits only a grant whose body is a `map`. There
// the mapping names every value the request carries, so a dropped key cannot
// have been a filter the upstream would have honoured. On any other grant the
// dropped key is exactly the silent #94 failure, so it is a build error.
func validateIgnoreUndeclared(cfg ignoreUndeclaredConfig, descs []opcore.Descriptor) error {
	if cfg == nil {
		return nil
	}
	byTool := make(map[string]opcore.Descriptor, len(descs))
	for _, d := range descs {
		byTool[toolName(d)] = d
	}
	for tool := range cfg {
		desc, served := byTool[tool]
		if !served {
			return fmt.Errorf(
				"mcp-beaver: `ignore-undeclared-arguments` names %q, which is not a grant-backed tool this spec serves", tool)
		}
		if len(desc.BodyMappings) == 0 {
			return fmt.Errorf(
				"mcp-beaver: `ignore-undeclared-arguments` names %q, whose grant has no body `map`; "+
					"only a mapped webhook body may drop undeclared arguments (mcp-beaver#94)", tool)
		}
	}
	return nil
}
