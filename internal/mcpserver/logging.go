package mcpserver

import (
	"context"
	"io"
	"log/slog"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/otel/trace"
)

// maxLoggedReason bounds one refusal reason. An upstream that answers a
// rejection with an HTML error page would otherwise put the whole page in the
// log line, and the actionable part is always the front.
const maxLoggedReason = 512

// logger is the process-wide structured logger. JSON to stderr, because the
// node collector reads `/var/log/pods/*` and the ingest pipeline promotes JSON
// bodies and maps `level` onto OTel severity - so JSON gets correct severity
// with no new parser.
//
// Generated servers used to emit exactly one line each, the startup banner,
// and nothing per call. Twelve hours across twenty-two pods produced one log
// line fleet-wide while four of those servers were failing every call, and
// every one of those failures had to be inferred from client spans because the
// server that knew what went wrong said nothing (mcp-beaver#78).
var logger = newLogger(os.Stderr, os.Getenv("MCP_BEAVER_LOG_LEVEL"))

// SetLogOutput redirects structured logs, for a test that needs to read them.
func SetLogOutput(w io.Writer, level string) { logger = newLogger(w, level) }

func newLogger(w io.Writer, level string) *slog.Logger {
	lvl := slog.LevelInfo
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn", "warning":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	}
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: lvl}))
}

// Log returns the process logger, so the command layer reports startup through
// the same handler a tool call does.
func Log() *slog.Logger { return logger }

// urlQuery matches the query string of any URL in an error message.
var urlQuery = regexp.MustCompile(`(https?://[^\s?]*)\?[^\s]*`)

// redactReason keeps a refusal actionable without widening the secret
// boundary. Every URL loses its query string and keeps its scheme, host and
// path.
//
// Query and header were once the only two surfaces a credential could reach.
// A base-url carrying a token in its path is the third, so the registered
// prefix goes too. See docs/logs.md.
func redactReason(reason string) string {
	reason = urlQuery.ReplaceAllString(reason, "$1?<redacted>")
	reason = redactSecretPaths(reason)
	reason = strings.TrimSpace(reason)
	if len(reason) > maxLoggedReason {
		return reason[:maxLoggedReason] + "..."
	}
	return reason
}

// firstTextContent reads the reason a tool error carries, which is where
// opcore's guard denials and upstream failures land.
func firstTextContent(result *mcp.CallToolResult) string {
	if result == nil {
		return ""
	}
	for _, content := range result.Content {
		if text, ok := content.(*mcp.TextContent); ok {
			return text.Text
		}
	}
	return ""
}

// withLogging records one tool call and its outcome.
//
// Outermost in the handler chain, so what it records is what the caller
// actually received - a cache hit, a declined confirmation and a guard denial
// all reach it, and none of them are visible from inside the wrapper that
// produced them.
//
// A refused call logs at WARN with its reason, because a refusal is the thing
// #78 was filed over: Playwright rejecting `browser_navigate` in 16
// milliseconds is a server-side decision with no server-side record anywhere.
func withLogging(name string, next mcp.ToolHandler) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		started := time.Now()
		result, err := next(ctx, req)
		attrs := []any{
			slog.String("tool", name),
			slog.Int64("duration_ms", time.Since(started).Milliseconds()),
		}
		if span := trace.SpanContextFromContext(ctx); span.IsValid() {
			attrs = append(attrs,
				slog.String("trace_id", span.TraceID().String()),
				slog.String("span_id", span.SpanID().String()))
		}
		switch {
		case err != nil:
			logger.ErrorContext(ctx, "tool call failed",
				append(attrs, slog.String("outcome", "handler_error"), slog.String("reason", redactReason(err.Error())))...)
		case result != nil && result.IsError:
			logger.WarnContext(ctx, "tool call refused",
				append(attrs, slog.String("outcome", "tool_error"), slog.String("reason", redactReason(firstTextContent(result))))...)
		default:
			logger.InfoContext(ctx, "tool call served", append(attrs, slog.String("outcome", "ok"))...)
		}
		return result, err
	}
}
