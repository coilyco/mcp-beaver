package mcpserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"forgejo.coilysiren.me/coilyco-flight-deck/umbra/http/opcore"
	"forgejo.coilysiren.me/coilyco-flight-deck/umbra/pkg/valuesource"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/trace"
)

// DefaultRequestTimeout bounds one inbound MCP or HTTP tool request end to
// end, including the upstream call it makes.
//
// The runtime previously bound nothing on this axis: `http.Server` was
// constructed with no timeouts and the proxy client was nil, so a wedged
// upstream held a request open for as long as the caller would wait. #49
// recorded one that ran 180.002s inside two healthy pods and outlived the turn
// that issued it. 60s is chosen to sit far enough under any caller's own
// budget that the error arrives as a tool failure the model can react to,
// rather than as the caller's timeout with nothing attributable behind it.
const DefaultRequestTimeout = 60 * time.Second

// Server is a guarded MCP runtime backed by either local opcore grants or an
// allowlisted upstream streamable-HTTP MCP server.
type Server struct {
	name           string
	specPath       string
	descs          []opcore.Descriptor
	cfg            opcore.RuntimeConfig
	tools          []*mcp.Tool
	resources      []mcp.Resource
	prompts        []mcp.Prompt
	handlers       map[string]mcp.ToolHandler
	upstreams      []adminUpstreamResponse
	sdk            *mcp.Server
	telemetry      *instrumentation
	requestTimeout time.Duration
	closeFn        func() error
}

// SetRequestTimeout overrides the per-request bound. A non-positive value
// disables it, which is the escape hatch for a genuinely long-running upstream
// - stated deliberately rather than reached by forgetting to set one.
func (s *Server) SetRequestTimeout(d time.Duration) {
	s.requestTimeout = d
}

