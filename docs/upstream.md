# The guarded passthrough proxy

`mcp-beaver serve-upstream --upstream <mcp-url> --tool <name>...` connects to a streamable-HTTP MCP upstream, snapshots the allowlisted tool contracts, and exposes only that subset over MCP and HTTP.

- **Projection** - each allowlisted tool becomes one MCP tool and one HTTP endpoint, keeping its contract.
- **Fail closed** - an unknown tool is a tool error, never a widened surface.
- **Drift** - each call, and an idle proxy every 10 minutes, compares a tool with its accepted contract. A `readOnlyHint` tool that only gains optional arguments is accepted and re-advertised. Any other change refuses that tool alone, by name, until the upstream matches again. Either sends one Sentry event with `alert=true` (`upstream_drift.go`, COI-3608).
- **One session** - calls and the drift check share one, because real Node upstreams reject a second. Its standalone stream stays open (#80), and a caller's protocol version never crosses to it (#85).
- **Survival** - the bound is time-to-first-byte, never `Client.Timeout`, which killed the session. A lost session is replaced next call, and `--connect-timeout` retries the first connect.
- **Credentials** - `--upstream-header 'Authorization=Bearer {env:TOKEN}'` presents a header per request. It needs one `{provider:address}` span, since a literal would put the value in argv, and never lands in an error. Minted: [oauth2.md](oauth2.md).

**`lint-upstream --tool <name>...`** runs the serving path's own `ValidateAllowlist`: empty entries, duplicates, and an empty list fail. `--read-only heuristic` screens names for mutation verbs, and `strict` (needs `--upstream`) fails any tool lacking a `readOnlyHint`.

## As a guardfile

One reviewable file for `serve-upstream <spec.mcp.kdl>`, `lint`, and `lint-upstream`, refused beside `--upstream` or `--tool`.

```kdl
mcp-upstream "ac.tandem/docs-mcp" {
    url "https://tandem.ac/mcp"
    auth header-token { header "Authorization"; prefix "Bearer "; value env "TOKEN" }
    can "search_docs"
}
```

`url` is `--upstream`, each `can` a `--tool`, and `auth` is the credential grammar on `--upstream-header`. Projected siblings: `description`, `instructions`, `oauth2-client`, `withhold`, `icon`, `server-info`, `confirm`, `pin`, `rate-limit` ([upstream-controls.md](upstream-controls.md)). Anything else fails closed, since an ignored node would serve wider than the file reads. `pull` writes one ([pull.md](pull.md)).

**`withhold`** is why to hand-author one: it names the neighbour an allowlist leaves out, and a stub shadowing a `can` fails `lint` ([guardfile-controls.md](guardfile-controls.md)).

**The chart** mounts one with `runtime.mode: upstream` plus a `spec` ([chart.md](chart.md)).

**umbra owns the grammar** (`mcpverb.ParseUpstream`, `mcpverb.Classify`): `auth` serves `header-token`, `bearer`, and `none`. `query-param` refuses rather than drop a secret, and a chain naming a second source refuses, since a header resolves one and never falls back.
