package mcpserver

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"time"

	internaltelemetry "github.com/coilyco/mcp-beaver/internal/telemetry"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

const instrumentationScope = "forgejo.coilysiren.me/coilyco-flight-deck/mcp-beaver"

const transportTraceparentHeader = "X-Ward-Mcp-Transport-Traceparent"

var mcpDurationBuckets = []float64{0.01, 0.02, 0.05, 0.1, 0.2, 0.5, 1, 2, 5, 10, 30, 60, 120, 300}

type directToolContextKey struct{}

type instrumentation struct {
	mode           string
	tools          map[string]struct{}
	tracer         trace.Tracer
	propagator     propagation.TextMapPropagator
	serverDuration metric.Float64Histogram
	clientDuration metric.Float64Histogram
}

func newInstrumentation(mode string, tools []*mcp.Tool) (*instrumentation, error) {
	toolNames := make(map[string]struct{}, len(tools))
	for _, tool := range tools {
		if tool != nil {
			toolNames[tool.Name] = struct{}{}
		}
	}
	meter := otel.Meter(instrumentationScope)
	serverDuration, err := meter.Float64Histogram(
		"mcp.server.operation.duration",
		metric.WithDescription("MCP request or notification duration as observed by the receiver."),
		metric.WithUnit("s"),
		metric.WithExplicitBucketBoundaries(mcpDurationBuckets...),
	)
	if err != nil {
		return nil, err
	}
	clientDuration, err := meter.Float64Histogram(
		"mcp.client.operation.duration",
		metric.WithDescription("MCP request or notification duration as observed by the sender."),
		metric.WithUnit("s"),
		metric.WithExplicitBucketBoundaries(mcpDurationBuckets...),
	)
	if err != nil {
		return nil, err
	}
	return &instrumentation{
		mode:           mode,
		tools:          toolNames,
		tracer:         otel.Tracer(instrumentationScope),
		propagator:     internaltelemetry.W3CPropagator(),
		serverDuration: serverDuration,
		clientDuration: clientDuration,
	}, nil
}

func (i *instrumentation) serverMiddleware(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		started := time.Now()
		method = boundedMCPMethod(method)
		tool := i.boundedTool(toolFromRequest(req))
		attrs := i.operationAttributes(method, tool, "mcp")

		// Stamp the method onto the enclosing HTTP span before dispatching.
		// #49's hung request was diagnosed from a transport span naming no MCP
		// method, which is the difference between "an MCP call hung" and "this
		// tool hung". The child span below carries it too, but a request that
		// never returns is read from the transport span - so the attribute has
		// to be set going in, not on the way out.
		annotateTransportSpan(ctx, method, tool)

		ambient := transportSpanContext(req)
		if !ambient.IsValid() {
			ambient = trace.SpanContextFromContext(ctx)
		}
		var remoteParent trace.SpanContext
		if params := requestParams(req, method, false); params != nil {
			carrier := metaCarrier(params.GetMeta())
			ctx = i.propagator.Extract(ctx, carrier)
			remoteParent = trace.SpanContextFromContext(i.propagator.Extract(context.Background(), carrier))
		}
		if !remoteParent.IsValid() && ambient.IsValid() {
			ctx = trace.ContextWithSpanContext(ctx, ambient)
		}
		parent := trace.SpanContextFromContext(ctx)
		options := []trace.SpanStartOption{
			trace.WithSpanKind(trace.SpanKindServer),
			trace.WithAttributes(attrs...),
		}
		if ambient.IsValid() && remoteParent.IsValid() && !sameSpanContext(ambient, parent) {
			options = append(options, trace.WithLinks(trace.Link{SpanContext: ambient}))
		}
		ctx, span := i.tracer.Start(ctx, mcpSpanName(method, tool), options...)
		defer span.End()

		result, err := next(ctx, method, req)
		errorType := operationErrorType(result, err)
		finishSpan(span, errorType)
		metricAttrs := append([]attribute.KeyValue(nil), attrs...)
		if rpcStatus := rpcResponseStatus(err); rpcStatus != "" {
			statusAttr := attribute.String("rpc.response.status_code", rpcStatus)
			span.SetAttributes(statusAttr)
			metricAttrs = append(metricAttrs, statusAttr)
		}
		if errorType != "" {
			metricAttrs = append(metricAttrs, attribute.String("error.type", errorType))
		}
		i.serverDuration.Record(ctx, time.Since(started).Seconds(), metric.WithAttributes(metricAttrs...))
		return result, err
	}
}