// New parses a `.mcp.kdl` source and builds the SDK-backed server: one MCP tool
// and matching HTTP endpoint per grant, with opcore still owning the guardfile
// parse, guard, and upstream request execution.
func New(name, specPath string, src []byte) (*Server, error) {
	descs, cfg, err := opcore.ParseInline(src)
	if err != nil {
		return nil, err
	}
	cfg.Providers = valuesource.Builtins()
	rt := opcore.NewRuntime(cfg)

	tools, err := localTools(descs)
	if err != nil {
		return nil, err
	}
	// Top-level `icon`, `instructions`, `resource`, `prompt`, `server-info`,
	// `confirm`, and `withhold` nodes ride beside `wrap`, outside the frozen
	// inline grammar opcore owns (deploy#255) - parsed here, projected onto
	// the served surface below.
	icons, err := parseIcons(src)
	if err != nil {
		return nil, err
	}
	instructions, err := parseInstructions(src)
	if err != nil {
		return nil, err
	}
	resources, err := parseResources(src)
	if err != nil {
		return nil, err
	}
	prompts, err := parsePrompts(src)
	if err != nil {
		return nil, err
	}
	infoCfg, err := parseServerInfo(src)
	if err != nil {
		return nil, err
	}
	confirmations, err := parseConfirmations(src)
	if err != nil {
		return nil, err
	}
	stubs, err := parseWithheld(src)
	if err != nil {
		return nil, err
	}
	rateCfg, err := parseRateLimit(src)
	if err != nil {
		return nil, err
	}
	// The server name is the default bucket key, so two pods rendering the
	// same guardfile charge one budget rather than one each (deploy#549).
	limiter, err := newRateBucket(rateCfg, name)
	if err != nil {
		return nil, err
	}
	caches, err := parseCaches(src)
	if err != nil {
		return nil, err
	}
	extracts, err := parseExtracts(src)
	if err != nil {
		return nil, err
	}
	if err := validateExtracts(extracts, descs); err != nil {
		return nil, err
	}
	if err := validateCaches(caches, descs, confirmations); err != nil {
		return nil, err
	}
	rejectEmpties, err := parseRejectEmpty(src)
	if err != nil {
		return nil, err
	}
	if err := validateRejectEmpty(rejectEmpties, descs); err != nil {
		return nil, err
	}
	emptyArgs, err := parseRejectEmptyArguments(src)
	if err != nil {
		return nil, err
	}
	if err := validateRejectEmptyArguments(emptyArgs, descs); err != nil {
		return nil, err
	}
	queryPins, err := parseQueryPins(src)
	if err != nil {
		return nil, err
	}
	if err := validateQueryPins(queryPins, descs); err != nil {
		return nil, err
	}
	// Built before telemetry and folded into the tool list, so the info tool
	// is a first-class member of the served surface: bounded in metrics like
	// any grant, and reported by ToolNames and `mcp-beaver lint`.
	infoTool, err := serverInfoTool(infoCfg, tools)
	if err != nil {
		return nil, err
	}
	if infoTool != nil {
		tools = append(tools, infoTool)
	}
	// Validated against the grant-backed surface plus the info tool, so a stub
	// cannot shadow anything the spec actually serves.
	stubTools, err := withheldTools(stubs, tools)
	if err != nil {
		return nil, err
	}
	tools = append(tools, stubTools...)
	sort.Slice(tools, func(i, j int) bool { return tools[i].Name < tools[j].Name })
	if err := validateConfirmations(confirmations, tools); err != nil {
		return nil, err
	}

	instrumentation, err := newInstrumentation("spec", tools)
	if err != nil {
		return nil, fmt.Errorf("initialize telemetry: %w", err)
	}
	// Before the first handler can fail and report the URL it failed on.
	registerBaseURLPath(context.Background(), rt)

	s := &Server{
		name:           name,
		specPath:       specPath,
		descs:          descs,
		cfg:            cfg,
		tools:          tools,
		handlers:       make(map[string]mcp.ToolHandler, len(descs)),
		sdk:            newSDKServer(name, icons, instructions),
		telemetry:      instrumentation,
		requestTimeout: DefaultRequestTimeout,
	}

	for _, d := range descs {
		desc := d
		spec := toolSpec(desc)
		handler := toolHandler(rt, desc, queryPins[spec.Name], extracts[spec.Name])
		// Innermost, so it reads the upstream's own answer: an empty result
		// becomes a tool error before anything downstream can cache it.
		if _, declared := rejectEmpties[spec.Name]; declared {
			handler = withRejectEmpty(spec.Name, handler)
		}
		// Inside the confirmation gate: a call awaiting a human's accept must
		// not hold an upstream slot, and a declined call must not have spent
		// one. The bucket is for requests that actually go out.
		handler = withRateLimit(limiter, handler)
		if message, gated := confirmations[spec.Name]; gated {
			handler = withConfirmation(message, handler)
		}
		// Outermost: a hit must not spend a rate-limit slot the community
		// needs for a request that was actually made.
		if ttl, cached := caches[spec.Name]; cached {
			handler = withResponseCache(newResponseCache(ttl), spec.Name, handler)
		}
		// Outermost of all: a write that carries nothing spends no slot and
		// asks no human to confirm what was going to be refused anyway.
		if fields, declared := emptyArgs[spec.Name]; declared {
			handler = withRejectEmptyArguments(spec.Name, fields, handler)
		}
		s.registerTool(spec, handler)
	}
	s.registerResources(resources)
	s.registerPrompts(prompts)
	s.registerServerInfo(infoTool)
	s.registerWithheld(stubs, stubTools)
	s.installMiddleware()
	return s, nil
}

// NewProxy connects to an upstream streamable-HTTP MCP server and exposes only
// the selected upstream tools. The outward contract preserves the upstream tool
// schemas, descriptions, titles, and annotations where possible.
func NewProxy(ctx context.Context, name, specPath, upstreamURL string, allowTools []string, httpClient *http.Client) (*Server, error) {
	return NewProxyWithPins(ctx, name, specPath, upstreamURL, allowTools, nil, httpClient)
}

