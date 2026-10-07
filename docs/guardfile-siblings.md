# Guardfile siblings: context

Top-level nodes stated beside `wrap`, outside the frozen inline grammar
`opcore.ParseInline` owns. Each fails closed on an unknown property or child.
All are opt-in except `server-info`, which is on by default and opts out.
Controls are in [guardfile-controls.md](guardfile-controls.md).

## Instructions

`instructions { text ... }` states what this server is for, published under the
shared policy sentence in `InitializeResult.Instructions`. Bounded at 500
characters, because a consumer rendering this into the model's prompt pays for
it every turn, once per rostered server. A guardfile declaring nothing
publishes exactly what it published before.

## Resources

`resource "<name>" uri=... { text ... }` serves static content on
`resources/read`. Inline only by design: a resource proxying an upstream read
would be a second, unguarded egress path beside the grants. Claude Code
surfaces these as `@` mentions. `audience "assistant"` and `priority=0.9` emit
the MCP annotations a harness gates on when pulling a resource into context
unprompted, so stating no audience means no harness includes it, and `lint`
warns.

**Prompts.** `prompt "<name>" { argument ...; text ... }` serves a message template on
`prompts/get` with `{arg}` substitution. A missing required argument is an
error, since a half-filled prompt reads as a complete one. Claude Code surfaces
these as slash commands.

## Apps

`app "<name>" uri="ui://..." file="..."` serves an interactive widget and links
it to the tools that render it. Its body is a file rather than `text` children,
and it is the only sibling that reads one. See [apps.md](apps.md).

## Composing across `inherit`

Siblings compose with the wrap body. The chain is read base-first, so a parser
sees a grandparent's declarations, then a parent's, then the child's.

Most nodes **union**, and each node's own duplicate check arbitrates. That is
what makes a base tier's `confirm` and `withhold` binding: the base's node is
still there, a child cannot drop it, and a child restating it by the same name
is a collision rather than an override. Silently taking either one is how a
weaker surface comes to read as the stronger one.

`instructions`, `server-info`, and `rate-limit` are **child-wins**, matching
`spec`, `base-url`, and `auth` inside the wrap body: a server states each once,
and a nearer guardfile replaces its base's. Only the inherit edge is decided
that way. Two of them in one file stays the fail-closed error that catches a
typo.

An `app` names its widget beside the guardfile that **declared** it, so a
parent's widget resolves against the parent's directory rather than the file
the runtime was pointed at.

A control an ancestor states on a tool this tier narrowed away is **dropped**,
and `lint` warns. There is nothing left to gate, so refusing would strand a
tier for correctly removing a tool. A control this guardfile states itself on a
tool it does not mint stays an error: that one is a typo.

Before mcp-beaver#113 none of this happened: a parent's siblings were dropped
in silence while its grants survived, so a child came out wider than its base.
The grant half is in [inherit.md](inherit.md).

## Unknown siblings

A top-level node nothing reads fails `New` (so `lint` and `serve`), naming it
and listing the known ones. A misspelled control such as `reject-emtpy` would
otherwise guard nothing while the file read as guarded. The check spans the
whole `inherit` chain. `description` is allowed because umbra owns it.

## OAuth2 clients

`oauth2-client "<name>" { token-url ...; client-id ...; client-secret <provider> "<address>" }`
declares a `client_credentials` client, and `value oauth2 "<name>"` presents its
token. The one value this runtime mints rather than
reads. See [oauth2.md](oauth2.md).

## Server info

One read-only tool reporting the server's identity, mode, and tool inventory.
It reaches no upstream and restores the liveness probe 2026-07-28 removed with
`ping`. On by default: every field is already reachable through `initialize`
and the list methods, and a probe present on only some servers lets an agent
read no meaning from its absence on the rest. `server-info name="status"`
renames it, `server-info disabled` removes it. It counts itself, so `lint` and
`tools/list` report the same surface.
