package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/otel/baggage"
	"go.opentelemetry.io/otel/trace"
)

// upstreamResponseHeaderTimeout bounds how long an upstream may take to START
// answering. It is deliberately NOT an `http.Client.Timeout`.
//
// `Client.Timeout` bounds the whole exchange including reading the body, and a
// streamable-HTTP MCP response IS a body that stays open. So a 45s
// `Client.Timeout` killed any tool call whose stream ran longer - a cold
// Chromium launch takes 45s on its own - and the abort took the upstream
// session with it. Every later call then failed instantly with `session not
// found`, which is how a playwright deployment recorded 0 successes over 24h
// at p95 16.8ms: one slow call poisoned the session, and nothing reconnected
// (mcp-beaver#79).
//
// Time-to-first-byte is the right bound for a stream. A hung upstream still
// fails here, and the per-call deadline in withToolDeadline bounds the rest
// through the request context, which is what #49 actually needed.
const upstreamResponseHeaderTimeout = 45 * time.Second

// boundedUpstreamClient bounds a caller-supplied client's time-to-first-byte
// when it declared no bound of its own. A caller that set one has made a
// deliberate choice, and this must not quietly override it.
func boundedUpstreamClient(client *http.Client) *http.Client {
	if client == nil {
		return &http.Client{Transport: boundedTransport(nil)}
	}
	if client.Timeout != 0 {
		return client
	}
	bounded := *client
	bounded.Transport = boundedTransport(client.Transport)
	return &bounded
}

// boundedTransport copies a transport and bounds its response-header wait.
// Anything that is not an *http.Transport is left alone: it is a caller's own
// round tripper, and reaching into it would be guessing.
func boundedTransport(rt http.RoundTripper) http.RoundTripper {
	base, ok := rt.(*http.Transport)
	if rt == nil {
		base, ok = http.DefaultTransport.(*http.Transport)
	}
	if !ok || base == nil {
		return rt
	}
	bounded := base.Clone()
	if bounded.ResponseHeaderTimeout == 0 {
		bounded.ResponseHeaderTimeout = upstreamResponseHeaderTimeout
	}
	return bounded
}

// The dial inputs are held so a lost session can be replaced. They were dropped
// when the per-call second session went away (#67), on the reasoning that
// nothing redials - which was true until a dead session turned out to brick the
// pod for 24 hours (#79).
type proxyBackend struct {
	mu         sync.Mutex
	endpoint   string
	httpClient *http.Client
	telemetry  *instrumentation
	session    *mcp.ClientSession
	allowlist  []string
	baseline   map[string]string
	selected   []*mcp.Tool
	driftErr   error
	closed     bool
}

func newProxyBackend(ctx context.Context, upstreamURL string, allowTools []string, headers []UpstreamHeader, providers ProviderSet, httpClient *http.Client, telemetry *instrumentation) (*proxyBackend, error) {
	if strings.TrimSpace(upstreamURL) == "" {
		return nil, fmt.Errorf("mcp-beaver: upstream endpoint is empty")
	}
	allowlist, err := ValidateAllowlist(allowTools)
	if err != nil {
		return nil, err
	}

	p := &proxyBackend{
		endpoint:   upstreamURL,
		httpClient: withUpstreamDiagnostics(withUpstreamHeaders(boundedUpstreamClient(httpClient), headers, providers)),
		telemetry:  telemetry,
		allowlist:  allowlist,
		baseline:   map[string]string{},
	}
	session, err := p.dial(ctx)
	if err != nil {
		return nil, fmt.Errorf("connect upstream MCP %q: %w", upstreamURL, err)
	}
	p.session = session
	if err := p.snapshot(ctx); err != nil {
		_ = session.Close()
		return nil, err
	}
	return p, nil
}