func (i *instrumentation) clientMiddleware(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		started := time.Now()
		method = boundedMCPMethod(method)
		tool := i.boundedTool(toolFromRequest(req))
		attrs := i.operationAttributes(method, tool, "mcp")
		ctx, span := i.tracer.Start(ctx, mcpSpanName(method, tool),
			trace.WithSpanKind(trace.SpanKindClient),
			trace.WithAttributes(attrs...),
		)
		defer span.End()

		if params := requestParams(req, method, true); params != nil {
			meta := params.GetMeta()
			if meta == nil {
				meta = map[string]any{}
				params.SetMeta(meta)
			}
			i.propagator.Inject(ctx, metaCarrier(meta))
		}

		result, err := next(ctx, method, req)
		errorType := operationErrorType(result, err)
		finishSpan(span, errorType)
		metricAttrs := append([]attribute.KeyValue(nil), attrs...)
		if rpcStatus := rpcResponseStatus(err); rpcStatus != "" {
			statusAttr := attribute.String("rpc.response.status_code", rpcStatus)
			span.SetAttributes(statusAttr)
			metricAttrs = append(metricAttrs, statusAttr)
		}
		if errorType != "" {
			metricAttrs = append(metricAttrs, attribute.String("error.type", errorType))
		}
		i.clientDuration.Record(ctx, time.Since(started).Seconds(), metric.WithAttributes(metricAttrs...))
		return result, err
	}
}

func (i *instrumentation) toolHandler(name string, next mcp.ToolHandler) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if !isDirectToolContext(ctx) {
			return next(ctx, req)
		}
		name = i.boundedTool(name)
		attrs := []attribute.KeyValue{
			attribute.String("gen_ai.operation.name", "execute_tool"),
			attribute.String("gen_ai.tool.name", name),
			attribute.String("mcp_beaver.transport", "http"),
			attribute.String("mcp_beaver.mode", i.mode),
		}
		ctx, span := i.tracer.Start(ctx, "execute_tool "+name, trace.WithAttributes(attrs...))
		defer span.End()
		result, err := next(ctx, req)
		finishSpan(span, operationErrorType(result, err))
		return result, err
	}
}

func (i *instrumentation) operationAttributes(method, tool, transport string) []attribute.KeyValue {
	attrs := []attribute.KeyValue{
		attribute.String("mcp.method.name", method),
		attribute.String("network.protocol.name", "http"),
		attribute.String("network.transport", "tcp"),
		attribute.String("mcp_beaver.transport", transport),
		attribute.String("mcp_beaver.mode", i.mode),
	}
	if tool != "" {
		attrs = append(attrs,
			attribute.String("gen_ai.operation.name", "execute_tool"),
			attribute.String("gen_ai.tool.name", tool),
		)
	}
	return attrs
}

func (i *instrumentation) boundedTool(name string) string {
	if name == "" {
		return ""
	}
	if _, ok := i.tools[name]; ok {
		return name
	}
	return "_OTHER"
}

func directToolContext(ctx context.Context) context.Context {
	return context.WithValue(ctx, directToolContextKey{}, true)
}

func isDirectToolContext(ctx context.Context) bool {
	direct, _ := ctx.Value(directToolContextKey{}).(bool)
	return direct
}

// boundedMCPMethod keeps the method label to a fixed set so a caller cannot
// drive the metric to unbounded cardinality. It spans both protocol eras:
// 2026-07-28 removed `initialize`, `notifications/initialized`, and `ping`, and
// added `server/discover` and `subscriptions/listen`. Pre-2026 clients still
// negotiate here, so both sides stay named rather than collapsing to _OTHER.
func boundedMCPMethod(method string) string {
	switch method {
	case "initialize", "notifications/initialized", "notifications/cancelled",
		"ping", "tools/list", "tools/call",
		"server/discover", "subscriptions/listen",
		"prompts/list", "prompts/get",
		"resources/list", "resources/read", "resources/templates/list":
		return method
	default:
		return "_OTHER"
	}
}

func toolFromRequest(req mcp.Request) string {
	switch params := requestParams(req, "", false).(type) {
	case *mcp.CallToolParams:
		return params.Name
	case *mcp.CallToolParamsRaw:
		return params.Name
	default:
		return ""
	}
}