// NewProxyWithPins is NewProxy plus server-side argument pins, which bound the
// scope a caller may reach when the scope rides in an argument rather than in
// the tool name. See ArgPin.
func NewProxyWithPins(ctx context.Context, name, specPath, upstreamURL string, allowTools []string, pins []ArgPin, httpClient *http.Client) (*Server, error) {
	return NewProxyWithOptions(ctx, name, specPath, upstreamURL, allowTools, ProxyOptions{Pins: pins, HTTPClient: httpClient})
}

// ProxyOptions carries the optional inputs of an upstream proxy. It exists so
// the third optional input did not mint a third positional constructor.
type ProxyOptions struct {
	// Pins fix arguments the caller may otherwise choose. See ArgPin.
	Pins []ArgPin
	// Headers are presented to the upstream on every request. See UpstreamHeader.
	Headers []UpstreamHeader
	// HTTPClient overrides the default upstream client, for tests and for a
	// caller that has already chosen its own bounds.
	HTTPClient *http.Client
}

// NewProxyWithOptions is the full upstream-proxy constructor. NewProxy and
// NewProxyWithPins are the shorthands that predate it.
func NewProxyWithOptions(ctx context.Context, name, specPath, upstreamURL string, allowTools []string, opts ProxyOptions) (*Server, error) {
	pins, httpClient := opts.Pins, opts.HTTPClient
	declaredTools := make([]*mcp.Tool, 0, len(allowTools))
	for _, tool := range allowTools {
		declaredTools = append(declaredTools, &mcp.Tool{Name: tool})
	}
	instrumentation, err := newInstrumentation("upstream", declaredTools)
	if err != nil {
		return nil, fmt.Errorf("initialize telemetry: %w", err)
	}
	// Validated before connecting: a pin naming an unserved tool means the
	// operator believes a surface is scoped while nothing applies it, and that
	// belief should not survive startup.
	if err := ValidatePins(pins, allowTools); err != nil {
		return nil, err
	}
	if err := ValidateUpstreamHeaders(opts.Headers); err != nil {
		return nil, err
	}
	proxy, err := newProxyBackend(ctx, upstreamURL, allowTools, opts.Headers, httpClient, instrumentation)
	if err != nil {
		return nil, err
	}
	pinned := pinsByTool(pins)

	s := &Server{
		name:           name,
		specPath:       specPath,
		tools:          proxy.selectedTools(),
		handlers:       make(map[string]mcp.ToolHandler, len(allowTools)),
		upstreams:      []adminUpstreamResponse{{Kind: "mcp", Mode: "streamable-http", Auth: upstreamAuthScheme(opts.Headers)}},
		sdk:            newSDKServer(name, nil, ""),
		telemetry:      instrumentation,
		requestTimeout: DefaultRequestTimeout,
		closeFn:        proxy.Close,
	}
	for _, tool := range proxy.selectedTools() {
		t := cloneTool(tool)
		s.registerTool(t, withArgPins(pinned[tool.Name], proxy.toolHandler(tool.Name)))
	}
	s.installMiddleware()
	return s, nil
}

// ToolNames returns the projected tool names in the order the runtime serves
// them. `mcp-beaver lint` prints these so a consumer can read the minted surface
// off the owning loader instead of writing a second parser for the same file.
func (s *Server) ToolNames() []string {
	return projectedToolNames(s.tools)
}

// ToolMethod is one projected tool's resolved HTTP method, plus whether the
// verb reached it by an explicit entry in opcore's table or by the unknown-verb
// POST fallthrough.
type ToolMethod struct {
	Tool        string
	Verb        string
	Method      string
	Fallthrough bool
}

