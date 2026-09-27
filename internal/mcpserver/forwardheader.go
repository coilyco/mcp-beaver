package mcpserver

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// forwardHeaderConfig maps each canonical header name a server forwards to
// whether a tool call must carry it.
type forwardHeaderConfig map[string]bool

// parseForwardHeaders reads top-level `forward-header` nodes, siblings of `wrap`:
//
//	forward-header "x-agent-origin" required=#false
//
// The caller's value for that header rides onto every upstream request the
// call makes, so an upstream sees who is behind the server and not only the
// server. A wrap-level `header` or `auth header-token` naming the same header
// is the fallback, because a forwarded value replaces it when present.
//
// Across `inherit`, `required` only ever tightens, so a child cannot relax a
// base that insists on the header. See docs/guardfile-controls.md.
func parseForwardHeaders(sources []guardSource) (forwardHeaderConfig, error) {
	nodes, err := parseInlineNodes(sources, "forward-header")
	if err != nil {
		return nil, err
	}
	cfg := forwardHeaderConfig{}
	seen := map[int]map[string]bool{}
	for _, sn := range nodes {
		n := sn.node
		if n.Name() != "forward-header" {
			continue
		}
		raw, err := oneStringArg(n, "forward-header")
		if err != nil {
			return nil, err
		}
		name := http.CanonicalHeaderKey(raw)
		if why, reserved := reservedForwardHeaders[strings.ToLower(name)]; reserved {
			return nil, fmt.Errorf("mcp-beaver: `forward-header %q` is refused: %s (fail-closed)", raw, why)
		}
		if seen[sn.index] == nil {
			seen[sn.index] = map[string]bool{}
		}
		if seen[sn.index][name] {
			return nil, fmt.Errorf("mcp-beaver: duplicate `forward-header %q` (fail-closed)", raw)
		}
		seen[sn.index][name] = true
		required := false
		for key, value := range n.Properties() {
			if key != "required" {
				return nil, fmt.Errorf("mcp-beaver: `forward-header` property %q is unknown (want required; fail-closed)", key)
			}
			if required, err = boolProp(value, "forward-header", key); err != nil {
				return nil, err
			}
		}
		if len(n.Children().Nodes) > 0 {
			return nil, fmt.Errorf("mcp-beaver: `forward-header` takes no children (fail-closed)")
		}
		cfg[name] = cfg[name] || required
	}
	return cfg, nil
}

// reservedForwardHeaders are headers a caller must never steer upstream: a
// credential, a cookie, or one the transport or runtime owns.
var reservedForwardHeaders = map[string]string{
	"authorization":        "`auth` owns it, and forwarding a caller's credential upstream is a confused deputy",
	"proxy-authorization":  "it is a credential",
	"cookie":               "it is a credential",
	"host":                 "the runtime sets it from base-url",
	"content-type":         "the runtime sets it from the request body",
	"content-length":       "the runtime sets it from the request body",
	"traceparent":          "telemetry owns trace propagation",
	"mcp-session-id":       "it belongs to this hop's MCP session, not the upstream's",
	"mcp-protocol-version": "it belongs to this hop's MCP session, not the upstream's",
}

type inboundHeadersKey struct{}

type forwardedHeadersKey struct{}

// withInboundHeaders carries a caller's HTTP headers to a tool handler on a
// path that has no MCP request extra, which is the direct HTTP tool API.
func withInboundHeaders(ctx context.Context, header http.Header) context.Context {
	return context.WithValue(ctx, inboundHeadersKey{}, header)
}

// withForwardHeaders picks the declared headers off the inbound call, refuses
// a call missing a required one before it can spend anything, and hands the
// rest to forwardTransport through the context.
func withForwardHeaders(cfg forwardHeaderConfig, next mcp.ToolHandler) mcp.ToolHandler {
	names := make([]string, 0, len(cfg))
	for name := range cfg {
		names = append(names, name)
	}
	sort.Strings(names)
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		inbound, _ := ctx.Value(inboundHeadersKey{}).(http.Header)
		if req != nil && req.Extra != nil && req.Extra.Header != nil {
			inbound = req.Extra.Header
		}
		forwarded := map[string]string{}
		for _, name := range names {
			if value := inbound.Get(name); value != "" {
				forwarded[name] = value
			} else if cfg[name] {
				return toolError(fmt.Errorf(
					"mcp-beaver: this server requires the %s header on every call, and this call carried none. "+
						"Send it from the MCP client's configuration", name)), nil
			}
		}
		if len(forwarded) > 0 {
			ctx = context.WithValue(ctx, forwardedHeadersKey{}, forwarded)
		}
		return next(ctx, req)
	}
}

// withForwardTransport returns a copy of client whose requests carry the
// forwarded headers of the call that made them. Copying keeps the runtime's
// redirect guard and timeout.
func withForwardTransport(client *http.Client) *http.Client {
	wrapped := *client
	base := client.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	wrapped.Transport = forwardTransport{base: base}
	return &wrapped
}

type forwardTransport struct {
	base http.RoundTripper
}

func (t forwardTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	forwarded, _ := req.Context().Value(forwardedHeadersKey{}).(map[string]string)
	if len(forwarded) == 0 {
		return t.base.RoundTrip(req)
	}
	// A RoundTripper may not mutate the request it is handed.
	out := req.Clone(req.Context())
	for name, value := range forwarded {
		out.Header.Set(name, value)
	}
	return t.base.RoundTrip(out)
}
