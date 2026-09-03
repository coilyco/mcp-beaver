# Features

What mcp-beaver ships today. It turns a umbra Guardfile into a guarded MCP
server with a matching HTTP tool API, distributed as one runtime image plus a
generic Helm chart.

## Commands

- [serve.md](serve.md) - the generic runtime, grant-to-tool projection.
- [lint.md](lint.md) - offline validation, and `lint-upstream`.
- [spec-mode.md](spec-mode.md) - swagger-resolved grants.
- [inherit.md](inherit.md) - tiers that compose the sibling nodes as well as the
  grants, and `flatten`.
- [upstream.md](upstream.md) - the guarded passthrough proxy, as flags or as a
  `wrap mcp upstream` guardfile, and the credential it presents upstream.
- [pull.md](pull.md) - `pull`, a `wrap mcp upstream` guardfile written from a
  registry entry and the upstream's own `readOnlyHint`.
- `version` - the release this binary was built from, stamped at build time and
  advertised in every MCP handshake. See [release.md](release.md).
- [directory.md](directory.md) - `directory`, the registry swept into a
  guardfile per server, a sweep record, and the two pages that index them.
- [oauth2.md](oauth2.md) - `oauth2-client`, the one credential this runtime
  mints rather than reads.
- [ssm.md](ssm.md) - the exact-parameter AWS reader.
- [s3.md](s3.md) - the asset publisher, and the one write-capable mode.
- [teable-admin.md](teable-admin.md) - a separate binary for the three
  field-schema verbs the guard withholds, with read-back verification and a
  value-snapshot recovery path for `convert-field`.

## Guardfile surface

- [guardfile-siblings.md](guardfile-siblings.md) - instructions, resources,
  prompts, server-info.
- [apps.md](apps.md) - MCP App widgets: a `ui://` resource plus the
  `_meta.ui.resourceUri` link a host renders from.
- [guardfile-controls.md](guardfile-controls.md) - pins, rate limit, cache,
  withheld verbs, confirmations.
- [extraction.md](extraction.md) - reading a PDF or feed an upstream returns.
- [upstream-pins.md](upstream-pins.md) - server-side argument pinning.

## Runtime

- [transports.md](transports.md) - streamable HTTP, the HTTP tool API, health.
- [conformance.md](conformance.md) - MCP 2026-07-28.
- [request-bounds.md](request-bounds.md) - deadlines and connection guards.
- [refusals.md](refusals.md) - an undeclared argument is refused, and a
  credential in a base-url path is never emitted.
- [conformance.md](conformance.md) - `/admin` describe and reload.
- [logs.md](logs.md) - structured logs and redaction.
- [telemetry.md](telemetry.md) - opt-in OpenTelemetry.

**Distribution.** - [release.md](release.md) is the installable binary and its
  Homebrew formula. [image.md](image.md), [ci.md](ci.md), [chart.md](chart.md),
  and [chart-values.md](chart-values.md) are the image the fleet deploys.
- [DESIGN.md](DESIGN.md) - why it is shaped this way.

## See also

- [README.md](../README.md), [AGENTS.md](../AGENTS.md),
  [.ward/ward.yaml](../.ward/ward.yaml).