// ToolMethods reports the resolved method behind each grant-backed tool.
//
// The method is otherwise invisible from every surface this project exposes:
// `lint` printed names only, and the MCP tool schema carries no method, so a
// grant whose verb resolved wrongly minted a tool that looked identical to a
// working one and failed at call time (#55). An unknown verb still produces a
// tool - that is the fallthrough working as designed for child sub-collections
// - so the fact worth surfacing is which of the two happened.
//
// Tools with no descriptor (the info tool, proxy grants) are omitted rather
// than reported with an empty method: they have no verb to resolve.
//
// Fallthrough reads opcore's own `MethodInferred` rather than re-deriving it
// from the verb. A grant that states `method "POST"` has an author's decision
// behind it and is owed no warning, and re-deriving would warn anyway - which
// is the whole point of the node existing (mcp-beaver#72).
func (s *Server) ToolMethods() []ToolMethod {
	out := make([]ToolMethod, 0, len(s.descs))
	for _, d := range s.descs {
		if d.Proxy != nil {
			continue
		}
		out = append(out, ToolMethod{
			Tool:        toolName(d),
			Verb:        d.Leaf,
			Method:      d.Method,
			Fallthrough: d.MethodInferred,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Tool < out[j].Tool })
	return out
}

// ResourcesWithoutAudience names each resource whose spec states no audience.
//
// A host decides on its own whether to pull a resource into a model's context,
// and the annotation is what it reads to decide. Absence is not a statement, so
// a host that gates on it cannot tell a resource written for a model from one
// written for a person and skips both. The resource then serves correctly,
// lints identically to a working one, and is read by nobody.
//
// An explicit audience is not reported, whichever roles it names: an author who
// wrote `audience "user"` has already answered the question.
func (s *Server) ResourcesWithoutAudience() []string {
	out := make([]string, 0, len(s.resources))
	for _, res := range s.resources {
		if res.Annotations != nil && len(res.Annotations.Audience) > 0 {
			continue
		}
		out = append(out, res.Name)
	}
	sort.Strings(out)
	return out
}

// WithheldTools returns the served tool names that are `withhold` stubs. A
// stub and the info tool both resolve no HTTP method, so an operator reading
// lint needs the two told apart: one is policy, the other is plumbing.
func (s *Server) WithheldTools() []string {
	var out []string
	for _, tool := range s.tools {
		if tool == nil {
			continue
		}
		if marked, _ := tool.GetMeta()[withheldMetaKey].(bool); marked {
			out = append(out, tool.Name)
		}
	}
	sort.Strings(out)
	return out
}

// NotReadOnly returns the served tool names the upstream does not annotate
// `readOnlyHint: true`, sorted. A tool with no annotations counts, since the
// MCP default for the hint is false: an upstream that stays silent has not
// promised anything, and a read-only allowlist must not assume one.
func (s *Server) NotReadOnly() []string {
	var out []string
	for _, tool := range s.tools {
		if tool == nil {
			continue
		}
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
			out = append(out, tool.Name)
		}
	}
	sort.Strings(out)
	return out
}

// Close releases any upstream session resources. Local opcore-backed servers do
// not hold additional runtime state, so Close is a no-op there.
func (s *Server) Close() error {
	if s.closeFn == nil {
		return nil
	}
	return s.closeFn()
}

// Handler exposes the runtime on /mcp using the official SDK streamable HTTP
// handler, automatically projects each tool at /api/{tool-name}, and retains
// the pod health probe plus operator admin endpoints.
//
// Stateless is required, not merely preferred: the SDK rejects a 2026-07-28
// client outright on a session-backed handler. Older clients still negotiate
// their own version here, they just stop receiving an `Mcp-Session-Id`.
// mcp-beaver holds no cross-call state of its own, so nothing is lost.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mcpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return s.sdk
	}, &mcp.StreamableHTTPOptions{
		JSONResponse: true,
		Stateless:    true,
		// Ties the in-flight handler to the originating HTTP request, so a
		// caller that has gone away stops work here instead of leaving it
		// running against an upstream with nowhere to deliver the answer.
		// That abandoned-but-still-running shape is what #49 caught: a root
		// span with no parent, outliving the turn that issued it.
		//
		// The SDK applies this only to >= 2026-07-28 clients, where the POST
		// is the whole request lifecycle. Older clients are unaffected, which
		// is why the per-call bound below is not redundant with it.
		PropagateRequestCancellation: true,
	})
	mux.Handle("/mcp", captureTransportSpan(mcpHandler))
	mux.HandleFunc(apiPrefix, s.serveAPITool)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	mux.HandleFunc(adminDescribePath, s.serveAdminDescribe)
	mux.HandleFunc(adminReloadPath, s.serveAdminReload)
	return otelhttp.NewHandler(withRequestDeadline(s.transportDeadline(), mux), "mcp-beaver HTTP",
		otelhttp.WithFilter(func(r *http.Request) bool { return r.URL.Path != "/healthz" }),
	)
}

