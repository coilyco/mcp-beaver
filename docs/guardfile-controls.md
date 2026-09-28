# Guardfile siblings: controls

Opt-in controls stated beside `wrap`. Context nodes: [guardfile-siblings.md](guardfile-
siblings.md).

## Query pins

`pin "<tool>" { query "<name>" env "<VAR>" }` fixes an outgoing query parameter server-
side, resolved at call time from `env`, `file`, or `literal`. The pinned name is absent
from the tool schema, so a caller can neither supply nor override it, and `set` writes
body values only, leaving a GET's scope nowhere else to go. Pinning a caller-input
parameter is a build error, and an unresolvable pin fails the call rather than sending an
unscoped request.

**Rate limit.** `rate-limit "1/1s"` is a per-server outbound bucket that serialises rather
than rejecting, since a queued call is slower but a 503 is a failed turn. Grant-backed
only, and a call queued past the request deadline fails with a stated timeout. `{ store
redis env "REDIS_URL" }` makes it **durable and shared**: it survives a pod roll, and two
pods rendering one guardfile charge one budget, keyed on the server name unless `bucket
"<key>"` overrides. That is what turns a rate into a spend budget. An outage refuses
rather than falling back to memory. No `store`, no change.

**Response cache.** `cache "<tool-name>" ttl="15m"` reuses one grant's upstream answer for
a window. Off by default. Keyed on the tool plus canonicalised arguments, and outside the
rate limiter so a hit spends no slot. Failed calls are never stored, and caching a
destructive or `confirm`-gated grant is a build error.

**Forwarded headers.** `forward-header "x-agent-origin"` copies that header from the
caller's MCP or HTTP API request onto every upstream request the call makes, so the
upstream learns who is behind this server and not only the server itself. A wrap-level
`header` or `auth header-token` naming the same header is the fallback for a caller that
sent none. `required=#true` refuses a call missing it before any other control runs, and
across `inherit` it only tightens. Credentials, cookies, and headers the runtime or the
MCP session owns are refused at build. Off by default.

**Reject empty.** `reject-empty "<tool>"` makes an empty result a tool error. `reject-
empty-argument "<tool>" field="<name>"` refuses a write carrying a blank field. Both off
by default. Empty is no content, whitespace, `null`, `""`, `[]`, or `{}` past the
`coverage` envelope. **`false` and `0` are answers**.

**Ignore undeclared arguments.** `ignore-undeclared-arguments "<tool>"` drops a top-level
argument the tool does not declare instead of refusing it, for a webhook sender that posts
its whole payload. Only a grant whose body is a `map` accepts it. Anywhere else a dropped
key is a dropped filter, so naming one fails the build ([refusals.md](refusals.md)).

## Withheld verbs and confirmations

`withhold "<tool-name>" { reason ...; alternative ... }` mints a discoverable stub for a
verb left out on purpose: it appears in `tools/list`, states why, names a substitute, and
refuses every call with a structured `verb_withheld` payload while reaching no upstream. A
`coilyco.io/withheld` marker in `_meta` separates stubs from live tools.
`confirm "<tool-name>"` gates one tool behind a Multi Round-Trip Request, running only on a
retry carrying an explicit accept. Both ride beside `mcp-upstream` as well as beside
`wrap`, along with `pin`, `rate-limit`, `icon`, and `server-info`, checked there against
the allowlist the file declares. What each does to a passthrough surface, and which
siblings stay REST-only: [upstream-controls.md](upstream-controls.md).
