# Guardfile siblings: context

Top-level nodes beside `wrap`, outside the frozen inline grammar `opcore.ParseInline` owns. Each fails closed on an unknown property or child, and all are opt-in except `server-info`. Controls: [guardfile-controls.md](guardfile-controls.md). Composition across `inherit`: [inherit.md](inherit.md).

**Instructions.** `instructions { text ... }` says what the server is for, published under the shared policy sentence in `InitializeResult.Instructions`. Bounded at 500 characters, since a consumer pays for it in the model's prompt every turn, once per rostered server.

**Resources.** `resource "<name>" uri=... { text ... }` serves static content on `resources/read`. Inline only by design: a resource proxying an upstream read would be a second, unguarded egress path beside the grants. Claude Code surfaces these as `@` mentions. `audience "assistant"` and `priority=0.9` emit the MCP annotations a harness gates on when pulling a resource in unprompted, so no audience means no harness includes it, and `lint` warns.

**Prompts.** `prompt "<name>" { argument ...; text ... }` serves a message template on `prompts/get` with `{arg}` substitution. A missing required argument is an error, since a half-filled prompt reads as complete. Claude Code surfaces these as slash commands.

**Apps.** `app "<name>" uri="ui://..." file="..."` serves an interactive widget and links it to the tools that render it. It is the only sibling with a file body ([apps.md](apps.md)).

**OAuth2 clients.** `oauth2-client "<name>" { token-url ...; client-id ...; client-secret <provider> "<address>" }` declares a `client_credentials` client, and `value oauth2 "<name>"` presents its token, the one value this runtime mints rather than reads ([oauth2.md](oauth2.md)).

**Server info.** One read-only tool reporting the server's identity, mode, and tool inventory. It reaches no upstream and restores the liveness probe that 2026-07-28 removed with `ping`. It is on by default, because every field is already reachable through `initialize` and the list methods, and a probe present on only some servers lets an agent read no meaning from its absence. `server-info name="status"` renames it and `server-info disabled` removes it. It counts itself, so `lint` and `tools/list` agree.

**Unknown siblings.** A top-level node nothing reads fails `New`, so `lint` and `serve`, naming it and the known ones, across the whole `inherit` chain. A misspelled control such as `reject-emtpy` would otherwise guard nothing while the file read as guarded. `description` is allowed because umbra owns it.
