package mcpserver

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	kdl "github.com/calico32/kdl-go"
	"github.com/coilyco/umbra/http/opcore"
)

// queryPin fixes one outgoing query parameter for one spec-mode tool, resolved
// server-side and never exposed to the caller.
//
// This is the spec-mode counterpart to ArgPin, which does the same job for the
// upstream proxy. The need is identical and the mechanism differs because the
// two modes assemble a request differently: a proxy forwards named arguments,
// while spec mode derives a URL from the grant.
//
// The gap it closes is that `set` writes fixed BODY values only, so a GET
// endpoint whose scope rides in the query string had nowhere to put it. The
// only alternative was declaring the scope as a caller query field, which
// hands the model the very parameter the deployment is trying to fix - for
// Steam, that is the difference between "Kai's library" and "anyone's library".
type queryPin struct {
	Name     string // outgoing query parameter
	Provider string // env | file | literal
	Source   string // env var name, file path, or the literal value
	From     string // optional extraction applied to the resolved value
}

// queryFromPrefix marks the one extraction a pin may apply: read a query
// parameter out of a URL the resolved value holds.
//
// Credentials arrive embedded in a URL more often than they should - private
// RSS and Atom feeds, signed links, webhook endpoints. Without this the only
// way to pin one is to store a SECOND copy of the same credential, pre-split,
// which means two secrets to rotate together and one of them silently going
// stale. Extracting at call time keeps a single source of truth.
//
// It only ever NARROWS: it takes a component of a value the server already
// resolved. It reaches no new source, widens no grant, and the pinned name is
// still absent from the tool schema.
const queryFromPrefix = "query:"

