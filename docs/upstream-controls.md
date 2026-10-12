# Guardfile controls on the proxy

The sibling nodes an `mcp-upstream` guardfile states. REST half: [guardfile-controls.md](guardfile-controls.md). Parse and credential: [upstream.md](upstream.md). Every node is checked **offline** against the declared allowlist, so a control naming a tool nobody serves is a `lint` error, not a boot that looks configured.

**`icon`.** The `serverInfo.icons` projection, as in spec mode. Prefer `data:` URIs, as a gated deploy 401s a hosted URL.

**`server-info`.** On by default, so `mcp_beaver_info` answers on every deployment. It reports name, mode, and inventory, reaches no upstream, and skips the rate limiter. `server-info name="..."` renames it, `disabled` removes it, and a name colliding with an allowlisted tool is a startup error.

**`confirm`.** `confirm "<tool>" message="..."` gates one proxied tool behind a Multi Round-Trip Request: the first call returns an elicitation and the upstream is reached only on a retry with an explicit accept. Per tool, since a blanket prompt wedges headless writes. A `withhold` stub or the info tool reaches no upstream, so confirming it wraps nothing.

**`rate-limit`.** The per-server outbound bucket, on proxied tools only. It sits inside the confirmation gate, so a call awaiting a human holds no slot, and inside the pins. `store redis`, `bucket`, and queueing read as beside `wrap`.

**`pin`.** `--pin <tool>.<arg>=<value>` or a `pin` node fixes one argument of one proxied tool, applied by the wrapper. An allowlist names tools, which is the whole authority only while the verb carries the scope: allowlisting one Bluesky read tool grants every account, because the account is a parameter.
- **Non-overridable** - a caller giving another value is refused, not corrected, since quiet rewriting lets a model believe it read one scope while reading another. The refusal names the caller's value, never the resolved one, since an `env` pin holds a secret.
- **Validated** - a pin outside the allowlist refuses to boot.
- **Exact values only.** Conjoining free-form filters needs the upstream's query language, and a wrong one widens silently.

Example: `pin "get_author_feed" { argument "actor" env "BSKY_ACTOR" }`. The child names the surface: `argument` fixes a forwarded argument, `query` (REST only) an outgoing query parameter, mismatches refused. The value is `env`, `file`, `literal`, or a minted `oauth2` client, resolved per call. A `--pin` flag states `literal`, never beside a node. `from="query:<parameter>"` takes one query parameter out of a resolved URL, for a credential embedded in a private feed or signed link. A non-URL or missing parameter fails the call.

## Not projected

Absence is a statement: `resource`, `prompt`, `app`, `cache`, `extract`, `reject-empty*`, `ignore-undeclared-arguments`, `forward-header`, and `set` serve or shape what opcore builds from a grant. A proxy forwards the upstream's contract verbatim, so stating one fails closed.