// responseGrace is the headroom the transport deadline keeps over the per-call
// one, so the tool is always what expires first.
//
// Without it both deadlines fire on the same tick and the request context dies
// while the runtime is still serializing the timeout error, so the caller gets
// an empty body rather than a stated failure - a wedged upstream would then be
// indistinguishable from a crashed pod, which is the confusion #49 started in.
const responseGrace = 5 * time.Second

func (s *Server) transportDeadline() time.Duration {
	if s.requestTimeout <= 0 {
		return 0
	}
	return s.requestTimeout + responseGrace
}

// withRequestDeadline bounds every request but the health probe. The deadline
// rides the request context, so it reaches the outbound upstream call rather
// than only cutting the response: opcore's Execute and the proxy client both
// take this context, and both abort on it. That is the difference between a
// bounded tool error and a socket the runtime holds until the caller gives up.
//
// /healthz is exempt because a liveness probe that can be failed by a wedged
// upstream turns one slow dependency into a pod restart loop.
func withRequestDeadline(timeout time.Duration, next http.Handler) http.Handler {
	if timeout <= 0 {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			next.ServeHTTP(w, r)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), timeout)
		defer cancel()
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func captureTransportSpan(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cloned := r.Clone(r.Context())
		cloned.Header = r.Header.Clone()
		cloned.Header.Del(transportTraceparentHeader)
		spanContext := trace.SpanContextFromContext(r.Context())
		if !spanContext.IsValid() {
			next.ServeHTTP(w, cloned)
			return
		}
		cloned.Header.Set(transportTraceparentHeader, fmt.Sprintf(
			"00-%s-%s-%02x",
			spanContext.TraceID(), spanContext.SpanID(), byte(spanContext.TraceFlags()),
		))
		next.ServeHTTP(w, cloned)
	})
}

func (s *Server) installMiddleware() {
	s.sdk.AddReceivingMiddleware(s.telemetry.serverMiddleware, func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			res, err := next(ctx, method, req)
			if method == "tools/call" {
				if jerr, ok := err.(*jsonrpc.Error); ok && jerr.Code == jsonrpc.CodeInvalidParams {
					if strings.HasPrefix(jerr.Message, "unknown tool") {
						return nil, &jsonrpc.Error{Code: jsonrpc.CodeMethodNotFound, Message: jerr.Message, Data: jerr.Data}
					}
				}
			}
			if err == nil {
				s.applyCacheTTL(res)
			}
			return res, err
		}
	})
}

// Cache TTLs for list results. 2026-07-28 requires `ttlMs` on every cacheable
// list result, and the SDK leaves it at 0, which tells a client the response is
// immediately stale and to re-list on every turn.
//
// A spec-driven surface is fixed for the process lifetime: the spec is baked
// into the image and `/admin/reload` answers restart-required, so the list can
// only change by the pod restarting. A proxied surface mirrors an upstream that
// can change under us, so it re-lists far more often. A client holding a stale
// list still fails closed, since an absent grant is an absent tool.
const (
	specListTTLMs     = 300_000
	upstreamListTTLMs = 60_000
)

