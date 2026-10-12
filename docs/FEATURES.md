# Features

mcp-beaver turns an umbra Guardfile (`.mcp.kdl`) into a guarded MCP server with a matching HTTP tool API, shipped as one generic runtime image and one Helm chart. Every `can` grant becomes one MCP tool and one `POST /api/{tool-name}` endpoint, with no per-server Go, Dockerfile, handler, or input schema. Tracking: coilysiren/inbox#164, coilyco-bridge/deploy#40.

## Design
**The spec configures the image interior**: which upstream API, how it authenticates, which grants become tools. Exposure (Deployment, Service, ingress, tailnet route, mounted Secret) is the chart's job and `deploy` rolls it out. The spec never reaches into a chart, and the chart mounts the `.mcp.kdl` as an opaque blob it never parses.

**Network HTTP, never stdio.** Servers are always-on pods reached by URL, so the one listener is HTTP and there is no transport fork.

**Built on umbra.** `wrap ward mcp <name>` is umbra's inline grammar, not a dependency on ward. umbra supplies the compiled policy IR and the KDL authoring layer (parsed, never evaluated), and the official MCP Go SDK supplies transport and sessions. One engine with no upstream knowledge drives every spec.

## Commands
- [serve.md](serve.md) - `serve`, the grant-to-tool projection, and `lint`.
- [spec-mode.md](spec-mode.md) - grants resolved against an API document.
- [inherit.md](inherit.md) - tiers, composed siblings, `flatten`.
- [upstream.md](upstream.md) - the guarded passthrough proxy and `lint-upstream`.
- [upstream-controls.md](upstream-controls.md) - proxy sibling nodes and argument pins.
- [pull.md](pull.md) - `pull` and the registry-probe prototype.
- [directory.md](directory.md) - `directory`, the registry swept into guardfiles.
- [oauth2.md](oauth2.md) - `oauth2-client`, the credential this runtime mints.
- [s3.md](s3.md) - `serve-s3` and `serve-ssm`.
- [release.md](release.md) - the binary, `version`, and the Homebrew formula.

## Guardfile surface
- [guardfile-siblings.md](guardfile-siblings.md) - instructions, resources, prompts, server-info.
- [guardfile-controls.md](guardfile-controls.md) - pins, rate limit, cache, headers, withhold, confirm, extraction.
- [apps.md](apps.md) - MCP App widgets.

## Runtime and distribution
- [conformance.md](conformance.md) - transports, MCP 2026-07-28, `/admin`, request bounds.
- [refusals.md](refusals.md) - undeclared arguments and unlisted tools.
- [telemetry.md](telemetry.md) - logs, OpenTelemetry, Sentry crashes.
- [ci.md](ci.md) - the gate, the image, the publish.
- [chart.md](chart.md), [chart-values.md](chart-values.md) - the Helm chart.

## See also
[README.md](../README.md) and [AGENTS.md](../AGENTS.md).
