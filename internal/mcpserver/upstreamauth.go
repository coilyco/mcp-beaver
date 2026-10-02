package mcpserver

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/coilyco/umbra/pkg/valuesource"
)

// UpstreamHeader is one request header the proxy presents to its upstream, with
// the secret part resolved server-side rather than written into the flag.
//
// `serve-upstream` could not authenticate at all before this. Every upstream on
// the fleet was an unauthenticated loopback sidecar, so the proxy's only reach
// was inside its own pod and a credential had nowhere to go. A hosted
// third-party MCP is the first upstream that asks for one (deploy#647), and
// spec mode already answered the same question with `auth header-token`. This
// is that answer for the mode that forwards MCP instead of building requests.
//
// Spec mode's `value <provider> "<address>"` triple is the grammar here too,
// through the same umbra registry, so a header reads the same whichever mode
// carries it.
type UpstreamHeader struct {
	// Name is the outgoing header, canonicalized on parse.
	Name string
	// segments alternate literal text and value sources, in template order.
	segments []headerSegment
}

// headerSegment is one piece of a header value: literal text, or a value read
// through a provider at call time. Source is empty for a literal piece.
type headerSegment struct {
	Literal  string
	Provider string
	Address  string
}

// upstreamHeaderTemplate is the `--upstream-header` value grammar:
//
//	Authorization=Bearer {env:MOXN_TOKEN}
//
// A `{provider:address}` span resolves through umbra's value registry (env,
// file, literal) and everything outside a span is literal text, so any prefix
// shape - `Bearer `, `token `, none - falls out without its own flag.
//
// At least one span is REQUIRED, and that is the point of the grammar rather
// than a parser convenience. A template with no span means the whole header
// value sits in argv, visible in `ps`, in the pod spec, and in any values file
// that sets it. Requiring a span means an operator who genuinely wants a
// constant writes `{literal:...}` and has said so on purpose.
const (
	upstreamHeaderOpen  = '{'
	upstreamHeaderClose = '}'
)

// ParseUpstreamHeader reads the `<name>=<template>` CLI form.
func ParseUpstreamHeader(raw string, providers ProviderSet) (UpstreamHeader, error) {
	name, template, found := strings.Cut(raw, "=")
	if !found {
		return UpstreamHeader{}, fmt.Errorf("mcp-beaver: upstream header %q must be <name>=<value template>, e.g. 'Authorization=Bearer {env:MOXN_TOKEN}'", raw)
	}
	name = strings.TrimSpace(name)
	if err := validateHeaderName(name, raw); err != nil {
		return UpstreamHeader{}, err
	}
	segments, err := parseHeaderTemplate(template, name, providers)
	if err != nil {
		return UpstreamHeader{}, err
	}
	return UpstreamHeader{Name: http.CanonicalHeaderKey(name), segments: segments}, nil
}

func validateHeaderName(name, raw string) error {
	if name == "" {
		return fmt.Errorf("mcp-beaver: upstream header %q names an empty header", raw)
	}
	if strings.ContainsAny(name, " \t:\r\n") {
		return fmt.Errorf("mcp-beaver: upstream header name %q may not contain whitespace or a colon", name)
	}
	return nil
}

// parseHeaderTemplate splits the template into literal and value-source
// segments. Braces are the only metacharacters and they do not nest.
func parseHeaderTemplate(template, name string, providers ProviderSet) ([]headerSegment, error) {
	if strings.ContainsAny(template, "\r\n") {
		return nil, fmt.Errorf("mcp-beaver: upstream header %q value may not contain a newline", name)
	}
	var (
		segments []headerSegment
		literal  strings.Builder
		sources  int
	)
	for i := 0; i < len(template); i++ {
		switch template[i] {
		case upstreamHeaderClose:
			return nil, fmt.Errorf("mcp-beaver: upstream header %q has a %q with no opening %q", name, string(upstreamHeaderClose), string(upstreamHeaderOpen))
		case upstreamHeaderOpen:
			end := strings.IndexByte(template[i:], upstreamHeaderClose)
			if end < 0 {
				return nil, fmt.Errorf("mcp-beaver: upstream header %q has an unclosed %q", name, string(upstreamHeaderOpen))
			}
			segment, err := parseHeaderSource(template[i+1:i+end], name, providers)
			if err != nil {
				return nil, err
			}
			if literal.Len() > 0 {
				segments = append(segments, headerSegment{Literal: literal.String()})
				literal.Reset()
			}
			segments = append(segments, segment)
			sources++
			i += end
		default:
			literal.WriteByte(template[i])
		}
	}
	if literal.Len() > 0 {
		segments = append(segments, headerSegment{Literal: literal.String()})
	}
	if sources == 0 {
		return nil, fmt.Errorf("mcp-beaver: upstream header %q states no {provider:address} value; a literal header value would put it in argv, so say `{literal:...}` if that is genuinely wanted", name)
	}
	return segments, nil
}

