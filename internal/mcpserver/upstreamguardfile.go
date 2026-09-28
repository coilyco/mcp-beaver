package mcpserver

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"forgejo.coilysiren.me/coilyco-flight-deck/umbra/http/guardfile"
	"forgejo.coilysiren.me/coilyco-flight-deck/umbra/http/mcpverb"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// AnnotationCoverage is umbra's marker, aliased so the renderer below and the
// parsed guardfile name one type rather than two that must be kept equal.
type AnnotationCoverage = mcpverb.AnnotationCoverage

// UpstreamSpec is an `mcp-upstream` guardfile with the parts this server
// projects resolved beside it. umbra owns the node's grammar; beaver owns the
// value registry a credential addresses, the headers the proxy presents, and
// which siblings it can honour. See docs/upstream.md.
type UpstreamSpec struct {
	// Upstream is umbra's parse: name, url, transport, coverage, auth, and the
	// allowlist in stated order.
	*mcpverb.Upstream
	// Headers is the credential `auth` presents, lifted onto the template
	// `serve-upstream --upstream-header` already takes.
	Headers []UpstreamHeader
	// Providers is the value registry the headers resolve through.
	Providers ProviderSet
	// Instructions is the sibling `instructions` text, empty when absent.
	Instructions string
	// Withheld is the sibling `withhold` stubs, validated against the
	// allowlist above so a stub cannot shadow a granted tool.
	Withheld []withheldStub
	// Icons is the sibling `icon` nodes, for `serverInfo.icons`.
	Icons []mcp.Icon
	// ServerInfo is the sibling `server-info` node, nil when it opts out.
	ServerInfo *serverInfoConfig
	// Confirmations is the sibling `confirm` nodes, keyed by tool name.
	Confirmations confirmConfig
	// Pins is the sibling `pin` nodes as argument pins, the same shape
	// `serve-upstream --pin` states.
	Pins []ArgPin
	// RateLimit is the sibling `rate-limit` node, nil when absent.
	RateLimit *rateLimitConfig
}

// WithheldTools names the stub tools this guardfile states, sorted. `lint`
// prints them beside the allowlist, because a stub is a served tool: absent
// from it, the printed surface is narrower than the one the proxy mints.
func (u *UpstreamSpec) WithheldTools() []string {
	out := make([]string, 0, len(u.Withheld))
	for _, stub := range u.Withheld {
		out = append(out, stub.tool)
	}
	sort.Strings(out)
	return out
}

// ServerInfoTool names the info tool this guardfile mints, empty when
// `server-info disabled` opts out. `lint` prints it beside the allowlist,
// because it is a served tool like any other.
func (u *UpstreamSpec) ServerInfoTool() string {
	if u.ServerInfo == nil {
		return ""
	}
	return u.ServerInfo.toolName
}

// Options is the proxy configuration this guardfile stands for.
func (u *UpstreamSpec) Options() ProxyOptions {
	return ProxyOptions{
		Headers:       u.Headers,
		Providers:     u.Providers,
		Instructions:  u.Instructions,
		Withheld:      u.Withheld,
		Icons:         u.Icons,
		ServerInfo:    u.ServerInfo,
		Confirmations: u.Confirmations,
		Pins:          u.Pins,
		RateLimit:     u.RateLimit,
	}
}

// ClassifyGuardfile reports which shape a guardfile carries, before either
// parser runs.
//
// The normalization is why this is not a bare `mcpverb.Classify`: umbra reads
// strict KDL and the inline wrap body does not, so classifying raw bytes would
// refuse a REST guardfile that spells `required=true` and serves fine today.
func ClassifyGuardfile(src []byte) (mcpverb.Shape, error) {
	return mcpverb.Classify(normalizeInlineBooleans(src))
}

