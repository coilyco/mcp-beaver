package mcpserver

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"unicode"

	"github.com/coilyco/umbra/http/mcpverb"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// DefaultRegistry is the official MCP registry, which answers unauthenticated,
// ships an OpenAPI spec, and verifies publisher identity by GitHub OIDC or DNS
// ownership, so a reverse-DNS name there is a trust signal (mcp-beaver#119).
const DefaultRegistry = "https://registry.modelcontextprotocol.io"

// registryStatusActive is the one lifecycle status a guardfile is generated
// from. A deprecated or deleted entry still answers, and quietly wrapping one
// would hand a consumer a surface its publisher has walked away from.
const registryStatusActive = "active"

// Scope selects which upstream tools a generated guardfile allows. It is an
// evidence axis before a capability one: each step is an ordered subset of
// the next, so widening adds lines without moving any.
type Scope string

const (
	// ScopeReadOnly allows tools the upstream declares `readOnlyHint: true`.
	// The default, and the only scope a proxy should deploy unread.
	ScopeReadOnly Scope = "read-only"
	// ScopeReadWrite allows tools declaring the hint either way: reads and
	// writes, both asserted by the author.
	ScopeReadWrite Scope = "read-write"
	// ScopeAll adds the tools that declare nothing, as their own step rather
	// than riding along inside a word.
	ScopeAll Scope = "all"
)

// ParseScope reads the `--scope` flag.
func ParseScope(raw string) (Scope, error) {
	switch Scope(raw) {
	case ScopeReadOnly, ScopeReadWrite, ScopeAll:
		return Scope(raw), nil
	case "":
		return ScopeReadOnly, nil
	}
	return "", fmt.Errorf("mcp-beaver: unknown scope %q (want read-only, read-write, all)", raw)
}

// PulledTool is one upstream tool as `tools/list` served it.
type PulledTool struct {
	Name        string
	Description string
	// ReadOnly is the upstream's own readOnlyHint, and nil when it declared
	// none. The distinction is the whole allow rule, and the SDK's typed view
	// folds an absent hint into false, which is why the raw list is read too.
	ReadOnly *bool
}

// Pulled is a registry entry joined to the tool surface it actually serves.
type Pulled struct {
	Name        string
	URL         string
	Description string
	// Tools is in the upstream's own `tools/list` order.
	Tools []PulledTool
	// Headers is the credential the probe presented, so the guardfile can
	// state the same one.
	Headers []UpstreamHeader
}

// PullOptions carries the optional inputs of a pull.
type PullOptions struct {
	// Registry is the base URL of the registry. Empty means DefaultRegistry.
	Registry string
	// Upstream skips the registry lookup and connects here. The name is then
	// whatever the caller says it is.
	Upstream string
	// Headers are presented to the upstream, for the half of the registry
	// that refuses anonymously.
	Headers []UpstreamHeader
	// Providers is the value registry the headers resolve through.
	Providers ProviderSet
	// HTTPClient overrides the default client, for tests.
	HTTPClient *http.Client
}

// Pull resolves a registry name to its remote, connects, and records the
// tool surface with each tool's own annotation. It reads and never calls: the
// hint is self-declared, and nothing here checks it by invoking a tool.
func Pull(ctx context.Context, name string, opts PullOptions) (*Pulled, error) {
	if strings.TrimSpace(name) == "" {
		return nil, fmt.Errorf("mcp-beaver: pull needs a registry name, e.g. `pull ac.tandem/docs-mcp`")
	}
	if err := ValidateUpstreamHeaders(opts.Headers); err != nil {
		return nil, err
	}
	base := boundedUpstreamClient(opts.HTTPClient)
	out := &Pulled{Name: name, URL: opts.Upstream, Headers: opts.Headers}
	if out.URL == "" {
		entry, err := lookupRegistry(ctx, base, opts.Registry, name)
		if err != nil {
			return nil, err
		}
		out.URL, out.Description = entry.url, entry.description
	}
	tools, err := probeUpstream(ctx, out.URL, withUpstreamHeaders(base, opts.Headers, opts.Providers))
	if err != nil {
		return nil, err
	}
	out.Tools = tools
	return out, nil
}

type registryEntry struct {
	url         string
	description string
}