// applyCacheTTL stamps the freshness hint on list results. cacheScope is left
// to the SDK, which defaults it to "public": a mcp-beaver tool list is derived
// from policy, identical for every caller of the server, and never per-user.
func (s *Server) applyCacheTTL(res mcp.Result) {
	ttl := specListTTLMs
	if len(s.upstreams) > 0 {
		ttl = upstreamListTTLMs
	}
	switch typed := res.(type) {
	case *mcp.ListToolsResult:
		typed.TTLMs = ttl
	case *mcp.ListPromptsResult:
		typed.TTLMs = ttl
	case *mcp.ListResourcesResult:
		typed.TTLMs = ttl
	case *mcp.ListResourceTemplatesResult:
		typed.TTLMs = ttl
	case *mcp.ReadResourceResult:
		typed.TTLMs = ttl
	}
}

// Registration is where logging is applied rather than in New, so every served
// tool is covered by construction: grants, the info tool, withheld stubs, the
// SSM readers, and the upstream proxy all arrive here. Inside the telemetry
// wrapper, so a logged call carries the span it belongs to.
func (s *Server) registerTool(tool *mcp.Tool, handler mcp.ToolHandler) {
	handler = withLogging(tool.Name, handler)
	handler = s.telemetry.toolHandler(tool.Name, handler)
	handler = s.withToolDeadline(handler)
	s.handlers[tool.Name] = handler
	s.sdk.AddTool(tool, handler)
}

// withToolDeadline bounds one tool call at the handler, independent of how the
// call arrived.
//
// The transport-level deadline is not sufficient on its own. The SDK
// propagates HTTP request cancellation only for >= 2026-07-28 clients, so an
// older client's call would otherwise run unbounded no matter what the inbound
// request said - which is the case #49 hit, and the reason a bound written
// only at the edge would have looked correct and done nothing.
//
// The deadline is read at call time rather than captured at registration, so
// SetRequestTimeout works after the tools are wired.
func (s *Server) withToolDeadline(next mcp.ToolHandler) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		timeout := s.requestTimeout
		if timeout <= 0 {
			return next(ctx, req)
		}
		// Never extend a deadline the caller already set tighter than ours.
		if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) <= timeout {
			return next(ctx, req)
		}
		ctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		return next(ctx, req)
	}
}

func localTools(descs []opcore.Descriptor) ([]*mcp.Tool, error) {
	out := make([]*mcp.Tool, 0, len(descs))
	seen := map[string]bool{}
	sort.Slice(descs, func(i, j int) bool { return toolName(descs[i]) < toolName(descs[j]) })
	for _, d := range descs {
		tname := toolName(d)
		if seen[tname] {
			return nil, fmt.Errorf("mcp-beaver: duplicate tool name %q from grant %q", tname, d.Grant)
		}
		seen[tname] = true
		out = append(out, toolSpec(d))
	}
	return out, nil
}

func toolSpecFromUpstream(tool *mcp.Tool) *mcp.Tool {
	if tool == nil {
		return nil
	}
	return cloneTool(tool)
}

func cloneTool(tool *mcp.Tool) *mcp.Tool {
	if tool == nil {
		return nil
	}
	cloned := *tool
	return &cloned
}

func newSDKServer(name string, icons []mcp.Icon, instructions string) *mcp.Server {
	return mcp.NewServer(
		&mcp.Implementation{Name: name, Version: "0.1.0", Icons: icons},
		&mcp.ServerOptions{
			Instructions: renderInstructions(instructions),
			// Empty rather than nil: nil means the SDK's historical
			// {"logging":{}} default, and 2026-07-28 deprecates Logging along
			// with Roots and Sampling. The suggested migration is
			// OpenTelemetry, which this runtime already emits. tools,
			// prompts, and resources are still inferred from what is
			// registered, so this drops only the deprecated claim.
			Capabilities: &mcp.ServerCapabilities{},
		},
	)
}

