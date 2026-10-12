# Logs, telemetry, and crash reports

## Structured logs

Every server writes one JSON object per line to stderr through `log/slog`, which the node collector promotes with `level` mapped to OTel severity. Servers once logged only a banner: twelve hours across twenty-two pods gave one line while four servers failed every call.

- **Startup** - mode, name, spec path, address, tool count, request timeout.
- **Every tool call** - `tool`, `outcome`, `duration_ms`, and `trace_id` and `span_id` when a span is active. Applied at registration, so every kind of tool is covered.
- **Refusals** - `outcome=tool_error` at WARN with the reason, and a handler failure is `handler_error` at ERROR.
- **Level** - `MCP_BEAVER_LOG_LEVEL` (`debug`, `info`, `warn`, `error`), default info.

**Redaction.** A reason keeps each URL's scheme, host, and path and drops the query as `?<redacted>`, where `pin` writes. Reasons are cut at 512. A base-url path is a third credential surface (Telegram's is `/bot<token>`, infrastructure#708), so the whole path is masked in the log and in the caller's error. A token-shaped pattern would fail silently when an upstream formats it differently.

## OpenTelemetry, opt-in

Standard `OTEL_*` variables configure it. With no exporter selector or OTLP endpoint, startup is a no-network no-op, and `OTEL_SDK_DISABLED=true` or a per-signal `none` disables export. Invalid config fails startup, and export failures never change a tool result.

- **MCP server** - one SERVER span and one `mcp.server.operation.duration` per request. `tools/call` carries `gen_ai.operation.name=execute_tool` and the tool name, and a tool error sets `error.type=tool_error`.
- **Upstream client** - `serve-upstream` emits CLIENT spans and `mcp.client.operation.duration`, injecting W3C context into upstream `params._meta`.
- **Direct HTTP** - `POST /api/{tool-name}` gets HTTP server telemetry and one `execute_tool` child span. `/healthz` is excluded.
- **Resource** - the server name is the default `service.name`, and shutdown flushes in five seconds.

**Never captured**: arguments, results, bodies, authorization headers, tokens, guardfile contents, spec paths, upstream URLs. Attributes stay bounded to methods, tools, transport, mode, and error classes. A refusal-reason log keeps the upstream host and path and drops the query.

## Crashes to Sentry

With `SENTRY_DSN` set, `serve`, `serve-ssm`, `serve-s3`, and `serve-upstream` send **crashes only**, tagged `server.name`: a failed start, a serve loop error, or an uncaught panic in `main` or a handler (re-raised after reporting, `http.ErrAbortHandler` excluded). A handler panic carries method, path, and credential-filtered headers, never the body, plus filtered info-and-above logs as breadcrumbs. Handled errors stay in SigNoz, at most 20 events a minute are sent, and a bad DSN leaves reporting off. A panic on another goroutine kills the process unreported, and CLI verbs never report.