// upstreamContext detaches a caller's request values from the upstream call,
// keeping its deadline, cancellation, and trace. See docs/upstream.md.
func upstreamContext(ctx context.Context) (context.Context, context.CancelFunc) {
	// The span and the baggage are carried across deliberately. Everything else
	// belongs to the caller's session rather than to this one.
	detached := trace.ContextWithSpan(context.Background(), trace.SpanFromContext(ctx))
	detached = baggage.ContextWithBaggage(detached, baggage.FromContext(ctx))
	var out context.Context
	var cancel context.CancelFunc
	if deadline, ok := ctx.Deadline(); ok {
		out, cancel = context.WithDeadline(detached, deadline)
	} else {
		out, cancel = context.WithCancel(detached)
	}
	// The caller's cancellation still reaches the upstream, only its values do not.
	stop := context.AfterFunc(ctx, cancel)
	return out, func() { stop(); cancel() }
}

// dial opens one session to the upstream. It holds no lock, so a reconnect can
// run without blocking a concurrent call that is only reading the baseline.
func (p *proxyBackend) dial(ctx context.Context) (*mcp.ClientSession, error) {
	ctx, cancel := upstreamContext(ctx)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "mcp-beaver", Version: Version}, nil)
	client.AddSendingMiddleware(p.telemetry.clientMiddleware)
	// The standalone SSE stream stays on. An upstream may deliver a tools/call
	// result over it, and with no stream that call hangs. See #80.
	return client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:   p.endpoint,
		HTTPClient: p.httpClient,
	}, nil)
}

// reconnect replaces a session the upstream has stopped honouring.
//
// Deliberately NOT a retry of the failing call: a tool call may have already
// reached the upstream and had its answer lost, and replaying it would turn a
// timeout into a duplicate action. The failing call still fails, and the next
// one gets a live session - which is the difference between one bad minute and
// the 24-hour outage in #79.
//
// The baseline is NOT re-snapshotted. Re-reading it would adopt whatever the
// upstream serves now as the reviewed contract, which is exactly the drift the
// check exists to catch.
func (p *proxyBackend) reconnect(ctx context.Context) error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return fmt.Errorf("mcp-beaver: upstream MCP session is closed")
	}
	stale := p.session
	p.session = nil
	p.mu.Unlock()

	if stale != nil {
		_ = stale.Close()
	}
	session, err := p.dial(ctx)
	if err != nil {
		return fmt.Errorf("reconnect upstream MCP %q: %w", p.endpoint, err)
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		_ = session.Close()
		return fmt.Errorf("mcp-beaver: upstream MCP session is closed")
	}
	p.session = session
	return nil
}

func (p *proxyBackend) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed = true
	if p.session == nil {
		return nil
	}
	err := p.session.Close()
	p.session = nil
	return err
}

func (p *proxyBackend) selectedTools() []*mcp.Tool {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]*mcp.Tool, 0, len(p.selected))
	for _, tool := range p.selected {
		out = append(out, cloneTool(tool))
	}
	return out
}

func (p *proxyBackend) toolHandler(name string) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if err := p.ensureFreshOrReconnect(ctx, name); err != nil {
			return toolError(err), nil
		}
		session, err := p.currentSession()
		if err != nil {
			return toolError(err), nil
		}
		args := map[string]any{}
		if len(req.Params.Arguments) > 0 {
			if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
				return toolError(fmt.Errorf("invalid tool arguments: %w", err)), nil
			}
		}
		// The proxy forwards the argument map verbatim, so an argument the
		// upstream does not declare would reach it and be ignored there. Refuse
		// it here instead: this is the layer that knows the declared surface.
		if declared, closed := p.declaredArgs(name); closed {
			if unknown := undeclaredArgs(declared, args); len(unknown) > 0 {
				return toolError(undeclaredArgError(name, unknown, declared)), nil
			}
		}
		upCtx, cancel := upstreamContext(ctx)
		defer cancel()
		resp, err := session.CallTool(upCtx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			return toolError(err), nil
		}
		return resp, nil
	}
}

// declaredArgs reads the selected tool's declared argument names off the
// snapshot taken at startup, so the answer comes from the contract this runtime
// accepted rather than from whatever the upstream is advertising right now.
func (p *proxyBackend) declaredArgs(name string) (map[string]bool, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, tool := range p.selected {
		if tool.Name == name {
			return declaredProperties(tool.InputSchema)
		}
	}
	return nil, false
}