// parseQueryPins reads top-level `pin` nodes, siblings of `wrap`:
//
//	pin "get_owned_games" {
//	    query "steamid" env "STEAM_STEAMID64"
//	    query "include_appinfo" literal "1"
//	}
//
// The argument is the PROJECTED TOOL NAME, matching `confirm` and `withhold`.
func parseQueryPins(sources []guardSource, providers ProviderSet) (map[string][]queryPin, error) {
	nodes, err := parseInlineNodes(sources, "pin")
	if err != nil {
		return nil, err
	}
	out := map[string][]queryPin{}
	for _, sn := range nodes {
		n := sn.node
		if n.Name() != "pin" {
			continue
		}
		tool, err := oneStringArg(n, "pin")
		if err != nil {
			return nil, err
		}
		if _, dup := out[tool]; dup {
			return nil, fmt.Errorf("mcp-beaver: duplicate `pin` for tool %q", tool)
		}
		if len(n.Properties()) > 0 {
			return nil, fmt.Errorf("mcp-beaver: `pin` %q takes no properties, only `query` children (fail-closed)", tool)
		}
		pins, err := parseQueryPinChildren(n, tool, providers)
		if err != nil {
			return nil, err
		}
		out[tool] = pins
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

func parseQueryPinChildren(n *kdl.Node, tool string, providers ProviderSet) ([]queryPin, error) {
	var pins []queryPin
	seen := map[string]bool{}
	for _, child := range n.Children().Nodes {
		if child.Name() != "query" {
			return nil, fmt.Errorf("mcp-beaver: unknown `pin` child %q (want query; fail-closed)", child.Name())
		}
		args := child.Arguments()
		if len(args) != 3 {
			return nil, fmt.Errorf("mcp-beaver: `pin` %q: `query` wants three arguments - name, provider, source - e.g. `query \"steamid\" env \"STEAM_STEAMID64\"`", tool)
		}
		pin := queryPin{Name: args[0].String(), Provider: args[1].String(), Source: args[2].String()}
		for key, value := range child.Properties() {
			if key != "from" {
				return nil, fmt.Errorf("mcp-beaver: `pin` %q: unknown `query` property %q (want from; fail-closed)", tool, key)
			}
			pin.From = value.String()
		}
		if pin.From != "" {
			name, ok := strings.CutPrefix(pin.From, queryFromPrefix)
			if !ok || name == "" {
				return nil, fmt.Errorf("mcp-beaver: `pin` %q: query %q has from=%q, want `from=\"query:<parameter>\"` (fail-closed)", tool, pin.Name, pin.From)
			}
		}
		switch {
		case pin.Name == "":
			return nil, fmt.Errorf("mcp-beaver: `pin` %q: query parameter name must be non-empty", tool)
		case pin.Source == "":
			return nil, fmt.Errorf("mcp-beaver: `pin` %q: query %q has an empty source", tool, pin.Name)
		}
		if err := providers.checkSource(pin.Provider, pin.Source); err != nil {
			return nil, fmt.Errorf("mcp-beaver: `pin` %q: query %q %w", tool, pin.Name, err)
		}
		if seen[pin.Name] {
			return nil, fmt.Errorf("mcp-beaver: `pin` %q pins query %q twice", tool, pin.Name)
		}
		seen[pin.Name] = true
		pins = append(pins, pin)
	}
	if len(pins) == 0 {
		return nil, fmt.Errorf("mcp-beaver: `pin` %q states no `query` children: a pin that fixes nothing is not a pin", tool)
	}
	return pins, nil
}

// validateQueryPins rejects a pin that names no minted tool, and one that
// collides with a declared query field.
//
// The collision case matters more than it looks. A pinned name that is also a
// caller-supplied field would reach opcore through both Args.Query and the
// caller's input, which errors at CALL time rather than build time - a
// deployment would look configured and fail on first use. Worse, an author
// could reasonably read the pin as overriding the field, when it does not.
func validateQueryPins(pins map[string][]queryPin, descs []opcore.Descriptor) error {
	byTool := make(map[string]opcore.Descriptor, len(descs))
	for _, d := range descs {
		if d.Proxy == nil {
			byTool[toolName(d)] = d
		}
	}
	for tool, group := range pins {
		desc, served := byTool[tool]
		if !served {
			return fmt.Errorf("mcp-beaver: `pin` names tool %q, which this spec does not mint as a grant: nothing would apply it", tool)
		}
		declared := make(map[string]bool, len(desc.QueryFlags))
		for _, f := range desc.QueryFlags {
			declared[f.Name] = true
			declared[f.QueryName()] = true
		}
		for _, pin := range group {
			if declared[pin.Name] {
				return fmt.Errorf("mcp-beaver: `pin` %q pins query %q, which the grant also declares as a caller input: remove one, since a pin does not override a supplied value", tool, pin.Name)
			}
		}
	}
	return nil
}

// resolveQueryPins reads each pin's source at CALL time, matching how `auth`
// resolves its value: nothing is baked into the image, and a rotated env var
// takes effect on the next call rather than the next restart.
func resolveQueryPins(ctx context.Context, pins []queryPin, providers ProviderSet) (map[string]string, error) {
	out := make(map[string]string, len(pins))
	for _, pin := range pins {
		provider, ok := providers.registry()[pin.Provider]
		if !ok {
			return nil, fmt.Errorf("mcp-beaver: unknown value provider %q", pin.Provider)
		}
		value, err := provider(ctx, pin.Source)
		if err != nil {
			return nil, fmt.Errorf("mcp-beaver: resolve pinned query %q: %w", pin.Name, err)
		}
		if pin.From != "" {
			value, err = extractQueryParam(value, strings.TrimPrefix(pin.From, queryFromPrefix))
			if err != nil {
				return nil, fmt.Errorf("mcp-beaver: pinned query %q: %w", pin.Name, err)
			}
		}
		out[pin.Name] = value
	}
	return out, nil
}

// extractQueryParam reads one query parameter out of a URL.
//
// Errors name the parameter and never the value: the value is the credential
// this exists to handle, and a resolve failure is exactly when something is
// most likely to be logged.
func extractQueryParam(raw, name string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", fmt.Errorf("from=%s%s: the resolved value is not a URL", queryFromPrefix, name)
	}
	values, err := url.ParseQuery(parsed.RawQuery)
	if err != nil {
		return "", fmt.Errorf("from=%s%s: the resolved URL has no readable query string", queryFromPrefix, name)
	}
	if !values.Has(name) {
		return "", fmt.Errorf("from=%s%s: the resolved URL carries no %q parameter", queryFromPrefix, name, name)
	}
	got := values.Get(name)
	if got == "" {
		return "", fmt.Errorf("from=%s%s: the resolved URL carries %q but it is empty", queryFromPrefix, name, name)
	}
	return got, nil
}