func toolSpec(d opcore.Descriptor) *mcp.Tool {
	return &mcp.Tool{
		Name:         toolName(d),
		Title:        toolTitle(d),
		Description:  describe(d),
		InputSchema:  json.RawMessage(d.InputSchema().JSONSchema()),
		OutputSchema: resultOutputSchema,
		Annotations:  toolAnnotations(d),
	}
}

func toolHandler(rt *opcore.Runtime, desc opcore.Descriptor, pins []queryPin, extract *extractSpec) mcp.ToolHandler {
	schema := desc.InputSchema()
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var rawArgs map[string]any
		if len(req.Params.Arguments) > 0 {
			if err := json.Unmarshal(req.Params.Arguments, &rawArgs); err != nil {
				return toolError(fmt.Errorf("invalid tool arguments: %w", err)), nil
			}
		}
		if unknown := undeclaredArgs(schemaNames(schema), rawArgs); len(unknown) > 0 {
			return toolError(undeclaredArgError(toolName(desc), unknown, schemaNames(schema))), nil
		}
		args := splitArgs(schema, rawArgs)
		// A pinned parameter is absent from the schema, so a caller supplying it
		// is now refused above rather than silently overruled here, and this
		// assignment still cannot be contested.
		if len(pins) > 0 {
			resolved, err := resolveQueryPins(ctx, pins)
			if err != nil {
				return toolError(err), nil
			}
			for name, value := range resolved {
				args.Query[name] = value
			}
		}
		resp, err := (&opcore.Operation{Desc: desc, RT: rt}).Execute(ctx, args)
		if err != nil {
			return toolError(err), nil
		}
		if extract != nil {
			return extractToolSuccess(ctx, resp, extract), nil
		}
		return toolSuccess(resp, desc), nil
	}
}

// toolName projects a descriptor onto its MCP tool name: `verb_resource`, e.g.
// `create_issue`. Leaf is the verb, Group the resource. See DESIGN.md.
func toolName(d opcore.Descriptor) string {
	return d.Leaf + "_" + d.Group
}

func toolTitle(d opcore.Descriptor) string {
	title := strings.NewReplacer("_", " ", "-", " ").Replace(toolName(d))
	if title == "" {
		return ""
	}
	return strings.ToUpper(title[:1]) + title[1:]
}

// describe is the tool's human description: the Guardfile `describe "..."` note
// when present, else a user-goal sentence derived from the authorizing grant.
func describe(d opcore.Descriptor) string {
	if strings.TrimSpace(d.Describe) != "" {
		return d.Describe
	}
	return fmt.Sprintf("Use this when the user wants to %s %s through the configured upstream service.", d.Leaf, d.Group)
}

func toolAnnotations(d opcore.Descriptor) *mcp.ToolAnnotations {
	destructive := d.Destructive
	openWorld := true
	return &mcp.ToolAnnotations{
		ReadOnlyHint:    isReadOnlyMethod(d.Method),
		DestructiveHint: &destructive,
		IdempotentHint:  isIdempotentMethod(d.Method),
		OpenWorldHint:   &openWorld,
	}
}

func isReadOnlyMethod(method string) bool {
	switch strings.ToUpper(method) {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	default:
		return false
	}
}

func isIdempotentMethod(method string) bool {
	switch strings.ToUpper(method) {
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodPut, http.MethodDelete:
		return true
	default:
		return false
	}
}

func (s *Server) specName() string {
	if s.specPath == "" {
		return s.name
	}
	base := s.specPath
	if i := strings.LastIndex(base, "/"); i >= 0 {
		base = base[i+1:]
	}
	base = strings.TrimSuffix(base, ".kdl")
	base = strings.TrimSuffix(base, ".mcp")
	if base == "" {
		return s.name
	}
	return base
}

// fingerprintTool serializes the upstream tool contract into a stable digest.
func fingerprintTool(tool *mcp.Tool) (string, error) {
	raw, err := json.Marshal(tool)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}