// ParseUpstreamSpec reads an `mcp-upstream` guardfile:
//
//	instructions { text "Search the published Tandem docs index." }
//
//	mcp-upstream "ac.tandem/docs-mcp" {
//	    url "https://tandem.ac/mcp"
//	    transport streamable-http
//	    annotation-coverage partial annotated=7 silent=6
//	    auth header-token { header "Authorization"; prefix "Bearer "; value env "TOKEN" }
//	    can "search_docs"
//	}
//
// The body is umbra's. What happens here is the wiring around it: the oauth2
// clients a credential may address, the headers the proxy will present, and
// the sibling nodes this server projects. Every other sibling fails closed.
func ParseUpstreamSpec(specPath string, src []byte) (*UpstreamSpec, error) {
	up, err := mcpverb.ParseUpstream(normalizeInlineBooleans(src))
	if err != nil {
		return nil, err
	}
	dir := "."
	if specPath != "" {
		dir = filepath.Dir(specPath)
	}
	sources := singleSource(src, dir)
	clients, err := parseOAuth2Clients(sources)
	if err != nil {
		return nil, err
	}
	spec := &UpstreamSpec{Upstream: up}
	spec.Providers, err = NewProviderSet(clients, nil)
	if err != nil {
		return nil, err
	}
	spec.Headers, err = upstreamAuthHeaders(up.Auth, spec.Providers)
	if err != nil {
		return nil, err
	}
	if err := ValidateUpstreamHeaders(spec.Headers); err != nil {
		return nil, err
	}
	if err := rejectUnprojectedSiblings(sources); err != nil {
		return nil, err
	}
	spec.Instructions, err = parseInstructions(sources)
	if err != nil {
		return nil, err
	}
	spec.Withheld, err = parseWithheld(sources)
	if err != nil {
		return nil, err
	}
	spec.Icons, err = parseIcons(sources)
	if err != nil {
		return nil, err
	}
	spec.ServerInfo, err = parseServerInfo(sources)
	if err != nil {
		return nil, err
	}
	spec.Confirmations, err = parseConfirmations(sources)
	if err != nil {
		return nil, err
	}
	spec.Pins, err = parseArgPins(sources, spec.Providers)
	if err != nil {
		return nil, err
	}
	spec.RateLimit, err = parseRateLimit(sources)
	if err != nil {
		return nil, err
	}
	// Against the DECLARED allowlist rather than the upstream's answer, so a
	// control naming a tool nobody serves fails in `lint`, offline, rather
	// than at connect. The proxy checks each again against what it selected.
	declared := declaredTools(up.Tools)
	info, err := serverInfoTool(spec.ServerInfo, declared)
	if err != nil {
		return nil, err
	}
	if info != nil {
		declared = append(declared, info)
	}
	stubs, err := withheldTools(spec.Withheld, declared)
	if err != nil {
		return nil, err
	}
	if err := validateConfirmations(spec.Confirmations, append(declared, stubs...)); err != nil {
		return nil, err
	}
	if err := ValidatePins(spec.Pins, up.Tools); err != nil {
		return nil, err
	}
	return spec, nil
}

// upstreamAuthHeaders lifts the parsed `auth` onto the header template
// `serve-upstream --upstream-header` already takes, so a credential reads the
// same whichever surface stated it and resolves through the same registry.
//
// umbra parses four schemes and this proxy presents headers, so `query-param`
// refuses rather than dropping a secret the file says to send. A value chain
// naming more than one source refuses for the same reason: a header resolves
// exactly one and never falls back.
func upstreamAuthHeaders(auth guardfile.Auth, providers ProviderSet) ([]UpstreamHeader, error) {
	switch auth.Scheme {
	case "", guardfile.AuthSchemeNone:
		return nil, nil
	case "header-token", "bearer":
	case "query-param":
		return nil, fmt.Errorf("mcp-beaver: `auth query-param` is not served by the proxy; it presents headers, and a forwarded MCP request has no query to carry the secret")
	default:
		return nil, fmt.Errorf("mcp-beaver: `auth %s` is not served by the proxy; header-token and bearer are", auth.Scheme)
	}
	if len(auth.Value) != 1 {
		return nil, fmt.Errorf("mcp-beaver: `auth %s` names %d value sources; the proxy resolves one and does not fall back", auth.Scheme, len(auth.Value))
	}
	source := auth.Value[0]
	if strings.ContainsAny(auth.Prefix+source.Provider+source.Address, "{}") {
		return nil, fmt.Errorf("mcp-beaver: `auth` prefix and value may not contain braces")
	}
	header, err := ParseUpstreamHeader(fmt.Sprintf("%s=%s{%s:%s}", auth.Header, auth.Prefix, source.Provider, source.Address), providers)
	if err != nil {
		return nil, err
	}
	return []UpstreamHeader{header}, nil
}

// upstreamSiblings names the sibling nodes projected onto a proxy surface.
// The rest are parsed for a REST guardfile only, and a proxy that silently
// ignored one would serve WIDER than the file reads.
//
// What is absent is what has no proxy meaning rather than what is unfinished:
// `resource`, `prompt` and `app` are content a REST guardfile serves beside
// its grants and a passthrough proxy mints none of, and `cache`, `extract`,
// `reject-empty`, `reject-empty-argument`, `ignore-undeclared-arguments` and
// `set` all shape a request or a response opcore assembles from a grant, which a
// proxy forwards verbatim instead. See docs/upstream-controls.md.
//
// `description` is umbra's own, read by the parse above rather than here.
var upstreamSiblings = map[string]bool{
	"description":   true,
	"instructions":  true,
	"oauth2-client": true,
	"withhold":      true,
	"icon":          true,
	"server-info":   true,
	"confirm":       true,
	"pin":           true,
	"rate-limit":    true,
}

func rejectUnprojectedSiblings(sources []guardSource) error {
	nodes, err := parseInlineNodes(sources, mcpverb.UpstreamNode)
	if err != nil {
		return err
	}
	for _, sn := range nodes {
		if !upstreamSiblings[sn.node.Name()] {
			return fmt.Errorf(
				"mcp-beaver: `%s` is not projected beside `%s`; %s are (fail-closed)",
				sn.node.Name(), mcpverb.UpstreamNode, strings.Join(projectedSiblingNames(), ", "),
			)
		}
	}
	return nil
}

// projectedSiblingNames lists what a refusal may name, read off the map rather
// than restated, so a node added above cannot leave the message stale.
// `description` is left out because umbra parses it rather than this file, so
// naming it here would advertise a node beaver does not own.
func projectedSiblingNames() []string {
	out := make([]string, 0, len(upstreamSiblings))
	for name := range upstreamSiblings {
		if name != "description" {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}