func requestParams(req mcp.Request, method string, create bool) mcp.Params {
	if req == nil {
		return nil
	}
	switch typed := req.(type) {
	case *mcp.ClientRequest[mcp.Params]:
		if typed.Params == nil && create {
			switch method {
			case "tools/list":
				typed.Params = &mcp.ListToolsParams{}
			case "ping":
				typed.Params = &mcp.PingParams{}
			case "notifications/initialized":
				typed.Params = &mcp.InitializedParams{}
			case "notifications/cancelled":
				typed.Params = &mcp.CancelledParams{}
			}
		}
		return typed.Params
	case *mcp.ClientRequest[*mcp.ListToolsParams]:
		if typed.Params == nil && create {
			typed.Params = &mcp.ListToolsParams{}
		}
		return typed.Params
	case *mcp.ClientRequest[*mcp.PingParams]:
		if typed.Params == nil && create {
			typed.Params = &mcp.PingParams{}
		}
		return typed.Params
	case *mcp.ClientRequest[*mcp.InitializedParams]:
		if typed.Params == nil && create {
			typed.Params = &mcp.InitializedParams{}
		}
		return typed.Params
	case *mcp.ClientRequest[*mcp.CancelledParams]:
		if typed.Params == nil && create {
			typed.Params = &mcp.CancelledParams{}
		}
		return typed.Params
	}
	params := req.GetParams()
	if params == nil {
		return nil
	}
	value := reflect.ValueOf(params)
	if value.Kind() == reflect.Pointer && value.IsNil() {
		return nil
	}
	return params
}

// annotateTransportSpan adds the MCP method, and the tool where there is one,
// to whatever span is already active - in the serving path that is otelhttp's
// server span for POST /mcp. Both values are already bounded to closed sets by
// their callers, so this adds no cardinality a caller can drive.
func annotateTransportSpan(ctx context.Context, method, tool string) {
	span := trace.SpanFromContext(ctx)
	if !span.SpanContext().IsValid() {
		return
	}
	attrs := []attribute.KeyValue{attribute.String("mcp.method.name", method)}
	if tool != "" {
		attrs = append(attrs, attribute.String("gen_ai.tool.name", tool))
	}
	span.SetAttributes(attrs...)
}

func mcpSpanName(method, tool string) string {
	if tool == "" {
		return method
	}
	return method + " " + tool
}

func operationErrorType(result any, err error) string {
	if toolResult, ok := result.(*mcp.CallToolResult); ok && toolResult != nil && toolResult.IsError {
		return "tool_error"
	}
	if err == nil {
		return ""
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	if errors.Is(err, context.Canceled) {
		return "cancelled"
	}
	var rpcErr *jsonrpc.Error
	if errors.As(err, &rpcErr) {
		switch rpcErr.Code {
		case -32700:
			return "parse_error"
		case -32600:
			return "invalid_request"
		case -32601:
			return "method_not_found"
		case -32602:
			return "invalid_params"
		default:
			return "jsonrpc_error"
		}
	}
	return "internal_error"
}

func finishSpan(span trace.Span, errorType string) {
	if errorType == "" {
		return
	}
	span.SetAttributes(attribute.String("error.type", errorType))
	span.SetStatus(codes.Error, "")
	span.AddEvent("exception", trace.WithAttributes(attribute.String("exception.type", errorType)))
}

func sameSpanContext(a, b trace.SpanContext) bool {
	return a.TraceID() == b.TraceID() && a.SpanID() == b.SpanID()
}

func transportSpanContext(req mcp.Request) trace.SpanContext {
	if req == nil || req.GetExtra() == nil {
		return trace.SpanContext{}
	}
	value := req.GetExtra().Header.Get(transportTraceparentHeader)
	if value == "" {
		return trace.SpanContext{}
	}
	carrier := propagation.MapCarrier{"traceparent": value}
	return trace.SpanContextFromContext(propagation.TraceContext{}.Extract(context.Background(), carrier))
}

type metaCarrier map[string]any

func (c metaCarrier) Get(key string) string {
	value, _ := c[key].(string)
	return value
}

func (c metaCarrier) Set(key, value string) {
	c[key] = value
}

func (c metaCarrier) Keys() []string {
	keys := make([]string, 0, len(c))
	for key := range c {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func rpcResponseStatus(err error) string {
	var rpcErr *jsonrpc.Error
	if errors.As(err, &rpcErr) {
		switch rpcErr.Code {
		case -32700:
			return "-32700"
		case -32600:
			return "-32600"
		case -32601:
			return "-32601"
		case -32602:
			return "-32602"
		default:
			return "_OTHER"
		}
	}
	return ""
}