// lookupRegistry reads `/v0/servers/{name}/versions/latest` and takes the
// first streamable-HTTP remote. A `packages` entry with no remote is out of
// scope: it would have to be installed and run, and nothing here does either.
func lookupRegistry(ctx context.Context, client *http.Client, registry, name string) (registryEntry, error) {
	if registry == "" {
		registry = DefaultRegistry
	}
	endpoint := strings.TrimRight(registry, "/") + "/v0/servers/" + url.PathEscape(name) + "/versions/latest"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return registryEntry{}, fmt.Errorf("mcp-beaver: registry request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return registryEntry{}, fmt.Errorf("mcp-beaver: registry %s: %w", endpoint, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return registryEntry{}, fmt.Errorf("mcp-beaver: registry has no server named %q", name)
	}
	if resp.StatusCode != http.StatusOK {
		return registryEntry{}, fmt.Errorf("mcp-beaver: registry %s answered HTTP %d", endpoint, resp.StatusCode)
	}
	var body struct {
		Server struct {
			Name        string `json:"name"`
			Description string `json:"description"`
			Remotes     []struct {
				Type string `json:"type"`
				URL  string `json:"url"`
			} `json:"remotes"`
		} `json:"server"`
		Meta struct {
			Official struct {
				Status string `json:"status"`
			} `json:"io.modelcontextprotocol.registry/official"`
		} `json:"_meta"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxSpecBytes)).Decode(&body); err != nil {
		return registryEntry{}, fmt.Errorf("mcp-beaver: registry entry for %q does not parse: %w", name, err)
	}
	if body.Meta.Official.Status != registryStatusActive {
		return registryEntry{}, fmt.Errorf("mcp-beaver: registry entry %q is %q, not %s", name, body.Meta.Official.Status, registryStatusActive)
	}
	for _, remote := range body.Server.Remotes {
		if remote.Type == mcpverb.UpstreamTransport && remote.URL != "" {
			return registryEntry{url: remote.URL, description: strings.TrimSpace(body.Server.Description)}, nil
		}
	}
	return registryEntry{}, fmt.Errorf("mcp-beaver: registry entry %q publishes no %s remote; a packages-only server is out of scope", name, mcpverb.UpstreamTransport)
}

// validateUpstreamURL refuses an endpoint the proxy could not dial. umbra runs
// the same check inside `mcp-upstream`; this covers the `--upstream` flag,
// which reaches no guardfile.
func validateUpstreamURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("mcp-beaver: `url %q` does not parse: %w", raw, err)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("mcp-beaver: `url %q` must be an absolute http or https URL", raw)
	}
	return nil
}

// probeUpstream connects the way the proxy connects and lists tools once,
// retrying one empty answer. Not optional: one registry server answered zero
// tools and then 25 on the same session shape, so a single reading produces
// false negatives.
func probeUpstream(ctx context.Context, endpoint string, client *http.Client) ([]PulledTool, error) {
	if err := validateUpstreamURL(endpoint); err != nil {
		return nil, err
	}
	capture := &rawToolsCapture{base: client.Transport}
	probing := *client
	probing.Transport = capture
	ctx, cancel := upstreamContext(ctx)
	defer cancel()
	session, err := mcp.NewClient(&mcp.Implementation{Name: "mcp-beaver-pull", Version: Version}, nil).
		Connect(ctx, &mcp.StreamableClientTransport{Endpoint: endpoint, HTTPClient: &probing}, nil)
	if err != nil {
		return nil, fmt.Errorf("mcp-beaver: connect upstream %q: %w", endpoint, err)
	}
	defer func() { _ = session.Close() }()
	var listed []*mcp.Tool
	for attempt := 0; attempt < 2; attempt++ {
		res, err := session.ListTools(ctx, nil)
		if err != nil {
			return nil, fmt.Errorf("mcp-beaver: list upstream tools: %w", err)
		}
		listed = res.Tools
		if len(listed) > 0 {
			break
		}
	}
	hints := capture.readOnlyHints()
	out := make([]PulledTool, 0, len(listed))
	for _, tool := range listed {
		if tool == nil {
			continue
		}
		out = append(out, PulledTool{
			Name:        tool.Name,
			Description: tool.Description,
			ReadOnly:    hints[tool.Name],
		})
	}
	return out, nil
}

// rawToolsCapture tees every `tools/list` response body the SDK reads, so the
// upstream's own JSON can answer whether `readOnlyHint` was present at all.
// The SDK reads the stream as it goes and the copy fills alongside it, so a
// stream the server holds open is never drained here.
type rawToolsCapture struct {
	base http.RoundTripper
	mu   sync.Mutex
	raw  []*bytes.Buffer
}

func (c *rawToolsCapture) RoundTrip(req *http.Request) (*http.Response, error) {
	base := c.base
	if base == nil {
		base = http.DefaultTransport
	}
	isList := false
	if req.Body != nil && req.Method == http.MethodPost {
		body, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		_ = req.Body.Close()
		req.Body = io.NopCloser(bytes.NewReader(body))
		isList = bytes.Contains(body, []byte(`"tools/list"`))
	}
	resp, err := base.RoundTrip(req)
	if err != nil || !isList || resp.Body == nil {
		return resp, err
	}
	buf := &bytes.Buffer{}
	c.mu.Lock()
	c.raw = append(c.raw, buf)
	c.mu.Unlock()
	resp.Body = teeCloser{Reader: io.TeeReader(resp.Body, buf), closer: resp.Body}
	return resp, nil
}

type teeCloser struct {
	io.Reader
	closer io.Closer
}

func (t teeCloser) Close() error { return t.closer.Close() }

// readOnlyHints maps tool name to its declared hint, absent when the tool
// declared none. The latest list wins, matching the retry.
func (c *rawToolsCapture) readOnlyHints() map[string]*bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := map[string]*bool{}
	for _, buf := range c.raw {
		for _, message := range jsonRPCMessages(buf.Bytes()) {
			var body struct {
				Result struct {
					Tools []struct {
						Name        string                     `json:"name"`
						Annotations map[string]json.RawMessage `json:"annotations"`
					} `json:"tools"`
				} `json:"result"`
			}
			if json.Unmarshal(message, &body) != nil || len(body.Result.Tools) == 0 {
				continue
			}
			for _, tool := range body.Result.Tools {
				raw, declared := tool.Annotations["readOnlyHint"]
				if !declared {
					out[tool.Name] = nil
					continue
				}
				var hint bool
				if json.Unmarshal(raw, &hint) != nil {
					out[tool.Name] = nil
					continue
				}
				out[tool.Name] = &hint
			}
		}
	}
	return out
}

// jsonRPCMessages splits a streamable-HTTP body into its JSON messages: the
// whole body when it is JSON, or one message per SSE event otherwise, with an
// event's `data:` lines joined the way the SSE grammar says.
func jsonRPCMessages(body []byte) [][]byte {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) > 0 && trimmed[0] == '{' {
		return [][]byte{trimmed}
	}
	var out [][]byte
	var event []string
	flush := func() {
		if len(event) > 0 {
			out = append(out, []byte(strings.Join(event, "\n")))
			event = nil
		}
	}
	scanner := bufio.NewScanner(bytes.NewReader(body))
	scanner.Buffer(make([]byte, 0, 64*1024), maxSpecBytes)
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case line == "":
			flush()
		case strings.HasPrefix(line, "data:"):
			event = append(event, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	flush()
	return out
}

// Coverage counts the declared and silent tools, the marker every generated
// guardfile carries. A server with no tools is undeclared: nothing asserted.
func (p *Pulled) Coverage() AnnotationCoverage {
	var out AnnotationCoverage
	for _, tool := range p.Tools {
		if tool.ReadOnly == nil {
			out.Silent++
		} else {
			out.Annotated++
		}
	}
	switch {
	case out.Annotated == 0:
		out.Kind = "undeclared"
	case out.Silent == 0:
		out.Kind = "declared"
	default:
		out.Kind = "partial"
	}
	return out
}

// Select returns the tools a scope allows, in upstream order.
func (p *Pulled) Select(scope Scope) []PulledTool {
	var out []PulledTool
	for _, tool := range p.Tools {
		switch scope {
		case ScopeReadOnly:
			if tool.ReadOnly == nil || !*tool.ReadOnly {
				continue
			}
		case ScopeReadWrite:
			if tool.ReadOnly == nil {
				continue
			}
		}
		out = append(out, tool)
	}
	return out
}

// maxPulledInstructions bounds the description a registry entry contributes,
// well under the budget `instructions` enforces, so a verbose publisher never
// makes the generated file fail its own lint.
const maxPulledInstructions = 400

// RenderUpstreamGuardfile writes the guardfile for a pull at one scope.
//
// Two things it refuses to do, each measured (mcp-beaver#119). No `withhold`
// block per excluded tool: a stub is discoverable, and 112 of them made 54%
// of an advertised surface refuse every call. No guessing: a tool declaring
// nothing is absent under read-only and read-write, and the marker says so.
func RenderUpstreamGuardfile(p *Pulled, scope Scope) (string, error) {
	coverage := p.Coverage()
	selected := p.Select(scope)
	var mutating int
	for _, tool := range p.Tools {
		if tool.ReadOnly != nil && !*tool.ReadOnly {
			mutating++
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "// %s\n", p.Name)
	b.WriteString("// Generated by `mcp-beaver pull` from the MCP registry and a live tools/list.\n")
	fmt.Fprintf(&b, "// %d tools: %d declared read-only, %d declared mutating, %d undeclared.\n",
		len(p.Tools), coverage.Annotated-mutating, mutating, coverage.Silent)
	fmt.Fprintf(&b, "// Scope %s exposes %d. Absent tools are denied without being named.\n", scope, len(selected))
	if text := instructionsText(p.Description); text != "" {
		fmt.Fprintf(&b, "\ninstructions {\n    text %s\n}\n", kdlQuote(text))
	}
	fmt.Fprintf(&b, "\n%s %s {\n", mcpverb.UpstreamNode, kdlQuote(p.Name))
	fmt.Fprintf(&b, "    url %s\n", kdlQuote(p.URL))
	fmt.Fprintf(&b, "    transport %s\n", mcpverb.UpstreamTransport)
	fmt.Fprintf(&b, "    annotation-coverage %s annotated=%d silent=%d\n", coverage.Kind, coverage.Annotated, coverage.Silent)
	if len(p.Headers) > 0 {
		auth, err := renderUpstreamAuth(p.Headers)
		if err != nil {
			return "", err
		}
		b.WriteString("\n" + auth)
	}
	if len(selected) > 0 {
		b.WriteString("\n")
	}
	for _, tool := range selected {
		fmt.Fprintf(&b, "    can %s\n", kdlQuote(tool.Name))
	}
	b.WriteString("}\n")
	return b.String(), nil
}

// renderUpstreamAuth writes the header back as `auth header-token`. Only a
// `<prefix>{provider:address}` template has that shape; anything else is a
// flag the file cannot carry, and saying so beats emitting a lie.
func renderUpstreamAuth(headers []UpstreamHeader) (string, error) {
	if len(headers) != 1 {
		return "", fmt.Errorf("mcp-beaver: a guardfile states one `auth header-token`, and the pull presented %d headers", len(headers))
	}
	h := headers[0]
	var prefix strings.Builder
	var provider, address string
	for i, segment := range h.segments {
		if segment.Provider == "" {
			prefix.WriteString(segment.Literal)
			continue
		}
		if i != len(h.segments)-1 || provider != "" {
			return "", fmt.Errorf("mcp-beaver: header %q is not `<prefix>{provider:address}`, which is the shape `auth header-token` can state", h.Name)
		}
		provider, address = segment.Provider, segment.Address
	}
	var b strings.Builder
	b.WriteString("    auth header-token {\n")
	fmt.Fprintf(&b, "        header %s\n", kdlQuote(h.Name))
	if prefix.Len() > 0 {
		fmt.Fprintf(&b, "        prefix %s\n", kdlQuote(prefix.String()))
	}
	fmt.Fprintf(&b, "        value %s %s\n", provider, kdlQuote(address))
	b.WriteString("    }\n")
	return b.String(), nil
}

// instructionsText folds a registry description onto one line and cuts it
// at a word boundary inside the budget.
func instructionsText(description string) string {
	text := strings.Join(strings.Fields(description), " ")
	if len(text) <= maxPulledInstructions {
		return text
	}
	cut := strings.LastIndex(text[:maxPulledInstructions], " ")
	if cut <= 0 {
		cut = maxPulledInstructions
	}
	return strings.TrimRight(text[:cut], " .,;:") + "..."
}

// kdlQuote renders a KDL string literal. Always quoted: a bare identifier is
// legal for most tool names and a formatter may strip the quotes, but a name
// that happens to spell a keyword or a number would silently change kind.
func kdlQuote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if unicode.IsControl(r) {
				fmt.Fprintf(&b, `\u{%x}`, r)
				continue
			}
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}
