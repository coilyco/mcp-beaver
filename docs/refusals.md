# Refused calls

## An argument the tool does not declare

A call carrying an undeclared argument is refused, never dropped and never forwarded to an upstream that will ignore it.

`signoz_aggregate_logs` was called with a `searchText` its schema never declared and returned every log in the window with `status: success`. No filter gave 8,759,997 (6,525,190 rows scanned), a string that cannot occur gave **8,760,201** (6,525,483 scanned), and a real `filter` gave 0. A large plausible number that is silently the unfiltered total passes every error and zero check, and a blast-radius estimate was published from one before a negative control caught it (mcp-beaver#94).

Both paths could do it. `splitArgs` skipped a name the schema lacked, and the proxy forwarded the argument map verbatim. The proxy now reads declared names off the startup snapshot. An upstream setting `additionalProperties` to anything but `false` keeps its permissive contract, and an **absent** one is treated as closed, inverting the JSON Schema default on purpose because a guard is stricter than the thing it guards.

A pinned parameter is absent from the schema, so supplying it is refused rather than overruled. Extra keys nested inside a declared property stay the body mapping's business, which lets a webhook post its whole payload at a one-field tool. A webhook whose extra keys sit at the top level, like Alertmanager's, needs `ignore-undeclared-arguments "<tool>"` (teable:coilyco/deploy#8419), allowed only on a grant whose body is a `map`.

## A tool the allowlist does not grant

A `tools/call` for an ungranted name returns a tool result with `isError: true` and the text `tool "<name>" is not on this server's allowlist. The allowlist refused the call, and nothing was sent upstream`. It was JSON-RPC `-32601`, which says the method is missing rather than that policy refused one tool (COI-2385).

- **Not -32601** - the spec files "Unknown tool" under protocol errors with `-32602` and keeps `-32601` for an unimplemented method.
- **Not -32602** - it reads as a malformed call, and no code says policy refused it. The spec has clients give tool execution errors to the model (SHOULD) and protocol errors only optionally (MAY).
- **Clients** - Codex keeps an `isError` result's content and reduces a JSON-RPC error to its message. The TypeScript client throws on a JSON-RPC error and returns an `isError` result. Claude Code is closed source.
- **Logged** - the call never reaches a handler, so middleware writes the WARN `tool call refused` line, and telemetry counts it as `error.type=tool_error` where it was `method_not_found`.
