# An argument the tool does not declare

A tool call carrying an argument its tool does not declare is refused, never
dropped and never forwarded to an upstream that will ignore it.

## Why a refusal

`signoz_aggregate_logs` was called with a `searchText` its schema never declared
and returned the count of every log in the window, with `status: success`.

- no filter - 8,759,997 returned, 6,525,190 rows scanned
- `searchText='zzzzz-nonexistent-string-qqqq'` - **8,760,201** returned, 6,525,483 rows scanned
- `filter="body CONTAINS 'zzzzz-nonexistent-string-qqqq'"` - 0 returned, 0 rows scanned

A string that cannot appear in any log returned the unfiltered total, and the
scan count is the tell: the dropped filter scanned everything. An error is
caught and a zero is caught. A large plausible number that is silently the
unfiltered total is not, and a blast-radius estimate was published from one
before a negative control caught it (mcp-beaver#94).


## Where it is enforced

Both paths could produce it. `splitArgs` skipped a name the schema did not carry,
with "the tool surface is exactly the schema" given as the reason: the claim
was right and the enforcement was a silent drop. The passthrough proxy forwarded
the argument map verbatim to an upstream that ignored it, and now reads the
declared names off the startup snapshot, the contract this runtime accepted.

An upstream setting `additionalProperties` to anything other than `false` keeps
the permissive contract it declares. An **absent** one is treated as closed,
inverting the JSON Schema default deliberately: that permissive default is what
let this through, and a guard is stricter than the thing it guards.

A pinned parameter is absent from the tool schema, so supplying one is refused
rather than silently overruled. Extra keys **nested inside** a declared property
stay the body mapping's business, which is what lets a webhook post its whole
payload at a tool that wants one field. A webhook whose extra keys sit at the top
level, as an Alertmanager body does, needs `ignore-undeclared-arguments "<tool>"`
(teable:coilyco/deploy#8419). It builds only on a grant whose body is a `map`, where no dropped
key can have been a filter.

# A tool the allowlist does not grant

A `tools/call` for a name the server never granted comes back as a tool result
with `isError: true` and the text `tool "<name>" is not on this server's
allowlist. The allowlist refused the call, and nothing was sent upstream`. It
was JSON-RPC `-32601 method not found: "tools/call"`, which tells a caller the
protocol method is missing rather than that policy refused one tool
(COI-2385).

- **Why not -32601** - the spec's error handling lists "Unknown tool" under
  protocol errors with `-32602`, and keeps `-32601` for a method the server does
  not implement. `tools/call` is implemented.
- **Why not -32602 either** - it reads as a malformed call, and no code in the
  spec says policy refused it. The spec has clients provide tool execution
  errors to the model (SHOULD) and protocol errors only optionally (MAY).
- **Clients** - Codex keeps an `isError` result's content blocks and reduces a
  JSON-RPC error to its message string. The TypeScript client throws on a
  JSON-RPC error and returns an `isError` result. Claude Code is closed source,
  so its handling is not read from code.
- **Logged** - the call never reaches a tool handler, so the middleware writes
  the WARN `tool call refused` line itself. Telemetry counts it as
  `error.type=tool_error` where it was `method_not_found`.
