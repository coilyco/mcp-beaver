# Pulling a guardfile from the registry

`mcp-beaver pull <registry-name>` writes an `mcp-upstream` guardfile for one server in the official MCP registry: its remote, the tools it serves now, and an allowlist decided by the upstream's own `readOnlyHint`. Spec: mcp-beaver#119.

```sh
mcp-beaver pull ac.tandem/docs-mcp --scope read-write -o tandem.mcp.kdl
mcp-beaver pull my/private --upstream https://mcp.example/mcp --upstream-header 'Authorization=Bearer {env:TOKEN}'
```

1. `GET <registry>/v0/servers/<name>/versions/latest`, unauthenticated. The entry must be `active` and publish a `streamable-http` remote, since a `packages`-only server must be installed and run. `--upstream <url>` skips this.
2. Connect as the proxy does, retrying once on an empty list, since one server answered zero tools and then 25.
3. Read each `readOnlyHint` off the upstream's own JSON, since the typed SDK view folds absent into false.
4. Emit the file, parse it back, then write it. It never calls a tool, so a mislabelled hint is trusted.

## Scope

`--scope` selects by what the upstream asserted, each step an ordered subset of the next, so widening adds lines without moving any. `read-only` (the default, and the only scope to deploy unread) takes `readOnlyHint: true`. `read-write` takes a hint either way. `all` adds undeclared tools. The rule is the hint, never the name: on 120 hinted tools, name screening caught 6 to 10 of 24 real mutators and missed `fire_billing_event` and `purchase_wizard` (mcp-beaver#118).

## What the file carries and refuses

- **`annotation-coverage <declared|partial|undeclared> annotated=<n> silent=<n>`** says whether the upstream annotates every tool, some, or none. Across 30 servers: 15, 2, and 13.
- **`instructions`** is the registry description, cut to 500 characters. **`auth header-token`** appears when the pull presented a `<prefix>{provider:address}` header.
- **No schemas.** The proxy snapshots contracts at connect, so restating them would duplicate a source that rots.
- **No `withhold` per exclusion.** Stubs are listed, so one each advertised 208 tools of which 96 answered, and a denial list on a closed allowlist enforces nothing.
- **No guessing.** An undeclared tool is absent under `read-only` and `read-write`. For 13 of 30 servers that means allowing nothing, which `lint` accepts and `serve-upstream` refuses to serve.

## The prototype

`scripts/registry-probe/` is the Python origin, shipped in nothing: `probe.py` writes `probe.json`, `classify.py` is the name heuristic behind #118, `gen_kdl.py` writes guardfiles, `render*.py` build pages. `just registry-guardfiles` and `registry-render` run offline. The 2026-09-01 run: 60 entries, 30 answered anonymously (21 refused with HTTP 401), 208 tools, 96 `readOnlyHint: true`, 24 false, 88 undeclared. `go test ./cmd/...` lints its 30 files.

See [upstream.md](upstream.md) for the proxy and [directory.md](directory.md) for the same pull across the registry.