func parseHeaderSource(body, name string, providers ProviderSet) (headerSegment, error) {
	provider, address, found := strings.Cut(body, ":")
	if !found {
		return headerSegment{}, fmt.Errorf("mcp-beaver: upstream header %q: {%s} must be {<provider>:<address>}, e.g. {env:MOXN_TOKEN}", name, body)
	}
	provider, address = strings.TrimSpace(provider), strings.TrimSpace(address)
	if err := providers.checkSource(provider, address); err != nil {
		return headerSegment{}, fmt.Errorf("mcp-beaver: upstream header %q %w", name, err)
	}
	if address == "" {
		return headerSegment{}, fmt.Errorf("mcp-beaver: upstream header %q: provider %q has an empty address", name, provider)
	}
	return headerSegment{Provider: provider, Address: address}, nil
}

// ValidateUpstreamHeaders rejects a set that names one header twice. Offline,
// so `lint-upstream` runs it in CI where no secret is reachable.
func ValidateUpstreamHeaders(headers []UpstreamHeader) error {
	seen := make(map[string]bool, len(headers))
	for _, h := range headers {
		if h.Name == "" || len(h.segments) == 0 {
			return fmt.Errorf("mcp-beaver: upstream header is unparsed; build it with ParseUpstreamHeader")
		}
		if seen[h.Name] {
			return fmt.Errorf("mcp-beaver: upstream header %q is set twice; the second would silently win", h.Name)
		}
		seen[h.Name] = true
	}
	return nil
}

// PreflightUpstreamHeaders resolves every header once and discards the values,
// so a missing secret fails at startup rather than inside the connect retry.
//
// Without it a `--connect-timeout 2m` deployment spends two minutes retrying
// what is a configuration error, and reports it as an unreachable upstream.
func PreflightUpstreamHeaders(ctx context.Context, headers []UpstreamHeader, providers ProviderSet) error {
	for _, h := range headers {
		if _, err := h.resolve(ctx, providers); err != nil {
			return err
		}
	}
	return nil
}

// resolve reads each source at CALL time, matching how spec-mode `auth` and
// query pins resolve: nothing is baked into the image, and a rotated secret
// takes effect on the next call rather than the next restart.
//
// No error path carries the resolved value. An error names the header and the
// address it failed to read, both of which are already in the flag.
func (h UpstreamHeader) resolve(ctx context.Context, providers ProviderSet) (string, error) {
	var out strings.Builder
	for _, segment := range h.segments {
		if segment.Provider == "" {
			out.WriteString(segment.Literal)
			continue
		}
		value, err := valuesource.Resolve(ctx, providers.registry(), segment.Provider, segment.Address)
		if err != nil {
			return "", fmt.Errorf("mcp-beaver: resolve upstream header %q from %s %q: %w", h.Name, segment.Provider, segment.Address, err)
		}
		if strings.ContainsAny(value, "\r\n") {
			return "", fmt.Errorf("mcp-beaver: upstream header %q resolved to a value containing a newline", h.Name)
		}
		out.WriteString(value)
	}
	return out.String(), nil
}

// upstreamHeaderTransport presents the configured headers on every upstream
// request, which covers the initial dial, each reconnect, and every tool call
// without any of them knowing about it.
type upstreamHeaderTransport struct {
	base      http.RoundTripper
	headers   []UpstreamHeader
	providers ProviderSet
}

func (t *upstreamHeaderTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// A RoundTripper may not mutate the request it is handed.
	out := req.Clone(req.Context())
	for _, h := range t.headers {
		value, err := h.resolve(req.Context(), t.providers)
		if err != nil {
			return nil, err
		}
		out.Header.Set(h.Name, value)
	}
	return t.base.RoundTrip(out)
}

// withUpstreamHeaders layers header presentation over an already-bounded
// client. Order matters: boundedTransport only recognizes an *http.Transport
// and leaves anything else alone, so wrapping first would silently drop the
// time-to-first-byte bound that #79 exists to keep.
func withUpstreamHeaders(client *http.Client, headers []UpstreamHeader, providers ProviderSet) *http.Client {
	if len(headers) == 0 {
		return client
	}
	base := client.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	out := *client
	out.Transport = &upstreamHeaderTransport{base: base, headers: headers, providers: providers}
	return &out
}