// ensureFreshOrReconnect runs the drift check, replacing the session once if it
// has gone away underneath.
//
// The retry is safe because the drift check is a `tools/list` - idempotent, and
// compared against the baseline captured at startup rather than a fresh one, so
// a reconnect cannot launder a changed upstream into an accepted contract.
// Schema drift is never retried: it is a decision about the upstream, not a
// transport failure, and reconnecting would only ask the same question twice.
func (p *proxyBackend) ensureFreshOrReconnect(ctx context.Context, name string) error {
	err := p.ensureFresh(ctx, name)
	if err == nil || p.hasDrifted() {
		return err
	}
	if rerr := p.reconnect(ctx); rerr != nil {
		return fmt.Errorf("%w (reconnect also failed: %v)", err, rerr)
	}
	return p.ensureFresh(ctx, name)
}

func (p *proxyBackend) hasDrifted() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.driftErr != nil
}

// currentSession resolves the long-lived upstream session for a caller that
// does not already hold the lock.
func (p *proxyBackend) currentSession() (*mcp.ClientSession, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.sessionLocked()
}

func (p *proxyBackend) sessionLocked() (*mcp.ClientSession, error) {
	if p.session == nil {
		return nil, fmt.Errorf("mcp-beaver: upstream MCP session is closed")
	}
	return p.session, nil
}

func (p *proxyBackend) snapshot(ctx context.Context) error {
	session, err := p.currentSession()
	if err != nil {
		return err
	}
	res, err := p.probeTools(ctx, session)
	if err != nil {
		return fmt.Errorf("list upstream tools: %w", err)
	}
	tools := make([]*mcp.Tool, 0, len(p.allowlist))
	baseline := make(map[string]string, len(p.allowlist))
	for _, name := range p.allowlist {
		tool, err := findUpstreamTool(res.Tools, name)
		if err != nil {
			return err
		}
		cloned := toolSpecFromUpstream(tool)
		fp, err := fingerprintTool(cloned)
		if err != nil {
			return fmt.Errorf("fingerprint upstream tool %q: %w", name, err)
		}
		baseline[name] = fp
		tools = append(tools, cloned)
	}
	p.mu.Lock()
	p.selected = tools
	p.baseline = baseline
	p.mu.Unlock()
	return nil
}

func (p *proxyBackend) ensureFresh(ctx context.Context, name string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.driftErr != nil {
		return p.driftErr
	}
	session, err := p.sessionLocked()
	if err != nil {
		return err
	}
	res, err := p.probeTools(ctx, session)
	if err != nil {
		return fmt.Errorf("refresh upstream tools: %w", err)
	}
	tool, err := findUpstreamTool(res.Tools, name)
	if err != nil {
		return err
	}
	fp, err := fingerprintTool(tool)
	if err != nil {
		return fmt.Errorf("fingerprint upstream tool %q: %w", name, err)
	}
	want, ok := p.baseline[name]
	if !ok {
		return fmt.Errorf("mcp-beaver: tool %q is not allowlisted", name)
	}
	if fp != want {
		p.driftErr = fmt.Errorf("mcp-beaver: upstream schema drift for tool %q", name)
		return p.driftErr
	}
	if !slices.Contains(p.allowlist, name) {
		return fmt.Errorf("mcp-beaver: tool %q is not allowlisted", name)
	}
	return nil
}

// probeTools lists the upstream surface over the session the backend already
// holds. It used to dial a second session per call, which real Node MCP
// upstreams reject at `notifications/initialized` with HTTP 400 - see
// docs/chart.md for why a co-located digest-pinned sidecar cannot drift
// underneath the long-lived session anyway.
func (p *proxyBackend) probeTools(ctx context.Context, session *mcp.ClientSession) (*mcp.ListToolsResult, error) {
	ctx, cancel := upstreamContext(ctx)
	defer cancel()
	if session == nil {
		return nil, fmt.Errorf("mcp-beaver: upstream MCP session is closed")
	}
	return session.ListTools(ctx, nil)
}

func findUpstreamTool(tools []*mcp.Tool, name string) (*mcp.Tool, error) {
	for _, tool := range tools {
		if tool != nil && tool.Name == name {
			return tool, nil
		}
	}
	return nil, fmt.Errorf("mcp-beaver: upstream tool %q was not found or is no longer exposed", name)
}
