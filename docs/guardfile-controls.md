# Guardfile siblings: controls

Opt-in controls beside `wrap`. Context nodes: [guardfile-siblings.md](guardfile-siblings.md).

**Query pins.** `pin "<tool>" { query "<name>" env "<VAR>" }` fixes an outgoing query parameter server-side from `env`, `file`, or `literal`, resolved per call. The name is absent from the tool schema, so a caller cannot supply it. Pinning a caller-input parameter is a build error and an unresolvable pin fails the call.

**Rate limit.** `rate-limit "1/1s"` is a per-server outbound bucket that queues instead of rejecting, and a call queued past the request deadline fails with a stated timeout. `{ store redis env "REDIS_URL" }` shares it across pods, keyed on the server name unless `bucket "<key>"` overrides, so a rate becomes a spend budget. A Redis outage refuses.

**Cache.** `cache "<tool>" ttl="15m"` reuses one grant's answer. Off by default, keyed on tool plus canonical arguments, outside the rate limiter, and failures are not stored. Caching a destructive or `confirm`-gated grant fails the build.

**Forwarded headers.** `forward-header "x-agent-origin"` copies that header from the caller onto every upstream request. A wrap-level `header` or `auth header-token` of that name is the fallback, `required=#true` refuses a call missing it, and across `inherit` it only tightens. Credentials, cookies, and runtime-owned headers are refused at build.

**Reject empty.** `reject-empty "<tool>"` makes an empty result (no content, whitespace, `null`, `""`, `[]`, `{}`) a tool error. `reject-empty-argument "<tool>" field="<name>"` refuses a blank write field. `false` and `0` are answers.

**Ignore undeclared arguments.** `ignore-undeclared-arguments "<tool>"` drops an undeclared top-level argument instead of refusing it, for a webhook that posts its whole payload, and only on a grant whose body is a `map` ([refusals.md](refusals.md)).

**Withhold and confirm.** `withhold "<tool>" { reason ...; alternative ... }` mints a stub for a verb left out on purpose: it is listed, states why, names a substitute, and refuses every call with a structured `verb_withheld` payload, reaching no upstream. `_meta` carries `coilyco.io/withheld`. `confirm "<tool>"` runs a tool only on a retry with an explicit accept. Both work beside `mcp-upstream` ([upstream-controls.md](upstream-controls.md)).

**Extraction.** `extract "<tool>" as="<kind>"` makes a response an agent cannot read readable. It needs `raw-response` on the grant, and another kind's bound is a build error.
- `as="pdf-text" max-pages="20"` - text only, no tables or OCR. 32MB gate, pages default 20 and cap 200, parsed off the request goroutine. Bad input is a tool error, and coverage gains `pages: {shown, total}`.
- `as="feed-entries" max-items="25"` - RSS or Atom to `title`, `link`, `author`, `id`, `published`, `updated`, `categories`, omitting absent ones and the body, most of the bytes. 8MB gate, items default 25 and cap 250, coverage gains `entries: {shown, total}`.
