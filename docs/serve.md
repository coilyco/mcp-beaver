# `mcp-beaver serve` and `lint`

`mcp-beaver serve <spec.mcp.kdl> --http :addr` renders any `.mcp.kdl` into a guarded MCP server (MCP Go SDK, streamable HTTP at `/mcp`) and the identical tools at `POST /api/{tool-name}`. No per-guardfile Go, and it never binds stdio.

**Parse.** `opcore.ParseInlineWithOptions` (umbra `http/opcore`) reads `wrap`, `base-url`, `auth`, `restrict`, and each `can <verb> <resource> { path/query/body/set }` grant. Body and query blocks keep typed scalars, arrays, nested objects, required fields, bounds, and mutually-exclusive groups. Method is inferred from the verb and path params from `{template}`. `AllowReservedNames` is on, so a body field may be named `query`. `sql` grants reach Postgres only (`pgx` is the one driver linked).

**Projection.** Each grant becomes one tool and one endpoint named `verb_resource`, with a draft-07 `inputSchema`, a description from `describe`, a title, annotations derived from the HTTP behavior, and a `{coverage, result}` output schema (`internal/mcpserver`). The surface is exactly the `can` grants, so an unwritten grant is an absent tool.

**Coverage first.** Every result leads with `coverage` (`truncated`, `bytes`, `over_budget` past the smallest measured consumer cap, and `items` naming each array and length) and carries the payload under `result`, because a harness that bounds a result keeps the front. The envelope is a struct, so field order is a contract.

**Execution.** Arguments route onto `opcore.Args` without flattening nested bodies, then `Operation.Execute` applies the metachar gate, `restrict` allowlist, base-url, and env-token auth. A denied or failed call is a tool result with `isError`, and violations fail before the upstream sees a request.

## `lint`

`mcp-beaver lint <spec.mcp.kdl>` is `serve` minus the listener and telemetry. It builds the same server through the same constructor, prints the projected tool names sorted one per line, and exits 0, so a consumer reads its surface off the owning loader instead of writing a second parser. A rejected spec exits non-zero with the failure on stderr and nothing on stdout. Grants that collide on one tool name fail. No network, so it runs in a sealed clone and CI.

- **Columns.** `--methods` prints the HTTP method and `--apps` the App widget uri (`-` for none). Opt-in and exclusive, since the bare listing is the diff a consumer pins.
- **Warnings** go to stderr: an unknown verb falling through to POST (a grant stating `method` is owed none), a `resource` with no `audience`, and an inherited control dropped because this tier narrowed away its tool.
- **Stated method.** `method "POST"` in a `can` body picks the method so the verb can name the tool well. A stated `DELETE` marks the grant destructive, and anything outside GET, POST, PUT, PATCH, DELETE, HEAD fails closed.

`serve-ssm` and `serve-s3` use their own grammar ([s3.md](s3.md)). `lint-upstream` is in [upstream.md](upstream.md), deadlines in [conformance.md](conformance.md).
