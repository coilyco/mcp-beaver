# Generating the directory

`mcp-beaver directory -o <dir>` sweeps the official MCP registry and writes what its servers actually serve: a record of the sweep, one `mcp-upstream` guardfile per answering server, and two pages indexing them. It is `pull` run across the registry, plus the pages (mcp-beaver#121).

```sh
mcp-beaver directory -o out                          # the whole registry, read-only
mcp-beaver directory -o out --limit 40 --scope all   # the first 40 listed, every declared tool
mcp-beaver directory -o out2 --from out/sweep.json   # re-render offline from an earlier sweep
```

## What it writes

- **`sweep.json`** - every listed server, answered or not, with state, description, published date, repository, and each tool's name, description, and `readOnlyHint` as declared. An absent hint stays absent, never false. It is the only input the rest needs, so `--from` re-renders without the network.
- **`guardfiles/<name>.mcp.kdl`** - one per answering server, `/` in the name written `__`, through `pull`'s renderer and parsed back before writing. `pull`'s rules hold: allow on the hint never the name, no `withhold` blocks, no guessing, upstream order.
- **`index.html`** - run counts, then one row per answering server with its tools split into declared read-only, declared mutating, and undeclared, its `annotation-coverage`, and a guardfile link, then the refusals with their state.
- **`guardfiles.html`** - every guardfile, grouped by coverage.

## How it runs

1. `GET <registry>/v0/servers?limit=100`, following `nextCursor`. Keep `isLatest` and `status: active` entries that publish a `streamable-http` remote, first remote wins, one per name. `--limit` caps the count in registry order.
2. Probe each entry as `pull` does, `--concurrency` at a time with `--timeout` per upstream (30s). A refusal is that server's state, `HTTP 401` for a status, `timeout` past the deadline, else the transport's words, and never the sweep's failure. Half the registry refuses anonymously, so a sweep that stopped at the first 401 would never write a directory. One line per server goes to stderr as it settles.
3. Record, emit, and render from the record.

No credential is presented, so the refused half stays outside the corpus, and `pull --upstream-header` reaches one by hand.

**Counts use the hint, not a name.** The prototype classified by name, and mcp-beaver#118 measured that at 6 of 24 real mutators, payment tools among the misses. A column disagreeing with the guardfile beside it is the one thing this page must not do, so the heuristic feeds nothing here, and an undeclared tool is counted as undeclared.

See [pull.md](pull.md) for one server, the scope axis, and the Python prototype, and [upstream.md](upstream.md) for the proxy each file configures.
