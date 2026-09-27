# Guardfile controls on the proxy

The sibling nodes an `mcp-upstream` guardfile states, and what each one does to
a passthrough surface. The REST half is
[guardfile-controls.md](guardfile-controls.md). The parse and the credential
are [upstream.md](upstream.md).

Every node here is checked **offline**, against the allowlist the file
declares. The allowlist is the whole surface a proxy serves - an absent
upstream tool already fails the snapshot - so a control naming a tool nobody
serves is a `lint` error rather than a boot that looks configured.

**`icon`.** The same `serverInfo.icons` projection spec mode does, so a
connector tile shows the brand mark instead of a placeholder. Prefer `data:`
URIs, since a gated deploy sits behind oauth2-proxy where a hosted icon URL
401s for the connecting client.

**`server-info`.** On by default here as well, so `mcp_beaver_info` answers on
every mcp-beaver deployment and its absence carries one meaning rather than
two. It reports the server's own name, mode, and inventory, reaches no
upstream, and is spared the rate limiter because it doubles as the liveness
probe the protocol's retired `ping` left without one. `server-info
name="..."` renames it, `server-info disabled` removes it, and a name that
collides with an allowlisted tool is a startup error. The flag form takes the
same default, having no file to state one in.

**`confirm`.** `confirm "<tool>" message="..."` gates one proxied tool behind
the Multi Round-Trip Request: the first call returns an elicitation and the
upstream is reached only on a retry carrying an explicit accept. Opt-in per
tool, because a proxy runs headless deployments where a blanket prompt would
wedge every write. Naming a `withhold` stub or the info tool is accepted and
wraps nothing: neither reaches an upstream, so there is nothing to confirm.

**`pin`.** The proxy half of the node spec mode states as `query`. See
[upstream-pins.md](upstream-pins.md).

**`rate-limit`.** The per-server outbound bucket, applied to proxied tools
alone. It sits inside the confirmation gate, so a call awaiting a human holds
no upstream slot and a declined one spends none, and inside the argument pins,
so a call contradicting one charges no budget. Everything else about
it - the `store redis` durable form, the shared `bucket` key, waiting rather
than rejecting - reads the same as it does beside `wrap`.

## What is not projected, and why

Absence here is a statement rather than a backlog. `resource`, `prompt` and
`app` are content a REST guardfile serves beside its grants, and a passthrough
proxy mints none of it. `cache`, `extract`, `reject-empty`,
`reject-empty-argument`, `forward-header` and `set` all shape a request or a
response opcore assembles from a grant. A proxy forwards the upstream's own contract verbatim
instead, which is the property the drift check exists to hold. Stating one
beside `mcp-upstream` fails closed, and the refusal names the set that works.
