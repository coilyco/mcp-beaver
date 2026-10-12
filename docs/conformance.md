# Transports, conformance, and bounds

- **Streamable HTTP** (`/mcp`) - SDK-backed `initialize`, `tools/list`, `tools/call`.
- **HTTP tool API** (`POST /api/{tool-name}`) - one endpoint per projected tool, no flag. The body is the argument object and a success is the MCP `CallToolResult` shape. Both surfaces share one handler, so guards and drift checks cannot diverge. It needs `application/json` up to 1 MiB, and errors return non-2xx JSON.
- **Health** - `GET /healthz`, exempt from every deadline so one slow dependency cannot become a restart loop.

**Authentication boundary.** mcp-beaver does not authenticate inbound callers. The consuming deployment owns identity, auth, TLS, ingress, and exposure. Guardfile `auth` is outbound to the upstream, and caller-supplied identity-shaped arguments are data, never trusted identity.

## MCP 2026-07-28

- **Stateless transport** - required by this revision. A session-backed handler rejects such a client. Older clients negotiate their own version.
- **Cacheable lists** - results carry `ttlMs` and `cacheScope`. Spec-driven surfaces change only on restart and get the longer hint, proxied ones get the shorter.
- **Deprecations** - the `logging` capability is no longer advertised. Observability is OpenTelemetry.
- **Methods** - `initialize`, `notifications/initialized`, `notifications/cancelled`, `ping`, `tools/list`, `tools/call`, `prompts/list`, `prompts/get`, `resources/list`, `resources/read`, `resources/templates/list`, `server/discover`, `subscriptions/listen`. Resources and prompts add no runtime-specific admin, lifecycle, reload, or control verb. Operator control stays on `/admin`, off the protocol, and that is the line to preserve.

**Operator HTTP.** `GET /admin/describe` returns the guardfile name and path, tool count, mode, upstream presence, and safe non-secret facts. `POST /admin/reload` is restart-only, because guarded state cannot hot-reload, and says so.

## Request bounds

Nothing was bound before: no server timeouts, a nil proxy client, and no cancellation to handlers, so a wedged upstream held a request as long as the caller waited.

- **Per-call deadline** - `--request-timeout` (60s, 0 disables) bounds one tool call end to end at the handler, for MCP, `/api`, and every protocol version. It rides the request context, so it aborts the outbound call.
- **Transport deadline** - the per-call bound plus 5s, so the tool expires first and the runtime can still report. Expiring together gave an empty body that reads as a crashed pod.
- **Cancellation** - a departed caller cancels the work (2026-07-28 clients and later).
- **Upstream client** - proxy mode bounds its own client at 45s when the caller set none. Spec mode keeps opcore's 30s default.
- **Connection guards** - 10s header timeout and 120s idle. No write timeout, which is absolute and would cut a slow upstream mid-response.
- **Attribution** - the method and tool are on the transport span before dispatch, so a hung request names itself.
